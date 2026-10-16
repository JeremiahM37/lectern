package tui

// Panels that show what the agent already records but nothing displayed:
// the model log (/model-events), the compaction log (/compact logs), and the
// subagents of the newest dispatch (/agents, /dispatch-status) with /cancel
// to stop one. Each view reads the backend when it draws, so it stays live.

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

func init() {
	overrideCommands(
		&command{Name: "model-events", Desc: "Model calls, retries and errors", Group: "Models",
			Run: func(m *Model, _ string) tea.Cmd {
				t := m.cur()
				m.pushModal(newModelEventsView(m.th, t.be, &t.panels))
				return nil
			}},
		// /dispatch-status is an alias: as a listed name it would widen the
		// palette's name column for every row.
		&command{Name: "agents", Aliases: []string{"dispatch-status"}, Desc: "Subagents of the newest dispatch", Group: "Intelligence",
			Run: func(m *Model, _ string) tea.Cmd {
				m.pushModal(newSubagentsView(m.th, m.cur().be))
				return nil
			}},
		&command{Name: "cancel", Args: "<id>", Desc: "Stop one subagent (ids in /agents)", Group: "Intelligence",
			Run: func(m *Model, args string) tea.Cmd {
				id := strings.TrimSpace(args)
				if id == "" {
					return flashCmd("info", "Usage: /cancel <id>. The ids are listed by /agents.")
				}
				if err := m.cur().be.CancelSubagent(id); err != nil {
					return flashCmd("err", "Could not cancel "+id+": "+err.Error())
				}
				return flashCmd("ok", "Cancelled subagent "+id+".")
			}},
	)
	addCompactLogs()
}

// addCompactLogs makes "/compact logs" open the compaction log. It wraps the
// existing /compact command instead of replacing it, so a plain /compact
// still compacts.
func addCompactLogs() {
	for _, c := range commandTable {
		if c.Name != "compact" {
			continue
		}
		run := c.Run
		c.Args = "[logs]"
		c.Run = func(m *Model, args string) tea.Cmd {
			if strings.TrimSpace(args) == "logs" {
				m.pushModal(newCompactionLogView(m.th, m.cur().be))
				return nil
			}
			return run(m, args)
		}
		return
	}
}

// panelTime is the local clock time of an event, for the panels.
func panelTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Local().Format("15:04:05")
}

// fmtMS shows a duration in whole milliseconds, or seconds past one second.
func fmtMS(d time.Duration) string {
	switch {
	case d <= 0:
		return "—"
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1f s", d.Seconds())
}

// statusCode is the HTTP status of an error or retry as "[429]", or "".
func statusCode(status int) string {
	if status <= 0 {
		return ""
	}
	return fmt.Sprintf("[%d]", status)
}

// panelFrame sizes the panels like the other full modals.
func panelFrame(cols, rows int) (int, int) {
	return min(cols-4, 100), min(rows-4, 26)
}

// ---------------------------------------------------------------------------
// Model events

// modelEventsTabs are the tabs of the model log.
var modelEventsTabs = []string{"Models", "Recent calls", "Errors"}

// panelState is the panel recording state of one tab. It lives for the
// session only: it is not written to config, and it survives closing the
// popup, so a recording can span a turn that is run after the popup closes.
type panelState struct {
	recording bool
	base      int // index into the model log where the current recording began
}

// modelEventsView is the model log (spec 5.1.8). Recording is off when a
// session starts. Turning it on shows calls from that moment; turning it off
// or clearing it empties the view.
type modelEventsView struct {
	th     theme.Theme
	be     Backend
	st     *panelState
	tab    int
	cursor int
	off    int
	width  int
}

func newModelEventsView(th theme.Theme, be Backend, st *panelState) *modelEventsView {
	return &modelEventsView{th: th, be: be, st: st}
}

// Title implements Modal.
func (v *modelEventsView) Title() string { return "Model events" }

// Icon implements Iconed.
func (v *modelEventsView) Icon() string { return "brain" }

// Footer implements Footered.
func (v *modelEventsView) Footer() string {
	return "Tab/←→ tabs · ↑↓ scroll · e record · c clear · Esc close"
}

// Size implements Sized.
func (v *modelEventsView) Size(cols, rows int) (int, int) {
	w, h := panelFrame(cols, rows)
	v.width = w
	return w, h
}

// events are the recorded model-log entries shown now.
func (v *modelEventsView) events() []agent.Event {
	if !v.st.recording {
		return nil
	}
	all := v.be.ModelEvents()
	if v.st.base > len(all) {
		v.st.base = len(all)
	}
	return all[v.st.base:]
}

// Update implements Modal.
func (v *modelEventsView) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return v, nil, false
	}
	switch km.String() {
	case "esc", "q":
		return v, nil, true
	case "tab", "right", "l":
		v.tab = (v.tab + 1) % len(modelEventsTabs)
		v.cursor, v.off = 0, 0
	case "shift+tab", "left", "h":
		v.tab = (v.tab + len(modelEventsTabs) - 1) % len(modelEventsTabs)
		v.cursor, v.off = 0, 0
	case "up", "k":
		v.cursor = max(0, v.cursor-1)
	case "down", "j":
		v.cursor++
	case "e":
		v.st.recording = !v.st.recording
		v.st.base = len(v.be.ModelEvents())
		v.cursor, v.off = 0, 0
		if v.st.recording {
			return v, flashCmd("info", "Recording model calls from now on."), false
		}
		return v, flashCmd("info", "Recording is off. The log is cleared."), false
	case "c":
		v.st.base = len(v.be.ModelEvents())
		v.cursor, v.off = 0, 0
		return v, flashCmd("info", "Model log cleared."), false
	}
	return v, nil, false
}

