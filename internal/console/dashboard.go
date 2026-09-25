package console

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var sections = []string{"sessions", "tasks", "routines", "projects", "targets", "approvals"}

type row map[string]any

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func id(r row) string { return str(r["id"]) }
func name(r row) string {
	for _, k := range []string{"name", "title", "tool_name", "tmux_session"} {
		if s := str(r[k]); s != "" {
			return s
		}
	}
	return "Untitled"
}

// Remote output is display data. Strip control sequences before adding our own
// styles: a preview must never change the operator's terminal or clipboard.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(s))
}
func oneLine(s string) string { return strings.Join(strings.Fields(clean(s)), " ") }
func fuzzy(query, value string) bool {
	value = strings.ToLower(value)
	for _, word := range strings.Fields(strings.ToLower(query)) {
		rest := value
		for _, r := range word {
			i := strings.IndexRune(rest, r)
			if i < 0 {
				return false
			}
			rest = rest[i+len(string(r)):]
		}
	}
	return true
}

// fuzzyFields keeps a query word inside one searchable field. Matching against
// the concatenated row metadata lets a query succeed by bridging the end of
// one field and the start of another, which produces false positives in the
// session list. Different words may still match different fields.
func fuzzyFields(query string, fields ...string) bool {
	for _, word := range strings.Fields(strings.ToLower(query)) {
		matched := false
		for _, field := range fields {
			if fuzzy(word, field) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

type rowsMsg struct {
	section    string
	generation int
	rows       []row
	err        error
}
type recentMsg struct {
	rows []row
	err  error
}
type refsMsg struct {
	projects, targets, agents, profiles []row
	err                                 error
}
type tickMsg time.Time
type resultMsg struct {
	label   string
	data    []byte
	err     error
	preview bool
	key     string
	// notice, when set, replaces the default "<label> completed" message so an
	// action can report what it actually did to the terminal.
	notice string
}
type attachedMsg struct{ err error }
type promotionPreviewMsg struct {
	data []byte
	err  error
}
type dashboard struct {
	focusSessionID                      string
	client                              *Client
	attach                              func(string, string) error
	insert                              func(string) error
	section                             int
	rows, visible                       []row
	selected, offset                    int
	width, height                       int
	generation                          int
	loading                             bool
	updated                             time.Time
	failure, notice                     string
	query                               textinput.Model
	searching                           bool
	review                              *codeReview
	nativeSearch                        *nativeSearchState
	native                              *nativeSelection
	grouping                            int
	groupingBySection                   map[string]int
	preferencePath                      string
	recentProjects                      []int64
	scratchNewSession                   bool
	collapsed                           map[string]bool
	matched                             int
	attention, ended, archived          bool
	preview                             viewport.Model
	previewFocus                        bool
	detailKey, detailTitle, detail      string
	help, menu                          bool
	menuIndex                           int
	form                                *dashboardForm
	pending                             *dashboardAction
	busy                                bool
	projects, targets, agents, profiles []row
	recentRows                          []row
	recentOpen                          bool
	recentSelected                      int
	recentPending                       row
	attachAfterRefresh                  bool
	// controlOnly hides the actions that open another native terminal, so the
	// controls popup cannot nest an attachment inside itself. popup marks the
	// surface as the tmux popup whose Esc leaves the popup.
	controlOnly  bool
	popup        bool
	pendingFocus *dashboardFocus
}

// dashboardFocus is the row a controls popup was opened for. It is resolved
// after the owning section loads so the popup never opens actions on the wrong
// row while the list is still empty.
type dashboardFocus struct {
	section  int
	id       string
	attempt  bool
	action   string
	openMenu bool
}

// DashboardOptions selects which dashboard surface to render.
type DashboardOptions struct {
	Attach func(string, string) error
	// Insert types text into the attached terminal without submitting it. Only
	// the Ctrl-] controls popup sets it, where a real pane sits underneath.
	Insert      func(string) error
	ControlOnly bool
	Popup       bool
	FocusKind   string
	FocusID     string
	Action      string
}

// RunDashboard uses a full-screen renderer that owns raw mode, resizing and the
// suspend/restore boundary around native tmux/SSH. The line client remains useful
// for pipes and accessibility via console --plain.
func RunDashboard(c *Client, in io.Reader, out io.Writer, attach func(string, string) error) error {
	return runDashboard(c, in, out, DashboardOptions{Attach: attach})
}

// RunControls renders the dashboard without native attachment actions. It is
// the surface behind `lectern controls` and the attached terminal's Ctrl-]
// popup: ordinary Lectern actions stay available, but opening another terminal
// would nest an attachment inside itself.
func RunControls(c *Client, in io.Reader, out io.Writer, opts DashboardOptions) error {
	opts.ControlOnly = true
	return runDashboard(c, in, out, opts)
}

func runDashboard(c *Client, in io.Reader, out io.Writer, opts DashboardOptions) error {
	m := newDashboardOpts(c, opts)
	// A contextual popup must find its attachment even if the dashboard saved
	// its group collapsed. Keep that temporary view out of saved preferences.
	if opts.FocusID == "" {
		m.loadPreferences()
	}
	_, err := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out), tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}
func newDashboard(c *Client, attach func(string, string) error) *dashboard {
	return newDashboardOpts(c, DashboardOptions{Attach: attach})
}
func newDashboardOpts(c *Client, opts DashboardOptions) *dashboard {
	q := textinput.New()
	q.Prompt = "/ "
	q.Placeholder = "Search name, project, target, agent…"
	q.CharLimit = 200
	m := &dashboard{client: c, attach: opts.Attach, insert: opts.Insert, width: 100, height: 30, query: q, preview: viewport.New(50, 20), groupingBySection: map[string]int{}}
	m.controlOnly = opts.ControlOnly
	m.popup = opts.Popup
	if kind := controlsKind(opts.FocusKind); kind != "" && opts.FocusID != "" {
		m.section = controlsSection(kind)
		m.pendingFocus = &dashboardFocus{section: m.section, id: opts.FocusID, attempt: kind == "attempt", action: opts.Action, openMenu: true}
		m.notice = "Opening controls for " + kind + " " + opts.FocusID + "…"
	}
	m.layout()
	return m
}

// controlsKind accepts the terminal kinds an attachment can name and returns
// the row family the controls popup should select.
func controlsKind(kind string) string {
	kind = strings.TrimSuffix(kind, "-shell")
	switch kind {
	case "session", "task", "attempt", "project":
		return kind
	}
	return ""
}

func controlsSection(kind string) int {
	switch kind {
	case "task", "attempt":
		return 1
	case "project":
		return 3
	}
	return 0
}
func (m *dashboard) Init() tea.Cmd { return tea.Batch(m.refresh(), m.references(), nextTick()) }
func nextTick() tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}
func (m *dashboard) references() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		out := refsMsg{}
		for _, target := range []struct {
			path string
			dest *[]row
		}{{"/projects", &out.projects}, {"/targets", &out.targets}, {"/agents", &out.agents}, {"/launch-profiles", &out.profiles}} {
			b, e := c.JSON("GET", target.path, nil)
			if e == nil {
				e = json.Unmarshal(b, target.dest)
			}
			if e != nil {
				out.err = e
				break
			}
		}
		return out
	}
}
func (m *dashboard) refresh() tea.Cmd {
	if m.loading {
		return nil
	}
	m.loading = true
	m.generation++
	section, generation, c := sections[m.section], m.generation, m.client
	selected := m.selectionID()
	path := "/" + section
	if section == "approvals" {
		path += "?status=pending"
	}
	if section == "sessions" {
		if m.archived {
			path += "?archived=true"
		} else if m.ended {
			path += "?all=true"
		} else {
			path += "?include_setup_failures=true"
		}
	}
	return func() tea.Msg {
		b, e := c.JSON("GET", path, nil)
		r := rowsMsg{section: section, generation: generation, err: e}
		if e == nil {
			r.err = json.Unmarshal(b, &r.rows)
			if section == "sessions" && r.err == nil {
				for _, item := range r.rows {
					ws, _ := item["workspace"].(map[string]any)
					if id(item) == selected && (item["setup_state"] == "creating" || ws["state"] == "extending") && ws["repositories"] != nil {
						progress, err := c.JSON("GET", "/sessions/"+id(item)+"/worktree", nil)
						if err == nil {
							var current map[string]any
							if json.Unmarshal(progress, &current) == nil {
								item["workspace"] = current
							}
						} else {
							item["setup_progress_error"] = err.Error()
						}
					}
				}
			}
		}
		return r
	}
}
func (m *dashboard) current() row {
	if m.selected < 0 || m.selected >= len(m.visible) {
		return nil
	}
	r := m.visible[m.selected]
	if isGroup(r) {
		return nil
	}
	return r
}
func (m *dashboard) key() string { return sections[m.section] + "/" + m.selectionID() }
func (m *dashboard) group(r row) string {
	if m.grouping == 2 {
		return "All"
	}
	field := "project_name"
	if m.grouping == 3 {
		field = "group_path"
	}
	if m.grouping == 1 {
		field = "target_name"
	}
	s := str(r[field])
	if s == "" {
		if m.grouping == 3 {
			s = "Ungrouped"
		} else if m.grouping == 1 {
			s = "Local / unassigned"
		} else {
			s = "Unassigned"
		}
	}
	return oneLine(s)
}
func (m *dashboard) filter() {
	selected := m.selectionID()
	q := m.query.Value()
	status := ""
	if len(q) > 0 {
		status = map[byte]string{'!': "running", '@': "waiting", '#': "idle", '&': "failed"}[q[0]]
		if status != "" {
			q = q[1:]
		}
	}
	m.visible = nil
	for _, r := range m.rows {
		s := str(r["status"])
		if m.attention && s != "waiting" && s != "review" && s != "pending" && s != "failed" && r["setup_state"] != "failed" {
			continue
		}
		if status != "" && s != status {
			continue
		}
		if !fuzzyFields(q, name(r), str(r["project_name"]), str(r["target_name"]), str(r["agent"]), str(r["launch_profile"]), str(r["workdir"]), str(r["group_path"]), workspaceBranch(r), id(r)) {
			continue
		}
		m.visible = append(m.visible, r)
	}
	sort.SliceStable(m.visible, func(i, j int) bool {
		a, b := m.visible[i], m.visible[j]
		if m.group(a) != m.group(b) {
			return m.group(a) < m.group(b)
		}
		return strings.ToLower(name(a)) < strings.ToLower(name(b))
	})
	m.matched = len(m.visible)
	if m.treeMode() && strings.TrimSpace(m.query.Value()) == "" {
		m.visible = m.groupTree(m.visible)
	}
	m.selected = 0
	for i, r := range m.visible {
		if displayID(r) == selected {
			m.selected = i
			break
		}
	}
	m.ensureSelection()
	m.updatePreview()
}
func (m *dashboard) ensureSelection() {
	m.selected = max(0, min(m.selected, len(m.visible)-1))
	if m.selected < m.offset {
		m.offset = m.selected
	}
	m.offset = max(0, min(m.offset, m.selected))
	for m.offset < m.selected && m.rowsHeight(m.offset, m.selected+1) > max(3, m.height-8) {
		m.offset++
	}
}

