package console

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

type choice struct{ Label, Value string }
type field struct {
	Key, Label, Value   string
	Options             []choice
	Multiline, Required bool
	Compact             bool
	Searchable          bool
	HideWhenProject     bool
	OptionFilter        string
	OptionCursor        int
}
type dashboardForm struct {
	title     string
	fields    []field
	index     int
	editor    textarea.Model
	submit    func(map[string]any) tea.Cmd
	cancel    func() tea.Cmd
	workspace *workspaceDraft
	// projectHiddenValues keeps a user's scratch target/directory draft while a
	// project is selected. Hidden values never enter formBody; restoring them
	// when the project is cleared makes switching back predictable.
	projectHiddenValues map[string]string
}
type dashboardAction struct {
	Label, Method, Path, Operation, Warning string
	Body                                    any
}
type loadedFormMsg struct {
	title, method, path string
	data                []byte
	err                 error
}
type discoveredMsg struct {
	rows []row
	err  error
}
type skillsLoadedMsg struct {
	path string
	data []byte
	err  error
}

func (m *dashboard) request(label, method, path string, body any, preview bool) tea.Cmd {
	if m.busy {
		return nil
	}
	m.busy = true
	c, key := m.client, m.key()
	return func() tea.Msg {
		b, e := c.JSON(method, path, body)
		return resultMsg{label: label, data: b, err: e, preview: preview, key: key}
	}
}
func (m *dashboard) execute(a dashboardAction) tea.Cmd {
	return m.request(a.Label, a.Method, a.Path, a.Body, false)
}
func (m *dashboard) choose(a dashboardAction) tea.Cmd {
	if a.Warning != "" {
		m.pending = &a
		return nil
	}
	switch a.Operation {
	case "recent-sessions":
		return m.loadRecentSessions()
	case "extend-workspace":
		return m.extendWorkspaceForm()
	case "cancel-extension":
		return m.workspaceExtensionAction("cancel")
	case "recover-extension":
		return m.workspaceExtensionAction("recover")
	case "launch-profiles":
		return m.manageProfilesForm()
	case "agents":
		return m.manageAgentsForm()
	case "toggle-group":
		m.toggleGroup()
		return nil
	case "collapse-group":
		m.collapseGroup()
		return nil
	case "expand-group":
		m.expandGroup()
		return nil
	case "attach":
		if m.controlOnly {
			return m.nativeDisabled()
		}
		return m.attachSelected(false)
	case "shell":
		if m.controlOnly {
			return m.nativeDisabled()
		}
		return m.attachSelected(true)
	case "blank-shell":
		if m.controlOnly {
			return m.nativeDisabled()
		}
		return m.newShellForm()
	case "group":
		return m.groupForm()
	case "mcp":
		return m.mcpSettingsForm()
	case "skills":
		return m.skillsSettingsForm()
	case "workflows":
		return m.workflowsForm()
	case "rename":
		return m.renameForm()
	case "edit":
		return m.editCommonForm()
	case "advanced":
		return m.editForm()
	case "send":
		return m.sendForm()
	case "upload":
		return m.uploadForm()
	case "history":
		return m.readDetail("History")
	case "saved-history":
		return m.savedConversations()
	case "review":
		return m.openReview()
	case "diff":
		return m.readDetail("Diff")
	case "files":
		return m.readResource("Files", "/term/"+strings.TrimSuffix(sections[m.section], "s")+"/"+id(m.current())+"/files")
	case "followup":
		return m.messageAction("Request changes", "/tasks/"+id(m.current())+"/followup", "feedback")
	case "commit":
		return m.messageAction("Commit task changes", "/tasks/"+id(m.current())+"/commit", "message")
	case "handoff":
		return m.request("Request handoff", "POST", "/sessions/"+id(m.current())+"/handoff", map[string]any{}, false)
	case "promote-conversation":
		return m.loadPromotionPreview()
	}
	if a.Method == "GET" {
		return m.readResource(a.Label, a.Path)
	}
	return m.execute(a)
}
func (m *dashboard) actions() []dashboardAction {
	list := m.allActions()
	if !m.controlOnly {
		return list
	}
	filtered := make([]dashboardAction, 0, len(list))
	for _, action := range list {
		if nativeTerminalAction(action.Operation) {
			continue
		}
		filtered = append(filtered, action)
	}
	return filtered
}