// modelRow is one line of the Models tab.
type modelRow struct {
	name          string
	calls, errors int
	total, last   time.Duration
	tokens        int
	timed         int
}

// modelRows groups the recorded calls by model, in first-seen order. A
// retry, fallback or error with no model of its own belongs to the model
// the log was on last.
func modelRows(evs []agent.Event) []modelRow {
	var rows []modelRow
	at := map[string]int{}
	cur := "—"
	row := func(name string) *modelRow {
		if name == "" {
			name = cur
		}
		i, ok := at[name]
		if !ok {
			at[name] = len(rows)
			rows = append(rows, modelRow{name: name})
			i = len(rows) - 1
		}
		return &rows[i]
	}
	for _, e := range evs {
		if e.Model != "" {
			cur = e.Model
		}
		switch e.Kind {
		case agent.EvStepFinish:
			r := row(e.Model)
			r.calls++
			r.tokens += e.Usage.InputTokens + e.Usage.OutputTokens
			if e.Duration > 0 {
				r.total += e.Duration
				r.timed++
				r.last = e.Duration
			}
		case agent.EvError:
			row(e.Model).errors++
		}
	}
	return rows
}

// lines returns the column heading and the rows of the current tab, newest
// first on the call and error tabs.
func (v *modelEventsView) lines(w int) (string, []string) {
	th := v.th
	evs := v.events()
	var head string
	var out []string
	switch v.tab {
	case 0:
		head = fg(th.TextDim).Render("  " + padRight("Model", 24) + padRight("Calls", 7) + padRight("Errors", 8) + padRight("Avg", 9) + padRight("Last", 9) + "Tokens")
		for i, r := range modelRows(evs) {
			avg := time.Duration(0)
			if r.timed > 0 {
				avg = r.total / time.Duration(r.timed)
			}
			line := padRight(render.Truncate(r.name, 23), 24) + padRight(fmt.Sprint(r.calls), 7) +
				padRight(fmt.Sprint(r.errors), 8) + padRight(fmtMS(avg), 9) + padRight(fmtMS(r.last), 9) +
				render.HumanTokens(r.tokens)
			out = append(out, pickLine(th, line, i == v.cursor))
		}
	case 1:
		head = fg(th.TextDim).Render("  " + padRight("Time", 9) + padRight("Model", 24) + padRight("Step", 6) + padRight("In → out", 16) + "Time")
		n := 0
		for i := len(evs) - 1; i >= 0; i-- {
			e := evs[i]
			if e.Kind != agent.EvStepFinish {
				continue
			}
			model := e.Model
			if model == "" {
				model = "—"
			}
			line := padRight(panelTime(e.At), 9) + padRight(render.Truncate(model, 23), 24) +
				padRight(fmt.Sprint(e.Step), 6) +
				padRight(render.HumanTokens(e.Usage.InputTokens)+" → "+render.HumanTokens(e.Usage.OutputTokens), 16) +
				fmtMS(e.Duration)
			out = append(out, pickLine(th, line, n == v.cursor))
			n++
		}
	default:
		head = fg(th.TextDim).Render("  " + padRight("Time", 9) + padRight("Kind", 9) + padRight("Code", 7) + padRight("Category", 16) + "Detail")
		n := 0
		for i := len(evs) - 1; i >= 0; i-- {
			e := evs[i]
			kind := ""
			switch e.Kind {
			case agent.EvRetry:
				kind = "retry"
			case agent.EvError:
				kind = "error"
			case agent.EvFallback:
				kind = "switch"
			default:
				continue
			}
			detail := oneLine(e.Text)
			if e.Kind == agent.EvFallback {
				detail = "to " + e.Model + " · " + detail
			}
			line := padRight(panelTime(e.At), 9) + padRight(kind, 9) + padRight(statusCode(e.Status), 7) +
				padRight(render.Truncate(string(e.Category), 15), 16) + render.Truncate(detail, max(10, w-46))
			if e.Kind == agent.EvError {
				line = fg(th.Error).Render(line)
			}
			out = append(out, pickLine(th, line, n == v.cursor))
			n++
		}
	}
	if len(out) == 0 {
		msg := "No model calls recorded yet."
		if !v.st.recording {
			msg = "Recording is off. Press e to record model calls from now on."
		}
		out = append(out, fg(th.TextFaint).Render("  "+msg))
	}
	return head, out
}

