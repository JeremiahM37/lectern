package console

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// keyHint is one entry of the key bar and the help screen: the key as the
// operator types it and what it does here, in plain words.
type keyHint struct{ Key, Label string }

// The status words every surface uses (docs/design/simple-tui.md). "needs
// you" is reserved for a person actually being needed: a pending approval.
const (
	statusWorking  = "working"
	statusNeedsYou = "needs you"
	statusReady    = "ready"
	statusStopped  = "stopped"
)

// sessionStatus is the one word a session row shows. Setup and reachability
// keep their own honest wording; everything else maps onto the four words.
func (m *dashboard) sessionStatus(r row) string {
	s := sessionWord(r, m.approvalFor(r) != nil)
	if unreachable(r) {
		s = "unreachable · " + s
	}
	return s
}

func sessionWord(r row, needsYou bool) string {
	switch {
	case r["setup_state"] == "creating" && r["setup_cancel_requested"] == true:
		return "cancelling"
	case r["setup_state"] == "creating":
		return "setting up"
	case r["setup_state"] == "failed":
		return "setup failed"
	case agentExited(r):
		return statusStopped + " · agent exited"
	case r["ended_at"] != nil || str(r["status"]) == "dead":
		return statusStopped
	case needsYou:
		return statusNeedsYou
	}
	switch str(r["status"]) {
	case "running", "starting":
		return statusWorking
	case "waiting", "idle":
		return statusReady
	}
	return str(r["status"])
}

func statusColor(s string) string {
	switch strings.TrimSuffix(strings.TrimPrefix(s, "unreachable · "), " · agent exited") {
	case statusWorking, "starting", "setting up", "running":
		return "114"
	case statusNeedsYou, "pending", "review":
		return "214"
	case "failed", "setup failed", "dead":
		return "203"
	case statusReady:
		return "111"
	}
	return "245"
}

// keyBar is the always-visible bottom line: only the keys that work for the
// current view and selection, most useful first. "? keys" is added by
// renderKeyBar and never cut off.
func (m *dashboard) keyBar() []keyHint {
	switch {
	case m.form != nil:
		if m.form.singleLine() {
			return []keyHint{{"Enter", "save"}, {"Esc", "cancel"}}
		}
		hints := []keyHint{{"Tab", "next field"}}
		if len(m.form.fields[m.form.index].Options) > 0 {
			hints = append(hints, keyHint{"←→", "choose"})
		}
		return append(hints, keyHint{"Enter", "next"}, keyHint{"Ctrl+S", m.form.submitLabel()}, keyHint{"Esc", "cancel"})
	case m.pending != nil:
		return []keyHint{{"y", m.pending.confirmWord()}, {"n/Esc", "cancel"}}
	case m.help:
		return []keyHint{{"↑↓", "scroll"}, {"/", "filter"}, {"Esc", "close"}}
	case m.palette:
		return []keyHint{{"type", "to search"}, {"↑↓", "choose"}, {"Enter", "run"}, {"Esc", "back"}}
	case m.menu:
		return []keyHint{{"↑↓", "choose"}, {"Enter", "run"}, {":", "all commands"}, {"Esc", "back"}}
	case m.recentOpen:
		return []keyHint{{"Enter", "restore"}, {"/", "search"}, {"h", "pick a conversation"}, {"Esc", "back"}}
	case m.searching:
		return []keyHint{{"type", "to filter"}, {"Enter", "keep filter"}, {"Esc", "done"}}
	}
	quit := keyHint{"q", "quit"}
	if m.popup {
		quit = keyHint{"Esc", "back to the agent"}
	}
	switch sections[m.section] {
	case "sessions":
		return append(m.sessionKeys(), quit)
	case "approvals":
		if m.current() == nil {
			return []keyHint{{"1", "sessions"}, {":", "commands"}, quit}
		}
		hints := []keyHint{{"y", "allow once"}}
		if approvalSessionID(m.current()) != "" {
			hints = append(hints, keyHint{"a", "allow for session"})
		}
		return append(hints, keyHint{"n", "deny"}, keyHint{"Enter", "open session"}, keyHint{"m", "more"}, quit)
	case "projects":
		return []keyHint{{"Enter", "open shell"}, {"n", "new project"}, {"v", "changes"}, {"/", "search"}, {"m", "more"}, quit}
	case "tasks":
		return []keyHint{{"Enter", "attach"}, {"n", "new task"}, {"v", "diff"}, {"/", "search"}, {"m", "more"}, quit}
	case "routines":
		return []keyHint{{"n", "new routine"}, {"/", "search"}, {"m", "more"}, {"1", "sessions"}, quit}
	case "targets":
		return []keyHint{{"n", "new machine"}, {"/", "search"}, {"m", "more"}, {"1", "sessions"}, quit}
	}
	return []keyHint{{"m", "more"}, quit}
}