// nativeTerminalAction reports whether an action opens another native
// terminal. A controls popup omits these so it cannot attach inside itself.
func nativeTerminalAction(operation string) bool {
	switch operation {
	case "attach", "shell", "blank-shell":
		return true
	}
	return false
}
func (m *dashboard) allActions() []dashboardAction {
	actions := m.rowActions()
	profile := dashboardAction{Label: "Manage launch profiles", Operation: "launch-profiles"}
	agents := dashboardAction{Label: "Manage agent runners", Operation: "agents"}
	blankShell := dashboardAction{Label: "Open blank shell", Operation: "blank-shell"}
	if len(actions) == 0 {
		if sections[m.section] == "sessions" {
			return []dashboardAction{blankShell, profile, {Label: "Recently closed", Operation: "recent-sessions"}}
		}
		return []dashboardAction{profile}
	}
	// Keep attachment first and destructive actions last.
	last := actions[len(actions)-1]
	if sections[m.section] == "sessions" {
		return append(actions[:len(actions)-1], agents, profile, blankShell, dashboardAction{Label: "Recently closed", Operation: "recent-sessions"}, last)
	}
	return append(actions[:len(actions)-1], agents, profile, last)
}
func (m *dashboard) rowActions() []dashboardAction {
	if m.selectedGroup() != nil {
		return []dashboardAction{
			{Label: "Expand / collapse group", Operation: "toggle-group"},
			{Label: "Collapse parent group", Operation: "collapse-group"},
			{Label: "Expand / enter group", Operation: "expand-group"},
		}
	}
	r := m.current()
	if r == nil {
		return nil
	}
	kind := sections[m.section]
	path := "/" + kind + "/" + id(r)
	op := func(label, operation string) dashboardAction {
		return dashboardAction{Label: label, Operation: operation}
	}
	post := func(label, suffix string) dashboardAction {
		return dashboardAction{Label: label, Method: "POST", Path: path + suffix, Body: map[string]any{}}
	}
	read := func(label, suffix string) dashboardAction {
		return dashboardAction{Label: label, Method: "GET", Path: path + suffix}
	}
	var actions []dashboardAction
	switch kind {
	case "sessions":
		if r["setup_state"] == "creating" {
			label := "Cancel setup"
			if r["setup_cancel_requested"] == true {
				label = "Retry cancellation"
			}
			return append([]dashboardAction{post(label, "/setup/cancel"), op("Rename", "rename"), op("Move to group", "group")}, workspaceActions(r, path)...)
		}
		if r["archived_at"] != nil {
			return append([]dashboardAction{{Label: "Unarchive record", Method: "DELETE", Path: path + "/archive"}, read("Archived terminal output", "/archive/history"), op("Saved conversations", "saved-history"), op("Rename", "rename"), op("Move to group", "group")}, workspaceActions(r, path)...)
		}
		archive := dashboardAction{Label: "Stop and archive", Method: "POST", Path: path + "/archive", Body: map[string]any{"stop": true}, Warning: "Stop this terminal process and move its record to Archive? Captured output, saved conversations and worktree files are retained. Unarchiving will not restart it."}
		if r["ended_at"] != nil {
			actions = []dashboardAction{op("Saved conversations", "saved-history"), op("Rename", "rename"), op("Move to group", "group"), read("Handoff summaries", "/wraps"), {Label: "Archive stopped record", Method: "POST", Path: path + "/archive", Body: map[string]any{"stop": false}}}
			if r["can_restore"] == true {
				actions = append([]dashboardAction{post("Track again", "/restore")}, actions...)
			}
			return append(actions, workspaceActions(r, path)...)
		}
		actions = []dashboardAction{op("Attach", "attach"), op("Companion shell", "shell"), op("Send message", "send"), op("Upload context file", "upload"), op("Review changes", "review"), op("Read history", "history"), op("Saved conversations", "saved-history"), op("Browse files", "files"), op("Rename", "rename"), op("Move / edit session", "edit"), op("Move to group", "group"), op("Request handoff", "handoff"), read("Handoff summaries", "/wraps")}
		if r["project_id"] == nil && (str(r["agent"]) == "shell" || str(r["agent"]) == "claude" || str(r["agent"]) == "codex") {
			actions = append(actions, op("Promote conversation", "promote-conversation"))
		}
		actions = append(actions, workspaceActions(r, path)...)
		actions = append(actions, archive)
		actions = append(actions, dashboardAction{Label: "Interrupt agent", Method: "POST", Path: path + "/send", Body: map[string]any{"key": "C-c"}, Warning: "Send Ctrl-c to this session's current command?"})
	case "tasks":
		actions = []dashboardAction{op("Attach to attempt", "attach"), op("Companion shell", "shell"), op("Send message", "send"), op("Upload context file", "upload"), op("Review diff", "diff"), op("Review live changes", "review"), read("Messages", "/messages"), read("Events", "/events"), post("Dispatch in worktree", "/dispatch"), post("Take over as interactive session", "/takeover"), op("Request changes", "followup"), op("Commit changes", "commit"), post("Mark complete", "/complete"), op("Edit task", "edit")}
		cancel := post("Cancel task", "/cancel")
		cancel.Warning = "Stop this task's active attempt?"
		actions = append(actions, cancel)
	case "routines":
		actions = []dashboardAction{post("Run now", "/run"), {Label: "Enable schedule", Method: "PATCH", Path: path, Body: map[string]any{"enabled": true}}, {Label: "Disable schedule", Method: "PATCH", Path: path, Body: map[string]any{"enabled": false}}, op("Rename", "rename"), op("Edit routine", "edit")}
	case "projects":
		actions = []dashboardAction{op("Open project shell", "attach"), op("Review changes", "review"), read("Project brief", "/brief"), read("Notes", "/notes"), read("Handoffs", "/wraps"), read("Capabilities", "/capability"), op("Rename", "rename"), op("Edit project", "edit"), op("MCP settings (add / edit / remove)", "mcp"), op("Skills (attach / detach)", "skills")}
		actions = append(actions, op("Workflows (Spec Kit / Maestro)", "workflows"))
	case "targets":
		actions = []dashboardAction{post("Check connection", "/check"), read("Check agent commands", "/agents"), op("Rename", "rename"), op("Edit target", "edit")}
	case "approvals":
		actions = []dashboardAction{{Label: "Approve", Method: "POST", Path: path + "/decision", Body: map[string]any{"decision": "approved"}, Warning: "Allow the selected pending tool request?"}, {Label: "Deny", Method: "POST", Path: path + "/decision", Body: map[string]any{"decision": "denied"}}}
	}
	if kind != "approvals" {
		actions = append(actions, op("Edit advanced fields (JSON)", "advanced"))
		warning := "Delete this " + strings.TrimSuffix(kind, "s") + "?"
		label := "Delete"
		if kind == "sessions" {
			if str(r["origin"]) == "discovered" {
				label = "Stop tracking (leave running)"
				warning = "Remove this adopted session from tracking? It keeps running. Use z to include untracked records, then m → Track again; f finds other running sessions."
			} else {
				label = "End session"
				warning = "Stop this Lectern-owned session and remove it from tracking?"
			}
		}
		actions = append(actions, dashboardAction{Label: label, Method: "DELETE", Path: path, Warning: warning})
	}
	return actions
}
func (m *dashboard) openForm(title string, fields []field, submit func(map[string]any) tea.Cmd) tea.Cmd {
	if len(fields) == 0 {
		return nil
	}
	e := textarea.New()
	e.ShowLineNumbers = false
	e.Prompt = "│ "
	e.CharLimit = 100000
	e.SetWidth(max(10, m.width-10))
	e.SetHeight(5)
	m.form = &dashboardForm{title: title, fields: fields, editor: e, submit: submit,
		projectHiddenValues: map[string]string{}}
	m.notice = ""
	return m.focusField()
}
func (m *dashboard) focusField() tea.Cmd {
	f := m.form
	current := &f.fields[f.index]
	f.editor.SetValue(current.Value)
	if current.Multiline {
		f.editor.SetHeight(max(3, min(7, m.height-16)))
	} else {
		f.editor.SetHeight(1)
	}
	if len(current.Options) > 0 {
		f.editor.Blur()
		current.OptionFilter = ""
		current.OptionCursor = optionIndex(*current, current.Value, current.OptionFilter)
		return nil
	}
	return f.editor.Focus()
}

func fieldVisible(fields []field, index int) bool {
	if index < 0 || index >= len(fields) {
		return false
	}
	if !fields[index].HideWhenProject {
		return true
	}
	for _, f := range fields {
		if f.Key == "project_id" {
			return strings.TrimSpace(f.Value) == ""
		}
	}
	return true
}

func nextVisibleField(fields []field, index, delta int) int {
	if len(fields) == 0 {
		return 0
	}
	for n := 0; n < len(fields); n++ {
		index = (index + delta + len(fields)) % len(fields)
		if fieldVisible(fields, index) {
			return index
		}
	}
	return index
}