// pickLine marks the highlighted row of a list.
func pickLine(th theme.Theme, line string, sel bool) string {
	if sel {
		return fgb(th.BrandSecondary).Render("› ") + fgb(th.TextPrimary).Render(line)
	}
	return "  " + fg(th.TextSecondary).Render(line)
}

// View implements Modal.
func (v *modelEventsView) View(w, h int) string {
	th := v.th
	colHead, rows := v.lines(w)
	v.cursor = clamp(v.cursor, 0, max(0, len(rows)-1))
	head := []string{modalTabs(th, modelEventsTabs, v.tab), "", colHead}
	rec := fg(th.TextFaint).Render("recording off")
	if v.st.recording {
		rec = fg(th.Success).Render("● recording")
	}
	foot := []string{"", rec}
	return scrollList(head, rows, foot, w, h, &v.off, v.cursor)
}

// scrollList draws a header, a body that scrolls to keep the row at cursor
// in view, and a footer, in h rows.
func scrollList(head, body, foot []string, w, h int, off *int, cursor int) string {
	avail := max(1, h-len(head)-len(foot))
	*off = keepInView(cursor, *off, avail)
	if last := len(body) - avail; *off > last {
		*off = max(0, last)
	}
	end := min(len(body), *off+avail)
	out := append(append([]string{}, head...), body[*off:end]...)
	out = append(out, foot...)
	return strings.Join(fitLines(out, w, h), "\n")
}

// ---------------------------------------------------------------------------
// Compaction log

// compactionLogView is the compaction history, newest first, with a text
// filter and a detail page. It follows the LogViewer rules of spec 5.1.8.
type compactionLogView struct {
	th      theme.Theme
	be      Backend
	filter  string
	cursor  int
	off     int
	detail  bool
	dscroll int
	note    string
}

func newCompactionLogView(th theme.Theme, be Backend) *compactionLogView {
	return &compactionLogView{th: th, be: be}
}

// Title implements Modal.
func (v *compactionLogView) Title() string { return "Compaction log" }

// Icon implements Iconed.
func (v *compactionLogView) Icon() string { return "compact" }

