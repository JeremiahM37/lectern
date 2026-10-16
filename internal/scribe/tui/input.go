package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// Input limits from the spec.
const (
	maxInputLines   = 10
	maxSteerQueue   = 5
	pasteCollapseAt = 5 // more lines than this collapse to a placeholder
	historyKeep     = 500
	historySearchN  = 50
)

// inputState is one tab's input box: the textarea plus history, stash, paste
// placeholders, history search and the popup selection.
type inputState struct {
	ta      textarea.Model
	history []string // oldest first
	hidx    int      // -1 = not browsing history; 0 = newest entry
	draft   string   // draft held while browsing history
	stashes []string // Alt+S drafts, newest last
	pastes  map[int]string
	pasteN  int
	search  *histSearch
	acSel   int
	// dismissed hides the popup until the text changes again (Esc).
	dismissed string
	popupOn   bool
}

// histSearch is the Ctrl+R overlay.
type histSearch struct {
	query   []rune
	matches []string
	sel     int
}

func newInputState() *inputState {
	ta := textarea.New()
	ta.Prompt = "› "
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.MaxHeight = 0
	ta.SetHeight(1)
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.Focus()
	return &inputState{ta: ta, hidx: -1, pastes: map[int]string{}}
}

func (s *inputState) value() string { return s.ta.Value() }

func (s *inputState) empty() bool { return strings.TrimSpace(s.ta.Value()) == "" }

func (s *inputState) clear() {
	s.ta.Reset()
	s.hidx = -1
	s.acSel = 0
}

func (s *inputState) setValue(v string) {
	s.ta.SetValue(v)
	s.acSel = 0
}

// resize sets the textarea's width (the box is two columns of border and two of padding wider).
func (s *inputState) resize(w int) {
	if w < 8 {
		w = 8
	}
	s.ta.SetWidth(w)
}

// inputHeight is the number of text rows, 1 to maxInputLines.
func (s *inputState) inputHeight() int {
	if s.search != nil {
		return 1
	}
	return clamp(s.ta.LineCount(), 1, maxInputLines)
}

// ---------------------------------------------------------------- popups

// slashMatches returns command popup rows for the current "/" text.
func (s *inputState) slashMatches() []*command {
	v := s.ta.Value()
	if !strings.HasPrefix(v, "/") || strings.Contains(v, "\n") {
		return nil
	}
	return matchCommands(strings.TrimPrefix(v, "/"))
}

// mentionToken is the trailing "@..." word, if the text ends in one.
func (s *inputState) mentionToken() (string, bool) {
	v := s.ta.Value()
	i := strings.LastIndexAny(v, " \n\t")
	tok := v[i+1:]
	if strings.HasPrefix(tok, "@") {
		return strings.TrimPrefix(tok, "@"), true
	}
	return "", false
}

// popupActive reports whether a popup should show, and which kind.
func (s *inputState) popupKind() string {
	if s.search != nil || s.hidx >= 0 {
		return ""
	}
	if s.dismissed == s.ta.Value() {
		return ""
	}
	if len(s.slashMatches()) > 0 {
		return "slash"
	}
	if _, ok := s.mentionToken(); ok {
		return "mention"
	}
	return ""
}