// resolveFocus selects the row a controls popup asked for and opens the actions
// menu only once that exact row is loaded. A missing row is reported explicitly
// rather than silently acting on whichever row happens to be first.
func (m *dashboard) resolveFocus(v rowsMsg) (tea.Cmd, bool) {
	f := m.pendingFocus
	if f == nil || v.section != sections[f.section] {
		return nil, false
	}
	m.pendingFocus = nil
	index := -1
	for i, r := range m.visible {
		if isGroup(r) {
			continue
		}
		if f.attempt {
			a, _ := r["attempt"].(map[string]any)
			if a != nil && id(row(a)) == f.id {
				index = i
				break
			}
			continue
		}
		if id(r) == f.id {
			index = i
			break
		}
	}
	if index < 0 {
		m.notice = focusMissingNotice(f)
		return nil, true
	}
	m.selected = index
	m.ensureSelection()
	m.previewFocus = false
	m.updatePreview()
	if !f.openMenu {
		return nil, true
	}
	m.menu = true
	m.menuIndex = 0
	if f.action != "" {
		m.menu = false
		return m.controlAction(f.action), true
	}
	return nil, true
}

func focusMissingNotice(f *dashboardFocus) string {
	switch sections[f.section] {
	case "tasks":
		return "No task in the current list matches " + f.id + "; it may be archived or belong to another project."
	case "projects":
		return "No project in the current list matches " + f.id + "."
	}
	return "No session in the current live list matches " + f.id + "; press A or z to widen the view."
}

