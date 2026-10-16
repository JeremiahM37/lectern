package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// memoryTool is the agent-facing `memory` tool.
type memoryTool struct{ stores *Stores }

// Register adds the `memory` tool to r, backed by s.
func Register(r *tools.Registry, s *Stores) { r.Register(&memoryTool{stores: s}) }

type memoryArgs struct {
	Action     string   `json:"action"`
	Scope      string   `json:"scope"`
	ID         string   `json:"id"`
	Category   string   `json:"category"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Tags       []string `json:"tags"`
	Files      []string `json:"files"`
	Query      string   `json:"query"`
	Limit      int      `json:"limit"`
	Pinned     *bool    `json:"pinned"`
	Supersedes string   `json:"supersedes"`
	Topic      string   `json:"topic"`
}

func (t *memoryTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "memory",
		Description: "Durable memory across sessions. Save a decision, convention, bug, preference, fact or todo " +
			"that a later session should know; search or list what is already saved; get, delete (a soft delete: restore brings it back) or pin a record by id; " +
			"review stale records. Save only things that will still matter later, with a short specific title (max 200 " +
			"characters), a body of at most 2000 characters, up to 8 tags and the file paths it concerns. When a new " +
			"memory contradicts or replaces an older one, save it with supersedes set to the old id, so the old one " +
			"stops being recalled. Relevant memories are also recalled automatically at the start of each turn.",
		Schema: tools.Schema(map[string]any{
			"action":     tools.Enum("What to do.", "save", "search", "list", "get", "delete", "restore", "pin", "stale"),
			"scope":      tools.Enum("project (this repo) or global (all repos). Default: project for save; all for search, list, get, delete, pin and stale.", "project", "global", "all"),
			"id":         tools.Str("Record id or unique id prefix (get, delete, pin, and supersedes)."),
			"category":   tools.Enum("Record category (save), or filter (list).", "decision", "convention", "bug", "preference", "fact", "todo"),
			"title":      tools.Str("Short headline, max 200 characters (save)."),
			"body":       tools.Str("The detail: what, why, and what to do about it, max 2000 characters (save)."),
			"tags":       tools.Arr("Free-form tags, up to 8 (save).", map[string]any{"type": "string"}),
			"files":      tools.Arr("Relative file paths the memory concerns, up to 16. Recall surfaces it when these files are touched (save).", map[string]any{"type": "string"}),
			"supersedes": tools.Str("Id of an older memory this one replaces (save). The older memory is kept but no longer recalled."),
			"query":      tools.Str("What to look for (search)."),
			"topic":      tools.Str("Only records carrying this tag (list)."),
			"limit":      tools.Int("Maximum results (search: default 10; list: default 20; stale: default 20)."),
			"pinned":     tools.Bool("pin: true (default) or false to unpin. list: true shows only pinned records."),
		}, "action"),
	}
}

// ReadOnly reports true. The tool never edits project files: save writes
// only the memory database (<root>/.scribe/memory.db or the global Scribe
// home), so it stays available in restricted modes such as plan.
func (t *memoryTool) ReadOnly() bool { return true }

func (t *memoryTool) Run(_ context.Context, _ *tools.Env, raw json.RawMessage) tools.Result {
	var a memoryArgs
	if err := tools.Decode(raw, &a); err != nil {
		return tools.Errorf("%v. Pass an object with at least an \"action\" field.", err)
	}
	if t.stores == nil {
		return tools.Errorf("memory is not available in this session.")
	}
	switch a.Action = normAction(a.Action); a.Action {
	case "save":
		return t.save(a)
	case "search":
		return t.search(a)
	case "list":
		return t.list(a)
	case "get":
		return t.get(a)
	case "delete":
		return t.delete(a)
	case "restore":
		return t.restore(a)
	case "pin":
		return t.pin(a)
	case "stale":
		return t.stale(a)
	case "":
		return tools.Errorf("action is required: save, search, list, get, delete, restore, pin or stale.")
	}
	return tools.Errorf("unknown action %q. Use save, search, list, get, delete, restore, pin or stale.", a.Action)
}

// normAction maps the near misses a model tends to send onto the real actions
// (forget and remove mean delete, unhide means restore, and so on).
func normAction(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "forget", "remove", "hide", "soft_delete", "softdelete":
		return "delete"
	case "unhide", "undelete", "unforget", "recover":
		return "restore"
	case "remember", "store", "add", "write":
		return "save"
	case "find", "query", "recall":
		return "search"
	case "pinned", "unpin":
		return "pin"
	case "show", "read", "fetch":
		return "get"
	}
	return strings.ToLower(strings.TrimSpace(s))
}

func (t *memoryTool) save(a memoryArgs) tools.Result {
	if t.stores.Policy.WriteScope == "none" {
		return tools.Errorf("%v", errWritesOff)
	}
	var db *DB
	var err error
	if a.Scope == "" {
		db, err = t.stores.writeDB()
	} else {
		db, err = t.stores.DB(a.Scope)
	}
	if err != nil {
		return tools.Errorf("%v", err)
	}
	// Resolve the record being replaced before writing, so a bad id fails
	// without leaving the new memory behind.
	var old *DB
	if a.Supersedes != "" {
		old = db
		if _, err := db.Get(a.Supersedes); err != nil {
			return tools.Errorf("supersedes: no memory %q in %s scope. Find the id with action search or list.", a.Supersedes, db.Scope())
		}
	}
	rec, dup, err := db.Add(Record{
		Category: a.Category, Title: a.Title, Body: a.Body, Tags: a.Tags, Files: a.Files,
		Pinned: a.Pinned != nil && *a.Pinned, Source: SourceAgent,
	})
	if err != nil {
		return tools.Errorf("could not save: %v. Give a title and body, and a category from decision, convention, bug, preference, fact, todo.", err)
	}
	var note string
	if old != nil {
		if err := old.Supersede(a.Supersedes, rec.ID); err != nil {
			note = fmt.Sprintf(" It was saved, but could not supersede %q: %v.", a.Supersedes, err)
		} else {
			note = fmt.Sprintf(" It supersedes %s, which is no longer recalled.", shortID(a.Supersedes))
		}
	}
	if dup {
		return tools.Result{
			Output:  fmt.Sprintf("Updated existing memory %s (%s): %s. It matched an earlier record, so the two were merged.", shortID(rec.ID), rec.Scope, rec.Title) + note,
			Summary: "merged",
		}
	}
	out := fmt.Sprintf("Saved memory %s (%s, %s): %s.", shortID(rec.ID), rec.Scope, rec.Category, rec.Title) + note
	// Near matches are reported, not merged: they may be a real second
	// memory, or the one this replaces.
	if sims, err := db.Similar(rec.ID, similarHint, 3); err == nil && len(sims) > 0 {
		var b strings.Builder
		b.WriteString("\nSimilar existing memories (if one of these is replaced by this, save again with supersedes):\n")
		for _, m := range sims {
			fmt.Fprintf(&b, "%s %s/%s: %s\n", shortID(m.ID), m.Scope, m.Category, m.Title)
		}
		out += strings.TrimRight(b.String(), "\n")
	}
	return tools.Result{Output: out, Summary: "saved"}
}

func (t *memoryTool) search(a memoryArgs) tools.Result {
	if strings.TrimSpace(a.Query) == "" {
		return tools.Errorf("search needs a query. Use action list to browse without one.")
	}
	limit := a.Limit
	if limit <= 0 {
		limit = 10
	}
	var all []Scored
	for _, db := range t.dbsFor(a.Scope) {
		got, err := db.Search(a.Query, limit)
		if err != nil {
			return tools.Errorf("search failed: %v", err)
		}
		all = append(all, got...)
	}
	if len(all) == 0 {
		return tools.Result{Output: fmt.Sprintf("No memories match %q.", a.Query), Summary: "0 matches"}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Score > all[j].Score })
	if len(all) > limit {
		all = all[:limit]
	}
	var b strings.Builder
	for _, m := range all {
		writeLine(&b, m.Record)
	}
	return tools.Result{Output: strings.TrimRight(b.String(), "\n"), Summary: fmt.Sprintf("%d matches", len(all))}
}

func (t *memoryTool) list(a memoryArgs) tools.Result {
	limit := a.Limit
	if limit <= 0 {
		limit = 20
	}
	if a.Category != "" && !validCategory(a.Category) {
		return tools.Errorf("unknown category %q. Use decision, convention, bug, preference, fact or todo.", a.Category)
	}
	var recs []Record
	for _, db := range t.dbsFor(a.Scope) {
		got, err := db.List(ListOptions{Category: a.Category, Topic: a.Topic, PinnedOnly: a.Pinned != nil && *a.Pinned, Limit: limit})
		if err != nil {
			return tools.Errorf("list failed: %v", err)
		}
		recs = append(recs, got...)
	}
	if len(recs) == 0 {
		return tools.Result{Output: "No memories saved yet.", Summary: "0 memories"}
	}
	if len(recs) > limit {
		recs = recs[:limit]
	}
	var b strings.Builder
	for _, r := range recs {
		writeLine(&b, r)
	}
	return tools.Result{Output: strings.TrimRight(b.String(), "\n"), Summary: fmt.Sprintf("%d memories", len(recs))}
}

func (t *memoryTool) get(a memoryArgs) tools.Result {
	if a.ID == "" {
		return tools.Errorf("get needs an id. Find it with action search or list.")
	}
	r, _, err := t.find(a)
	if err != nil {
		return tools.Errorf("%v", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s/%s", shortID(r.ID), r.Scope, r.Category)
	if r.Pinned {
		b.WriteString(" [pinned]")
	}
	fmt.Fprintf(&b, ": %s\n%s\n", r.Title, r.Body)
	if len(r.Tags) > 0 {
		fmt.Fprintf(&b, "tags: %s\n", strings.Join(r.Tags, ", "))
	}
	if len(r.Files) > 0 {
		fmt.Fprintf(&b, "files: %s\n", strings.Join(r.Files, ", "))
	}
	fmt.Fprintf(&b, "source: %s; used %d times; last used %s", r.Source, r.Uses, r.LastUsed.Format("2006-01-02"))
	return tools.Result{Output: b.String(), Summary: "1 memory"}
}

// staleAfter and staleUses define a stale record: not used for this long,
// and used fewer than this many times.
const (
	staleAfter = 90 * 24 * time.Hour
	staleUses  = 2
)

// stale lists records that look unused. It deletes nothing: the caller
// decides, with delete or pin.
func (t *memoryTool) stale(a memoryArgs) tools.Result {
	limit := a.Limit
	if limit <= 0 {
		limit = 20
	}
	var recs []Record
	for _, db := range t.dbsFor(a.Scope) {
		got, err := db.StaleCandidates(staleAfter, staleUses, limit)
		if err != nil {
			return tools.Errorf("stale check failed: %v", err)
		}
		recs = append(recs, got...)
	}
	if len(recs) == 0 {
		return tools.Result{Output: "No stale memories.", Summary: "0 stale"}
	}
	if len(recs) > limit {
		recs = recs[:limit]
	}
	var b strings.Builder
	for _, r := range recs {
		writeLine(&b, r)
	}
	b.WriteString("These have not been used in 90 days. Nothing was deleted; delete the ones that no longer apply, or pin the ones that still do.")
	return tools.Result{Output: b.String(), Summary: fmt.Sprintf("%d stale", len(recs))}
}

func (t *memoryTool) delete(a memoryArgs) tools.Result {
	if a.ID == "" {
		return tools.Errorf("delete needs an id. Find it with action search or list.")
	}
	r, db, err := t.find(a)
	if err != nil {
		return tools.Errorf("%v", err)
	}
	if r.Hidden {
		return tools.Errorf("memory %s is already deleted. action restore with this id brings it back.", shortID(r.ID))
	}
	if err := db.Hide(r.ID); err != nil {
		return tools.Errorf("delete failed: %v", err)
	}
	return tools.Result{Output: fmt.Sprintf("Deleted memory %s (%s): %s. It is kept but no longer recalled; action restore with this id brings it back.", shortID(r.ID), r.Scope, r.Title), Summary: "deleted"}
}

// restore brings a soft-deleted memory back into recall, search and list.
func (t *memoryTool) restore(a memoryArgs) tools.Result {
	if a.ID == "" {
		return tools.Errorf("restore needs an id. Soft-deleted memories are listed by the memory modal's hidden tab.")
	}
	r, db, err := t.find(a)
	if err != nil {
		return tools.Errorf("%v", err)
	}
	if err := db.Restore(r.ID); err != nil {
		return tools.Errorf("restore failed: %v", err)
	}
	return tools.Result{Output: fmt.Sprintf("Restored memory %s (%s): %s.", shortID(r.ID), r.Scope, r.Title), Summary: "restored"}
}

func (t *memoryTool) pin(a memoryArgs) tools.Result {
	if a.ID == "" {
		return tools.Errorf("pin needs an id. Find it with action search or list.")
	}
	want := a.Pinned == nil || *a.Pinned
	r, db, err := t.find(a)
	if err != nil {
		return tools.Errorf("%v", err)
	}
	if err := db.Pin(r.ID, want); err != nil {
		return tools.Errorf("pin failed: %v", err)
	}
	if want {
		return tools.Result{Output: fmt.Sprintf("Pinned memory %s (%s): %s. It will be recalled in every session.", shortID(r.ID), r.Scope, r.Title), Summary: "pinned"}
	}
	return tools.Result{Output: fmt.Sprintf("Unpinned memory %s (%s): %s.", shortID(r.ID), r.Scope, r.Title), Summary: "unpinned"}
}

// find locates a record by id or prefix across the scopes a.Scope selects.
func (t *memoryTool) find(a memoryArgs) (Record, *DB, error) {
	for _, db := range t.dbsFor(a.Scope) {
		r, err := db.Get(a.ID)
		if err == nil {
			return r, db, nil
		}
		if err != ErrNotFound {
			return Record{}, nil, err
		}
	}
	return Record{}, nil, fmt.Errorf("no memory with id %q. Find the id with action search or list", a.ID)
}

// dbsFor returns the stores selected by scope: "project", "global", or
// "" / "all" for every open store.
func (t *memoryTool) dbsFor(scope string) []*DB {
	switch scope {
	case ScopeProject:
		if t.stores.Project != nil {
			return []*DB{t.stores.Project}
		}
		return nil
	case ScopeGlobal:
		if t.stores.Global != nil {
			return []*DB{t.stores.Global}
		}
		return nil
	}
	return t.stores.DBs()
}

func writeLine(b *strings.Builder, r Record) {
	mark := ""
	if r.Pinned {
		mark = " [pinned]"
	}
	fmt.Fprintf(b, "%s %s/%s%s: %s", shortID(r.ID), r.Scope, r.Category, mark, r.Title)
	if len(r.Files) > 0 {
		fmt.Fprintf(b, " (files: %s)", strings.Join(r.Files, ", "))
	}
	b.WriteString("\n")
}
