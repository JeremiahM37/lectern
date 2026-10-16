package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// maxSearchShown caps the search results the model is shown.
const maxSearchShown = 20

// skillsTool is the agent-facing `skills` tool. It re-discovers on every
// call, so a skill installed mid-session is visible at once. It also keeps
// the set of skills loaded this session (one tool per session registry), so
// list_active and unload can answer for it.
type skillsTool struct {
	// registry answers search. Nil means the public registry.
	registry *Registry

	mu     sync.Mutex
	active []string // names loaded this session, in load order
}

// Register adds the `skills` tool to r. The project root comes from the
// call's Env.Workdir.
func Register(r *tools.Registry) { r.Register(&skillsTool{}) }

type skillsArgs struct {
	Action string `json:"action"`
	Name   string `json:"name"`
	Query  string `json:"query"`
	ID     string `json:"id"`
	Global bool   `json:"global"`
}

func (*skillsTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "skills",
		Description: "Skills are instruction packs kept in SKILL.md files. action list shows the installed skills. " +
			"action load with a name returns that skill's instructions and the other files in its directory. " +
			"action list_active shows the skills loaded this session; unload with a name stops following one. " +
			"action search with a query finds skills in the public registry; install with the id from search " +
			"(owner/repo#skill, or a git URL or local directory) adds one to the project (global true for all projects). " +
			"Load a skill when its description fits the task, then follow it.",
		Schema: tools.Schema(map[string]any{
			"action": tools.Enum("What to do.", "list", "search", "list_active", "load", "unload", "install"),
			"name":   tools.Str("Skill name (load, unload)."),
			"query":  tools.Str("What to search the registry for (search)."),
			"id":     tools.Str("Install source from search, e.g. owner/repo#skill (install)."),
			"global": tools.Bool("Install for every project, not just this one (install)."),
		}, "action"),
	}
}

// ReadOnly reports true for the actions that only read: list, search,
// list_active, load and unload. Install writes a skill, so ReadOnlyCall
// reports per call.
func (*skillsTool) ReadOnly() bool { return true }

// ReadOnlyCall reports whether this call only reads. Only install writes
// (it copies a skill onto disk), so plan modes allow every other action.
func (*skillsTool) ReadOnlyCall(raw json.RawMessage) bool {
	var a skillsArgs
	if err := tools.Decode(raw, &a); err != nil {
		return false
	}
	return normalizeAction(a.Action) != "install"
}

// normalizeAction folds the spellings a model tends to use onto the action
// names. The empty string is returned unchanged so the caller can report it.
func normalizeAction(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "_")
	switch s {
	case "list_installed", "installed", "ls":
		return "list"
	}
	return s
}

func (t *skillsTool) Run(ctx context.Context, env *tools.Env, raw json.RawMessage) tools.Result {
	var a skillsArgs
	if err := tools.Decode(raw, &a); err != nil {
		return tools.Errorf("%v. Pass an object with an \"action\": list, search, list_active, load, unload or install.", err)
	}
	root := ""
	if env != nil {
		root = env.Workdir
	}
	action := normalizeAction(a.Action)
	switch action {
	case "search":
		return t.search(ctx, firstNonEmpty(a.Query, a.Name))
	case "install":
		return t.install(root, firstNonEmpty(a.ID, a.Name), a.Global)
	}
	set, err := Discover(root)
	if err != nil {
		return tools.Errorf("skill discovery failed: %v", err)
	}
	switch action {
	case "list":
		if len(set.Skills) == 0 {
			return tools.Result{Output: "No skills installed. Put a directory with SKILL.md under .scribe/skills/ or ~/.scribe/skills/ to add one, or use action search and install.", Summary: "0 skills"}
		}
		var b strings.Builder
		for _, sk := range set.Skills {
			desc := sk.Description
			if desc == "" {
				desc = "(no description)"
			}
			fmt.Fprintf(&b, "%s (%s): %s\n", sk.Name, sk.Scope, desc)
		}
		return tools.Result{Output: strings.TrimRight(b.String(), "\n"), Summary: fmt.Sprintf("%d skills", len(set.Skills))}
	case "list_active":
		return t.listActive(set)
	case "load":
		if strings.TrimSpace(a.Name) == "" {
			return tools.Errorf("load needs a name. Use action list to see the names.")
		}
		text, err := set.Render(a.Name)
		if err != nil {
			return tools.Errorf("%v. Available: %s", err, namesOrNone(set, a.Name))
		}
		sk, _ := set.Find(a.Name)
		t.markActive(sk.Name)
		return tools.Result{Output: text, Summary: "loaded " + a.Name}
	case "unload":
		return t.unload(a.Name)
	case "":
		return tools.Errorf("action is required: list, search, list_active, load, unload or install.")
	}
	if near := closest(a.Action, []string{"list", "search", "list_active", "load", "unload", "install"}); near != "" {
		return tools.Errorf("unknown action %q. Did you mean %q? Use list, search, list_active, load, unload or install.", a.Action, near)
	}
	return tools.Errorf("unknown action %q. Use list, search, list_active, load, unload or install.", a.Action)
}

// firstNonEmpty returns the first argument that is not blank.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// markActive records name as loaded, once, in load order.
func (t *skillsTool) markActive(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, n := range t.active {
		if n == name {
			return
		}
	}
	t.active = append(t.active, name)
}