// controlAction runs one direct controls shortcut (Ctrl-] u and friends) after
// the requested row is selected.
func (m *dashboard) controlAction(name string) tea.Cmd {
	switch name {
	case "upload":
		return m.uploadForm()
	case "send":
		return m.sendForm()
	case "review":
		return m.openReview()
	case "rename":
		return m.renameForm()
	case "history":
		return m.readDetail("History")
	case "":
		return nil
	}
	m.notice = "Unknown controls action " + name
	return nil
}

// nativeDisabled explains why a native terminal action is unavailable here.
func (m *dashboard) nativeDisabled() tea.Cmd {
	m.notice = "Native terminals are disabled in the controls popup; detach (Ctrl-b d) or quit and attach from the dashboard."
	return nil
}

// showHome returns the controls popup to its actions menu after a form or detail
// closes, so Esc keeps the popup on the menu it was opened with.
func (m *dashboard) showHome() {
	if m.popup {
		m.menu = true
		m.menuIndex = 0
	}
}
func (m *dashboard) layout() {
	m.query.Width = max(12, m.width-6)
	w := m.width - 4
	if m.width >= 100 {
		w = m.width - m.listWidth() - 5
	}
	m.preview.Width = max(10, w)
	m.preview.Height = max(3, m.height-11)
	if m.form != nil {
		m.form.editor.SetWidth(max(10, m.width-10))
	}
	m.ensureSelection()
	m.updatePreview()
}
func (m *dashboard) listWidth() int { return min(48, max(30, m.width*42/100)) }
func (m *dashboard) updatePreview() {
	if r := m.selectedGroup(); r != nil {
		m.preview.SetContent(fmt.Sprintf("%s\n\n%d sessions · %d need attention\n\nEnter or Space: expand / collapse\n[ Collapse parent group\n] Expand group\n/ Search also finds hidden sessions", str(r["name"]), r["count"], r["attention"]))
		return
	}
	r := m.current()
	if r == nil && m.detailKey != m.key() {
		m.preview.SetContent("No matches. / Search · n New · f Find running agents")
		return
	}
	bottom := m.preview.AtBottom()
	offset := m.preview.YOffset
	content := m.detail
	if m.detailKey != m.key() {
		m.detailKey = ""
		m.detail = ""
		m.detailTitle = ""
	}
	if m.detailKey == "" {
		switch sections[m.section] {
		case "sessions":
			content = fmt.Sprintf("%s\n%s · %s · %s\n%s\n\n%s", name(r), str(r["agent"]), str(r["status"]), str(r["target_name"]), str(r["workdir"]), str(r["pane_tail"]))
			if label := str(r["launch_profile"]); label != "" {
				content = "Launch profile: " + label + "\n" + content
			}
			if ws, ok := r["workspace"].(map[string]any); ok {
				content = "Worktree: " + str(ws["branch"]) + " · " + str(ws["state"]) + "\nBase: " + str(ws["base"]) + "\n\n" + content
				if setup := str(ws["setup_state"]); setup != "" {
					content = "Project setup: " + setup + "\n" + str(ws["setup_output"]) + "\n" + content
				}
				if failure := str(ws["error"]); failure != "" {
					content = "Setup error: " + failure + "\n\n" + content
				}
			}
			if r["setup_state"] == "creating" {
				content = "Setting up workspace…\nAttach becomes available when setup finishes.\n\n" + content
				if r["setup_cancel_requested"] == true {
					content = "Cancellation requested. Waiting for checkout to stop; files will be retained.\n\n" + content
				}
			}
			if failure := str(r["setup_error"]); failure != "" {
				label := "Setup failed: "
				if r["setup_state"] == "creating" {
					label = "Setup status: "
				}
				content = label + failure + "\n\n" + content
			}
			if failure := str(r["setup_progress_error"]); failure != "" {
				content += "\nProgress unavailable: " + failure
			}
			if ws, ok := r["workspace"].(map[string]any); ok {
				if repos, ok := ws["repositories"].([]any); ok {
					for _, value := range repos {
						repo, _ := value.(map[string]any)
						child, _ := repo["worktree"].(map[string]any)
						content += "\n" + str(repo["name"]) + ": " + str(child["state"])
						if command := str(child["setup_command"]); command != "" {
							state := str(child["setup_state"])
							if state == "" {
								state = "not completed"
							}
							content += "\n  Project setup: " + state
							if output := str(child["setup_output"]); output != "" {
								content += "\n" + output
							}
						}
						if failure := str(child["error"]); failure != "" {
							content += "\n  Setup error: " + failure
						}
					}
				}
			}
		case "tasks":
			content = fmt.Sprintf("%s\n%s · %s\n\n%s", name(r), str(r["status"]), str(r["project_name"]), str(r["prompt"]))
			if a, ok := r["attempt"].(map[string]any); ok {
				content += "\n\nBranch: " + str(a["branch"]) + "\nWorktree: " + str(a["worktree_path"]) + "\n\nResult\n" + readable(a["result"])
			}
		default:
			content = readable(r)
		}
	}
	rendered := ansi.Wrap(clean(content), m.preview.Width, "")
	if m.detailTitle == "Diff" {
		lines := strings.Split(rendered, "\n")
		for i, line := range lines {
			if strings.HasPrefix(line, "+") {
				lines[i] = lipgloss.NewStyle().Foreground(lipgloss.Color("114")).Render(line)
			} else if strings.HasPrefix(line, "-") {
				lines[i] = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Render(line)
			} else if strings.HasPrefix(line, "@@") {
				lines[i] = accent.Render(line)
			}
		}
		rendered = strings.Join(lines, "\n")
	}
	m.preview.SetContent(rendered)
	if bottom && m.detailKey == "" {
		m.preview.GotoBottom()
	} else {
		m.preview.SetYOffset(offset)
	}
}
func pretty(v any) string { b, _ := json.MarshalIndent(v, "", "  "); return string(b) }
func (m *dashboard) switchSection(i int) tea.Cmd {
	m.groupingBySection[sections[m.section]] = m.grouping
	m.section = (i + len(sections)) % len(sections)
	m.grouping = m.groupingBySection[sections[m.section]]
	m.rows = nil
	m.visible = nil
	m.selected = 0
	m.offset = 0
	m.loading = false
	m.detailKey = ""
	m.query.SetValue("")
	m.attention = false
	m.previewFocus = false
	return m.refresh()
}
func (m *dashboard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case nativeSearchForkMsg:
		return m, m.receiveNativeSearchFork(v)
	case promotionPreviewMsg:
		m.busy = false
		if v.err != nil {
			if he, ok := v.err.(*HTTPError); ok && he.Status == 404 {
				m.notice = "Conversation promotion is unavailable on the running server; restart or update Lectern, then try again."
			} else {
				m.notice = clean(v.err.Error())
			}
			return m, nil
		}
		return m, m.promotionPreviewForm(v.data)
	case nativeSearchMsg:
		return m, m.receiveNativeSearch(v)
	case nativeSearchReadMsg:
		m.receiveNativeSearchRead(v)
		return m, nil
	case nativeSearchTick:
		if m.nativeSearch == v.owner && v.generation == v.owner.generation && !v.owner.data.Done && !v.owner.starting {
			return m, m.pollNativeSearch(v.owner)
		}
		return m, nil
	case nativeListMsg:
		return m, m.nativePicker(v)
	case reviewMsg:
		m.receiveReview(v)
		return m, nil
	case tea.WindowSizeMsg:
		m.width = v.Width
		m.height = v.Height
		m.layout()
		m.renderNativeSearch()
		if r := m.review; r != nil && !r.loading && r.failure == "" {
			offset := r.viewport.YOffset
			m.receiveReview(reviewMsg{owner: r, generation: r.generation, data: r.data})
			r.viewport.SetYOffset(offset)
		}
		return m, nil
	case tickMsg:
		return m, tea.Batch(m.refresh(), nextTick())
	case rowsMsg:
		if v.generation != m.generation || v.section != sections[m.section] {
			return m, nil
		}
		m.loading = false
		if v.err != nil {
			m.failure = clean(v.err.Error())
			return m, nil
		}
		m.failure = ""
		m.updated = time.Now()
		m.rows = v.rows
		m.filter()
		if m.focusSessionID != "" && v.section == "sessions" {
			for i, r := range m.visible {
				if id(r) == m.focusSessionID {
					m.selected = i
					m.ensureSelection()
					m.updatePreview()
					m.focusSessionID = ""
					if m.attachAfterRefresh {
						m.attachAfterRefresh = false
						return m, m.attachSelected(false)
					}
					break
				}
			}
		}
		if cmd, handled := m.resolveFocus(v); handled {
			return m, cmd
		}
		return m, nil
	case recentMsg:
		m.busy = false
		if v.err != nil {
			m.notice = "Recently closed: " + clean(v.err.Error())
			return m, nil
		}
		m.recentRows = v.rows
		m.recentSelected = 0
		m.recentOpen = true
		return m, nil
	case refsMsg:
		m.projects = v.projects
		m.targets = v.targets
		m.agents = v.agents
		m.profiles = v.profiles
		if v.err != nil {
			m.notice = "Reference lists: " + clean(v.err.Error())
		}
		return m, nil
	case resultMsg:
		m.busy = false
		if v.err != nil {
			if v.label == "Create blank shell" {
				if he, ok := v.err.(*HTTPError); ok && he.Status == 404 {
					m.notice = "Quick shell is unavailable on the running server; restart or update Lectern, then try again."
					return m, nil
				}
			}
			if v.label == "Promote conversation" {
				if he, ok := v.err.(*HTTPError); ok && he.Status == 404 {
					m.notice = "Conversation promotion is unavailable on the running server; restart or update Lectern, then try again."
					return m, nil
				}
			}
			if v.label == "Resume recently closed" && m.recentPending != nil {
				if httpErr, ok := v.err.(*HTTPError); ok && (httpErr.Status == 404 || httpErr.Status == 409) {
					r := m.recentPending
					m.recentPending = nil
					return m, m.recentHistory(r)
				}
			}
			m.notice = clean(v.err.Error())
			return m, nil
		}
		if v.preview {
			if v.key == m.key() {
				m.detailKey = v.key
				m.detailTitle = v.label
				m.detail = clean(formatDetail(v.label, v.data))
				if v.label == "Saved conversation" && m.native != nil {
					var p struct{ Before *int64 }
					json.Unmarshal(v.data, &p)
					m.native.before = p.Before
				}
				m.preview.GotoTop()
				m.updatePreview()
				m.previewFocus = true
			}
			return m, nil
		}
		m.form = nil
		m.pending = nil
		if v.label == "Resume recently closed" || v.label == "Restore tracking" {
			m.recentPending = nil
			m.recentOpen = false
			var resumed row
			if json.Unmarshal(v.data, &resumed) == nil && id(resumed) != "" {
				m.focusSessionID = id(resumed)
				m.attachAfterRefresh = true
			}
		}
		if v.notice != "" {
			m.notice = v.notice
		} else {
			m.notice = v.label + " completed"
		}
		if v.label == "Create blank shell" {
			var created row
			if json.Unmarshal(v.data, &created) == nil && id(created) != "" {
				// Only a confirmed project shell counts as project use; a
				// machine shell carries no project_id and is left alone.
				m.rememberProject(rowProjectID(created))
				// The action is global, so its result may arrive while another
				// section, archive view, or filtered session list is visible.
				// Return to the live Sessions list before the auto-attach pass.
				m.section = 0
				m.rows = nil
				m.visible = nil
				m.selected = 0
				m.offset = 0
				m.query.SetValue("")
				m.attention = false
				m.ended = false
				m.archived = false
				m.focusSessionID = id(created)
				m.attachAfterRefresh = true
				m.notice = "Blank shell created; attaching"
			}
		}
		if v.label == "Create session" {
			var created row
			if json.Unmarshal(v.data, &created) == nil && id(created) != "" {
				// The server-confirmed row is the result, so a failed launch or
				// a form abandoned before the request never records anything.
				if projectID := rowProjectID(created); projectID > 0 {
					m.rememberProject(projectID)
				} else {
					// An ordinary new session with no project (Scratch) keeps
					// Scratch as the default; a shell never reaches this path.
					m.rememberScratchSession()
				}
			}
		}
		if v.label == "Add repository" {
			m.notice = "Repository addition started; the original terminal stays available"
		}
		var reserved row
		if json.Unmarshal(v.data, &reserved) == nil && reserved["setup_state"] == "creating" && (v.label == "Create session" || v.label == "Fork conversation") {
			m.notice = "Workspace setup started. Progress appears in the session preview."
		}
		if reserved["setup_cancel_requested"] == true && (v.label == "Cancel setup" || v.label == "Retry cancellation" || v.label == "Cancel remaining checkout") {
			m.notice = "Cancellation requested. Files already created will be retained."
		}
		return m, tea.Batch(m.refresh(), m.references())
	case loadedFormMsg:
		m.busy = false
		if v.err != nil {
			m.notice = v.err.Error()
			return m, nil
		}
		if v.path == "/settings" {
			return m, m.notificationForm(v.data)
		}
		if strings.HasSuffix(v.path, "/mcp") {
			return m, m.mcpSettingsLoaded(v.data, v.path)
		}
		var body any
		if e := json.Unmarshal(v.data, &body); e != nil {
			m.notice = e.Error()
			return m, nil
		}
		return m, m.jsonForm(v.title, v.method, v.path, body)
	case skillsLoadedMsg:
		m.busy = false
		return m, m.skillsSettingsLoaded(v)
	case workflowsLoadedMsg:
		m.busy = false
		return m, m.workflowsLoaded(v)
	case discoveredMsg:
		m.busy = false
		if v.err != nil {
			m.notice = v.err.Error()
			return m, nil
		}
		return m, m.adoptForm(v.rows)
	case attachedMsg:
		if v.err != nil {
			m.notice = "Attach: " + clean(v.err.Error())
		} else {
			m.notice = "Detached. Session keeps running."
		}
		return m, tea.Batch(m.refresh(), tea.WindowSize())
	case tea.KeyMsg:
		if m.review != nil {
			return m, m.updateReview(v)
		}
		// A terminal read can contain several ordinary keystrokes. Process them
		// in order, allowing '/' to focus search before its following text.
		// Bracketed paste outside an input is data, never a command sequence.
		if v.Paste && !m.searching && m.form == nil {
			return m, nil
		}
		if len(v.Runes) > 1 && !m.searching && m.form == nil {
			var cmds []tea.Cmd
			for _, r := range v.Runes {
				_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
				cmds = append(cmds, cmd)
			}
			return m, tea.Batch(cmds...)
		}
		if m.nativeSearch != nil && m.form == nil {
			return m, m.updateNativeSearch(v)
		}
		if v.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.form != nil {
			return m, m.updateForm(v)
		}
		if m.pending != nil {
			if v.String() == "y" {
				a := *m.pending
				m.pending = nil
				return m, m.execute(a)
			}
			if v.String() == "n" || v.String() == "esc" {
				m.pending = nil
			}
			return m, nil
		}
		if m.help {
			m.help = false
			return m, nil
		}
		if m.searching {
			if v.String() == "enter" || v.String() == "esc" {
				m.searching = false
				m.query.Blur()
				return m, nil
			}
			var cmd tea.Cmd
			m.query, cmd = m.query.Update(v)
			m.filter()
			return m, cmd
		}
		if m.menu {
			list := m.actions()
			switch v.String() {
			case "esc", "q":
				if m.popup {
					return m, tea.Quit
				}
				m.menu = false
			case "m":
				m.menu = false
			case "up", "k":
				m.menuIndex = max(0, m.menuIndex-1)
			case "down", "j":
				m.menuIndex = min(len(list)-1, m.menuIndex+1)
			case "enter":
				if len(list) > 0 {
					m.menu = false
					return m, m.choose(list[m.menuIndex])
				}
			}
			return m, nil
		}
		if m.recentOpen {
			switch v.String() {
			case "esc", "backspace", "q", "C":
				m.recentOpen = false
			case "up", "k":
				m.recentSelected = max(0, m.recentSelected-1)
			case "down", "j":
				m.recentSelected = min(len(m.recentRows)-1, m.recentSelected+1)
			case "enter", "a":
				return m, m.resumeRecentSelected()
			case "h", "H":
				return m, m.recentHistorySelected()
			}
			return m, nil
		}
		switch v.String() {
		case "q":
			return m, tea.Quit
		case "G":
			return m, m.groupForm()
		case "F":
			return m, m.nativeSearchForm()
		case "H":
			return m, m.savedConversations()
		case "C":
			return m, m.loadRecentSessions()
		case "O":
			return m, m.olderNative()
		case "?":
			m.help = true
		case "/":
			m.searching = true
			return m, m.query.Focus()
		case "esc":
			if m.popup && !m.searching && m.detailKey == "" {
				return m, tea.Quit
			}
			m.query.SetValue("")
			m.detailKey = ""
			m.detail = ""
			m.previewFocus = false
			m.filter()
		case "1", "2", "3", "4", "5", "6":
			return m, m.switchSection(int(v.String()[0] - '1'))
		case "left":
			return m, m.switchSection(m.section - 1)
		case "right":
			return m, m.switchSection(m.section + 1)
		case "tab":
			m.previewFocus = !m.previewFocus
		case "up", "k":
			if m.previewFocus {
				m.preview.LineUp(1)
			} else {
				m.selected--
				m.ensureSelection()
				m.updatePreview()
			}
		case "down", "j":
			if m.previewFocus {
				m.preview.LineDown(1)
			} else {
				m.selected++
				m.ensureSelection()
				m.updatePreview()
			}
		case "pgup", "ctrl+u":
			m.preview.HalfPageUp()
		case "pgdown", "ctrl+d":
			m.preview.HalfPageDown()
		case "home":
			if m.previewFocus {
				m.preview.GotoTop()
			} else {
				m.selected = 0
				m.ensureSelection()
				m.updatePreview()
			}
		case "end":
			if m.previewFocus {
				m.preview.GotoBottom()
			} else {
				m.selected = len(m.visible) - 1
				m.ensureSelection()
				m.updatePreview()
			}
		case "g":
			modes := 3
			if m.section == 0 {
				modes = 4
			}
			m.grouping = (m.grouping + 1) % modes
			m.filter()
			m.savePreferences()
		case " ":
			m.toggleGroup()
		case "[":
			m.collapseGroup()
		case "]":
			m.expandGroup()
		case "w":
			m.attention = !m.attention
			m.filter()
		case "A":
			m.archived = !m.archived
			return m, m.refresh()
		case "z":
			m.archived = false
			m.ended = !m.ended
			return m, m.refresh()
		case "r":
			return m, m.refresh()
		case "P":
			return m, m.manageProfilesForm()
		case "Q":
			return m, m.manageAgentsForm()
		case "p":
			m.detailKey = ""
			m.previewFocus = !m.previewFocus
			m.updatePreview()
		case "enter", "a":
			if m.selectedGroup() != nil {
				m.toggleGroup()
				return m, nil
			}
			if m.controlOnly {
				return m, m.nativeDisabled()
			}
			return m, m.attachSelected(false)
		case "s":
			if m.controlOnly {
				return m, m.nativeDisabled()
			}
			return m, m.attachSelected(true)
		case "S":
			if m.controlOnly {
				return m, m.nativeDisabled()
			}
			return m, m.newShellForm()
		case "m":
			m.menu = true
			m.menuIndex = 0
		case "n":
			return m, m.newForm()
		case "e":
			return m, m.renameForm()
		case "h":
			return m, m.readDetail("History")
		case "v":
			return m, m.readDetail("Diff")
		case "f":
			return m, m.discover()
		case "7":
			return m, m.settingsForm()
		case "8":
			return m, m.readResource("Usage", "/stats")
		case "9":
			return m, m.apiForm()
		case "u":
			return m, m.uploadForm()
		}
	case tea.MouseMsg:
		if m.nativeSearch != nil && m.form == nil {
			var cmd tea.Cmd
			m.nativeSearch.viewport, cmd = m.nativeSearch.viewport.Update(v)
			return m, cmd
		}
		if m.form != nil || m.menu || m.help || m.pending != nil || m.review != nil || m.recentOpen {
			return m, nil
		}
		if v.Button == tea.MouseButtonWheelUp {
			m.preview.LineUp(3)
		} else if v.Button == tea.MouseButtonWheelDown {
			m.preview.LineDown(3)
		} else if v.Button == tea.MouseButtonLeft && v.Action == tea.MouseActionPress {
			if m.width >= 100 && v.X > m.listWidth()+1 {
				m.previewFocus = true
			} else if v.Y >= 4 && !(m.width < 100 && m.previewFocus) {
				index := m.rowAt(v.Y - 4)
				if index < 0 {
					return m, nil
				}
				m.selected = index
				m.ensureSelection()
				m.previewFocus = false
				m.updatePreview()
				if m.selectedGroup() != nil {
					m.toggleGroup()
				} else if m.section == 0 {
					return m, m.attachSelected(false)
				}
			}
		}
	}
	return m, nil
}

