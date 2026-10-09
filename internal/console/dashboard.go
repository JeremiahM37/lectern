package console

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// sections are the panes. The first four are numbered on screen (1–4);
// Routines and Machines are reached from the palette, and 5 still opens
// Machines as a silent alias from the old numbering.
var sections = []string{"sessions", "approvals", "projects", "tasks", "routines", "targets"}

const numberedPanes = 4

type row map[string]any

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func id(r row) string { return str(r["id"]) }

// unreachable reports a session whose machine is not answering the status poll.
func unreachable(r row) bool {
	reach, _ := r["target_reach"].(map[string]any)
	return reach["unreachable"] == true
}
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
	// agentMenuOrder is GET /agents/menu's shown/ordered agent names — the
	// same list web pickers show before "More agents…". The TUI's own
	// agent select is already just a scrollable list (never the "long
	// dropdown" problem this order exists to solve on the web/mobile
	// surfaces), so instead of hiding anything it simply sorts shown agents
	// to the top in the saved order and leaves the rest below them.
	agentMenuOrder []string
	// relaunched is what restart recovery brought back since the notice was
	// last dismissed.
	relaunched []row
	// tmux is the server's tmux_installed, nil when it does not say.
	tmux *bool
	err  error
}
type tickMsg time.Time

// historyRowMsg is the session whose history picker the dashboard was asked
// to open.
type historyRowMsg struct {
	row row
	err error
}
type resultMsg struct {
	label   string
	data    []byte
	err     error
	preview bool
	key     string
	// notice, when set, replaces the default "<label> completed" message so an
	// action can report what it actually did to the terminal.
	notice string
	// noAttach keeps a restored session in the list instead of attaching.
	noAttach bool
}
type attachedMsg struct{ err error }
type batchOpenedMsg struct {
	err  error
	exit bool
	ids  []string
}

type terminalOpenedMsg struct {
	err  error
	exit bool
	id   string
}
type promotionPreviewMsg struct {
	data []byte
	err  error
}
type dashboard struct {
	focusSessionID                 string
	client                         *Client
	attach                         func(string, string) error
	openTerminal                   func(string, string, bool) error
	openBatch                      func([]string) error
	batchSelected                  map[string]bool
	terminalWorkspace              bool
	batchOpen                      bool
	insert                         func(string) error
	section                        int
	rows, visible                  []row
	selected, offset               int
	width, height                  int
	generation                     int
	loading                        bool
	refsLoaded, shellWaiting       bool
	updated                        time.Time
	failure, notice                string
	query                          textinput.Model
	searching                      bool
	review                         *codeReview
	nativeSearch                   *nativeSearchState
	native                         *nativeSelection
	grouping                       int
	groupingBySection              map[string]int
	preferencePath                 string
	recentProjects                 []int64
	scratchNewSession              bool
	collapsed                      map[string]bool
	matched                        int
	attention, ended, archived     bool
	preview                        viewport.Model
	previewFocus                   bool
	detailKey, detailTitle, detail string
	help, menu, palette            bool
	menuIndex                      int
	paletteQuery                   string
	helpOffset                     int
	helpQuery, helpContext         string
	helpSearching                  bool
	approvals                      []row
	approvalsLoading               bool
	// agentState caches GET /targets/{id}/agents per machine for this run:
	// agent name → available / missing / unchecked.
	agentState map[string]map[string]string
	// cwd is where the dashboard was started, offered as the new session's
	// folder when the server runs on this machine.
	cwd string
	// reviewPaused keeps an open review while its commit form is shown.
	reviewPaused *codeReview
	// historySessionID is DashboardOptions.HistorySessionID until used.
	historySessionID string
	// historyResume makes the next saved-conversation picker default to
	// resuming, for a picker opened to restore a session.
	historyResume bool
	// noTmux: the server has no tmux, so there are no agents running in
	// tmux to find, and the dashboard does not offer to look.
	noTmux bool
	// commitRetry is the last commit sent, to send again with a git name
	// and email when the server says it has none.
	commitRetry *commitRequest
	// permissionDefault is the server's session_permission_mode setting.
	permissionDefault                   string
	form                                *dashboardForm
	pending                             *dashboardAction
	busy                                bool
	projects, targets, agents, profiles []row
	agentMenuOrder                      []string
	recentRows                          []row
	recentOpen                          bool
	recentSelected                      int
	recentPending                       row
	recentQuery                         string
	relaunchShown                       bool
	recentSearching                     bool
	attachAfterRefresh                  bool
	// controlOnly hides the actions that open another native terminal, so the
	// controls popup cannot nest an attachment inside itself. popup marks the
	// surface as the tmux popup whose Esc leaves the popup.
	controlOnly  bool
	popup        bool
	directAction bool
	pendingFocus *dashboardFocus
	// mods are the plugin mods running in this console (mods.go).
	mods *modState
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
	Attach            func(string, string) error
	OpenTerminal      func(string, string, bool) error
	OpenBatch         func([]string) error
	TerminalWorkspace bool
	BatchOpen         bool
	InitialSessionID  string
	// Insert types text into the attached terminal without submitting it. Only
	// the Ctrl-] controls popup sets it, where a real pane sits underneath.
	Insert      func(string) error
	ControlOnly bool
	Popup       bool
	FocusKind   string
	FocusID     string
	Action      string
	// Cwd is the directory the dashboard was started in, set only when the
	// server shares this machine's filesystem.
	Cwd string
	// HistorySessionID opens that session's saved-conversation picker as
	// soon as the dashboard has loaded (`lectern restore ID` for a session
	// whose own conversation cannot be found).
	HistorySessionID string
}

