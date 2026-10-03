package console

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/console/mods"
	"github.com/JeremiahM37/lectern/v2/internal/version"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Mods (docs/mods.md) run in the console through internal/console/mods. The
// console has no live stream, so it learns about mods the way it learns about
// everything else: by polling. The list is fetched at start and every
// modReloadTicks refreshes, and only a changed hash set restarts anything.
// server.event is synthesised from the same polls: a session row whose status
// changed, or an approval that newly appeared.

// modReloadTicks is how many 3 s refresh ticks pass between mod list checks.
const modReloadTicks = 5

type modState struct {
	host      *mods.Host
	signature string
	loading   bool
	listening bool
	ticks     int
	// gone: the server has no mods endpoint (an older Lectern); stop asking.
	gone bool

	cards     map[string]mods.Rendered // session id → its session.card render
	status    []mods.Element
	pane      *mods.Pane
	paneElems []mods.Element

	// What the polls last saw, to tell a change from a repeat.
	sessions  map[string]string
	approvals map[string]bool
}

type modsLoadedMsg struct {
	list []mods.Mod
	err  error
}

// modNoticeMsg merges the notices that arrived together, so a burst of
// $.state.set calls costs one render.
type modNoticeMsg struct {
	toasts        []string
	render, panes bool
}

type modCommandMsg struct {
	title string
	res   any
	err   error
}

// modEvent is the hook an action goes through before it runs (approval.decide).
type modEvent struct {
	name string
	e    map[string]any
}

// errModDenied is what a {deny} answer becomes: shown as the console's error,
// and like any failed request it keeps the form and its draft open.
type errModDenied struct{ reason string }

func (e errModDenied) Error() string { return "Blocked by a mod: " + e.reason }

func newModState(c *Client) *modState {
	dir := ""
	if path, err := dashboardPreferencePath(c.Base); err == nil {
		dir = strings.TrimSuffix(path, ".json") + ".mods"
	}
	return &modState{host: mods.NewHost(mods.Options{API: c, StateDir: dir, Version: version.Version}), cards: map[string]mods.Rendered{}}
}

func (s *modState) handles(event string) bool {
	return s != nil && s.host != nil && s.host.Handles(event)
}

func (m *dashboard) loadMods() tea.Cmd {
	s := m.mods
	if s == nil || s.loading || s.gone {
		return nil
	}
	s.loading = true
	c := m.client
	return func() tea.Msg {
		b, err := c.JSON("GET", "/plugins/mods?surface=cli", nil)
		var list []mods.Mod
		if err == nil {
			err = json.Unmarshal(b, &list)
		}
		return modsLoadedMsg{list, err}
	}
}

func (m *dashboard) receiveMods(v modsLoadedMsg) tea.Cmd {
	s := m.mods
	s.loading = false
	if v.err != nil {
		var he *HTTPError
		if errors.As(v.err, &he) && (he.Status == 404 || he.Status == 405) {
			s.gone = true
		}
		return nil
	}
	sig := mods.Signature(v.list)
	if sig == s.signature {
		return nil
	}
	s.signature = sig
	s.host.Load(v.list)
	if problems := s.host.Problems(); len(problems) > 0 {
		m.notice = "Mods: " + oneLine(strings.Join(problems, "; "))
	}
	m.renderMods()
	m.filter()
	if !s.listening {
		s.listening = true
		return m.waitModNotice()
	}
	return nil
}

func (m *dashboard) waitModNotice() tea.Cmd {
	ch := m.mods.host.Notices()
	return func() tea.Msg {
		var out modNoticeMsg
		add := func(n mods.Notice) {
			switch n.Kind {
			case mods.NoticeToast:
				out.toasts = append(out.toasts, n.Mod+": "+n.Text)
			case mods.NoticePanes:
				out.panes = true
			}
			out.render = true
		}
		add(<-ch)
		for {
			select {
			case n := <-ch:
				add(n)
			default:
				return out
			}
		}
	}
}

func (m *dashboard) receiveModNotice(v modNoticeMsg) tea.Cmd {
	if len(v.toasts) > 0 {
		m.notice = oneLine(v.toasts[len(v.toasts)-1])
	}
	if v.panes {
		m.syncModPane()
	}
	if v.render {
		m.renderMods()
		m.filter()
	}
	return m.waitModNotice()
}