// Footer implements Footered.
func (v *compactionLogView) Footer() string {
	if v.detail {
		return "↑↓ scroll · ^Y copy · Esc back"
	}
	return "↑↓ nav · Enter detail · ^Y copy · Esc close"
}

// Size implements Sized.
func (v *compactionLogView) Size(cols, rows int) (int, int) {
	return min(cols-4, 100), min(rows-4, 30)
}

// compactKind names an entry the way the log shows it.
func compactKind(e agent.Event) (kind, icon string) {
	switch e.Kind {
	case agent.EvCompactStart:
		if e.Text == "manual" {
			return "compact", "◆"
		}
		return "auto-trigger", "⚡"
	case agent.EvCompactDone:
		if skipped, _ := e.Meta["skipped"].(bool); skipped {
			return "skipped", "•"
		}
		return "compact", "◆"
	}
	return "error", "✗"
}

// metaInt reads an integer Meta value, which the agent stores as int.
func metaInt(e agent.Event, key string) (int, bool) {
	switch n := e.Meta[key].(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	}
	return 0, false
}

// compactMessage is the one-line summary of an entry.
func compactMessage(e agent.Event) string {
	switch e.Kind {
	case agent.EvCompactStart:
		return "started (" + e.Text + ")"
	case agent.EvCompactDone:
		if skipped, _ := e.Meta["skipped"].(bool); skipped {
			return "nothing replaced"
		}
		before, _ := metaInt(e, "messages_before")
		after, _ := metaInt(e, "messages_after")
		return fmt.Sprintf("%d messages → %d", before, after)
	}
	return oneLine(e.Text)
}

// entries are the log entries, newest first, that match the filter.
func (v *compactionLogView) entries() []agent.Event {
	all := v.be.CompactionLog()
	q := strings.ToLower(strings.TrimSpace(v.filter))
	var out []agent.Event
	for i := len(all) - 1; i >= 0; i-- {
		e := all[i]
		if q == "" {
			out = append(out, e)
			continue
		}
		kind, _ := compactKind(e)
		hay := strings.ToLower(kind + " " + compactMessage(e) + " " + e.Text)
		if strings.Contains(hay, q) {
			out = append(out, e)
		}
	}
	return out
}

// Update implements Modal.
func (v *compactionLogView) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return v, nil, false
	}
	n := len(v.entries())
	switch km.String() {
	case "esc":
		if v.detail {
			v.detail = false
			v.note = ""
			return v, nil, false
		}
		return v, nil, true
	case "up", "k":
		if v.detail {
			v.dscroll = max(0, v.dscroll-1)
		} else {
			v.cursor = clamp(v.cursor-1, 0, max(0, n-1))
		}
	case "down", "j":
		if v.detail {
			v.dscroll++
		} else {
			v.cursor = clamp(v.cursor+1, 0, max(0, n-1))
		}
	case "enter":
		if !v.detail && n > 0 {
			v.detail, v.dscroll = true, 0
		}
	case "ctrl+y":
		if text := v.copyText(); text != "" {
			copyToTerminal(text)
			v.note = "Copied!"
		}
	case "backspace":
		if !v.detail {
			v.filter = dropLastRune(v.filter)
			v.cursor, v.off = 0, 0
		}
	default:
		if !v.detail && km.Type == tea.KeyRunes {
			v.filter += string(km.Runes)
			v.cursor, v.off = 0, 0
		}
	}
	return v, nil, false
}

// copyText is what ^Y copies: the summary of the selected entry, or its message.
func (v *compactionLogView) copyText() string {
	list := v.entries()
	if v.cursor < 0 || v.cursor >= len(list) {
		return ""
	}
	e := list[v.cursor]
	if e.Text != "" && e.Kind == agent.EvCompactDone {
		return e.Text
	}
	return compactMessage(e)
}

