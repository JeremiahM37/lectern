package mcp

// list_agents and create_project: the two tools a chat model needs so it never
// has to guess. list_agents answers "which agents and model IDs does Lectern
// accept?" from Lectern's own catalog (a chat model's training data is older
// than the model line-up and must not be the judge of what exists);
// create_project registers a new project, optionally creating its directory.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// subagentModelEnv is the Claude Code variable that picks the model its
// sub-agents (the "workers" of an orchestrating session) run on.
const subagentModelEnv = "CLAUDE_CODE_SUBAGENT_MODEL"

// knownModels returns the model IDs Lectern lists for an agent, nil on error.
func (s *Server) knownModels(agent string) []string {
	raw, err := s.api("GET", "/models", nil)
	if err != nil {
		return nil
	}
	m, _ := raw.(map[string]any)
	var out []string
	if arr, ok := m[agent].([]any); ok {
		for _, v := range arr {
			if id, ok := v.(string); ok {
				out = append(out, id)
			}
		}
	}
	return out
}

// modelNote reports whether a requested model is in Lectern's list. It never
// blocks or substitutes: the value is passed through verbatim either way.
func (s *Server) modelNote(agent, model string) string {
	if model == "" {
		return ""
	}
	known := s.knownModels(agent)
	for _, k := range known {
		if k == model {
			return ""
		}
	}
	return fmt.Sprintf("model %q is not in Lectern's suggestion list for %s (%s) but was passed to the agent verbatim; "+
		"the list is only suggestions. Check read_session shortly: if the model is invalid, the agent CLI prints its own error there.",
		model, agent, strings.Join(known, ", "))
}

// withModel records the agent and model start_session was given on its result,
// so the caller can see exactly what was requested, plus a warning for an
// unlisted model. The model is never altered.
func (s *Server) withModel(res map[string]any, agent, model string) map[string]any {
	res["agent"] = agent
	if model != "" {
		res["model_requested"] = model
		if n := s.modelNote(agent, model); n != "" {
			res["model_warning"] = n
		}
	}
	return res
}