// renderMods asks the mods for the session cards, the status segment and the
// open pane. It runs on data changes, not on every frame, and the host bounds
// each render to 100 ms however many rows there are.
func (m *dashboard) renderMods() {
	s := m.mods
	if s == nil {
		return
	}
	s.cards = map[string]mods.Rendered{}
	s.status = nil
	if s.host.Loaded() == 0 {
		s.paneElems = nil
		return
	}
	if sections[m.section] == "sessions" && len(m.rows) > 0 {
		props := make([]map[string]any, len(m.rows))
		for i, r := range m.rows {
			props[i] = map[string]any{"session": map[string]any(r)}
		}
		for i, out := range s.host.RenderMany("session.card", props) {
			if out.Hidden || len(out.Append) > 0 {
				s.cards[id(m.rows[i])] = out
			}
		}
	}
	_, s.status = s.host.Render("status", map[string]any{})
	if s.pane != nil {
		s.paneElems = s.host.RenderPane(*s.pane)
	}
}

// modHidden reports a session row a mod hid. Only Sessions has cards.
func (m *dashboard) modHidden(r row) bool {
	if m.mods == nil || sections[m.section] != "sessions" {
		return false
	}
	return m.mods.cards[id(r)].Hidden
}

// modCardInline is what the mods append to a session row, drawn inline.
func (m *dashboard) modCardInline(r row) string {
	if m.mods == nil || sections[m.section] != "sessions" {
		return ""
	}
	return renderModInline(m.mods.cards[id(r)].Append)
}

// modStatusSegment is the footer's mod segment: the "status" component's
// elements, then every mod's $.ui.status text.
func (m *dashboard) modStatusSegment() string {
	if m.mods == nil {
		return ""
	}
	parts := []string{}
	if s := renderModInline(m.mods.status); s != "" {
		parts = append(parts, s)
	}
	for _, text := range m.mods.host.Status() {
		parts = append(parts, muted.Render(oneLine(text)))
	}
	return strings.Join(parts, muted.Render(" · "))
}

// statusLine is the last screen line: the notice on the left and the mods'
// segment on the right, the notice giving way first.
func (m *dashboard) statusLine(status string) string {
	segment := m.modStatusSegment()
	if segment == "" {
		return clip(" "+status, m.width-1)
	}
	segment = clip(segment, max(10, (m.width-1)/2))
	w := ansi.StringWidth(segment)
	left := clip(" "+status, max(0, m.width-2-w))
	return left + strings.Repeat(" ", max(1, m.width-1-ansi.StringWidth(left)-w)) + segment
}

// The server's other live updates are not polled by the console, so those are
// the only server.event types a console mod sees.
func (m *dashboard) modSessionEvents(rows []row) {
	s := m.mods
	if s == nil {
		return
	}
	first := s.sessions == nil
	seen := map[string]string{}
	var changed []row
	for _, r := range rows {
		state := str(r["status"]) + "|" + str(r["state"]) + "|" + str(r["setup_state"]) + "|" + str(r["ended_at"])
		seen[id(r)] = state
		if !first {
			if before, ok := s.sessions[id(r)]; !ok || before != state {
				changed = append(changed, r)
			}
		}
	}
	// Merge rather than replace: a filtered list (ended, archived) must not
	// make every live session look new on the next poll.
	if s.sessions == nil {
		s.sessions = map[string]string{}
	}
	for k, v := range seen {
		s.sessions[k] = v
	}
	m.serverEvents("session", changed)
}

func (m *dashboard) modApprovalEvents(rows []row) {
	s := m.mods
	if s == nil {
		return
	}
	first := s.approvals == nil
	seen := map[string]bool{}
	var fresh []row
	for _, a := range rows {
		seen[id(a)] = true
		if !first && !s.approvals[id(a)] {
			fresh = append(fresh, a)
		}
	}
	s.approvals = seen
	m.serverEvents("approval", fresh)
}

// serverEvents only notifies, so it does not hold up the dashboard: the
// handlers run in the background and their effects arrive as notices.
func (m *dashboard) serverEvents(kind string, rows []row) {
	if len(rows) == 0 || !m.mods.handles("server.event") {
		return
	}
	host := m.mods.host
	go func() {
		for _, r := range rows {
			_, _ = host.Dispatch(context.Background(), "server.event", map[string]any{"type": kind, "data": map[string]any(r)}, nil)
		}
	}()
}