// RunDashboard uses a full-screen renderer that owns raw mode, resizing and the
// suspend/restore boundary around native tmux/SSH. The line client remains useful
// for pipes and accessibility via console --plain.
func RunDashboard(c *Client, in io.Reader, out io.Writer, attach func(string, string) error) error {
	return runDashboard(c, in, out, DashboardOptions{Attach: attach})
}

// RunDashboardWithOptions enables native attachment and background workspace tabs.
func RunDashboardWithOptions(c *Client, in io.Reader, out io.Writer, opts DashboardOptions) error {
	return runDashboard(c, in, out, opts)
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
	m.openTerminal = opts.OpenTerminal
	m.openBatch = opts.OpenBatch
	m.batchSelected = map[string]bool{}
	m.terminalWorkspace = opts.TerminalWorkspace
	m.batchOpen = opts.BatchOpen
	m.focusSessionID = opts.InitialSessionID
	m.controlOnly = opts.ControlOnly
	m.historySessionID = opts.HistorySessionID
	m.popup = opts.Popup
	m.cwd = opts.Cwd
	m.agentState = map[string]map[string]string{}
	m.mods = newModState(c)
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
		return 3
	case "project":
		return 2
	}
	return 0
}
func (m *dashboard) Init() tea.Cmd {
	return tea.Batch(m.refresh(), m.references(), m.pollApprovals(), m.loadMods(), nextTick())
}
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
		if b, e := c.JSON("GET", "/sessions/relaunched", nil); e == nil {
			_ = json.Unmarshal(b, &out.relaunched)
		}
		if b, e := c.JSON("GET", "/onboarding", nil); e == nil {
			var status struct {
				Tmux *bool `json:"tmux_installed"`
			}
			if json.Unmarshal(b, &status) == nil {
				out.tmux = status.Tmux
			}
		}
		if out.err == nil {
			if b, e := c.JSON("GET", "/agents/menu", nil); e == nil {
				var menu struct {
					Agents []string `json:"agents"`
				}
				if json.Unmarshal(b, &menu) == nil {
					out.agentMenuOrder = menu.Agents
				}
			}
			// A menu fetch failure is not fatal to references() as a whole —
			// the agent select still works, just unordered, exactly as it
			// did before this field existed.
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
			s = "No machine"
		} else {
			s = "No project"
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
		if m.modHidden(r) {
			continue
		}
		s := str(r["status"])
		if m.attention && s != "waiting" && s != "review" && s != "pending" && s != "failed" && r["setup_state"] != "failed" && !agentExited(r) && m.approvalFor(r) == nil && r["state"] != "needs_you" {
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
	for m.offset < m.selected && m.rowsHeight(m.offset, m.selected+1) > m.bodyHeight() {
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
		m.directAction = true
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
	return "No session in the current live list matches " + f.id + "; press z to include ended sessions."
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
	m.notice = "Native terminals are disabled in the controls popup; Esc goes back to the agent, and Ctrl+] d leaves it."
	return nil
}

// showHome returns the controls popup to its actions menu after a form or detail
// closes, so Esc keeps the popup on the menu it was opened with. A popup
// opened for one action (Ctrl+\ sends a file) closes instead: the person
// asked for that action, not for the menu.
func (m *dashboard) showHome() tea.Cmd {
	if m.popup && m.directAction {
		return tea.Quit
	}
	if m.popup {
		m.menu = true
		m.menuIndex = 0
	}
	return nil
}
func (m *dashboard) layout() {
	m.query.Width = max(12, m.width-6)
	w := m.width - 4
	if m.width >= 100 {
		w = m.width - m.listWidth() - 5
	}
	m.preview.Width = max(10, w)
	m.preview.Height = max(3, m.height-12)
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
		m.preview.SetContent("")
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
			content = fmt.Sprintf("%s\n%s · %s · %s\n%s\n\n%s", name(r), str(r["agent"]), m.sessionStatus(r), str(r["target_name"]), str(r["workdir"]), str(r["pane_tail"]))
			if a := m.approvalFor(r); a != nil {
				content = "Needs you: " + approvalSummary(a) + "\ny allow once · a allow for this session · N deny\n\n" + content
			} else if agentExited(r) {
				content = "The agent exited; this terminal is at a shell prompt.\nr starts the agent again (its conversation is resumed when one was saved).\n\n" + content
			}
			if unreachable(r) {
				content = str(r["target_name"]) + " is not answering; this is the last status seen.\n" + content
			}
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
		case "approvals":
			content = approvalPreview(r)
		case "tasks":
			content = fmt.Sprintf("%s\n%s · %s\n\n%s", name(r), str(r["status"]), str(r["project_name"]), str(r["prompt"]))
			if a, ok := r["attempt"].(map[string]any); ok {
				content += "\n\nBranch: " + str(a["branch"]) + "\nWorktree: " + str(a["worktree_path"]) + "\n\nResult\n" + readable(a["result"])
			}
		case "projects", "targets", "routines":
			content = itemPreview(sections[m.section], r)
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
		m.mods.host.SetViewport(v.Width, v.Height)
		m.layout()
		m.renderNativeSearch()
		if r := m.review; r != nil && !r.loading && r.failure == "" {
			offset := r.viewport.YOffset
			m.receiveReview(reviewMsg{owner: r, generation: r.generation, data: r.data})
			r.viewport.SetYOffset(offset)
		}
		return m, nil
	case tickMsg:
		var reload tea.Cmd
		if m.mods.ticks++; m.mods.ticks%modReloadTicks == 0 {
			reload = m.loadMods()
		}
		return m, tea.Batch(m.refresh(), m.pollApprovals(), reload, nextTick())
	case modsLoadedMsg:
		return m, m.receiveMods(v)
	case modNoticeMsg:
		return m, m.receiveModNotice(v)
	case modCommandMsg:
		m.receiveModCommand(v)
		return m, nil
	case approvalsMsg:
		m.approvalsLoading = false
		if v.err == nil {
			m.modApprovalEvents(v.rows)
			m.approvals = v.rows
			if sections[m.section] == "sessions" {
				m.updatePreview()
			}
		}
		return m, nil
	case agentStateMsg:
		return m, m.receiveAgentState(v)
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
		if v.section == "sessions" {
			m.modSessionEvents(v.rows)
			m.renderMods()
		}
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
		if sid := m.historySessionID; sid != "" && v.section == "sessions" {
			m.historySessionID = ""
			c := m.client
			m.notice = "Finding the saved conversations of session " + sid + "…"
			return m, func() tea.Msg {
				b, e := c.JSON("GET", "/sessions/"+sid, nil)
				var r row
				if e == nil {
					e = json.Unmarshal(b, &r)
				}
				return historyRowMsg{row: r, err: e}
			}
		}
		return m, nil
	case historyRowMsg:
		if v.err != nil {
			m.notice = "Restore: " + clean(v.err.Error())
			return m, nil
		}
		m.notice = "Pick the conversation to continue"
		return m, m.recentHistory(v.row)
	case undoMsg:
		m.busy = false
		if v.history {
			m.notice = "The last closed session has no bound conversation; choose one from its history"
			return m, m.recentHistory(v.row)
		}
		if v.result.err != nil && (v.row["agent"] == "claude" || v.row["agent"] == "codex") {
			if httpErr, ok := v.result.err.(*HTTPError); ok && httpErr.Status == 409 && str(v.row["action"]) == "resume" {
				return m, m.recentHistory(v.row)
			}
		}
		// Undo brings the session back to the list without attaching to it.
		v.result.noAttach = true
		return m.Update(v.result)
	case recentMsg:
		m.busy = false
		if v.err != nil {
			m.notice = "Restore: " + clean(v.err.Error())
			return m, nil
		}
		m.recentRows = v.rows
		m.recentSelected = 0
		m.recentQuery, m.recentSearching = "", false
		m.recentOpen = true
		return m, nil
	case refsMsg:
		m.refsLoaded = true
		m.projects = v.projects
		m.targets = v.targets
		m.agents = v.agents
		m.agentMenuOrder = v.agentMenuOrder
		m.profiles = v.profiles
		if v.tmux != nil {
			m.noTmux = !*v.tmux
		}
		if v.err != nil {
			m.notice = "Reference lists: " + clean(v.err.Error())
			m.shellWaiting = false
		} else if len(v.relaunched) > 0 && !m.relaunchShown {
			// Once per dashboard run; Dismiss in the web clears it for good.
			m.relaunchShown = true
			names := make([]string, 0, len(v.relaunched))
			for _, r := range v.relaunched {
				names = append(names, name(r))
			}
			m.notice = fmt.Sprintf("Relaunched %d session(s) after a restart: %s", len(v.relaunched), strings.Join(names, ", "))
		}
		if m.shellWaiting && v.err == nil {
			// The blank-shell action was asked for before the machine list
			// arrived; open it now instead of reporting no machines.
			m.shellWaiting = false
			return m, m.newShellForm()
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
			if v.label == commitLabel && needsGitIdentity(v.err) && m.commitRetry != nil {
				if m.review != nil && m.reviewPaused == nil {
					m.review, m.reviewPaused = nil, m.review
				}
				return m, m.gitIdentityForm()
			}
			if v.label == restoreLabel && m.recentPending != nil {
				// A resume whose conversation cannot be found falls back to
				// the history picker, as the server's needs_history says.
				r := m.recentPending
				m.recentPending = nil
				if httpErr, ok := v.err.(*HTTPError); ok && httpErr.Status == 409 && (str(r["action"]) == "resume" || str(r["action"]) == "history") &&
					(r["agent"] == "claude" || r["agent"] == "codex") {
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
		if m.popup && m.directAction {
			// The one thing this popup was opened for is done (a file sent,
			// a message typed): go straight back to the agent.
			return m, tea.Quit
		}
		if v.label == reviveLabel {
			var revived row
			if json.Unmarshal(v.data, &revived) == nil && id(revived) != "" {
				m.focusSessionID = id(revived)
				m.attachAfterRefresh = true
				v.notice = "Revived " + name(revived) + "; attaching"
			}
		}
		if v.label == restoreLabel {
			m.recentPending = nil
			m.recentOpen = false
			if restored, message := restoredSession(v.data); restored != "" {
				m.focusSessionID = restored
				m.attachAfterRefresh = !v.noAttach
				if message != "" {
					v.notice = message
				}
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
				// Like `lectern claude`: the new session is selected and, once it
				// is ready, attached, so the next thing is talking to the agent.
				m.focusSessionID = id(created)
				m.notice = "Started " + strconv.Quote(oneLine(name(created)))
				if created["setup_state"] != "creating" && !m.controlOnly && m.attach != nil {
					m.attachAfterRefresh = true
					m.notice += "; attaching. Ctrl+] d comes back here."
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
		if v.label == commitLabel && m.reviewPaused != nil {
			m.review, m.reviewPaused = m.reviewPaused, nil
			return m, tea.Batch(m.loadReview(""), m.refresh())
		}
		if v.label == decideLabel {
			if m.popup {
				// Answered from Ctrl+] m while attached: go straight back to
				// the agent that asked.
				return m, tea.Quit
			}
			return m, tea.Batch(m.refresh(), m.pollApprovals())
		}
		return m, tea.Batch(m.refresh(), m.references(), m.pollApprovals())
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
	case batchOpenedMsg:
		m.busy = false
		if v.err != nil {
			m.notice = "Open terminals: " + clean(v.err.Error())
		} else {
			for _, id := range v.ids {
				delete(m.batchSelected, id)
			}
			m.notice = fmt.Sprintf("Opened %d terminals", len(v.ids))
			if v.exit {
				return m, tea.Quit
			}
		}
		return m, tea.EnableMouseCellMotion
	case terminalOpenedMsg:
		m.busy = false
		if v.err != nil {
			m.notice = "New terminal: " + clean(v.err.Error())
		} else if v.exit {
			return m, tea.Quit
		} else {
			m.notice = "Opened session " + v.id + " in a new terminal"
		}
		return m, tea.EnableMouseCellMotion
	case attachedMsg:
		if v.err != nil {
			m.notice = "Attach: " + clean(v.err.Error())
		} else {
			m.notice = "Detached. Session keeps running."
		}
		return m, tea.Batch(m.refresh(), tea.WindowSize(), tea.EnableMouseCellMotion)
	case tea.KeyMsg:
		if m.review != nil {
			return m, m.updateReview(v)
		}
		typing := m.searching || m.palette || m.helpSearching || m.form != nil || m.recentSearching
		// A terminal read can contain several ordinary keystrokes. Process them
		// in order, allowing '/' to focus search before its following text.
		// Bracketed paste outside an input is data, never a command sequence.
		if v.Paste && !typing {
			return m, nil
		}
		if len(v.Runes) > 1 && !typing {
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
		if m.pending == nil && m.modPaneOpen() {
			return m, m.updateModPane(v)
		}
		if m.pending != nil {
			if v.String() == "y" {
				a := *m.pending
				m.pending = nil
				return m, m.execute(a)
			}
			if v.String() == "n" || v.String() == "esc" || v.String() == "q" {
				m.pending = nil
				m.notice = "Cancelled"
				if m.reviewPaused != nil {
					m.review, m.reviewPaused = m.reviewPaused, nil
				}
			}
			return m, nil
		}
		if m.help {
			return m, m.updateHelp(v)
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
		if m.palette {
			return m, m.updatePalette(v)
		}
		if m.menu {
			return m, m.updateMenu(v)
		}
		if m.recentOpen && m.recentSearching {
			switch v.Type {
			case tea.KeyEnter, tea.KeyEsc:
				m.recentSearching = false
				if v.Type == tea.KeyEsc {
					m.recentQuery = ""
				}
			case tea.KeyBackspace:
				if r := []rune(m.recentQuery); len(r) > 0 {
					m.recentQuery = string(r[:len(r)-1])
				}
			case tea.KeySpace:
				m.recentQuery += " "
			case tea.KeyRunes:
				m.recentQuery += string(v.Runes)
			}
			m.recentSelected = 0
			return m, nil
		}
		if m.recentOpen {
			switch v.String() {
			case "esc", "backspace", "q", "C", "r":
				if m.recentQuery != "" && v.String() == "esc" {
					m.recentQuery = ""
					m.recentSelected = 0
					return m, nil
				}
				m.recentOpen = false
			case "/":
				m.recentSearching = true
			case "up", "k":
				m.recentSelected = max(0, m.recentSelected-1)
			case "down", "j":
				m.recentSelected = min(len(m.recentVisible())-1, m.recentSelected+1)
			case "enter", "a":
				return m, m.resumeRecentSelected()
			case "h", "H":
				return m, m.recentHistorySelected()
			case "?":
				m.openHelp()
			}
			return m, nil
		}
		return m, m.updateTopLevel(v)
	case tea.MouseMsg:
		if m.nativeSearch != nil && m.form == nil {
			var cmd tea.Cmd
			m.nativeSearch.viewport, cmd = m.nativeSearch.viewport.Update(v)
			return m, cmd
		}
		if m.form != nil || m.menu || m.help || m.pending != nil || m.review != nil || m.recentOpen || m.modPaneOpen() {
			return m, nil
		}
		if v.Button == tea.MouseButtonWheelUp {
			m.preview.LineUp(3)
		} else if v.Button == tea.MouseButtonWheelDown {
			m.preview.LineDown(3)
		} else if (v.Button == tea.MouseButtonLeft || v.Button == tea.MouseButtonRight) && v.Action == tea.MouseActionPress {
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
					if v.Button == tea.MouseButtonRight {
						return m, m.attachSelectedTo(false, true)
					}
					if m.batchOpen {
						m.toggleBatchSelection()
						return m, nil
					}
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
	return m.attachSelectedTo(shell, false)
}

func (m *dashboard) attachSelectedTo(shell, newTerminal bool) tea.Cmd {
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
		if r["can_restore"] == true && r["archived_at"] == nil {
			m.notice = "This session has ended. r tracks it again, then Enter attaches."
		} else {
			m.notice = "This session has ended. r opens the Restore list, which brings back its conversation."
		}
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
		m.openMenu()
		return nil
	}
	// Projects already resolve to a shell; Enter and s open the same terminal
	// in the repository without creating an agent session.
	if shell && kind != "project" {
		kind += "-shell"
	}
	if newTerminal {
		if m.openTerminal == nil {
			m.notice = "New terminal launcher unavailable"
			return nil
		}
		if m.busy {
			return nil
		}
		open, batch, workspace := m.openTerminal, m.batchOpen, m.terminalWorkspace
		m.busy = true
		done := func(err error) tea.Msg { return terminalOpenedMsg{err: err, exit: workspace && err == nil, id: rid} }
		if workspace {
			return tea.Exec(attachmentExec{func() error { return open(kind, rid, batch) }}, done)
		}
		return func() tea.Msg { return done(open(kind, rid, batch)) }
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

// bodyHeight is the room between the four header lines and the two footer
// lines (the key bar and the status line).
func (m *dashboard) bodyHeight() int { return max(3, m.height-8) }

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
	title := accent.Bold(true).Render(" ◆ Lectern")
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
	header := clip(title, m.width) + "\n" + clip(m.tabsView(), m.width) + "\n" + clip(m.query.View(), m.width-1) + "\n"
	if banner := m.approvalBanner(m.width - 1); banner != "" {
		header += needsStyle.Render(clip(banner, m.width-1)) + "\n"
	} else {
		group := []string{"project", "machine", "none", "named group"}[m.grouping]
		meta := fmt.Sprintf(" %d/%d items · group: %s", m.matched, len(m.rows), group)
		if m.batchOpen {
			meta = fmt.Sprintf(" BATCH SELECT · %d selected · click/Space marks · Enter opens · b stops", len(m.batchSelected))
		}
		if m.attention {
			meta += " · needs you only"
		}
		if m.archived {
			meta += " · archive"
		} else if m.ended {
			meta += " · includes ended"
		}
		header += muted.Render(clip(meta, m.width)) + "\n"
	}
	bodyHeight := m.bodyHeight()
	var body string
	switch {
	case m.form != nil:
		body = m.formView()
	case m.pending != nil:
		body = "\n " + accent.Bold(true).Render(m.pending.Label+"?") + "\n\n " + ansi.Wrap(m.pending.Warning, max(10, m.width-2), "")
	case m.modPaneOpen():
		body = m.modPaneView(bodyHeight)
	case m.help:
		body = m.helpView(bodyHeight)
	case m.palette:
		body = m.paletteView(bodyHeight)
	case m.menu:
		body = m.menuView(bodyHeight)
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
			previewTitle += " · focused (Esc back)"
		}
		preview := accent.Render(" "+previewTitle) + "\n" + m.preview.View() + "\n" + muted.Render(fmt.Sprintf(" %.0f%% · p focus · PgUp/PgDn scroll", 100*m.preview.ScrollPercent()))
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
	if m.form != nil {
		status = "" // The form shows its notice in full.
	}
	if m.failure != "" {
		status = m.failure
	}
	if m.busy {
		status = "Working… " + status
	}
	footer := renderKeyBar(m.keyBar(), m.width) + "\n" + m.statusLine(status)
	return header + strings.Join(lines, "\n") + "\n" + footer
}

// tabsView numbers the four main panes and shows the approval count. On a
// pane reached from the palette (Routines, Machines) its name is added.
func (m *dashboard) tabsView() string {
	label := func(i int) string {
		text := fmt.Sprintf("%d %s", i+1, paneTitle(sections[i]))
		if sections[i] == "approvals" && len(m.approvals) > 0 {
			text += fmt.Sprintf(" (%d)", len(m.approvals))
		}
		return text
	}
	if m.width < 60 {
		current := paneTitle(sections[m.section])
		if m.section < numberedPanes {
			current = label(m.section)
		}
		out := chosen.Render(" ‹ " + current + " › ")
		if len(m.approvals) > 0 && sections[m.section] != "approvals" {
			out += needsStyle.Render(fmt.Sprintf(" 2 Approvals (%d)", len(m.approvals)))
		} else {
			out += muted.Render(" Tab next")
		}
		return out
	}
	var tabs []string
	for i := 0; i < numberedPanes; i++ {
		text := " " + label(i) + " "
		switch {
		case i == m.section:
			text = chosen.Render(text)
		case sections[i] == "approvals" && len(m.approvals) > 0:
			text = needsStyle.Render(text)
		}
		tabs = append(tabs, text)
	}
	if m.section >= numberedPanes {
		tabs = append(tabs, chosen.Render(" "+paneTitle(sections[m.section])+" "))
	}
	return strings.Join(tabs, " ")
}
func (m *dashboard) listView(height int) string {
	if len(m.visible) == 0 {
		switch {
		case m.query.Value() != "":
			return "\n No matching items.\n Esc clears the search."
		case sections[m.section] == "approvals":
			return "\n Nothing needs you right now.\n Agents that ask before running a command show up here."
		case sections[m.section] == "sessions" && (m.ended || m.archived):
			return "\n No matching items.\n z or A goes back to live sessions."
		case sections[m.section] == "sessions":
			return "\n No live sessions.\n\n n    start an agent\n r    restore an ended one\n z    include ended sessions\n f    find agents already running"
		}
		return "\n No matching items.\n n New · / Search"
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
		switch sections[m.section] {
		case "sessions":
			s = m.sessionStatus(r)
		case "approvals":
			s = statusNeedsYou
		}
		if sections[m.section] == "routines" {
			s = str(r["schedule"])
			if r["enabled"] == false {
				s = "disabled"
			}
		}
		line := "  " + oneLine(m.rowTitle(r))
		if i == m.selected {
			line = "› " + oneLine(m.rowTitle(r))
		}
		// What mods append to a session card goes after the title, which
		// gives up room first.
		extra := m.modCardInline(r)
		lw := w
		if extra != "" {
			lw = max(8, w-ansi.StringWidth(extra)-1)
		}
		if m.batchOpen && m.section == 0 {
			marker := "[ ] "
			if m.batchSelected[id(r)] {
				marker = "[x] "
			}
			prefix := "  "
			if i == m.selected {
				prefix = "› "
			}
			line = prefix + marker + oneLine(m.rowTitle(r))
		}
		line = clip(line, lw)
		if m.batchOpen && m.batchSelected[id(r)] && i != m.selected {
			line = lipgloss.NewStyle().Foreground(lipgloss.Color("114")).Bold(true).Render(line)
		}
		if i == m.selected {
			line = chosen.Render(line + strings.Repeat(" ", max(0, lw-ansi.StringWidth(line))))
		}
		if extra != "" {
			line += " " + extra
		}
		color := statusColor(s)
		word := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(s)
		if strings.HasPrefix(s, statusNeedsYou) {
			word = needsStyle.Render(s)
		}
		meta := muted.Render("  "+m.group(r)+" · ") + word + muted.Render(" · "+str(r["agent"]))
		if sections[m.section] == "sessions" {
			// Age goes last so a narrow row drops it before the status word.
			if age := sessionAge(r); age != "" {
				meta += muted.Render(" · " + age)
			}
		}
		if line := itemMeta(sections[m.section], r); line != "" {
			meta = muted.Render("  " + line)
		}
		if sections[m.section] == "approvals" {
			meta = muted.Render("  ") + needsStyle.Render(statusNeedsYou) + muted.Render(" · "+approvalSummary(r))
		}
		lines = append(lines, line, clip(meta, w))
		if m.rowHeight(i) == 3 {
			lines = append(lines, "")
		}
	}
	return strings.Join(lines, "\n")
}

func (m *dashboard) toggleBatchSelection() {
	if m.busy || m.controlOnly || m.section != 0 || m.selectedGroup() != nil || m.selected < 0 || m.selected >= len(m.visible) {
		return
	}
	r := m.visible[m.selected]
	if r["ended_at"] != nil || r["setup_state"] == "creating" || r["setup_state"] == "failed" {
		m.notice = "Select a ready, live session"
		return
	}
	if m.batchSelected == nil {
		m.batchSelected = map[string]bool{}
	}
	key := id(r)
	if m.batchSelected[key] {
		delete(m.batchSelected, key)
	} else {
		m.batchSelected[key] = true
	}
}
func (m *dashboard) openSelectedBatch() tea.Cmd {
	if m.busy || m.controlOnly {
		return nil
	}
	ids := []string{}
	// Use refreshed rows, including selected rows outside the current search.
	for _, r := range m.rows {
		if m.batchSelected[id(r)] && r["ended_at"] == nil && r["setup_state"] != "creating" && r["setup_state"] != "failed" {
			ids = append(ids, id(r))
		}
	}
	if len(ids) == 0 {
		m.notice = "Select sessions with click or Space first"
		return nil
	}
	if m.openBatch == nil {
		m.notice = "Terminal launcher unavailable"
		return nil
	}
	open, workspace := m.openBatch, m.terminalWorkspace
	m.busy = true
	done := func(err error) tea.Msg { return batchOpenedMsg{err: err, exit: workspace && err == nil, ids: ids} }
	if workspace {
		return tea.Exec(attachmentExec{func() error { return open(ids) }}, done)
	}
	return func() tea.Msg { return done(open(ids)) }
}