// popupLines draws the popup above the box. m may be nil when only sizing.
func (s *inputState) popupLines(m *Model, w int) []string {
	kind := s.popupKind()
	if kind == "" || m == nil {
		return nil
	}
	th := m.th
	var rows []string
	footer := ""
	switch kind {
	case "slash":
		cmds := s.slashMatches()
		s.acSel = clamp(s.acSel, 0, len(cmds)-1)
		start := 0
		maxRows := 8
		if s.acSel >= maxRows {
			start = s.acSel - maxRows + 1
		}
		for i := start; i < len(cmds) && i < start+maxRows; i++ {
			c := cmds[i]
			mark := fg(th.TextDim).Render("  ")
			name := fg(th.BorderSlash).Render("/" + c.Name)
			desc := fg(th.TextDim).Render(c.Desc)
			if i == s.acSel {
				mark = fgb(th.BorderSlash).Render("› ")
				name = fgb(th.Info).Render("/" + c.Name)
				desc = fg(th.TextSecondary).Render(c.Desc)
			}
			rows = append(rows, render.Truncate(mark+name+"  "+desc, w-6))
		}
		if len(cmds) > maxRows {
			rows = append(rows, fg(th.TextDim).Render(itoa(s.acSel+1)+"/"+itoa(len(cmds))))
		}
		footer = "↑↓ select · Tab accept · Enter run · Esc dismiss"
	case "mention":
		q, _ := s.mentionToken()
		files := m.cur().be.FileCandidates(q, 8)
		s.acSel = clamp(s.acSel, 0, max(0, len(files)-1))
		if len(files) == 0 {
			rows = append(rows, fg(th.TextFaint).Render("  no matching files"))
		}
		for i, f := range files {
			mark := "  "
			st := fg(th.TextSecondary)
			if i == s.acSel {
				mark = fgb(th.BorderSlash).Render("› ")
				st = fgb(th.TextPrimary)
			}
			rows = append(rows, render.Truncate(mark+st.Render("@"+f), w-6))
		}
		footer = "↑↓ select · Tab/Enter insert · Esc dismiss"
	}
	return popupBox(rows, w, th.BorderSlash, footer, th)
}

// popupBox frames popup rows above the input.
func popupBox(rows []string, w int, col lipgloss.TerminalColor, footer string, th theme.Theme) []string {
	body := make([]string, 0, len(rows))
	for _, r := range rows {
		body = append(body, " "+r)
	}
	return frame("", "", body, footer, w, col, th)
}

// ---------------------------------------------------------------- history

// pushHistory records a submitted prompt (deduplicated, newest last).
func (s *inputState) pushHistory(v string) {
	if strings.TrimSpace(v) == "" {
		return
	}
	out := s.history[:0]
	for _, h := range s.history {
		if h != v {
			out = append(out, h)
		}
	}
	s.history = append(out, v)
	if len(s.history) > historyKeep {
		s.history = s.history[len(s.history)-historyKeep:]
	}
	s.hidx = -1
}

// histPrev steps to an older entry.
func (s *inputState) histPrev() {
	if len(s.history) == 0 {
		return
	}
	if s.hidx == -1 {
		s.draft = s.ta.Value()
	}
	if s.hidx < len(s.history)-1 {
		s.hidx++
	}
	s.setValue(s.history[len(s.history)-1-s.hidx])
}

// histNext steps to a newer entry, restoring the draft past the newest.
func (s *inputState) histNext() {
	if s.hidx == -1 {
		return
	}
	s.hidx--
	if s.hidx == -1 {
		s.setValue(s.draft)
		return
	}
	s.setValue(s.history[len(s.history)-1-s.hidx])
}

// openSearch starts Ctrl+R.
func (s *inputState) openSearch() {
	s.search = &histSearch{}
	s.refreshSearch()
}

func (s *inputState) refreshSearch() {
	q := strings.ToLower(string(s.search.query))
	s.search.matches = s.search.matches[:0]
	seen := map[string]bool{}
	for i := len(s.history) - 1; i >= 0 && len(s.history)-i <= historyKeep; i-- {
		h := s.history[i]
		if seen[h] {
			continue
		}
		seen[h] = true
		if q == "" || strings.Contains(strings.ToLower(h), q) {
			s.search.matches = append(s.search.matches, h)
			if len(s.search.matches) >= historySearchN {
				break
			}
		}
	}
	s.search.sel = clamp(s.search.sel, 0, max(0, len(s.search.matches)-1))
}

// ---------------------------------------------------------------- paste

// collapsePaste returns the text to insert for a paste. Long pastes become a placeholder.
func (s *inputState) collapsePaste(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Count(text, "\n") + 1
	if lines <= pasteCollapseAt {
		return text
	}
	s.pasteN++
	s.pastes[s.pasteN] = text
	return fmt.Sprintf("[Pasted text #%d +%d lines]", s.pasteN, lines)
}

// expand replaces paste placeholders with the text they stand for.
func (s *inputState) expand(v string) string {
	for n, text := range s.pastes {
		ph := fmt.Sprintf("[Pasted text #%d +%d lines]", n, strings.Count(text, "\n")+1)
		if strings.Contains(v, ph) {
			v = strings.Replace(v, ph, text, 1)
			delete(s.pastes, n)
		}
	}
	return v
}

