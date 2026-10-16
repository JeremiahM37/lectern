package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// skillsTool is the agent-facing `skills` tool. It re-discovers on every
// call, so a skill installed mid-session is visible at once.
type skillsTool struct{}

// Register adds the `skills` tool to r. The project root comes from the
// call's Env.Workdir.
func Register(r *tools.Registry) { r.Register(skillsTool{}) }

type skillsArgs struct {
	Action string `json:"action"`
	Name   string `json:"name"`
}

func (skillsTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "skills",
		Description: "Skills are instruction packs kept in SKILL.md files. action list shows the installed skills. " +
			"action load with a name returns that skill's instructions and the other files in its directory. " +
			"Load a skill when its description fits the task, then follow it.",
		Schema: tools.Schema(map[string]any{
			"action": tools.Enum("list or load.", "list", "load"),
			"name":   tools.Str("Skill name (load)."),
		}, "action"),
	}
}

// ReadOnly reports true: the tool only reads skill directories.
func (skillsTool) ReadOnly() bool { return true }

func (skillsTool) Run(_ context.Context, env *tools.Env, raw json.RawMessage) tools.Result {
	var a skillsArgs
	if err := tools.Decode(raw, &a); err != nil {
		return tools.Errorf("%v. Pass an object with an \"action\" of list or load.", err)
	}
	root := ""
	if env != nil {
		root = env.Workdir
	}
	set, err := Discover(root)
	if err != nil {
		return tools.Errorf("skill discovery failed: %v", err)
	}
	switch a.Action {
	case "list":
		if len(set.Skills) == 0 {
			return tools.Result{Output: "No skills installed. Put a directory with SKILL.md under .scribe/skills/ or ~/.scribe/skills/ to add one.", Summary: "0 skills"}
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
	case "load":
		if strings.TrimSpace(a.Name) == "" {
			return tools.Errorf("load needs a name. Use action list to see the names.")
		}
		text, err := set.Render(a.Name)
		if err != nil {
			return tools.Errorf("%v. Available: %s", err, namesOrNone(set, a.Name))
		}
		return tools.Result{Output: text, Summary: "loaded " + a.Name}
	case "":
		return tools.Errorf("action is required: list or load.")
	}
	if near := closest(a.Action, []string{"list", "load"}); near != "" {
		return tools.Errorf("unknown action %q. Did you mean %q? Use list or load.", a.Action, near)
	}
	return tools.Errorf("unknown action %q. Use list or load.", a.Action)
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