// The existing attachment resolver is run only while Bubble Tea has released
// raw mode and its alternate screen. Detaching returns to the same selection.
type attachmentExec struct{ run func() error }

func (e attachmentExec) Run() error          { return e.run() }
func (e attachmentExec) SetStdin(io.Reader)  {}
func (e attachmentExec) SetStdout(io.Writer) {}
func (e attachmentExec) SetStderr(io.Writer) {}
func (m *dashboard) attachSelected(shell bool) tea.Cmd {
	if m.controlOnly {
		return m.nativeDisabled()
	}
	r := m.current()
	if r == nil {
		return nil
	}
	kind := strings.TrimSuffix(sections[m.section], "s")
	rid := id(r)
	if kind == "session" && r["setup_state"] == "creating" {
		m.notice = "Workspace is setting up. Attach becomes available when setup finishes."
		return nil
	}
	if kind == "session" && r["setup_state"] == "failed" {
		m.notice = "Workspace setup failed. Inspect its error and retained files before launching again."
		return nil
	}
	if kind == "session" && r["ended_at"] != nil {
		m.menu = true
		m.menuIndex = 0
		m.notice = "Restore tracking before attaching, or use f to find running sessions."
		return nil
	}
	if kind == "task" {
		a, _ := r["attempt"].(map[string]any)
		if a == nil {
			m.notice = "This task has no running attempt. Use m → Dispatch."
			return nil
		}
		kind = "attempt"
		rid = id(row(a))
	}
	if kind != "session" && kind != "attempt" && kind != "project" {
		m.menu = true
		m.menuIndex = 0
		return nil
	}
	// Projects already resolve to a shell; Enter and s open the same terminal
	// in the repository without creating an agent session.
	if shell && kind != "project" {
		kind += "-shell"
	}
	if m.attach == nil {
		m.notice = "Native attachment unavailable"
		return nil
	}
	attach := m.attach
	return tea.Exec(attachmentExec{func() error { return attach(kind, rid) }}, func(e error) tea.Msg { return attachedMsg{e} })
}

