package console

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The New session form is one screen with four questions — where, which
// agent, whether it asks before running commands, and an optional first
// message — and "More options…" for everything else. It mirrors the simple
// face of the web dialog and `lectern claude`'s defaults.

type agentStateMsg struct {
	target     string
	states     map[string]string
	permission string
	err        error
	then       string
}

// probeAgents asks one machine which agent commands are installed, once per
// dashboard run, then continues with what the operator asked for.
func (m *dashboard) probeAgents(target, then string) tea.Cmd {
	if m.busy {
		return nil
	}
	m.busy = true
	m.notice = "Checking which agents are installed…"
	c := m.client
	return func() tea.Msg {
		out := agentStateMsg{target: target, states: map[string]string{}, then: then}
		if settings, err := c.JSON("GET", "/settings", nil); err == nil {
			var values map[string]string
			if json.Unmarshal(settings, &values) == nil {
				out.permission = strings.TrimSpace(values["session_permission_mode"])
			}
		}
		if target == "" {
			return out
		}
		data, err := c.JSON("GET", "/targets/"+target+"/agents", nil)
		if err != nil {
			out.err = err
			return out
		}
		var rows []struct{ Name, State string }
		if err := json.Unmarshal(data, &rows); err != nil {
			out.err = err
			return out
		}
		for _, r := range rows {
			out.states[r.Name] = r.State
		}
		return out
	}
}

func (m *dashboard) receiveAgentState(v agentStateMsg) tea.Cmd {
	m.busy = false
	m.notice = ""
	// A machine that could not be checked keeps an empty map: every agent is
	// offered rather than none.
	m.agentState[v.target] = v.states
	if v.permission != "" {
		m.permissionDefault = v.permission
	}
	if v.then == "session" && m.form == nil {
		return m.newSessionForm()
	}
	return nil
}

// sessionTarget is the machine a new session in this place would run on:
// the project's machine, or the local one for a folder or a scratch room.
func (m *dashboard) sessionTarget(where string) string {
	if strings.HasPrefix(where, "project:") {
		for _, p := range m.projects {
			if "project:"+id(p) == where {
				return str(p["target_id"])
			}
		}
	}
	for _, t := range m.targets {
		if str(t["kind"]) == "local" {
			return id(t)
		}
	}
	if len(m.targets) > 0 {
		return id(m.targets[0])
	}
	return ""
}

// defaultWhere picks the new session's place: the project that contains the
// folder the dashboard was started in, else the selected row's project, else
// the last project used, else that folder itself, else the only project,
// else a scratch room.
func (m *dashboard) defaultWhere() string {
	if m.cwd != "" {
		if p := projectContaining(m.projects, m.cwd); p != "" {
			return "project:" + p
		}
	}
	if r := m.current(); r != nil && sections[m.section] == "sessions" {
		if project := str(r["project_id"]); project != "" {
			return "project:" + project
		}
	}
	if sections[m.section] == "projects" {
		if r := m.current(); r != nil {
			return "project:" + id(r)
		}
	}
	if project := m.defaultProjectChoice(); project != "" {
		return "project:" + project
	}
	if m.scratchNewSession {
		return "scratch" // The last session was started in scratch on purpose.
	}
	if m.cwd != "" {
		return "dir:" + m.cwd
	}
	if len(m.projects) == 1 {
		return "project:" + id(m.projects[0])
	}
	return "scratch"
}

func projectContaining(projects []row, dir string) string {
	dir = filepath.Clean(dir)
	best, bestLen := "", 0
	for _, p := range projects {
		repo := filepath.Clean(str(p["repo_path"]))
		if repo == "." || repo == "" {
			continue
		}
		if dir == repo || strings.HasPrefix(dir, repo+string(filepath.Separator)) {
			if len(repo) > bestLen {
				best, bestLen = id(p), len(repo)
			}
		}
	}
	return best
}

func (m *dashboard) whereChoices() []choice {
	var out []choice
	for _, p := range orderProjects(m.projects, m.recentProjects) {
		label := str(p["name"])
		if path := str(p["repo_path"]); path != "" {
			label += " — " + path
		}
		out = append(out, choice{oneLine(label), "project:" + id(p)})
	}
	if m.cwd != "" && projectContaining(m.projects, m.cwd) == "" {
		out = append(out, choice{"This folder — " + m.cwd, "dir:" + m.cwd})
	}
	return append(out, choice{"New empty scratch folder", "scratch"})
}

// agentChoices lists the agents that are installed on the machine (or that
// could not be checked, such as a custom command), never one that is known
// to be missing, and picks a default the operator would expect.
func (m *dashboard) agentChoices(target, project string, taskOnly bool) ([]choice, string, []string) {
	states := m.agentState[target]
	var available, unchecked []choice
	var missing []string
	for _, a := range m.agents {
		if taskOnly && a["builtin"] != true && a["task"] == nil && a["acp"] == nil {
			continue
		}
		value := str(a["id"])
		if value == "" {
			value = str(a["name"])
		}
		switch states[value] {
		case "missing":
			missing = append(missing, value)
		case "available":
			available = append(available, choice{value, value})
		default:
			label := value
			if len(states) > 0 {
				label += " (not checked)"
			}
			unchecked = append(unchecked, choice{label, value})
		}
	}
	if len(m.agents) == 0 {
		unchecked = []choice{{"claude", "claude"}, {"codex", "codex"}}
	}
	available = m.sortByAgentMenu(available)
	unchecked = m.sortByAgentMenu(unchecked)
	all := append(available, unchecked...)
	def := ""
	for _, p := range m.projects {
		if id(p) == project {
			def = str(p["default_agent"])
		}
	}
	if def == "" || states[def] == "missing" || !hasChoice(all, def) {
		def = ""
		if len(all) > 0 {
			def = all[0].Value
		}
	}
	return all, def, missing
}