func (m *dashboard) sessionKeys() []keyHint {
	if m.batchOpen {
		return []keyHint{{"Space", "select"}, {"Enter", "open selected"}, {"b", "done"}}
	}
	if m.selectedGroup() != nil {
		return []keyHint{{"Enter", "fold"}, {"n", "new session"}, {"[ ]", "fold all"}, {":", "commands"}}
	}
	r := m.current()
	if r == nil {
		return []keyHint{{"n", "new session"}, {"r", "restore"}, {":", "commands"}}
	}
	if m.approvalFor(r) != nil {
		return []keyHint{{"y", "allow once"}, {"a", "allow for session"}, {"2", "deny…"}, {"Enter", "attach"}, {"x", "end"}, {"n", "new"}}
	}
	switch {
	case r["setup_state"] == "creating":
		return []keyHint{{"m", "setup actions"}, {"n", "new session"}, {":", "commands"}}
	case agentExited(r):
		return []keyHint{{"r", "start agent again"}, {"Enter", "attach"}, {"x", "end"}, {"n", "new"}, {":", "commands"}}
	case r["ended_at"] != nil:
		return []keyHint{{"r", "restore"}, {"m", "more"}, {"n", "new"}, {":", "commands"}}
	}
	// The search line above the list already says "/ Search", so the bar
	// spends its room on keys that are not visible anywhere else.
	return []keyHint{{"Enter", "attach"}, {"n", "new"}, {"x", "end"}, {"r", "restore"}, {":", "commands"}, {"m", "menu"}}
}

// renderKeyBar fits as many hints as the width allows, in order. A final
// quit or back hint and "? keys" are pinned to the right edge, so leaving and
// help are one key away at any width.
func renderKeyBar(hints []keyHint, width int) string {
	var pinned []keyHint
	if n := len(hints); n > 0 && (hints[n-1].Key == "q" || hints[n-1].Label == "back to the agent") {
		pinned, hints = hints[n-1:], hints[:n-1]
	}
	pinned = append(pinned, keyHint{"?", "keys"})
	render := func(list []keyHint) (string, int) {
		var parts []string
		plain := 0
		for i, h := range list {
			parts = append(parts, keyStyle.Render(h.Key)+" "+h.Label)
			plain += ansi.StringWidth(h.Key + " " + h.Label)
			if i > 0 {
				plain += 3
			}
		}
		return strings.Join(parts, muted.Render(" · ")), plain
	}
	tail, tailWidth := render(pinned)
	room := width - 2 - tailWidth - 3
	var kept []keyHint
	used := 0
	for _, h := range hints {
		w := ansi.StringWidth(h.Key + " " + h.Label)
		if len(kept) > 0 {
			w += 3
		}
		if used+w > room {
			break
		}
		kept = append(kept, h)
		used += w
	}
	left, leftWidth := render(kept)
	gap := max(1, width-2-leftWidth-tailWidth)
	return " " + left + strings.Repeat(" ", gap) + tail
}

var keyStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("141"))
var needsStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))

// helpView is the `?` screen: every key for the view it was opened from,
// then the keys that work everywhere and the keys that moved this release.
// It scrolls and filters instead of closing on the first key.
func (m *dashboard) helpLines() []string {
	var lines []string
	add := func(title string, hints []keyHint) {
		var kept []string
		for _, h := range hints {
			line := "  " + padRight(h.Key, 16) + h.Label
			if m.helpQuery != "" && !fuzzyFields(m.helpQuery, h.Key, h.Label, title) {
				continue
			}
			kept = append(kept, line)
		}
		if len(kept) == 0 {
			return
		}
		lines = append(lines, "", " "+title)
		lines = append(lines, kept...)
	}
	add("This view: "+m.helpContext, m.helpFor(m.helpContext))
	add("Everywhere", []keyHint{
		{"1 2 3 4", "Sessions, Approvals, Projects, Tasks"},
		{"Tab / ←→", "next pane (Shift+Tab goes back)"},
		{"↑↓ / j k", "move"},
		{"/", "filter the list; @ ready · ! working · # idle · & failed"},
		{"m", "short menu for the selection"},
		{": / Ctrl+K", "all commands, searchable by plain words"},
		{"Esc", "back one level (never quits)"},
		{"q", "back in a sub-view; quit at the top (agents keep running)"},
		{"Ctrl+R", "refresh now (the list also refreshes every 3 s)"},
		{"Ctrl+C", "quit from anywhere"},
		{"Forms", "Tab next field · ←→ choose · Enter next, submits on the last field · Ctrl+S submit"},
	})
	add("While attached to a session", []keyHint{
		{"Ctrl+]", "menu: the bar shows what comes next"},
		{"Ctrl+] d", "leave; the session keeps running (Ctrl-b d also works)"},
		{"Ctrl+\\", "send a file to the agent"},
		{"double-click", "open a path or link the agent printed"},
		{"Ctrl+] |  Ctrl+] -", "a shell beside or below the agent"},
		{"Ctrl+] ?", "every attach key in a menu"},
	})
	add("Keys that moved (the old key still works unless noted)", []keyHint{
		{"C → r", "restore list"},
		{"R → r", "start an exited agent again"},
		{"r → Ctrl+R", "refresh (r now restores on Sessions)"},
		{"Tab → p", "focus the preview (Tab now switches panes)"},
		{"2 3 4", "now Approvals, Projects, Tasks; Routines and Machines are in :"},
		{"5 6", "still Machines and Approvals"},
		{"U F H O P Q S G A", "undo, search, saved conversations, older messages, profiles, agent runners, shell, group, archive: also in :"},
		{"7 8 9", "settings, usage, API: also in :"},
		{"q in review", "goes back instead of quitting"},
	})
	if len(lines) > 0 {
		lines = lines[1:]
	}
	if len(lines) == 0 {
		lines = []string{" No keys match " + m.helpQuery + "."}
	}
	return lines
}