func init() {
	tools = append(tools, tool{
		Name: "list_agents",
		Description: "Lectern's source of truth for which coding agents are installed and which model IDs each accepts " +
			"(for start_session's `agent` and `model`, create_project's `worker_model`). ALWAYS use this, not your own " +
			"knowledge, to judge whether a model exists: new models appear faster than any chat model's training data. " +
			"The model list is a suggestion list, not a gate — a model you were explicitly told to use may be passed " +
			"to start_session even when it is not listed.",
		Schema: obj(map[string]any{}),
		Run: func(s *Server, _ map[string]any) (any, error) {
			agents, err := s.list("/agents")
			if err != nil {
				return nil, err
			}
			var caps map[string]any
			if raw, err := s.api("GET", "/agents/capabilities", nil); err == nil {
				caps, _ = raw.(map[string]any)
			}
			var models map[string]any
			if raw, err := s.api("GET", "/models", nil); err == nil {
				models, _ = raw.(map[string]any)
			}
			out := make([]map[string]any, 0, len(agents))
			for _, a := range agents {
				name, _ := a["name"].(string)
				mf, _ := a["model_flag"].(string)
				row := map[string]any{
					"name":           name,
					"takes_model":    mf != "",
					"skip_approvals": len(stringSlice(a["yolo_args"])) > 0,
				}
				if c, ok := caps[name].(map[string]any); ok {
					row["installed"] = c["installed"]
				}
				if m, ok := models[name]; ok {
					row["models"] = m
				}
				out = append(out, row)
			}
			sort.Slice(out, func(i, j int) bool { return fmt.Sprint(out[i]["name"]) < fmt.Sprint(out[j]["name"]) })
			return out, nil
		},
	})

	tools = append(tools, tool{
		Name: "create_project",
		Description: "Register a NEW Lectern project (a named working directory sessions can run in). Use when the " +
			"user wants a session on a project that does not exist yet (check list_projects first). `path` is an " +
			"absolute directory under the user's home, or a bare name, which becomes ~/projects/<name>. The " +
			"directory is created if missing; git_init (default true) makes it a git repo with one empty commit so " +
			"worktrees work. `worker_model` sets the model Claude Code sub-agents (the workers of an orchestrating " +
			"session) use, via " + subagentModelEnv + "; `default_agent` picks the project's default agent. Then " +
			"call start_session with project:<name>. Refuses a name that already exists.",
		Schema: obj(map[string]any{
			"name":          str("project name (unique)"),
			"path":          str("absolute directory under the user's home, or a bare name for ~/projects/<name>"),
			"git_init":      flag("git init the directory with an empty first commit if it is not a repo (default true)"),
			"default_agent": str("default agent for sessions in this project, e.g. claude (see list_agents)"),
			"worker_model":  str("model ID for sub-agents/workers (sets " + subagentModelEnv + "); passed verbatim"),
			"target_id":     num("target to register against (default: the local target)"),
		}, "name"),
		Run: func(s *Server, args map[string]any) (any, error) {
			name := strings.TrimSpace(argStr(args, "name"))
			if name == "" {
				return nil, fmt.Errorf("name is required")
			}
			if rows, err := s.list("/projects"); err == nil {
				for _, p := range rows {
					if pn, _ := p["name"].(string); strings.EqualFold(pn, name) {
						return nil, fmt.Errorf("project %q already exists (repo %v) — use it with start_session", pn, p["repo_path"])
					}
				}
			}
			home, err := os.UserHomeDir()
			if err != nil || home == "" {
				return nil, fmt.Errorf("cannot determine the home directory to place the project in")
			}
			p := strings.TrimSpace(argStr(args, "path"))
			if p == "" {
				p = name
			}
			if !filepath.IsAbs(p) {
				if strings.ContainsAny(p, `/\`) || strings.Contains(p, "..") {
					return nil, fmt.Errorf("a relative path must be a bare directory name; pass an absolute path under %s otherwise", home)
				}
				p = filepath.Join(home, "projects", p)
			}
			p = filepath.Clean(p)
			if rel, err := filepath.Rel(home, p); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
				return nil, fmt.Errorf("path %q must be inside %s", p, home)
			}
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return nil, fmt.Errorf("%s exists and is not a directory", p)
			}
			if err := os.MkdirAll(p, 0o755); err != nil {
				return nil, fmt.Errorf("create %s: %w", p, err)
			}
			gitNote := ""
			if argBool(args, "git_init", true) {
				if _, err := os.Stat(filepath.Join(p, ".git")); err != nil {
					cmds := [][]string{{"init", "-q", "-b", "main"},
						{"-c", "user.name=lectern", "-c", "user.email=lectern@localhost", "commit", "-q", "--allow-empty", "-m", "Initial commit"}}
					for _, c := range cmds {
						cmd := exec.Command("git", c...)
						cmd.Dir = p
						if out, err := cmd.CombinedOutput(); err != nil {
							return nil, fmt.Errorf("git %s in %s: %v: %s", c[0], p, err, strings.TrimSpace(string(out)))
						}
					}
					gitNote = "git repo initialised with an empty first commit"
				}
			}
			targetID := argInt(args, "target_id")
			if targetID == 0 {
				if ts, err := s.list("/targets"); err == nil {
					for _, t := range ts {
						if k, _ := t["kind"].(string); k == "local" {
							if f, ok := t["id"].(float64); ok {
								targetID = int64(f)
								break
							}
						}
					}
				}
			}
			body := map[string]any{"name": name, "repo_path": p, "target_id": targetID}
			if a := strings.TrimSpace(argStr(args, "default_agent")); a != "" {
				if err := s.validAgentName(a); err != nil {
					return nil, err
				}
				body["default_agent"] = a
			}
			wm := strings.TrimSpace(argStr(args, "worker_model"))
			if wm != "" {
				body["env"] = map[string]any{subagentModelEnv: wm}
			}
			raw, err := s.api("POST", "/projects", body)
			if err != nil {
				return nil, err
			}
			proj, _ := raw.(map[string]any)
			out := map[string]any{"id": proj["id"], "name": name, "repo": p, "next": "start_session with project " + name}
			if gitNote != "" {
				out["git"] = gitNote
			}
			if wm != "" {
				out["worker_model"] = wm
				if n := s.modelNote("claude", wm); n != "" {
					out["model_warning"] = n
				}
			}
			return out, nil
		},
	})
}