var accent = lipgloss.NewStyle().Foreground(lipgloss.Color("141"))
var muted = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
var chosen = lipgloss.NewStyle().Foreground(lipgloss.Color("255")).Background(lipgloss.Color("60"))

func clip(s string, w int) string { return ansi.Truncate(s, max(0, w), "…") }
func (m *dashboard) View() string {
	if m.nativeSearch != nil && m.form == nil {
		return m.nativeSearchView()
	}
	if m.review != nil {
		return m.reviewView()
	}
	if m.width < 35 || m.height < 12 {
		return "Lectern\nResize to at least 35 × 12.\nq to quit"
	}
	title := accent.Bold(true).Render(" ◆ Lectern") + muted.Render("  Terminal workspace")
	if m.width < 65 {
		title = accent.Bold(true).Render(" ◆ Lectern")
	}
	connection := "Connecting…"
	if !m.updated.IsZero() {
		connection = "LIVE · " + m.updated.Format("15:04:05")
		if time.Since(m.updated) > 10*time.Second {
			connection = "STALE · fetching"
		}
	}
	if m.failure != "" {
		connection = "OFFLINE · retrying"
	}
	title += strings.Repeat(" ", max(1, m.width-ansi.StringWidth(title)-len(connection)-2)) + connection
	var tabs []string
	for i, s := range sections {
		label := fmt.Sprintf(" %d %s ", i+1, strings.Title(s))
		if i == m.section {
			label = chosen.Render(label)
		}
		tabs = append(tabs, label)
	}
	if m.width < 100 {
		tabs = []string{chosen.Render(fmt.Sprintf(" ‹ %d %s › ", m.section+1, strings.Title(sections[m.section]))), muted.Render(" 1–6 sections · ←/→ switch")}
	}
	header := clip(title, m.width) + "\n" + clip(strings.Join(tabs, ""), m.width) + "\n" + clip(m.query.View(), m.width-1) + "\n"
	group := []string{"project", "target", "none", "named group"}[m.grouping]
	meta := fmt.Sprintf(" %d/%d items · group: %s", m.matched, len(m.rows), group)
	if m.attention {
		meta += " · needs attention"
	}
	if m.archived {
		meta += " · archive"
	} else if m.ended {
		meta += " · includes ended"
	}
	header += muted.Render(clip(meta, m.width)) + "\n"
	bodyHeight := max(3, m.height-8)
	var body string
	switch {
	case m.form != nil:
		body = m.formView()
	case m.pending != nil:
		body = "\n " + m.pending.Label + "?\n\n " + ansi.Wrap(m.pending.Warning, max(10, m.width-2), "") + "\n\n y Confirm · n / Esc Cancel"
	case m.help:
		body = dashboardHelp
	case m.menu:
		list := m.actions()
		start := max(0, m.menuIndex-bodyHeight+4)
		lines := []string{" Actions · ↑↓ choose · Enter run · Esc close", ""}
		for i := start; i < len(list) && len(lines) < bodyHeight; i++ {
			s := "  " + list[i].Label
			if i == m.menuIndex {
				s = chosen.Render(s)
			}
			lines = append(lines, s)
		}
		body = strings.Join(lines, "\n")
	case m.recentOpen:
		body = m.recentView(bodyHeight)
	default:
		list := m.listView(bodyHeight)
		previewTitle := "Live preview"
		if m.selectedGroup() != nil {
			previewTitle = "Group"
		}
		if m.detailTitle != "" {
			previewTitle = m.detailTitle
		}
		if m.previewFocus {
			previewTitle += " · focused"
		}
		preview := accent.Render(" "+previewTitle) + "\n" + m.preview.View() + "\n" + muted.Render(fmt.Sprintf(" %.0f%% · Tab focus · PgUp/PgDn scroll", 100*m.preview.ScrollPercent()))
		if m.width >= 100 {
			body = lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(m.listWidth()).Render(list), muted.Render(" │ "), preview)
		} else if m.previewFocus {
			body = preview
		} else {
			body = list
		}
	}
	// Clip by display cells, not bytes: CJK, emoji and resized terminals must not
	// wrap an extra row and overwrite the footer or leave stale screen fragments.
	lines := strings.Split(body, "\n")
	for i := range lines {
		lines[i] = clip(lines[i], m.width-1)
	}
	if len(lines) > bodyHeight {
		lines = lines[:bodyHeight]
	}
	for len(lines) < bodyHeight {
		lines = append(lines, "")
	}
	status := m.notice
	if m.failure != "" {
		status = m.failure
	}
	if m.busy {
		status = "Working… " + status
	}
	keys := " Enter attach · S blank shell · / filter · F text search · n new · C closed · m actions · ? help · q quit"
	if m.selectedGroup() != nil {
		keys = " Enter fold · [ parent · ] expand · / search · ? help · q quit"
	}
	// C (recently closed) is listed because it is otherwise invisible once
	// any session is running: the empty-list menu is the only other place it
	// was offered by name.
	if m.width < 112 && m.selectedGroup() == nil {
		keys = " Enter attach · S shell · / filter · n new · C closed · m actions · ? help · q quit"
	}
	if m.width < 86 {
		keys = " Enter attach · / find · ? help · q quit"
		if m.selectedGroup() != nil {
			keys = " Enter fold · / find · ? help · q quit"
		}
	}
	if m.width < 42 {
		keys = " Enter attach · / find · q quit"
		if m.selectedGroup() != nil {
			keys = " Enter fold · / find · q quit"
		}
	}
	if sections[m.section] == "projects" {
		keys = " Enter open project shell · / find · m actions · q quit"
	}
	if m.section == 0 {
		keys = strings.Replace(keys, "Enter attach", "Click attach", 1)
	}
	footer := muted.Render(clip(keys, m.width-1)) + "\n" + clip(" "+status, m.width-1)
	return header + strings.Join(lines, "\n") + "\n" + footer
}
func (m *dashboard) listView(height int) string {
	if len(m.visible) == 0 {
		return "\n No matching items.\n / Search · n New · f Discover"
	}
	w := m.width - 2
	if m.width >= 100 {
		w = m.listWidth()
	}
	var lines []string
	for i := m.offset; i < len(m.visible) && len(lines)+m.rowHeight(i) <= height; i++ {
		r := m.visible[i]
		if isGroup(r) {
			marker := "▾"
			if m.collapsed[str(r["group_key"])] {
				marker = "▸"
			}
			line := fmt.Sprintf("%s%s %s (%d)", strings.Repeat("  ", r["depth"].(int)), marker, str(r["name"]), r["count"])
			if count := r["attention"].(int); count > 0 {
				line += fmt.Sprintf(" · %d need attention", count)
			}
			line = clip(line, w)
			if i == m.selected {
				line = chosen.Render(line + strings.Repeat(" ", max(0, w-ansi.StringWidth(line))))
			}
			lines = append(lines, line)
			continue
		}
		s := str(r["status"])
		if r["setup_state"] == "creating" {
			s = "setting up"
			if r["setup_cancel_requested"] == true {
				s = "cancelling"
			}
		}
		if r["setup_state"] == "failed" {
			s = "setup failed"
		}
		if sections[m.section] == "routines" {
			s = str(r["schedule"])
			if r["enabled"] == false {
				s = "disabled"
			}
		}
		line := "  " + oneLine(name(r))
		if i == m.selected {
			line = "› " + oneLine(name(r))
		}
		line = clip(line, w)
		if i == m.selected {
			line = chosen.Render(line + strings.Repeat(" ", max(0, w-ansi.StringWidth(line))))
		}
		color := "245"
		switch s {
		case "running", "starting", "setting up":
			color = "114"
		case "waiting", "pending", "review":
			color = "214"
		case "failed", "dead", "setup failed":
			color = "203"
		}
		meta := muted.Render("  "+m.group(r)+" · ") + lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(s) + muted.Render(" · "+str(r["agent"]))
		lines = append(lines, line, clip(meta, w))
		if m.rowHeight(i) == 3 {
			lines = append(lines, "")
		}
	}
	return strings.Join(lines, "\n")
}