func (m *dashboard) helpFor(context string) []keyHint {
	switch context {
	case "Approvals":
		return []keyHint{{"y", "allow once"}, {"a", "allow for this session (asks first)"}, {"n", "deny"}, {"m", "deny with a reason and more"}, {"Enter", "attach to the session that is asking"}}
	case "Projects":
		return []keyHint{{"Enter", "open a shell in the project"}, {"n", "new project"}, {"v", "review changes"}, {"x", "delete the project record (asks first)"}, {"m", "brief, notes, skills, MCP and more"}}
	case "Tasks":
		return []keyHint{{"Enter", "attach to the running attempt"}, {"n", "new task"}, {"v", "captured diff"}, {"x", "delete (asks first)"}, {"m", "dispatch, request changes, commit and more"}}
	case "Review":
		return []keyHint{{"←→ / [ ]", "previous / next file"}, {"s", "working tree or staged"}, {"c", "commit these changes"}, {"Tab", "next repository"}, {"r", "refresh"}, {"↑↓ PgUp PgDn", "scroll"}, {"Esc / q", "back"}}
	case "Sessions":
		return []keyHint{
			{"Enter / click", "attach here (Ctrl+] d comes back)"},
			{"o / right-click", "open in a new terminal window"},
			{"b, Space", "select several, Enter opens them all"},
			{"n", "new session"},
			{"x / d / Delete", "end the session (asks first; r restores it)"},
			{"r", "restore: start an exited agent again, or open the Restore list"},
			{"y / a", "allow once / allow for this session, when it needs you"},
			{"v", "review changes"},
			{"u", "send a file"},
			{"e", "rename"},
			{"p, PgUp/PgDn", "focus / scroll the preview"},
			{"w", "only sessions that need you"},
			{"g", "group by project, machine, none or named group"},
			{"z / A", "include ended sessions / archive"},
		}
	}
	return []keyHint{{"n", "new"}, {"m", "actions for the selection"}, {"/", "search"}}
}

func padRight(s string, n int) string {
	return s + strings.Repeat(" ", max(1, n-ansi.StringWidth(s)))
}

func (m *dashboard) viewTitle() string {
	if m.review != nil {
		return "Review"
	}
	return paneTitle(sections[m.section])
}

func paneTitle(section string) string {
	switch section {
	case "targets":
		return "Machines"
	}
	return strings.ToUpper(section[:1]) + section[1:]
}

func (m *dashboard) openHelp() {
	m.help = true
	m.helpOffset = 0
	m.helpQuery = ""
	m.helpSearching = false
	m.helpContext = m.viewTitle()
}

func (m *dashboard) helpView(height int) string {
	lines := m.helpLines()
	head := accent.Bold(true).Render(" Keys") + muted.Render(" · ↑↓ PgUp/PgDn scroll · / filter · Esc close")
	if m.helpSearching || m.helpQuery != "" {
		head = accent.Bold(true).Render(" Keys") + " / " + m.helpQuery
		if m.helpSearching {
			head += "▏"
		}
	}
	room := max(1, height-1)
	m.helpOffset = max(0, min(m.helpOffset, len(lines)-room))
	end := min(len(lines), m.helpOffset+room)
	out := append([]string{head}, lines[m.helpOffset:end]...)
	if end < len(lines) {
		out[len(out)-1] = muted.Render(" ↓ more")
	}
	return strings.Join(out, "\n")
}

// rowTitle is a row's first line. An approval is named after who is asking,
// not after the tool.
func (m *dashboard) rowTitle(r row) string {
	if sections[m.section] == "approvals" {
		return approvalWho(r)
	}
	return name(r)
}