// ---------------------------------------------------------------- keys

// handleInputKey routes a key to the input box. Global keys were already offered.
func (m *Model) handleInputKey(km tea.KeyMsg) tea.Cmd {
	t := m.cur()
	s := t.in
	key := km.String()
	if s.search != nil {
		return m.searchKey(t, km)
	}
	if kind := s.popupKind(); kind != "" {
		switch key {
		case "up", "down":
			n := 0
			if kind == "slash" {
				n = len(s.slashMatches())
			} else {
				n = len(t.be.FileCandidates(mentionQuery(s), 8))
			}
			if n > 0 {
				step := 1
				if key == "up" {
					step = -1
				}
				s.acSel = (s.acSel + step + n) % n
			}
			return nil
		case "tab", "right":
			if kind == "slash" {
				if c := s.slashMatches(); len(c) > 0 {
					s.setValue("/" + c[clamp(s.acSel, 0, len(c)-1)].Name + " ")
				}
			} else {
				m.acceptMention(t)
			}
			return nil
		case "enter":
			if kind == "slash" {
				c := s.slashMatches()
				if len(c) == 0 {
					break
				}
				pick := c[clamp(s.acSel, 0, len(c)-1)]
				if pick.Args != "" && !strings.Contains(s.value(), " ") {
					s.setValue("/" + pick.Name + " ")
					return nil
				}
				return m.submit("/" + pick.Name)
			}
			m.acceptMention(t)
			return nil
		case "esc":
			s.dismissed = s.value()
			return nil
		}
	}
	switch key {
	case "enter":
		return m.submit(s.value())
	case "shift+enter", "alt+enter", "ctrl+j":
		s.ta.InsertString("\n")
		return nil
	case "alt+s":
		if !s.empty() {
			s.stashes = append(s.stashes, s.value())
			s.clear()
			m.setFlash("ok", "Draft stashed (Alt+P to pop)")
		}
		return nil
	case "alt+p":
		if n := len(s.stashes); n > 0 && s.empty() {
			s.setValue(s.stashes[n-1])
			s.stashes = s.stashes[:n-1]
		}
		return nil
	case "ctrl+r":
		s.openSearch()
		return nil
	case "?":
		if s.empty() {
			m.openHelp()
			return nil
		}
	case "up":
		if s.ta.Line() == 0 && len(s.history) > 0 {
			s.histPrev()
			return nil
		}
	case "down":
		if s.ta.Line() >= s.ta.LineCount()-1 && s.hidx >= 0 {
			s.histNext()
			return nil
		}
	case "ctrl+u":
		// delete the current line
		v := s.value()
		lines := strings.Split(v, "\n")
		if len(lines) > 0 {
			li := s.ta.Line()
			if li < len(lines) {
				lines = append(lines[:li], lines[li+1:]...)
				s.setValue(strings.Join(lines, "\n"))
			}
		}
		return nil
	}
	if km.Paste {
		text := s.collapsePaste(string(km.Runes))
		s.ta.InsertString(text)
		s.hidx = -1
		return nil
	}
	s.hidx = -1
	before := s.value()
	var cmd tea.Cmd
	s.ta, cmd = s.ta.Update(km)
	if s.value() != before {
		s.acSel = 0
	}
	return cmd
}

// mentionQuery returns the text after the trailing "@", or "".
func mentionQuery(s *inputState) string {
	q, _ := s.mentionToken()
	return q
}

// acceptMention replaces the trailing "@partial" with the selected file.
func (m *Model) acceptMention(t *tab) {
	s := t.in
	files := t.be.FileCandidates(mentionQuery(s), 8)
	if len(files) == 0 {
		return
	}
	pick := files[clamp(s.acSel, 0, len(files)-1)]
	v := s.value()
	i := strings.LastIndexAny(v, " \n\t")
	s.setValue(v[:i+1] + "@" + pick + " ")
}

