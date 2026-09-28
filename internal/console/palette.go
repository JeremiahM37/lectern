package console

import (
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Two ways to find a command without memorising keys:
//
//   - m opens a short context menu: what the selection is most often used
//     for, at most eight entries, the last of which opens the palette.
//   - : or Ctrl+K opens the palette: every command for the selection and
//     every global one, searched by plain words, Enter running the
//     highlighted (best) match.
//
// Both show each command's key, so using them teaches the shortcuts.

const menuLimit = 8

var allCommandsAction = dashboardAction{Label: "All commands…", Operation: "palette", Key: ":"}

// preferred lists, per selection kind, the labels the context menu shows
// first. Anything missing for this row is skipped.
var preferred = map[string][]string{
	"session":        {"Allow once", "Allow for this session", "Deny", "Attach", "Open in a new window", "Send message", "Upload context file", "Review changes", "Rename", "End session", "Stop tracking (leave running)"},
	"session-exited": {reviveLabel, "Attach", "Review changes", "Read history", "Rename", "End session", "Stop tracking (leave running)"},
	"session-archived": {"Unarchive record", "Archived terminal output", "Saved conversations", "Rename"},
	"session-ended":  {"Track again", "Saved conversations", "Rename", "Handoff summaries", "Archive stopped record"},
	"session-setup":  {"Cancel setup", "Retry cancellation", "Workspace setup progress", "Rename", "Move to group"},
	"approval":       {"Allow once", "Allow for this session", "Deny", "Deny with a reason…", "Open the session"},
	"task":           {"Attach to attempt", "Review diff", "Send message", "Dispatch in worktree", "Request changes", "Commit changes", "Mark complete"},
	"project":        {"Open project shell", "Review changes", "Project brief", "Rename", "Edit project", "Skills (attach / detach)", "MCP settings (add / edit / remove)"},
	"target":         {"Check connection", "Check agent commands", "Rename", "Edit target"},
	"routine":        {"Run now", "Enable schedule", "Disable schedule", "Rename", "Edit routine"},
}

func (m *dashboard) selectionKind() string {
	if m.selectedGroup() != nil {
		return "group"
	}
	r := m.current()
	if r == nil {
		return ""
	}
	switch sections[m.section] {
	case "sessions":
		switch {
		case r["setup_state"] == "creating":
			return "session-setup"
		case r["archived_at"] != nil:
			return "session-archived"
		case r["ended_at"] != nil:
			return "session-ended"
		case agentExited(r):
			return "session-exited"
		}
		return "session"
	case "approvals":
		return "approval"
	}
	return strings.TrimSuffix(sections[m.section], "s")
}

// contextActions is the short menu: the preferred actions this row has, then
// its other actions until the menu holds seven, then "All commands…".
func (m *dashboard) contextActions() []dashboardAction {
	actions := m.actionsFor(m.rowActions())
	if len(actions) == 0 {
		actions = m.actionsFor(m.globalActions())
		if len(actions) > menuLimit-1 {
			actions = actions[:menuLimit-1]
		}
		return append(actions, allCommandsAction)
	}
	byLabel := map[string]int{}
	for i, a := range actions {
		if _, ok := byLabel[a.Label]; !ok {
			byLabel[a.Label] = i
		}
	}
	used := map[int]bool{}
	var out []dashboardAction
	for _, label := range preferred[m.selectionKind()] {
		if i, ok := byLabel[label]; ok && !used[i] && len(out) < menuLimit-1 {
			out = append(out, actions[i])
			used[i] = true
		}
	}
	for i, a := range actions {
		if len(out) >= menuLimit-1 || len(out) >= 4 {
			break
		}
		if !used[i] && a.Warning == "" {
			out = append(out, a)
			used[i] = true
		}
	}
	return append(out, allCommandsAction)
}

// actionsFor drops the actions a controls popup cannot run.
func (m *dashboard) actionsFor(list []dashboardAction) []dashboardAction {
	if !m.controlOnly {
		return list
	}
	out := make([]dashboardAction, 0, len(list))
	for _, a := range list {
		if !nativeTerminalAction(a.Operation) {
			out = append(out, a)
		}
	}
	return out
}

// globalActions are the commands that do not depend on the selection. Their
// keys are the silent aliases this release keeps; the palette is how a new
// user finds them.
func (m *dashboard) globalActions() []dashboardAction {
	kind := strings.TrimSuffix(sections[m.section], "s")
	if kind == "target" {
		kind = "machine"
	}
	list := []dashboardAction{
		{Label: "New session", Operation: "new-session", Key: "n", Keywords: "create start launch agent claude codex"},
	}
	if kind != "session" && kind != "approval" {
		list = append(list, dashboardAction{Label: "New " + kind, Operation: "new", Key: "n", Keywords: "create add"})
	}
	list = append(list,
		dashboardAction{Label: "Restore closed or interrupted sessions", Operation: "recent-sessions", Key: "r", Keywords: "reopen resume ended archived history back"},
		dashboardAction{Label: "Undo: reopen the session ended last", Operation: "undo-close", Key: "U", Keywords: "restore reopen"},
		dashboardAction{Label: "Filter this list", Operation: "filter-list", Key: "/", Keywords: "search find"},
		dashboardAction{Label: "Search past conversation text", Operation: "search-history", Key: "F", Keywords: "history transcript find saved"},
		dashboardAction{Label: "Saved conversations and forks", Operation: "saved-history", Key: "H", Keywords: "history resume fork"},
		dashboardAction{Label: "Find and track agents already running", Operation: "discover", Key: "f", Keywords: "adopt discover tmux untracked"},
		dashboardAction{Label: "Open a blank shell in a project or on a machine", Operation: "blank-shell", Key: "S", Keywords: "terminal bash"},
		dashboardAction{Label: "Select several sessions to open", Operation: "batch-terminals", Key: "b", Keywords: "batch multiple windows"},
		dashboardAction{Label: "Open the selected session in a new window", Operation: "new-terminal", Key: "o", Keywords: "terminal window tab"},
		dashboardAction{Label: "Go to Sessions", Operation: "pane:0", Key: "1"},
		dashboardAction{Label: "Go to Approvals", Operation: "pane:1", Key: "2", Keywords: "needs you permission allow deny"},
		dashboardAction{Label: "Go to Projects", Operation: "pane:2", Key: "3", Keywords: "repository repo"},
		dashboardAction{Label: "Go to Tasks", Operation: "pane:3", Key: "4", Keywords: "board"},
		dashboardAction{Label: "Go to Routines", Operation: "pane:4", Keywords: "schedule cron"},
		dashboardAction{Label: "Go to Machines", Operation: "pane:5", Key: "5", Keywords: "targets ssh hosts"},
		dashboardAction{Label: "Show only sessions that need you", Operation: "attention", Key: "w", Keywords: "attention waiting filter"},
		dashboardAction{Label: "Include ended sessions", Operation: "ended", Key: "z", Keywords: "stopped closed"},
		dashboardAction{Label: "Show archived sessions", Operation: "archive", Key: "A", Keywords: "archive"},
		dashboardAction{Label: "Group sessions by project, machine, none or named group", Operation: "grouping", Key: "g", Keywords: "group sort"},
		dashboardAction{Label: "Move the selected session to a named group", Operation: "group", Key: "G"},
		dashboardAction{Label: "Launch profiles", Operation: "launch-profiles", Key: "P", Keywords: "profile account model"},
		dashboardAction{Label: "Agent runners (add a custom agent CLI)", Operation: "agents", Key: "Q", Keywords: "agents custom cli"},
		dashboardAction{Label: "Notification settings", Operation: "settings", Key: "7", Keywords: "settings discord ntfy alerts"},
		dashboardAction{Label: "Usage and token statistics", Operation: "usage", Key: "8", Keywords: "cost tokens stats"},
		dashboardAction{Label: "API explorer", Operation: "api", Key: "9", Keywords: "json http advanced"},
		dashboardAction{Label: "Refresh now", Operation: "refresh", Key: "Ctrl+R", Keywords: "reload"},
		dashboardAction{Label: "Keys and help", Operation: "help", Key: "?", Keywords: "shortcuts keyboard"},
		dashboardAction{Label: "Quit (agents keep running)", Operation: "quit", Key: "q", Keywords: "exit close"},
	)
	if m.popup {
		out := list[:0]
		for _, a := range list {
			if a.Operation != "quit" {
				out = append(out, a)
			}
		}
		list = out
	}
	return list
}

// paletteActions ranks every command against the query. The selection's own
// actions come first on a tie, since they are what the person is looking at.
func (m *dashboard) paletteActions() []dashboardAction {
	list := append(m.actionsFor(m.rowActions()), m.actionsFor(m.globalActions())...)
	words := strings.Fields(strings.ToLower(m.paletteQuery))
	if len(words) == 0 {
		return list
	}
	type scored struct {
		a     dashboardAction
		score int
	}
	var hits []scored
	for _, a := range list {
		if s := commandScore(a, words); s > 0 {
			hits = append(hits, scored{a, s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]dashboardAction, len(hits))
	for i, h := range hits {
		out[i] = h.a
	}
	return out
}

// commandScore ranks a label word that starts with the query word above one
// that merely contains it, so "end" finds End session before Send message.
func commandScore(a dashboardAction, words []string) int {
	label := strings.ToLower(a.Label)
	labelWords := strings.FieldsFunc(label, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') })
	keywords := strings.Fields(strings.ToLower(a.Keywords))
	total := 0
	for _, w := range words {
		best := 0
		for _, lw := range labelWords {
			if strings.HasPrefix(lw, w) {
				best = max(best, 4)
			}
		}
		if best == 0 && strings.Contains(label, w) {
			best = 2
		}
		for _, kw := range keywords {
			if best < 3 && strings.HasPrefix(kw, w) {
				best = 3
			}
		}
		if best == 0 {
			return 0
		}
		total += best
	}
	if strings.HasPrefix(label, strings.Join(words, " ")) {
		total += 5
	}
	return total
}

func (m *dashboard) openMenu() {
	m.menu = true
	m.palette = false
	m.menuIndex = 0
}

func (m *dashboard) openPalette(query string) {
	m.menu = false
	m.palette = true
	m.paletteQuery = query
	m.menuIndex = 0
}

func (m *dashboard) closeMenus() {
	m.menu = false
	m.palette = false
	m.paletteQuery = ""
	m.menuIndex = 0
}

func (m *dashboard) updateMenu(k tea.KeyMsg) tea.Cmd {
	list := m.contextActions()
	switch k.String() {
	case "esc", "q", "m":
		m.closeMenus()
		if m.popup && k.String() != "m" {
			return tea.Quit
		}
		return nil
	case ":", "/", "ctrl+k":
		m.openPalette("")
		return nil
	case "up", "k", "ctrl+p":
		m.menuIndex = (m.menuIndex - 1 + len(list)) % len(list)
		return nil
	case "down", "j", "ctrl+n", "tab":
		m.menuIndex = (m.menuIndex + 1) % len(list)
		return nil
	case "enter":
		a := list[max(0, min(m.menuIndex, len(list)-1))]
		m.closeMenus()
		return m.choose(a)
	}
	// An item's own key runs it from the menu, which is how the menu
	// teaches its shortcuts.
	for _, a := range list {
		if a.Key != "" && a.Key == k.String() {
			m.closeMenus()
			return m.choose(a)
		}
	}
	return nil
}

func (m *dashboard) updatePalette(k tea.KeyMsg) tea.Cmd {
	list := m.paletteActions()
	switch k.Type {
	case tea.KeyEsc:
		if m.paletteQuery != "" {
			m.paletteQuery = ""
			m.menuIndex = 0
			return nil
		}
		m.closeMenus()
		m.showHome()
		return nil
	case tea.KeyEnter:
		if len(list) == 0 {
			return nil
		}
		a := list[max(0, min(m.menuIndex, len(list)-1))]
		m.closeMenus()
		return m.choose(a)
	case tea.KeyUp, tea.KeyCtrlP:
		if len(list) > 0 {
			m.menuIndex = (m.menuIndex - 1 + len(list)) % len(list)
		}
		return nil
	case tea.KeyDown, tea.KeyCtrlN, tea.KeyTab:
		if len(list) > 0 {
			m.menuIndex = (m.menuIndex + 1) % len(list)
		}
		return nil
	case tea.KeyBackspace:
		if r := []rune(m.paletteQuery); len(r) > 0 {
			m.paletteQuery = string(r[:len(r)-1])
		}
	case tea.KeySpace:
		m.paletteQuery += " "
	case tea.KeyRunes:
		if len([]rune(m.paletteQuery)) < 200 {
			m.paletteQuery += string(k.Runes)
		}
	default:
		return nil
	}
	m.menuIndex = 0
	return nil
}

func (m *dashboard) menuView(height int) string {
	title := "Actions"
	if r := m.current(); r != nil {
		title = oneLine(name(r))
		if sections[m.section] == "approvals" {
			title = "Approval · " + approvalWho(r)
		}
	}
	lines := []string{accent.Bold(true).Render(" " + title), ""}
	lines = append(lines, m.commandLines(m.contextActions(), height-2)...)
	return strings.Join(lines, "\n")
}

func (m *dashboard) paletteView(height int) string {
	list := m.paletteActions()
	lines := []string{accent.Bold(true).Render(" :") + " " + m.paletteQuery + "▏", ""}
	if len(list) == 0 {
		lines = append(lines, muted.Render(" No command matches. Backspace edits · Esc clears"))
		return strings.Join(lines, "\n")
	}
	lines = append(lines, m.commandLines(list, height-2)...)
	return strings.Join(lines, "\n")
}

// commandLines renders a list with the highlighted row kept on screen and
// each command's key right-aligned.
func (m *dashboard) commandLines(list []dashboardAction, room int) []string {
	room = max(1, room)
	m.menuIndex = max(0, min(m.menuIndex, len(list)-1))
	start := max(0, m.menuIndex-room+1)
	width := min(max(40, m.width-6), 72)
	var lines []string
	for i := start; i < len(list) && len(lines) < room; i++ {
		label := "   " + list[i].Label
		if i == m.menuIndex {
			label = " › " + list[i].Label
		}
		key := list[i].Key
		label = clip(label, width-ansi.StringWidth(key)-2)
		line := label + strings.Repeat(" ", max(1, width-ansi.StringWidth(label)-ansi.StringWidth(key))) + keyStyle.Render(key)
		if i == m.menuIndex {
			line = chosen.Render(label+strings.Repeat(" ", max(1, width-ansi.StringWidth(label)-ansi.StringWidth(key)))) + keyStyle.Render(key)
		}
		lines = append(lines, line)
	}
	return lines
}