func (m *dashboard) syncProjectTarget() {
	projectSelected := false
	for _, f := range m.form.fields {
		if f.Key == "project_id" {
			projectSelected = strings.TrimSpace(f.Value) != ""
			break
		}
	}
	for i := range m.form.fields {
		if !m.form.fields[i].HideWhenProject {
			continue
		}
		if projectSelected {
			if m.form.fields[i].Value != "" {
				m.form.projectHiddenValues[m.form.fields[i].Key] = m.form.fields[i].Value
			}
			m.form.fields[i].Value = ""
			continue
		}
		if m.form.fields[i].Value == "" {
			if value := m.form.projectHiddenValues[m.form.fields[i].Key]; value != "" {
				m.form.fields[i].Value = value
			}
		}
	}
}
func (m *dashboard) saveField() {
	f := m.form
	if len(f.fields[f.index].Options) == 0 {
		f.fields[f.index].Value = f.editor.Value()
	}
}
func (m *dashboard) updateForm(msg tea.KeyMsg) tea.Cmd {
	f := m.form
	if m.busy {
		if msg.String() == "esc" {
			m.notice = "Waiting for the current request; your fields are preserved."
		}
		return nil
	}
	current := &f.fields[f.index]
	noun := optionNoun(*current)
	if len(current.Options) > 0 && current.Searchable {
		if msg.String() == "ctrl+u" {
			current.OptionFilter = ""
			current.OptionCursor = optionIndex(*current, current.Value, current.OptionFilter)
			m.notice = strings.Title(noun) + " filter cleared."
			return nil
		}
		if msg.String() == "backspace" {
			if current.OptionFilter != "" {
				r := []rune(current.OptionFilter)
				current.OptionFilter = string(r[:len(r)-1])
				current.OptionCursor = optionIndex(*current, current.Value, current.OptionFilter)
			}
			return nil
		}
		if len(msg.Runes) > 0 {
			current.OptionFilter += string(msg.Runes)
			current.OptionCursor = optionIndex(*current, current.Value, current.OptionFilter)
			return nil
		}
	}
	switch msg.String() {
	case "esc":
		if f.cancel != nil {
			m.saveField()
			return f.cancel()
		}
		m.form = nil
		m.notice = "Cancelled"
		m.showHome()
		return nil
	case "ctrl+s":
		if len(current.Options) > 0 && current.Searchable && current.OptionFilter != "" && len(filteredChoices(*current)) == 0 {
			m.notice = "No matching " + noun + ". Clear the filter with Ctrl-u or Backspace."
			return nil
		}
		if current.Searchable && !commitOptionSelection(current) {
			m.notice = "No matching " + noun + ". Clear the filter with Ctrl-u or Backspace."
			return nil
		}
		if current.Key == "project_id" || current.Key == "project_ids" {
			m.syncProjectTarget()
		}
		m.saveField()
		body, e := formBody(f.fields)
		if e != nil {
			m.notice = e.Error()
			return nil
		}
		return f.submit(body)
	case "tab", "shift+tab":
		if msg.String() == "tab" && current.Searchable && !commitOptionSelection(current) {
			m.notice = "No matching " + noun + ". Clear the filter with Ctrl-u or Backspace."
			return nil
		}
		if msg.String() == "tab" && (current.Key == "project_id" || current.Key == "project_ids") {
			m.syncProjectTarget()
		}
		m.saveField()
		delta := 1
		if msg.String() == "shift+tab" {
			delta = -1
		}
		f.index = nextVisibleField(f.fields, f.index, delta)
		return m.focusField()
	case "enter":
		if current.Searchable {
			if !commitOptionSelection(current) {
				m.notice = "No matching " + noun + ". Clear the filter with Ctrl-u or Backspace."
				return nil
			}
			m.syncProjectTarget()
			if f.title == "Blank persistent shell" {
				m.saveField()
				body, err := formBody(f.fields)
				if err != nil {
					m.notice = err.Error()
					return nil
				}
				return f.submit(body)
			}
			f.index = nextVisibleField(f.fields, f.index, 1)
			return m.focusField()
		}
		if !f.fields[f.index].Multiline {
			m.saveField()
			f.index = nextVisibleField(f.fields, f.index, 1)
			return m.focusField()
		}
	}
	if len(current.Options) > 0 {
		delta := 0
		switch msg.String() {
		case "left", "up", "k":
			if current.Searchable && msg.String() == "k" && current.OptionFilter != "" {
				break
			}
			delta = -1
		case "right", "down", "j", " ":
			if current.Searchable && msg.String() == "j" && current.OptionFilter != "" {
				break
			}
			delta = 1
		}
		if delta != 0 {
			options := filteredChoices(*current)
			if len(options) == 0 {
				return nil
			}
			current.OptionCursor = (current.OptionCursor + delta + len(options)) % len(options)
			current.Value = options[current.OptionCursor].Value
			if current.Key == "project_id" || current.Key == "project_ids" {
				m.syncProjectTarget()
			}
		}
		return nil
	}
	var cmd tea.Cmd
	f.editor, cmd = f.editor.Update(msg)
	return cmd
}
func formBody(fields []field) (map[string]any, error) {
	out := map[string]any{}
	for i, f := range fields {
		if !fieldVisible(fields, i) {
			continue
		}
		v := f.Value
		if !f.Multiline {
			v = strings.TrimSpace(v)
		}
		if f.Required && strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("%s is required", f.Label)
		}
		if v == "" {
			continue
		}
		switch {
		case f.Key == "skill_id":
			out[f.Key] = v
		case strings.HasSuffix(f.Key, "_id") || f.Key == "port" || f.Key == "max_concurrent":
			n, e := strconv.ParseInt(v, 10, 64)
			if e != nil || n <= 0 {
				return nil, fmt.Errorf("Choose a valid %s", f.Label)
			}
			out[f.Key] = n
		case f.Key == "project_ids":
			n, e := strconv.ParseInt(v, 10, 64)
			if e != nil || n <= 0 {
				return nil, fmt.Errorf("Choose a project")
			}
			out[f.Key] = []int64{n}
		case f.Key == "multi_repo" || f.Key == "isolated" || f.Key == "resume" || f.Key == "brief" || f.Key == "yolo" || f.Key == "enabled" || f.Key == "dispatch" || f.Key == "strict_mcp" || f.Key == "prompt_arg" || f.Key == "task_enabled":
			out[f.Key] = v == "true"
		default:
			out[f.Key] = v
		}
	}
	return out, nil
}
func (m *dashboard) formView() string {
	f := m.form
	lines := []string{accent.Bold(true).Render(clip(" "+f.title, m.width-4)), muted.Render(clip(" Tab next · Shift-Tab back · arrows choose · Ctrl-s submit · Esc cancel", m.width-4)), ""}
	start := max(0, f.index-max(1, m.height-17))
	for i := start; i < len(f.fields) && len(lines) < max(5, m.height-12); i++ {
		if !fieldVisible(f.fields, i) {
			continue
		}
		v := f.fields[i]
		value := v.Value
		for _, c := range v.Options {
			if c.Value == value {
				value = c.Label
				break
			}
		}
		if value == "" {
			value = "—"
		}
		line := "  " + v.Label + ": " + oneLine(value)
		if i == f.index {
			line = chosen.Render("› " + v.Label + ": " + oneLine(value))
		}
		lines = append(lines, clip(line, m.width-4))
	}
	current := f.fields[f.index]
	lines = append(lines, "", accent.Render(" "+current.Label))
	if len(current.Options) > 0 {
		options := filteredChoices(current)
		if current.Searchable {
			selected := "—"
			for _, c := range current.Options {
				if c.Value == current.Value {
					selected = c.Label
					break
				}
			}
			lines = append(lines, muted.Render(clip(fmt.Sprintf(" Filter: %q · %d matches · Selected: %s", current.OptionFilter, len(options), selected), m.width-4)))
			lines = append(lines, muted.Render(" Type to filter · Ctrl-u clears · Backspace erases"))
			if len(options) == 0 {
				lines = append(lines, muted.Render(" No matching "+optionNoun(current)+" · Ctrl-u clears · Backspace removes"))
				return strings.Join(lines, "\n")
			}
		}
		var opts []string
		for n, c := range options {
			if current.Compact && c.Value != current.Value {
				continue
			}
			label := c.Label
			if c.Value == current.Value || (current.Searchable && n == current.OptionCursor) {
				label = "[" + label + "]"
			}
			opts = append(opts, label)
		}
		lines = append(lines, clip(" ← "+strings.Join(opts, " · ")+" →", m.width-4))
	} else {
		lines = append(lines, f.editor.View())
	}
	if current.Key == "project_id" && strings.TrimSpace(current.Value) != "" {
		lines = append(lines, muted.Render(" Target: derived from selected project"))
	}
	return strings.Join(lines, "\n")
}
func options(rows []row, blank string) []choice {
	out := []choice{}
	if blank != "" {
		out = append(out, choice{blank, ""})
	}
	for _, r := range rows {
		out = append(out, choice{name(r), id(r)})
	}
	return out
}
func optionField(key, label, value string, opts []choice, required bool) field {
	if value == "" && required && len(opts) > 0 {
		value = opts[0].Value
	}
	return field{Key: key, Label: label, Value: value, Options: opts, Required: required, Searchable: key == "project_id" || key == "project_ids"}
}