// searchKey handles keys while Ctrl+R is open.
func (m *Model) searchKey(t *tab, km tea.KeyMsg) tea.Cmd {
	s := t.in
	ss := s.search
	switch km.String() {
	case "esc", "ctrl+r":
		s.search = nil
		return nil
	case "enter":
		if len(ss.matches) > 0 {
			s.setValue(ss.matches[ss.sel])
		}
		s.search = nil
		return nil
	case "up":
		if n := len(ss.matches); n > 0 {
			ss.sel = (ss.sel - 1 + n) % n
		}
	case "down":
		if n := len(ss.matches); n > 0 {
			ss.sel = (ss.sel + 1) % n
		}
	case "backspace":
		if r := ss.query; len(r) > 0 {
			ss.query = r[:len(r)-1]
			ss.sel = 0
			s.refreshSearch()
		}
	default:
		if txt := printable(km); txt != "" {
			ss.query = append(ss.query, []rune(txt)...)
			ss.sel = 0
			s.refreshSearch()
		}
	}
	return nil
}

// ---------------------------------------------------------------- submit

// submit runs whatever the input holds: a slash command, a shell command, a
// steering message (while busy), or a new turn.
func (m *Model) submit(raw string) tea.Cmd {
	t := m.cur()
	s := t.in
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	text := strings.TrimSpace(s.expand(raw))
	s.pushHistory(raw)
	s.clear()
	s.dismissed = ""
	if strings.HasPrefix(text, "/") {
		return m.runCommand(text)
	}
	if strings.HasPrefix(text, "!") {
		return m.runShell(t, strings.TrimSpace(text[1:]))
	}
	if t.busy {
		m.enterChat()
		if len(t.steerQ) >= maxSteerQueue {
			m.setFlash("err", "steering queue is full (5); wait for the next step")
			s.setValue(raw)
			return nil
		}
		if t.be.Steer(text) {
			t.steerQ = append(t.steerQ, text)
			return nil
		}
		// the turn ended while we were typing: send it as a new turn
	}
	return m.startTurn(t, text)
}

// startTurn sends a prompt as a new turn.
func (m *Model) startTurn(t *tab, text string) tea.Cmd {
	now := time.Now()
	m.enterChat()
	t.chat.AddUser(text, 0, now, false)
	t.busy = true
	t.since = now
	t.be.Submit(text, nil)
	return nil
}

// runShell runs a "!command" and prints its output as a system message.
func (m *Model) runShell(t *tab, command string) tea.Cmd {
	if command == "" {
		return nil
	}
	t.chat.AddSystem("shell", "$ "+command)
	be := t.be
	id := t.id
	return func() tea.Msg {
		out, code, err := be.Shell(shellCtx(), command)
		text := strings.TrimRight(out, "\n")
		if err != nil {
			text = strings.TrimSpace(text + "\n" + err.Error())
		}
		if code != 0 {
			text += fmt.Sprintf("\n(exit %d)", code)
		}
		return shellMsg{tab: id, text: text}
	}
}

// shellMsg carries the output of a "!command".
type shellMsg struct {
	tab  int
	text string
}

// ---------------------------------------------------------------- render

// boxLines draws the input box (or the Ctrl+R overlay) at width w.
func (m *Model) boxLines(t *tab, w int, modeCol lipgloss.TerminalColor) []string {
	th := m.th
	s := t.in
	s.ta.FocusedStyle.Prompt = lipgloss.NewStyle().Bold(true).Foreground(modeCol)
	s.ta.Placeholder = "Ask about your code, or type / for commands"
	if t.busy {
		s.ta.Placeholder = "'/' for commands · or steer by sending a new message"
	}
	s.ta.SetHeight(s.inputHeight())
	var body []string
	border := th.Border
	footer := ""
	switch {
	case s.search != nil:
		border = th.Warning
		q := string(s.search.query)
		row := fg(th.Warning).Bold(true).Render("search: ") + fg(th.TextPrimary).Render(q) + fg(th.Warning).Render("▌")
		body = append(body, " "+row)
		footer = "type to filter · Enter load · Esc cancel"
	default:
		if s.popupKind() == "slash" {
			border = th.BorderSlash
		} else if t.busy {
			border = th.BrandDim
		} else {
			border = th.BorderFocused
		}
		for _, l := range strings.Split(s.ta.View(), "\n") {
			body = append(body, " "+l)
		}
		if strings.Contains(s.value(), "\n") {
			footer = "Shift+⏎ newline · ^U del line"
		}
	}
	return frame("", "", body, footer, w, border, th)
}

// shellCtx is the context for "!command" runs.
func shellCtx() context.Context { return context.Background() }