const dashboardHelp = ` Keyboard shortcuts

 Click session  Attach          Click group  Fold/unfold
 ↑/k ↓/j       Select item       Enter/a  Attach (Ctrl-b d returns)
 1–6 / ←→      Change section    Tab/p    Focus list / preview
 /             Fuzzy search     @ ! # &  Search prefix: waiting/running/idle/failed
 G             Move to named group
 Space/Enter   Fold selected group   [ Collapse parent   ] Expand group
 g             Group by project/target/name   w  Needs attention only
 n             New item         e        Rename   u Upload context
 S             Blank persistent shell in a project or on a machine
 P             Launch profiles
 Q             Agent runners (add custom CLIs)
 m             All actions      f        Find and track running agents
 C             Recently closed (sessions)
 h             Full history     v        Review task diff
 F             Search saved conversation text across targets
 H             Saved conversations / fork   O Earlier saved messages
 PgUp/PgDn     Scroll preview   Esc       Clear search / return to live preview
 z             Include ended sessions   A  Archive view
 r             Refresh now
 7 Settings    8 Usage           9 Full API

 Forms: Tab/Shift-Tab move; ←/→ choose named options; Ctrl-s submit; Esc cancel.
 Quit closes only this dashboard. Your tmux sessions keep running.
 Grouping and folded groups are remembered for this server on this device.

 Press any key to close help.`