func filteredChoices(f field) []choice {
	if !f.Searchable || strings.TrimSpace(f.OptionFilter) == "" {
		return f.Options
	}
	query := strings.ToLower(strings.TrimSpace(f.OptionFilter))
	filtered := make([]choice, 0, len(f.Options))
	for _, c := range f.Options {
		if strings.Contains(strings.ToLower(c.Label), query) {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

func optionNoun(f field) string {
	switch f.Key {
	case "target_id":
		return "machines"
	case "location":
		return "projects or machines"
	}
	return "projects"
}

func commitOptionSelection(f *field) bool {
	options := filteredChoices(*f)
	if len(options) == 0 {
		return false
	}
	if f.OptionCursor < 0 || f.OptionCursor >= len(options) {
		f.OptionCursor = 0
	}
	f.Value = options[f.OptionCursor].Value
	f.OptionFilter = ""
	f.OptionCursor = optionIndex(*f, f.Value, "")
	return true
}

func optionIndex(f field, value, filter string) int {
	f.OptionFilter = filter
	options := filteredChoices(f)
	for i, c := range options {
		if c.Value == value {
			return i
		}
	}
	return 0
}
func boolField(key, label string, def bool) field {
	return optionField(key, label, strconv.FormatBool(def), []choice{{"No", "false"}, {"Yes", "true"}}, false)
}
func (m *dashboard) newForm() tea.Cmd {
	kind := sections[m.section]
	r := m.current()
	project := str(r["project_id"])
	target := str(r["target_id"])
	if project != "" {
		target = ""
	}
	if kind == "approvals" {
		m.notice = "Approvals are created by agents when they need a decision."
		return nil
	}
	projects := options(m.projects, "Scratch / no project")
	targets := options(m.targets, "Server default")
	agents := []choice{}
	taskCapable := kind == "tasks" || kind == "routines"
	for _, a := range m.agents {
		if taskCapable && a["builtin"] != true && a["task"] == nil {
			continue
		}
		value := str(a["id"])
		if value == "" {
			value = str(a["name"])
		}
		agents = append(agents, choice{value, value})
	}
	if len(agents) == 0 {
		agents = []choice{{"Codex", "codex"}, {"Claude", "claude"}}
	}
	agent := optionField("agent", "Agent", "codex", agents, true)
	var fields []field
	switch kind {
	case "sessions":
		agent.Label = "Agent (without a profile)"
		targetField := optionField("target_id", "Target", target, targets, false)
		targetField.HideWhenProject = true
		workdirField := field{Key: "workdir", Label: "Directory (blank uses project or scratch)"}
		workdirField.HideWhenProject = true
		fields = []field{{Key: "name", Label: "Session name", Required: true}, optionField("profile_id", "Launch profile", "", m.profileChoices("Agent and project defaults"), false), optionField("project_id", "Project", project, projects, false), targetField, agent, {Key: "model", Label: "Model (blank uses default)"}, workdirField, {Key: "prime", Label: "Initial prompt", Multiline: true}, boolField("isolated", "Isolate files in a new Git worktree", false), boolField("multi_repo", "Choose additional repositories after this form", false), {Key: "worktree_base", Label: "Worktree base (blank = committed HEAD)"}, {Key: "worktree_branch", Label: "New branch (blank = unique name)"}, boolField("resume", "Resume latest conversation", false), boolField("brief", "Include project brief", true), boolField("yolo", "Skip agent permission prompts", false), {Key: "group_path", Label: "Group path (optional, e.g. Work/Client)"}}
	case "tasks":
		fields = []field{{Key: "title", Label: "Task title", Required: true}, optionField("project_id", "Project", project, options(m.projects, ""), true), {Key: "prompt", Label: "Task prompt", Multiline: true, Required: true}, agent, {Key: "model", Label: "Model"}, {Key: "base_branch", Label: "Base branch (blank uses project default)"}, boolField("dispatch", "Dispatch now in an isolated worktree", true)}
	case "routines":
		fields = []field{{Key: "name", Label: "Routine name", Required: true}, optionField("project_ids", "Project", "", options(m.projects, ""), true), {Key: "prompt", Label: "Prompt", Multiline: true, Required: true}, {Key: "schedule", Label: "Schedule (blank for manual)"}, agent, boolField("dispatch", "Dispatch generated tasks", true)}
	case "projects":
		fields = []field{{Key: "name", Label: "Project name", Required: true}, optionField("target_id", "Target", target, options(m.targets, ""), true), {Key: "repo_path", Label: "Repository path", Required: true}, {Key: "default_base_branch", Label: "Base branch", Value: "main"}}
	case "targets":
		fields = []field{{Key: "name", Label: "Target name", Required: true}, optionField("kind", "Connection", "ssh", []choice{{"SSH", "ssh"}, {"Local", "local"}}, true), {Key: "host", Label: "Host / SSH alias"}, {Key: "user", Label: "SSH user"}, {Key: "port", Label: "SSH port", Value: "22"}, {Key: "key_path", Label: "SSH key path on server"}, {Key: "max_concurrent", Label: "Concurrent agents", Value: "4"}}
	}
	return m.openForm("New "+strings.TrimSuffix(kind, "s"), fields, func(body map[string]any) tea.Cmd {
		if kind == "sessions" {
			if body["multi_repo"] == true {
				if body["isolated"] != true || body["project_id"] == nil || body["workdir"] != nil || body["resume"] == true {
					m.notice = "Choose a primary project and isolated files, with no directory override or resume."
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
			if body["project_id"] != nil {
				delete(body, "target_id")
			}
			if body["project_id"] == nil && body["workdir"] == nil {
				body["scratch"] = true
			}
		}
		if kind == "sessions" {
			multi := body["multi_repo"] == true
			delete(body, "multi_repo")
			if multi {
				return m.workspaceRepositoryForm(body, m.form)
			}
		}
		if kind == "tasks" && body["dispatch"] == true {
			return m.createAndDispatch(body)
		}
		return m.request("Create "+strings.TrimSuffix(kind, "s"), "POST", "/"+kind, body, false)
	})
}

func (m *dashboard) newShellForm() tea.Cmd {
	if len(m.targets) == 0 && len(m.projects) == 0 {
		m.notice = "No machines are configured. Add a target first."
		return nil
	}
	// One searchable location field groups projects and machines instead of
	// stacking a project picker above a machine picker. A project opens a fresh
	// tracked shell in its own folder (project_id); a machine keeps the scratch
	// shell it always did (target_id).
	location := optionField("location", "Project or machine", "", shellLocations(m.targets, m.projects), true)
	location.Searchable = true
	return m.openForm("Blank persistent shell", []field{location}, func(body map[string]any) tea.Cmd {
		value := strings.TrimSpace(str(body["location"]))
		launch, ok := shellBody(value)
		if !ok {
			m.notice = "Choose a project or machine."
			return nil
		}
		return m.request("Create blank shell", "POST", "/shells", launch, false)
	})
}

// shellBody turns the single picker value into the one selector /shells takes:
// project_id for a project folder, target_id for a machine. There is no path
// in the request — the server derives the project's target and folder itself.
func shellBody(value string) (map[string]any, bool) {
	projectID, machineID, ok := parseShellLocation(value)
	if !ok {
		return nil, false
	}
	if projectID != 0 {
		return map[string]any{"project_id": projectID}, true
	}
	return map[string]any{"target_id": machineID}, true
}

// shellLocations builds the single picker list: projects first, then machines,
// each labelled with enough to tell look-alikes apart. A project shows its
// folder and target; a machine shows its kind.
func shellLocations(targets, projects []row) []choice {
	out := make([]choice, 0, len(projects)+len(targets))
	for _, p := range projects {
		label := str(p["name"])
		if path := str(p["repo_path"]); path != "" {
			label += " — " + path
		}
		if target := str(p["target_name"]); target != "" {
			label += " · " + target
		}
		out = append(out, choice{Label: label, Value: "project:" + id(p)})
	}
	for _, t := range targets {
		label := str(t["name"])
		if kind := str(t["kind"]); kind != "" {
			label += " · " + kind
		}
		out = append(out, choice{Label: label, Value: "machine:" + id(t)})
	}
	return out
}

func parseShellLocation(value string) (projectID, machineID int64, ok bool) {
	switch {
	case strings.HasPrefix(value, "project:"):
		n, err := strconv.ParseInt(strings.TrimPrefix(value, "project:"), 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		return n, 0, true
	case strings.HasPrefix(value, "machine:"):
		n, err := strconv.ParseInt(strings.TrimPrefix(value, "machine:"), 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		return 0, n, true
	}
	return 0, 0, false
}

func (m *dashboard) renameForm() tea.Cmd {
	r := m.current()
	if r == nil {
		return nil
	}
	kind := sections[m.section]
	if kind == "approvals" {
		return nil
	}
	key := "name"
	if kind == "tasks" {
		key = "title"
	}
	path := "/" + kind + "/" + id(r)
	return m.openForm("Rename", []field{{Key: key, Label: "Name", Value: name(r), Required: true}}, func(body map[string]any) tea.Cmd { return m.request("Rename", "PATCH", path, body, false) })
}
func (m *dashboard) messageAction(label, path, key string) tea.Cmd {
	return m.openForm(label, []field{{Key: key, Label: label, Multiline: true, Required: true}}, func(body map[string]any) tea.Cmd { return m.request(label, "POST", path, body, false) })
}
func (m *dashboard) sendForm() tea.Cmd {
	r := m.current()
	if r == nil {
		return nil
	}
	path := "/" + sections[m.section] + "/" + id(r)
	if sections[m.section] == "sessions" {
		path += "/send"
	} else {
		path += "/messages"
	}
	return m.messageAction("Send message", path, "text")
}

func (m *dashboard) loadPromotionPreview() tea.Cmd {
	r := m.current()
	if r == nil || sections[m.section] != "sessions" || m.busy {
		return nil
	}
	m.busy = true
	c, path := m.client, "/sessions/"+id(r)+"/promote/preview"
	return func() tea.Msg {
		data, err := c.JSON("GET", path, nil)
		return promotionPreviewMsg{data: data, err: err}
	}
}

// promotionPreviewForm only offers projects proven compatible by the server.
// The expected identity is sent back with the mutation so the backend can
// reject a changed process, directory, terminal, or native conversation.
func (m *dashboard) promotionPreviewForm(data []byte) tea.Cmd {
	var preview struct {
		IdentityRaw json.RawMessage `json:"identity"`
		Existing    []row           `json:"existing_projects"`
		SessionID   int64           `json:"session_id"`
	}
	if err := json.Unmarshal(data, &preview); err != nil {
		m.notice = "Promotion preview: " + err.Error()
		return nil
	}
	var identity struct {
		Agent            string `json:"agent"`
		CID              string `json:"cid"`
		Workspace        string `json:"workdir"`
		TmuxSession      string `json:"tmux_session"`
		ProcStart        string `json:"proc_start"`
		TrackingIdentity string `json:"tracking_identity"`
		PID              int    `json:"pid"`
		TargetID         int64  `json:"target_id"`
	}
	if len(preview.IdentityRaw) == 0 || json.Unmarshal(preview.IdentityRaw, &identity) != nil || preview.SessionID <= 0 || identity.Agent == "" || identity.CID == "" || identity.Workspace == "" {
		m.notice = "Promotion preview did not contain an exact native conversation binding."
		return nil
	}
	choices := []choice{{"New project", ""}}
	choices = append(choices, options(preview.Existing, "")...)
	fields := []field{optionField("project_id", "Existing project", "", choices, false), {Key: "name", Label: "Project name (new project)"}}
	sessionID := fmt.Sprint(preview.SessionID)
	return m.openForm("Promote conversation", fields, func(body map[string]any) tea.Cmd {
		projectID := strings.TrimSpace(str(body["project_id"]))
		if projectID != "" {
			delete(body, "name")
		} else if strings.TrimSpace(str(body["name"])) == "" {
			m.notice = "Enter a name for the new project, or choose an existing project."
			return nil
		}
		body["expected_identity"] = preview.IdentityRaw
		warning := fmt.Sprintf("Bind this exact running conversation?\n  Agent: %s (PID %d)\n  Directory: %s\n  Terminal: %s\n  Conversation: %s\nThe terminal, session ID, and native history stay in place.", identity.Agent, identity.PID, identity.Workspace, identity.TmuxSession, identity.CID)
		m.pending = &dashboardAction{Label: "Promote conversation", Method: "POST", Path: "/sessions/" + sessionID + "/promote", Body: body, Warning: warning}
		m.form = nil
		m.notice = "Review the exact agent, directory, and terminal, then press y to confirm."
		return nil
	})
}
func (m *dashboard) uploadForm() tea.Cmd {
	r := m.current()
	kind := strings.TrimSuffix(sections[m.section], "s")
	if r == nil || kind != "session" && kind != "task" {
		m.notice = "Select a session or task to upload context."
		return nil
	}
	rid := id(r)
	return m.openForm("Upload context", []field{{Key: "file", Label: "Local file path", Required: true}}, func(body map[string]any) tea.Cmd {
		if m.busy {
			return nil
		}
		m.busy = true
		c, insert := m.client, m.insert
		path := str(body["file"])
		return func() tea.Msg {
			data, e := c.Upload(kind, rid, path)
			if e != nil {
				return resultMsg{label: "Upload", data: data, err: e}
			}
			var v row
			_ = json.Unmarshal(data, &v)
			dest := str(v["path"])
			// The popup can only reach the running attachment through tmux; the
			// dashboard has no attached pane, so it just reports the path.
			notice := "Uploaded: " + dest + ". Mention or paste this path in your prompt."
			if insert != nil {
				if e := insert(shellq.Quote(dest) + " "); e != nil {
					notice = "Uploaded: " + dest + ". " + e.Error() + "; copy this path into your prompt."
				} else {
					notice = "Uploaded: " + dest + ". Path inserted; press Enter when ready."
				}
			}
			return resultMsg{label: "Uploaded: " + dest, data: data, notice: notice}
		}
	})
}
func (m *dashboard) jsonForm(title, method, path string, value any) tea.Cmd {
	return m.openForm(title, []field{{Key: "json", Label: "Changed fields (JSON)", Value: pretty(value), Multiline: true, Required: true}}, func(body map[string]any) tea.Cmd {
		var data map[string]any
		if e := json.Unmarshal([]byte(str(body["json"])), &data); e != nil {
			m.notice = "Invalid JSON: " + e.Error()
			return nil
		}
		return m.request(title, method, path, data, false)
	})
}

// mcpSettingsForm is the terminal equivalent of the project MCP editor. It
// edits the redacted document through the conditional MCP endpoint, so
// add/edit/remove remain available without opening a browser and a failed save
// leaves the textarea intact for retry.
func (m *dashboard) mcpSettingsForm() tea.Cmd {
	r := m.current()
	if r == nil || sections[m.section] != "projects" || m.busy {
		return nil
	}
	m.busy = true
	c, path := m.client, "/projects/"+id(r)+"/mcp"
	return func() tea.Msg {
		data, err := c.JSON("GET", path, nil)
		return loadedFormMsg{"MCP settings (JSON)", "PUT", path, data, err}
	}
}

func (m *dashboard) mcpSettingsLoaded(data []byte, path string) tea.Cmd {
	var envelope struct {
		MCP       map[string]any `json:"mcp"`
		Revision  string         `json:"revision"`
		StrictMCP bool           `json:"strict_mcp"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		m.notice = "MCP settings: " + err.Error()
		return nil
	}
	revision := envelope.Revision
	return m.openForm("MCP settings (JSON) — add / edit / remove", []field{
		{Key: "mcp", Label: "Servers JSON (redacted secrets are retained)", Value: pretty(envelope.MCP), Multiline: true, Required: true},
		boolField("strict_mcp", "Claude strict replacement (Codex does not support this)", envelope.StrictMCP),
	}, func(body map[string]any) tea.Cmd {
		var servers map[string]any
		if err := json.Unmarshal([]byte(str(body["mcp"])), &servers); err != nil {
			m.notice = "MCP settings must be a JSON object: " + err.Error()
			return nil
		}
		if m.busy {
			return nil
		}
		m.busy = true
		c := m.client
		payload := map[string]any{"mcp": servers, "revision": revision, "strict_mcp": body["strict_mcp"]}
		return func() tea.Msg {
			result, err := c.JSON("PUT", path, payload)
			if he, ok := err.(*HTTPError); ok && he.Status == 409 {
				// Refresh only the revision. The form remains open with the
				// user's draft, and the next explicit save resolves the conflict.
				fresh, freshErr := c.JSON("GET", path, nil)
				if freshErr == nil {
					var latest struct {
						Revision string `json:"revision"`
					}
					if json.Unmarshal(fresh, &latest) == nil && latest.Revision != "" {
						revision = latest.Revision
					}
				}
			}
			return resultMsg{label: "Save MCP settings", data: result, err: err}
		}
	})
}

// skillsSettingsForm keeps discovery and attachment records visible in the
// terminal. Skill IDs remain target-local; the dashboard only submits the
// selected provider and ID back to the dedicated endpoints.
func (m *dashboard) skillsSettingsForm() tea.Cmd {
	r := m.current()
	if r == nil || sections[m.section] != "projects" || m.busy {
		return nil
	}
	m.busy = true
	c := m.client
	projectID := id(r)
	agent := str(r["default_agent"])
	if agent != "claude" && agent != "codex" {
		agent = "claude"
	}
	path := "/projects/" + projectID + "/skills"
	return func() tea.Msg {
		available, err := c.JSON("GET", "/skills?project_id="+projectID+"&agent="+agent, nil)
		if err != nil {
			return skillsLoadedMsg{path: path, err: err}
		}
		attached, err := c.JSON("GET", path+"?agent="+agent, nil)
		if err != nil {
			return skillsLoadedMsg{path: path, err: err}
		}
		var a, b map[string]any
		if err = json.Unmarshal(available, &a); err != nil {
			return skillsLoadedMsg{path: path, err: err}
		}
		if err = json.Unmarshal(attached, &b); err != nil {
			return skillsLoadedMsg{path: path, err: err}
		}
		combined, _ := json.Marshal(map[string]any{"agent": agent, "skills": a["skills"], "attachments": b["attachments"], "skill_sources_json": r["skill_sources_json"]})
		return skillsLoadedMsg{path: path, data: combined}
	}
}

func (m *dashboard) skillsSettingsLoaded(msg skillsLoadedMsg) tea.Cmd {
	if msg.err != nil {
		m.notice = "Skills: " + clean(msg.err.Error()) + " (choose Skills again to retry)"
		return nil
	}
	var envelope struct {
		Agent  string `json:"agent"`
		Skills []struct {
			ID, Name, Source, EntryName, Description string
		} `json:"skills"`
		Attachments []struct {
			ID                           int64 `json:"id"`
			SkillID, EntryName, SourceID string
		} `json:"attachments"`
		SkillSourcesJSON string `json:"skill_sources_json"`
	}
	if err := json.Unmarshal(msg.data, &envelope); err != nil {
		m.notice = "Skills: " + clean(err.Error())
		return nil
	}
	if envelope.Agent == "" {
		envelope.Agent = "claude"
	}
	skillChoices := []choice{}
	for _, s := range envelope.Skills {
		label := s.Name
		if label == "" {
			label = s.EntryName
		}
		label += " · " + s.Source
		if s.Description != "" {
			label += " · " + s.Description
		}
		skillChoices = append(skillChoices, choice{clean(oneLine(label)), s.ID})
	}
	attachmentChoices := []choice{}
	for _, a := range envelope.Attachments {
		label := a.EntryName
		if label == "" {
			label = a.SkillID
		}
		label += " · " + a.SourceID
		attachmentChoices = append(attachmentChoices, choice{clean(oneLine(label)), fmt.Sprint(a.ID)})
	}
	if len(skillChoices) == 0 {
		skillChoices = []choice{{"No discovered skills", ""}}
	}
	if len(attachmentChoices) == 0 {
		attachmentChoices = []choice{{"No attached skills", ""}}
	}
	sources := ""
	var sourceList []string
	if envelope.SkillSourcesJSON != "" {
		_ = json.Unmarshal([]byte(envelope.SkillSourcesJSON), &sourceList)
		sources = strings.Join(sourceList, "\n")
	}
	return m.openForm("Project skills — target-local catalog", []field{
		optionField("agent", "Provider", envelope.Agent, []choice{{"Claude Code", "claude"}, {"Codex", "codex"}}, true),
		optionField("operation", "Operation", "attach", []choice{{"Attach discovered skill", "attach"}, {"Detach attached skill", "detach"}, {"Save target directories", "sources"}}, true),
		optionField("skill_id", "Skill (name · source)", skillChoices[0].Value, skillChoices, false),
		optionField("attachment_id", "Attachment (name · source)", attachmentChoices[0].Value, attachmentChoices, false),
		{Key: "skill_sources", Label: "Extra target directories (one per line)", Value: sources, Multiline: true},
	}, func(body map[string]any) tea.Cmd {
		op := str(body["operation"])
		agent := str(body["agent"])
		switch op {
		case "attach":
			if str(body["skill_id"]) == "" {
				m.notice = "No discovered skill is available for this provider."
				return nil
			}
			return m.request("Attach skill", "POST", "/projects/"+id(m.current())+"/skills", map[string]any{"agent": agent, "skill_id": body["skill_id"]}, false)
		case "detach":
			attachmentID := str(body["attachment_id"])
			if attachmentID == "" {
				m.notice = "No attached skill is available for this provider."
				return nil
			}
			return m.request("Detach skill", "DELETE", "/projects/"+id(m.current())+"/skills/"+attachmentID, nil, false)
		case "sources":
			values := []string{}
			for _, line := range strings.Split(str(body["skill_sources"]), "\n") {
				if line = strings.TrimSpace(line); line != "" {
					values = append(values, line)
				}
			}
			return m.request("Save skill directories", "PATCH", "/projects/"+id(m.current()), map[string]any{"skill_sources": values}, false)
		}
		m.notice = "Choose an operation."
		return nil
	})
}
func (m *dashboard) editForm() tea.Cmd {
	if m.current() == nil {
		return nil
	}
	return m.jsonForm("Edit fields", "PATCH", "/"+sections[m.section]+"/"+id(m.current()), map[string]any{})
}
func (m *dashboard) settingsForm() tea.Cmd {
	if m.busy {
		return nil
	}
	m.busy = true
	c := m.client
	return func() tea.Msg {
		data, e := c.JSON("GET", "/settings", nil)
		return loadedFormMsg{"Settings", "PUT", "/settings", data, e}
	}
}
func (m *dashboard) apiForm() tea.Cmd {
	return m.openForm("Full API", []field{optionField("method", "Method", "GET", []choice{{"GET", "GET"}, {"POST", "POST"}, {"PATCH", "PATCH"}, {"PUT", "PUT"}, {"DELETE", "DELETE"}}, true), {Key: "path", Label: "API path", Value: "/health", Required: true}, {Key: "json", Label: "JSON body", Value: "{}", Multiline: true}}, func(body map[string]any) tea.Cmd {
		var payload any
		if e := json.Unmarshal([]byte(str(body["json"])), &payload); e != nil {
			m.notice = "Invalid JSON: " + e.Error()
			return nil
		}
		method, path := str(body["method"]), str(body["path"])
		if method == "GET" {
			m.form = nil
			return m.readResource("API result", path)
		}
		m.form = nil
		m.pending = &dashboardAction{Label: method + " " + path, Method: method, Path: path, Body: payload, Warning: "Send this API request?\n" + pretty(payload)}
		return nil
	})
}
func (m *dashboard) readResource(label, path string) tea.Cmd {
	return m.request(label, "GET", path, nil, true)
}
func (m *dashboard) readDetail(label string) tea.Cmd {
	r := m.current()
	if r == nil {
		return nil
	}
	kind := sections[m.section]
	if label == "Diff" {
		if kind != "tasks" {
			return m.openReview()
		}
		return m.readResource(label, "/tasks/"+id(r)+"/diff")
	}
	terminalKind, rid := strings.TrimSuffix(kind, "s"), id(r)
	if kind == "tasks" {
		a, _ := r["attempt"].(map[string]any)
		if a == nil {
			m.notice = "No task attempt yet"
			return nil
		}
		terminalKind = "attempt"
		rid = id(row(a))
	}
	if kind != "sessions" && kind != "tasks" && kind != "projects" {
		return nil
	}
	return m.readResource(label, "/term/"+terminalKind+"/"+rid+"/history?lines=1000")
}
func formatDetail(label string, data []byte) string {
	if label == "Repository addition progress" {
		var rows []struct {
			ID              int64
			State, Error    string
			CancelRequested bool `json:"cancel_requested"`
		}
		if json.Unmarshal(data, &rows) == nil {
			lines := []string{"Repository additions (newest first)", "Original session terminal remains available.", ""}
			if len(rows) == 0 {
				lines = append(lines, "No repository additions yet.")
			}
			for _, op := range rows {
				line := fmt.Sprintf("Addition %d: %s", op.ID, op.State)
				if op.CancelRequested {
					line += " · cancellation requested"
				}
				if op.Error != "" {
					line += "\n" + op.Error
				}
				lines = append(lines, line)
			}
			return strings.Join(lines, "\n")
		}
	}

	if label == "Check agent commands" {
		var rows []struct{ Name, State, Path, Detail string }
		if json.Unmarshal(data, &rows) == nil {
			lines := []string{"Default target commands. Project/profile overrides may differ.", "No agents started; login and model access are not checked.", ""}
			for _, row := range rows {
				state := map[string]string{"available": "Found", "missing": "Not found", "unchecked": "Not checked"}[row.State]
				if state == "" {
					state = "Not checked"
				}
				detail := row.Path
				if detail == "" {
					detail = row.Detail
				}
				lines = append(lines, row.Name+" · "+state, "  "+detail, "")
			}
			return strings.Join(lines, "\n")
		}
	}

	if label == "Saved conversation" {
		return nativeText(data)
	}
	var value any
	if json.Unmarshal(data, &value) != nil {
		return string(data)
	}
	if r, ok := value.(map[string]any); ok {
		if s, ok := r["text"].(string); ok {
			return s
		}
		if label == "Diff" {
			var patches []string
			if files, ok := r["files"].([]any); ok {
				for _, v := range files {
					if f, ok := v.(map[string]any); ok {
						patches = append(patches, str(f["path"])+"\n"+str(f["patch"]))
					}
				}
			}
			if len(patches) > 0 {
				return strings.Join(patches, "\n\n")
			}
			return "No changed files in the captured diff."
		}
	}
	return readable(value)
}
func (m *dashboard) discover() tea.Cmd {
	if m.busy {
		return nil
	}
	m.busy = true
	c := m.client
	return func() tea.Msg {
		b, e := c.JSON("GET", "/sessions/discover", nil)
		var rows []row
		if e == nil {
			e = json.Unmarshal(b, &rows)
		}
		return discoveredMsg{rows, e}
	}
}
func (m *dashboard) adoptForm(rows []row) tea.Cmd {
	choices := []choice{}
	for i, r := range rows {
		if r["tracked"] == true || r["adopted"] == true {
			continue
		}
		label := str(r["tmux_session"]) + " · " + str(r["target_name"])
		choices = append(choices, choice{label, strconv.Itoa(i)})
	}
	if len(choices) == 0 {
		m.notice = "No untracked running agents found."
		return nil
	}
	return m.openForm("Track running agent", []field{optionField("candidate", "Running session", choices[0].Value, choices, true), {Key: "name", Label: "Display name (optional)"}, optionField("project_id", "Project", "", options(m.projects, "Unassigned"), false)}, func(body map[string]any) tea.Cmd {
		index, _ := strconv.Atoi(str(body["candidate"]))
		r := rows[index]
		delete(body, "candidate")
		for _, k := range []string{"target_id", "tmux_session", "agent", "workdir"} {
			if r[k] != nil {
				body[k] = r[k]
			}
		}
		return m.request("Track session", "POST", "/sessions/adopt", body, false)
	})
}

// Creation is durable even if dispatch fails. Close the form and identify the
// created card so retrying dispatch cannot accidentally create another task.
func (m *dashboard) createAndDispatch(body map[string]any) tea.Cmd {
	if m.busy {
		return nil
	}
	m.busy = true
	c := m.client
	return func() tea.Msg {
		data, e := c.JSON("POST", "/tasks", body)
		if e != nil {
			return resultMsg{err: e}
		}
		var task row
		if e = json.Unmarshal(data, &task); e != nil {
			return resultMsg{err: e}
		}
		_, e = c.JSON("POST", "/tasks/"+id(task)+"/dispatch", map[string]any{})
		label := "Created and dispatched task " + id(task)
		if e != nil {
			label = "Created task " + id(task) + "; dispatch failed: " + e.Error() + ". Use Actions → Dispatch to retry"
		}
		return resultMsg{label: label, data: data}
	}
}

func (m *dashboard) editCommonForm() tea.Cmd {
	r := m.current()
	if r == nil {
		return nil
	}
	kind := sections[m.section]
	path := "/" + kind + "/" + id(r)
	var fields []field
	add := func(key, label string, multi, required bool) {
		fields = append(fields, field{Key: key, Label: label, Value: str(r[key]), Multiline: multi, Required: required})
	}
	switch kind {
	case "sessions":
		add("name", "Session name", false, true)
		fields = append(fields, optionField("project_id", "Project", str(r["project_id"]), options(m.projects, "Unassigned"), false))
		add("model", "Model (applies at next launch)", false, false)
	case "tasks":
		add("title", "Task title", false, true)
		add("prompt", "Prompt", true, true)
		add("model", "Model", false, false)
		add("base_branch", "Base branch", false, false)
	case "routines":
		add("name", "Routine name", false, true)
		add("prompt", "Prompt", true, true)
		add("schedule", "Schedule (blank for manual)", false, false)
		add("model", "Model", false, false)
		fields = append(fields, boolField("enabled", "Enabled", r["enabled"] != false), boolField("dispatch", "Dispatch generated tasks", r["dispatch"] != false))
	case "projects":
		add("name", "Project name", false, true)
		add("default_base_branch", "Base branch", false, true)
		add("verify_cmd", "Verification command", false, false)
		add("setup_cmd", "New worktree setup command (before agent starts)", true, false)
	case "targets":
		add("name", "Target name", false, true)
		add("host", "Host / SSH alias", false, false)
		add("user", "SSH user", false, false)
		add("port", "SSH port", false, true)
		add("key_path", "SSH key path on server", false, false)
		add("max_concurrent", "Concurrent agents", false, true)
	default:
		return nil
	}
	return m.openForm("Edit "+strings.TrimSuffix(kind, "s"), fields, func(body map[string]any) tea.Cmd {
		for _, f := range fields {
			if _, ok := body[f.Key]; !ok {
				if f.Key == "project_id" {
					body[f.Key] = nil
				} else {
					body[f.Key] = ""
				}
			}
		}
		return m.request("Edit "+strings.TrimSuffix(kind, "s"), "PATCH", path, body, false)
	})
}
func (m *dashboard) notificationForm(data []byte) tea.Cmd {
	var r row
	if e := json.Unmarshal(data, &r); e != nil {
		m.notice = e.Error()
		return nil
	}
	fields := []field{{Key: "discord_webhook", Label: "Discord webhook", Value: str(r["discord_webhook"])}, {Key: "ntfy_server", Label: "ntfy server", Value: str(r["ntfy_server"])}, {Key: "ntfy_topic", Label: "ntfy topic", Value: str(r["ntfy_topic"])}}
	return m.openForm("Notification settings", fields, func(body map[string]any) tea.Cmd {
		for _, f := range fields {
			if body[f.Key] == nil {
				body[f.Key] = ""
			}
		}
		return m.request("Save settings", "PUT", "/settings", body, false)
	})
}

func (m *dashboard) groupForm() tea.Cmd {
	r := m.current()
	if r == nil || sections[m.section] != "sessions" {
		return nil
	}
	endpoint := "/sessions/" + id(r)
	return m.openForm("Move to group", []field{{Key: "group_path", Label: "Group path (Work/Client; blank ungroups)", Value: str(r["group_path"])}}, func(body map[string]any) tea.Cmd {
		if body["group_path"] == nil {
			body["group_path"] = ""
		}
		return m.request("Move to group", "PATCH", endpoint, body, false)
	})
}
func workspaceBranch(r row) string {
	if ws, ok := r["workspace"].(map[string]any); ok {
		return str(ws["branch"])
	}
	return ""
}

func workspaceActions(r row, path string) []dashboardAction {
	if ws, ok := r["workspace"].(map[string]any); ok && ws["state"] != "removed" {
		actions := []dashboardAction{}
		failed := r["setup_state"] == "failed" || ws["state"] == "failed"
		if failed {
			actions = append(actions, dashboardAction{Label: "Cancel remaining checkout", Method: "POST", Path: path + "/setup/cancel", Body: map[string]any{}})
			actions = append(actions, dashboardAction{Label: "Recover allocation (keep files)", Method: "POST", Path: path + "/worktree/recover", Body: map[string]any{}})
		}
		// The initial grouped setup owns the allocation until it is ready. Keep
		// extension controls out of that menu; the backend correctly rejects
		// additions during setup, and the progress/cancel entry belongs to the
		// initial setup flow instead.
		if repositories, ok := ws["repositories"].([]any); ok && len(repositories) > 0 {
			if !failed && r["setup_state"] != "creating" {
				actions = append(actions, dashboardAction{Label: "Add repository", Operation: "extend-workspace"}, dashboardAction{Label: "Repository addition progress", Method: "GET", Path: path + "/worktree/operations"}, dashboardAction{Label: "Cancel repository addition", Operation: "cancel-extension"}, dashboardAction{Label: "Check interrupted addition", Operation: "recover-extension"})
			}
			actions = append(actions, dashboardAction{Label: "Workspace setup progress", Method: "GET", Path: path + "/worktree?format=text"})
		}
		if r["setup_state"] == "creating" && !failed {
			return actions
		}
		return append(actions, dashboardAction{Label: "Remove worktree (keep branch)", Method: "DELETE", Path: path + "/worktree", Warning: "Remove " + str(ws["path"]) + "? End its sessions first. Changed, untracked or ignored files prevent removal. The branch is kept."})
	}
	return nil
}