// View implements Modal.
func (v *compactionLogView) View(w, h int) string {
	th := v.th
	list := v.entries()
	if v.detail && v.cursor < len(list) {
		return v.viewDetail(list[v.cursor], w, h)
	}
	head := []string{fg(th.TextMuted).Render(v.filterLine()), ""}
	if len(list) == 0 {
		msg := "no compaction events yet"
		if strings.TrimSpace(v.filter) != "" {
			msg = "no matching events"
		}
		out := append(head, fg(th.TextFaint).Render("· "+msg))
		return strings.Join(fitLines(out, w, h), "\n")
	}
	body := make([]string, 0, len(list))
	for i, e := range list {
		kind, icon := compactKind(e)
		sel := i == v.cursor
		marker := "  "
		msg := fg(th.TextSecondary).Render(render.Truncate(compactMessage(e), max(10, w-40)))
		if sel {
			marker = fgb(th.BrandSecondary).Render("› ")
			msg = fgb(th.TextPrimary).Render(render.Truncate(compactMessage(e), max(10, w-40)))
		}
		kindCol := fg(th.Brand).Render(padRight(kind, 14))
		switch kind {
		case "auto-trigger":
			kindCol = fg(th.Success).Render(padRight(kind, 14))
		case "error":
			kindCol = fg(th.Error).Render(padRight(kind, 14))
		case "skipped":
			kindCol = fg(th.TextMuted).Render(padRight(kind, 14))
		}
		line := marker + fg(th.TextMuted).Render(icon+" ") + kindCol + msg
		body = append(body, line)
	}
	foot := []string{"", fg(th.TextFaint).Render(v.countLine(len(list))) + v.noteSuffix()}
	avail := max(1, h-len(head)-len(foot))
	v.cursor = clamp(v.cursor, 0, len(body)-1)
	v.off = keepInView(v.cursor, v.off, avail)
	if last := len(body) - avail; v.off > last {
		v.off = max(0, last)
	}
	end := min(len(body), v.off+avail)
	out := append(append([]string{}, head...), body[v.off:end]...)
	out = append(out, foot...)
	return strings.Join(fitLines(out, w, h), "\n")
}

// filterLine is the filter field, with its placeholder when empty.
func (v *compactionLogView) filterLine() string {
	if v.filter == "" {
		return "› type to filter..."
	}
	return "› " + v.filter
}

// countLine is "<n> event" or "<n> events".
func (v *compactionLogView) countLine(n int) string {
	if n == 1 {
		return "1 event"
	}
	return fmt.Sprintf("%d events", n)
}

// noteSuffix is " · Copied!" after a copy, else nothing.
func (v *compactionLogView) noteSuffix() string {
	if v.note == "" {
		return ""
	}
	return " · " + v.note
}

// viewDetail is the detail page of one entry: the fields it has, then its summary.
func (v *compactionLogView) viewDetail(e agent.Event, w, h int) string {
	th := v.th
	kind, icon := compactKind(e)
	var lines []string
	add := func(label, value string) {
		if value != "" {
			lines = append(lines, fg(th.TextSecondary).Render(label+": ")+value)
		}
	}
	add("Kind", kind)
	add("Time", panelTime(e.At))
	if e.Kind == agent.EvCompactStart {
		add("Reason", e.Text)
	}
	before, okB := metaInt(e, "messages_before")
	after, okA := metaInt(e, "messages_after")
	if okB || okA {
		b, a := "?", "?"
		if okB {
			b = fmt.Sprint(before)
		}
		if okA {
			a = fmt.Sprint(after)
		}
		add("Messages", b+" → "+a)
	}
	if e.Text != "" && e.Kind == agent.EvCompactDone {
		add("Summary", fmt.Sprintf("%d chars", len([]rune(e.Text))))
		lines = append(lines, "", fg(th.TextDim).Render("── Summary ──"))
		for _, l := range strings.Split(e.Text, "\n") {
			lines = append(lines, render.Truncate(l, max(10, w-4)))
		}
	}
	if e.Kind == agent.EvWarning || e.Kind == agent.EvError {
		lines = append(lines, "", fg(th.TextDim).Render("── Error ──"))
		lines = append(lines, wrapLines(e.Text, max(10, w-4))...)
	}
	head := []string{fgb(th.Brand).Render(icon+" "+kind) + fg(th.TextFaint).Render("  "+panelTime(e.At)) + v.noteSuffix(), ""}
	foot := []string{"", fg(th.TextFaint).Render("Esc back to the list")}
	avail := max(1, h-len(head)-len(foot))
	v.dscroll = clamp(v.dscroll, 0, max(0, len(lines)-avail))
	end := min(len(lines), v.dscroll+avail)
	out := append(append([]string{}, head...), lines[v.dscroll:end]...)
	out = append(out, foot...)
	return strings.Join(fitLines(out, w, h), "\n")
}

