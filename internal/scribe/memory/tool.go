package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// memoryTool is the agent-facing `memory` tool.
type memoryTool struct{ stores *Stores }

// Register adds the `memory` tool to r, backed by s.
func Register(r *tools.Registry, s *Stores) { r.Register(&memoryTool{stores: s}) }

type memoryArgs struct {
	Action   string   `json:"action"`
	Scope    string   `json:"scope"`
	ID       string   `json:"id"`
	Category string   `json:"category"`
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	Tags     []string `json:"tags"`
	Files    []string `json:"files"`
	Query    string   `json:"query"`
	Limit    int      `json:"limit"`
	Pinned   *bool    `json:"pinned"`
}

func (t *memoryTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "memory",
		Description: "Durable memory across sessions. Save a decision, convention, bug, preference, fact or todo " +
			"that a later session should know; search or list what is already saved; delete or pin a record by id. " +
			"Save only things that will still matter later, with a short specific title and the file paths it concerns. " +
			"Relevant memories are also recalled automatically at the start of each turn.",
		Schema: tools.Schema(map[string]any{
			"action":   tools.Enum("What to do.", "save", "search", "list", "delete", "pin"),
			"scope":    tools.Enum("project (this repo) or global (all repos). Default: project for save; all for search, list, delete and pin.", "project", "global", "all"),
			"id":       tools.Str("Record id or unique id prefix (delete, pin)."),
			"category": tools.Enum("Record category (save), or filter (list).", "decision", "convention", "bug", "preference", "fact", "todo"),
			"title":    tools.Str("Short headline, max 200 characters (save)."),
			"body":     tools.Str("The detail: what, why, and what to do about it (save)."),
			"tags":     tools.Arr("Free-form tags (save).", map[string]any{"type": "string"}),
			"files":    tools.Arr("Relative file paths the memory concerns. Recall surfaces it when these files are touched (save).", map[string]any{"type": "string"}),
			"query":    tools.Str("What to look for (search)."),
			"limit":    tools.Int("Maximum results (search: default 10; list: default 20)."),
			"pinned":   tools.Bool("pin: true (default) or false to unpin. list: true shows only pinned records."),
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
	switch a.Action {
	case "save":
		return t.save(a)
	case "search":
		return t.search(a)
	case "list":
		return t.list(a)
	case "delete":
		return t.delete(a)
	case "pin":
		return t.pin(a)
	case "":
		return tools.Errorf("action is required: save, search, list, delete or pin.")
	}
	return tools.Errorf("unknown action %q. Use save, search, list, delete or pin.", a.Action)
}

func (t *memoryTool) save(a memoryArgs) tools.Result {
	scope := a.Scope
	if scope == "" {
		scope = ScopeProject
		if t.stores.Project == nil {
			scope = ScopeGlobal
		}
	}
	db, err := t.stores.DB(scope)
	if err != nil {
		return tools.Errorf("%v", err)
	}
	rec, dup, err := db.Add(Record{
		Category: a.Category, Title: a.Title, Body: a.Body, Tags: a.Tags, Files: a.Files,
		Pinned: a.Pinned != nil && *a.Pinned,
	})
	if err != nil {
		return tools.Errorf("could not save: %v. Give a title and body, and a category from decision, convention, bug, preference, fact, todo.", err)
	}
	if dup {
		return tools.Result{
			Output:  fmt.Sprintf("Updated existing memory %s (%s): %s. It matched an earlier record, so the two were merged.", shortID(rec.ID), rec.Scope, rec.Title),
			Summary: "merged",
		}
	}
	return tools.Result{
		Output:  fmt.Sprintf("Saved memory %s (%s, %s): %s.", shortID(rec.ID), rec.Scope, rec.Category, rec.Title),
		Summary: "saved",
	}
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
		got, err := db.List(ListOptions{Category: a.Category, PinnedOnly: a.Pinned != nil && *a.Pinned, Limit: limit})
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

func (t *memoryTool) delete(a memoryArgs) tools.Result {
	if a.ID == "" {
		return tools.Errorf("delete needs an id. Find it with action search or list.")
	}
	r, db, err := t.find(a)
	if err != nil {
		return tools.Errorf("%v", err)
	}
	if err := db.Delete(r.ID); err != nil {
		return tools.Errorf("delete failed: %v", err)
	}
	return tools.Result{Output: fmt.Sprintf("Deleted memory %s (%s): %s.", shortID(r.ID), r.Scope, r.Title), Summary: "deleted"}
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