func hasChoice(list []choice, value string) bool {
	for _, c := range list {
		if c.Value == value {
			return true
		}
	}
	return false
}

func (m *dashboard) newSessionForm() tea.Cmd {
	where := m.defaultWhere()
	target := m.sessionTarget(where)
	if _, ok := m.agentState[target]; !ok && m.knownTarget(target) {
		return m.probeAgents(target, "session")
	}
	project := strings.TrimPrefix(where, "project:")
	agents, agent, missing := m.agentChoices(target, project, false)
	if len(agents) == 0 {
		m.notice = "No agent CLI was found on this machine (" + strings.Join(missing, ", ") + " are not installed). Install one, or add a custom agent with : → Agent runners."
		return nil
	}
	permission := "ask"
	if m.permissionDefault == "bypass" {
		permission = "bypass"
	}
	whereField := optionField("where", "Where", where, m.whereChoices(), true)
	whereField.Searchable = true
	agentField := optionField("agent", "Agent", agent, agents, true)
	approvals := optionField("permission_mode", "Approvals", permission, []choice{{"Ask before running commands (recommended)", "ask"}, {"Let the agent run without asking", "bypass"}}, true)
	more := field{Key: moreKey, Label: "More options…"}
	adv := func(f field) field { f.Advanced = true; return f }
	targets := options(m.targets, "Same as Where")
	fields := []field{
		whereField, agentField, approvals,
		{Key: "prime", Label: "First message (optional)"},
		more,
		adv(field{Key: "name", Label: "Session name (blank names it after the folder)"}),
		adv(optionField("profile_id", "Launch profile", "", m.profileChoices("Agent and project defaults"), false)),
		adv(field{Key: "model", Label: "Model (blank uses the agent's default)"}),
		adv(optionField("target_id", "Machine (for a folder or scratch)", "", targets, false)),
		adv(boolField("isolated", "Work in a separate Git worktree", false)),
		adv(boolField("multi_repo", "Choose additional repositories after this form", false)),
		adv(field{Key: "worktree_base", Label: "Worktree base (blank = committed HEAD)"}),
		adv(field{Key: "worktree_branch", Label: "New branch (blank = unique name)"}),
		adv(boolField("resume", "Resume the latest conversation", false)),
		adv(boolField("brief", "Include the project brief", true)),
		adv(field{Key: "group_path", Label: "Group path (optional, e.g. Work/Client)"}),
	}
	cmd := m.openForm("New session", fields, func(body map[string]any) tea.Cmd {
		return m.submitNewSession(body)
	})
	if m.form != nil {
		m.form.submitVerb = "start"
		if len(missing) > 0 {
			m.notice = "Not installed here: " + strings.Join(missing, ", ") + "."
		}
	}
	return cmd
}

const moreKey = "__more"

// submitNewSession turns the one-screen answers into the session API's
// fields and keeps every rule the long form enforced.
func (m *dashboard) submitNewSession(body map[string]any) tea.Cmd {
	where := str(body["where"])
	delete(body, "where")
	if strings.TrimSpace(str(body["name"])) == "" {
		if name := m.placeName(where); name != "" {
			body["name"] = m.unusedName(name)
		}
	}
	switch {
	case strings.HasPrefix(where, "project:"):
		n, err := strconv.ParseInt(strings.TrimPrefix(where, "project:"), 10, 64)
		if err != nil || n <= 0 {
			m.notice = "Choose where the agent should work."
			return nil
		}
		body["project_id"] = n
		delete(body, "target_id")
	case strings.HasPrefix(where, "dir:"):
		body["workdir"] = strings.TrimPrefix(where, "dir:")
	default:
		body["scratch"] = true
	}
	if body["multi_repo"] == true {
		if body["isolated"] != true || body["project_id"] == nil || body["resume"] == true {
			m.notice = "Extra repositories need a project, a separate Git worktree and no resume."
			return nil
		}
	}
	if body["profile_id"] != nil {
		delete(body, "agent") // The selected profile determines its agent.
	}
	if body["isolated"] == true {
		body["background"] = true
		body["worktree"] = map[string]any{"base": body["worktree_base"], "branch": body["worktree_branch"]}
	}
	delete(body, "isolated")
	delete(body, "worktree_base")
	delete(body, "worktree_branch")
	multi := body["multi_repo"] == true
	delete(body, "multi_repo")
	if multi {
		return m.workspaceRepositoryForm(body, m.form)
	}
	return m.request("Create session", "POST", "/sessions", body, false)
}

func (m *dashboard) knownTarget(target string) bool {
	for _, t := range m.targets {
		if target != "" && id(t) == target {
			return true
		}
	}
	return false
}

// placeName names a session after where it works, as `lectern claude` does:
// the project, or the folder. A scratch room is left to the server.
func (m *dashboard) placeName(where string) string {
	switch {
	case strings.HasPrefix(where, "project:"):
		for _, p := range m.projects {
			if "project:"+id(p) == where {
				return oneLine(name(p))
			}
		}
	case strings.HasPrefix(where, "dir:"):
		return filepath.Base(strings.TrimPrefix(where, "dir:"))
	}
	return ""
}

// unusedName adds " 2", " 3"… so two sessions in one place stay apart.
func (m *dashboard) unusedName(base string) string {
	taken := map[string]bool{}
	for _, r := range m.rows {
		if r["ended_at"] == nil {
			taken[name(r)] = true
		}
	}
	if !taken[base] {
		return base
	}
	for n := 2; ; n++ {
		if candidate := base + " " + strconv.Itoa(n); !taken[candidate] {
			return candidate
		}
	}
}