// unload drops name from the loaded set. The earlier load result stays in the
// conversation, so the reply says the model should stop following it.
func (t *skillsTool) unload(name string) tools.Result {
	name = strings.TrimSpace(name)
	if name == "" {
		return tools.Errorf("unload needs a name. Use action list_active to see the loaded skills.")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, n := range t.active {
		if n == name {
			t.active = append(t.active[:i], t.active[i+1:]...)
			return tools.Result{
				Output:  fmt.Sprintf("Unloaded %s. Its instructions from the earlier load are still in the conversation; stop following them.", name),
				Summary: "unloaded " + name,
			}
		}
	}
	return tools.Errorf("%s is not loaded. Use action list_active to see the loaded skills.", name)
}

// listActive shows the skills loaded this session and whether each is still
// installed.
func (t *skillsTool) listActive(set *Set) tools.Result {
	t.mu.Lock()
	names := append([]string(nil), t.active...)
	t.mu.Unlock()
	if len(names) == 0 {
		return tools.Result{Output: "No skills are loaded. Use action load with a name from action list.", Summary: "0 active"}
	}
	var b strings.Builder
	for _, n := range names {
		if sk, ok := set.Find(n); ok {
			fmt.Fprintf(&b, "%s (%s)\n", n, sk.Scope)
		} else {
			fmt.Fprintf(&b, "%s (no longer installed)\n", n)
		}
	}
	return tools.Result{Output: strings.TrimRight(b.String(), "\n"), Summary: fmt.Sprintf("%d active", len(names))}
}

// search lists registry skills matching q. An unreachable registry is an
// error that says what to do instead, not a silent empty list.
func (t *skillsTool) search(ctx context.Context, q string) tools.Result {
	if q == "" {
		return tools.Errorf("search needs a query: pass query with a word or two about the skill, such as \"review\".")
	}
	reg := t.registry
	if reg == nil {
		reg = NewRegistry()
	}
	entries, err := reg.Search(ctx, q)
	if errors.Is(err, ErrRegistryUnavailable) {
		return tools.Errorf("the skill registry could not be reached (%v). Install a skill from a git URL or local directory with action install and id instead.", err)
	}
	if err != nil {
		return tools.Errorf("search failed: %v", err)
	}
	if len(entries) == 0 {
		return tools.Result{Output: fmt.Sprintf("No registry skills match %q.", q), Summary: "0 matches"}
	}
	var b strings.Builder
	for i, e := range entries {
		if i == maxSearchShown {
			fmt.Fprintf(&b, "... %d more not shown; narrow the query.\n", len(entries)-maxSearchShown)
			break
		}
		fmt.Fprintf(&b, "%s/%s (install id %s#%s, %d installs)\n", e.Source, e.SkillID, e.Source, e.SkillID, e.Installs)
	}
	return tools.Result{Output: strings.TrimRight(b.String(), "\n"), Summary: fmt.Sprintf("%d matches", len(entries))}
}

// install copies a skill into the project or, with global, into the global
// skills directory. id is an install source: a local directory, a git URL, or
// owner/repo#skill as search prints it.
func (t *skillsTool) install(root, id string, global bool) tools.Result {
	if id == "" {
		return tools.Errorf("install needs an id: pass id as printed by search (owner/repo#skill), or a git URL or local directory.")
	}
	scope := ScopeProject
	if global {
		scope = ScopeGlobal
	}
	sk, err := Install(root, installSource(id), scope)
	if err != nil {
		return tools.Errorf("install %s failed: %v", id, err)
	}
	return tools.Result{
		Output:  fmt.Sprintf("Installed skill %s (%s) at %s. Load it with action load and name %s.", sk.Name, sk.Scope, sk.Dir, sk.Name),
		Summary: "installed " + sk.Name,
	}
}

// installSource turns the owner/repo#skill form search prints into the
// GitHub clone source Install takes. Anything that is a local path or already
// a URL is returned unchanged.
func installSource(id string) string {
	base, sub, hasSub := strings.Cut(id, "#")
	if _, err := os.Stat(base); err == nil || strings.Count(base, "/") != 1 || strings.Contains(base, "://") {
		return id
	}
	if !repoRe.MatchString(base) {
		return id
	}
	src := "https://github.com/" + base + ".git"
	if hasSub {
		src += "#" + sub
	}
	return src
}

// namesOrNone lists up to eight skill names for a name that did not match.
// Names that contain the wanted text, or that it contains, come first, then
// the rest by edit distance, so a near-miss gets a useful suggestion.
func namesOrNone(set *Set, want string) string {
	if len(set.Skills) == 0 {
		return "none installed"
	}
	w := strings.ToLower(want)
	type scored struct {
		name string
		hit  bool
		dist int
	}
	var all []scored
	for _, sk := range set.Skills {
		n := strings.ToLower(sk.Name)
		all = append(all, scored{sk.Name, strings.Contains(n, w) || strings.Contains(w, n), editDistance(n, w)})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].hit != all[j].hit {
			return all[i].hit
		}
		return all[i].dist < all[j].dist
	})
	var names []string
	for i, s := range all {
		if i == 8 {
			break
		}
		names = append(names, s.name)
	}
	return strings.Join(names, ", ")
}

// closest returns the option within a small edit distance of word (and at
// most a third of its length), or "" when none is near enough.
func closest(word string, options []string) string {
	w := strings.ToLower(strings.TrimSpace(word))
	if w == "" {
		return ""
	}
	best, bestD := "", -1
	for _, o := range options {
		d := editDistance(w, o)
		if bestD < 0 || d < bestD {
			best, bestD = o, d
		}
	}
	if bestD <= 2 && bestD*3 <= len(w)+2 {
		return best
	}
	return ""
}

// editDistance is the Levenshtein distance between a and b, in runes.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