// viaMods runs one request through a mod event. The request is Lectern's own
// behaviour at the end of the chain; body builds it from the (possibly
// rewritten) event. A {deny} answer becomes the action's error.
func (m *dashboard) viaMods(event string, e map[string]any, label, method, path string, body func(map[string]any) any, notice string) tea.Cmd {
	if m.busy {
		return nil
	}
	m.busy = true
	c, key, host := m.client, m.key(), m.mods.host
	return func() tea.Msg {
		var mu sync.Mutex
		var sent *resultMsg
		res, err := host.Dispatch(context.Background(), event, e, func(e map[string]any) (any, error) {
			data, err := c.JSON(method, path, body(e))
			mu.Lock()
			sent = &resultMsg{label: label, data: data, err: err, key: key}
			mu.Unlock()
			if err != nil {
				return nil, err
			}
			return map[string]any{"ok": true}, nil
		})
		mu.Lock()
		defer mu.Unlock()
		if sent != nil {
			if sent.err == nil {
				sent.notice = notice
			}
			return *sent
		}
		if reason, ok := mods.Denied(res); ok {
			return resultMsg{label: label, err: errModDenied{oneLine(reason)}, key: key}
		}
		if err != nil {
			return resultMsg{label: label, err: err, key: key}
		}
		return resultMsg{label: label, err: errors.New("a mod answered " + event + " itself; nothing was sent"), key: key}
	}
}

// submitPrompt sends a message to a session through prompt.submit.
func (m *dashboard) submitPrompt(sessionID, path string, body map[string]any) tea.Cmd {
	if !m.mods.handles("prompt.submit") {
		return m.request("Send message", "POST", path, body, false)
	}
	text, _ := body["text"].(string)
	return m.viaMods("prompt.submit", map[string]any{"session_id": sessionID, "text": text}, "Send message", "POST", path, func(e map[string]any) any {
		out := map[string]any{}
		for k, v := range body {
			out[k] = v
		}
		if t, ok := e["text"].(string); ok {
			out["text"] = t
		}
		return out
	}, "")
}

// approvalEvent is approval.decide's event for a decision the console sends.
func approvalEvent(a row, decision string) *modEvent {
	word := "allow"
	if decision == "denied" {
		word = "deny"
	}
	return &modEvent{name: "approval.decide", e: map[string]any{"approval": map[string]any(a), "decision": word}}
}

// modCommandActions are the mods' palette entries: their commands, and the
// buttons on the selected session's card.
func (m *dashboard) modCommandActions() []dashboardAction {
	if m.mods == nil || m.mods.host.Loaded() == 0 {
		return nil
	}
	var out []dashboardAction
	for _, c := range m.mods.host.Commands() {
		out = append(out, dashboardAction{Label: oneLine(c.Title), Operation: "mod:" + c.ID, Keywords: "mod plugin " + c.Name + " " + c.ID})
	}
	if r := m.current(); r != nil {
		for i, b := range mods.Buttons(m.modCardButtons(r)) {
			out = append(out, dashboardAction{Label: oneLine(b.Label), Operation: "mod-button:" + id(r) + ":" + strconv.Itoa(i), Keywords: "mod plugin button"})
		}
	}
	return out
}

func (m *dashboard) modCardButtons(r row) []mods.Element {
	if m.mods == nil || sections[m.section] != "sessions" {
		return nil
	}
	return m.mods.cards[id(r)].Append
}

// chooseMod runs a palette entry from modCommandActions.
func (m *dashboard) chooseMod(a dashboardAction) tea.Cmd {
	if rest, ok := strings.CutPrefix(a.Operation, "mod-button:"); ok {
		sid, index, _ := strings.Cut(rest, ":")
		for _, r := range m.rows {
			if id(r) != sid {
				continue
			}
			buttons := mods.Buttons(m.modCardButtons(r))
			if i, err := strconv.Atoi(index); err == nil && i >= 0 && i < len(buttons) {
				buttons[i].Press()
			}
		}
		return nil
	}
	commandID := strings.TrimPrefix(a.Operation, "mod:")
	host, title := m.mods.host, a.Label
	return func() tea.Msg {
		res, err := host.RunCommand(context.Background(), commandID, nil)
		return modCommandMsg{title: title, res: res, err: err}
	}
}