// ---------------------------------------------------------------------------
// Subagents

// subagentsView lists the subagents of the newest dispatch. It is read-only;
// /cancel <id> stops one.
type subagentsView struct {
	th     theme.Theme
	be     Backend
	cursor int
	off    int
	width  int
}

func newSubagentsView(th theme.Theme, be Backend) *subagentsView {
	return &subagentsView{th: th, be: be}
}

// Title implements Modal.
func (v *subagentsView) Title() string { return "Subagents" }

// Icon implements Iconed.
func (v *subagentsView) Icon() string { return "ghost" }

// Footer implements Footered.
func (v *subagentsView) Footer() string {
	return "↑↓ move · /cancel <id> stops one · Esc close"
}

// Size implements Sized.
func (v *subagentsView) Size(cols, rows int) (int, int) {
	w, h := panelFrame(cols, rows)
	v.width = w
	return w, h
}

// Update implements Modal.
func (v *subagentsView) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return v, nil, false
	}
	switch km.String() {
	case "esc", "q":
		return v, nil, true
	case "up", "k":
		v.cursor = max(0, v.cursor-1)
	case "down", "j":
		v.cursor++
	}
	return v, nil, false
}

// subagentGlyph is the state mark of a subagent row.
func subagentGlyph(th theme.Theme, status string) (string, lipgloss.TerminalColor) {
	switch status {
	case "running":
		return "◐", th.Info
	case "done":
		return "✓", th.Success
	case "error":
		return "✗", th.Error
	case "cancelled":
		return "⊘", th.Warning
	}
	return "○", th.TextDim
}

// View implements Modal.
func (v *subagentsView) View(w, h int) string {
	th := v.th
	list := v.be.Subagents()
	if len(list) == 0 {
		out := []string{fg(th.TextFaint).Render("  No dispatch has run in this session. Ask for parallel work to start one."),
			"", fg(th.TextFaint).Render("  Subagents appear here while a dispatch runs, then stay until the next one.")}
		return strings.Join(fitLines(out, w, h), "\n")
	}
	v.cursor = clamp(v.cursor, 0, len(list)-1)
	head := []string{fg(th.TextDim).Render(padRight("", 2) + padRight("Id", 5) + padRight("Role", 8) +
		padRight("State", 12) + padRight("Model", 22) + padRight("Steps", 6) + padRight("Tokens", 8) + "Last")}
	body := make([]string, 0, len(list))
	for i, s := range list {
		g, col := subagentGlyph(th, s.Status)
		last := s.Last
		if s.Error != "" {
			last = s.Error
		}
		state := fg(col).Render(g+" ") + fg(th.TextMuted).Render(padRight(s.Status, 10))
		tokens := render.HumanTokens(s.Tokens.InputTokens + s.Tokens.OutputTokens)
		cells := padRight(s.ID, 5) + padRight(s.Role, 8) + state + padRight(render.Truncate(s.Model, 21), 22) +
			padRight(fmt.Sprint(s.Step), 6) + padRight(tokens, 8) + render.Truncate(oneLine(last), max(8, w-62))
		if i == v.cursor {
			body = append(body, fgb(th.BrandSecondary).Render("› ")+fgb(th.TextPrimary).Render(cells))
		} else {
			body = append(body, "  "+fg(th.TextSecondary).Render(cells))
		}
	}
	foot := []string{"", fg(th.TextFaint).Render(subagentCount(len(list)))}
	return scrollList(head, body, foot, w, h, &v.off, v.cursor)
}

// subagentCount is "1 subagent" or "<n> subagents".
func subagentCount(n int) string {
	if n == 1 {
		return "1 subagent in the newest dispatch"
	}
	return fmt.Sprintf("%d subagents in the newest dispatch", n)
}
