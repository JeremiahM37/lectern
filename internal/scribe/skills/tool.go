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
	return tools.Errorf("unknown action %q. Use list or load.", a.Action)
}

// namesOrNone lists skill names, closest to want first (substring match),
// so a near-miss name gets a useful suggestion.
func namesOrNone(set *Set, want string) string {
	if len(set.Skills) == 0 {
		return "none installed"
	}
	w := strings.ToLower(want)
	type scored struct {
		name string
		hit  bool
	}
	var all []scored
	for _, sk := range set.Skills {
		n := strings.ToLower(sk.Name)
		all = append(all, scored{sk.Name, strings.Contains(n, w) || strings.Contains(w, n)})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].hit && !all[j].hit })
	var names []string
	for i, s := range all {
		if i == 8 {
			break
		}
		names = append(names, s.name)
	}
	return strings.Join(names, ", ")
}