func (m *dashboard) receiveModCommand(v modCommandMsg) {
	switch reason, denied := mods.Denied(v.res); {
	case v.err != nil:
		m.notice = v.title + ": " + clean(v.err.Error())
	case denied:
		m.notice = errModDenied{oneLine(reason)}.Error()
	default:
		if text, ok := v.res.(string); ok && text != "" {
			m.notice = v.title + ": " + oneLine(text)
		} else if m.notice == "" || !strings.HasPrefix(m.notice, v.title) {
			m.notice = "Ran " + v.title
		}
	}
}

// syncModPane shows the most recently opened pane, if any.
func (m *dashboard) syncModPane() {
	s := m.mods
	panes := s.host.OpenPanes()
	if len(panes) == 0 {
		s.pane, s.paneElems = nil, nil
		return
	}
	p := panes[len(panes)-1]
	if s.pane == nil || *s.pane != p {
		s.pane = &p
	}
}

func (m *dashboard) modPaneOpen() bool { return m.mods != nil && m.mods.pane != nil }

func (m *dashboard) updateModPane(k tea.KeyMsg) tea.Cmd {
	s := m.mods
	if k.String() == "esc" {
		s.host.ClosePane(*s.pane)
		m.syncModPane()
		if s.pane != nil {
			s.paneElems = s.host.RenderPane(*s.pane)
		}
		return nil
	}
	for _, b := range mods.Buttons(s.paneElems) {
		if b.Hotkey != "" && b.Hotkey == k.String() {
			b.Press()
			return nil
		}
	}
	return nil
}

func (m *dashboard) modPaneKeys() []keyHint {
	hints := []keyHint{}
	for _, b := range mods.Buttons(m.mods.paneElems) {
		if b.Hotkey != "" {
			hints = append(hints, keyHint{b.Hotkey, oneLine(b.Label)})
		}
	}
	return append(hints, keyHint{"Esc", "close"})
}

func (m *dashboard) modPaneView(height int) string {
	s := m.mods
	lines := []string{accent.Bold(true).Render(" " + oneLine(s.pane.Title)), ""}
	if len(s.paneElems) == 0 {
		lines = append(lines, muted.Render(" (empty)"))
	}
	for _, e := range s.paneElems {
		for _, line := range strings.Split(renderModElement(e, true), "\n") {
			lines = append(lines, " "+line)
		}
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

// toneStyle maps a mod tone to the console's existing colours.
func toneStyle(tone string) lipgloss.Style {
	switch tone {
	case "dim":
		return muted
	case "accent":
		return accent
	case "warn":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	case "danger":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	case "ok":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	}
	return lipgloss.NewStyle()
}

func renderModInline(elems []mods.Element) string {
	parts := make([]string, 0, len(elems))
	for _, e := range elems {
		if s := renderModElement(e, false); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}

// renderModElement draws one element. Text from a mod is display data and is
// cleaned like any remote output. column is false where only one line fits
// (a session row, the footer): a column Box is then laid out as a row.
func renderModElement(e mods.Element, column bool) string {
	switch e.Type {
	case "Text":
		style := toneStyle(e.Tone)
		if e.Bold {
			style = style.Bold(true)
		}
		return style.Render(oneLine(e.Text))
	case "Badge":
		tone := e.Tone
		if tone == "" {
			tone = "accent"
		}
		return toneStyle(tone).Bold(true).Background(lipgloss.Color("236")).Render(" " + oneLine(e.Text) + " ")
	case "Button":
		label := oneLine(e.Label)
		if hk := oneLine(e.Hotkey); hk != "" {
			return "[" + keyStyle.Render(hk) + " " + label + "]"
		}
		return "[" + label + "]"
	case "Link":
		return lipgloss.NewStyle().Underline(true).Render(oneLine(e.Label))
	case "Box":
		parts := make([]string, 0, len(e.Children))
		for _, c := range e.Children {
			if s := renderModElement(c, column); s != "" {
				parts = append(parts, s)
			}
		}
		if column && e.Direction == "column" {
			return strings.Join(parts, strings.Repeat("\n", 1+min(e.Gap, 2)))
		}
		return strings.Join(parts, strings.Repeat(" ", max(1, e.Gap)))
	}
	return ""
}
