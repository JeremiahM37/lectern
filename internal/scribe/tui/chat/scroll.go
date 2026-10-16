package chat

import (
	"fmt"
	"math"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// build renders every record (finished ones from cache), records where each
// record starts, and collects the clickable rows.
func (v *View) build() {
	w := v.bodyW()
	now := v.clock()
	v.advanceAll(now)
	lines := []string{}
	hits := map[int]hitTarget{}
	if v.header != "" {
		lines = append(lines, fg(v.th.TextFaint, "── "+v.header+" ──"), "")
	}
	if v.earlier > 0 {
		lines = append(lines, earlierDivider(v.earlier, w, v.th.TextFaint), "")
	}
	v.recStart = make([]int, len(v.msgs))
	for i, rec := range v.msgs {
		if i > 0 {
			lines = append(lines, "")
		}
		v.recStart[i] = len(lines)
		var rb builder
		dyn := v.dynamic(i, rec)
		if c := rec.cache; !dyn && c != nil && c.gen == v.gen && c.width == w &&
			c.verbose == v.opt.Verbose && c.reasoning == v.opt.ShowReasoning && c.style == v.opt.ChatStyle {
			rb = builder{lines: c.lines, hits: c.hits}
		} else {
			rb = v.renderRecord(i, rec, w, now)
			if !dyn {
				rec.cache = &cached{gen: v.gen, width: w, verbose: v.opt.Verbose, reasoning: v.opt.ShowReasoning,
					style: v.opt.ChatStyle, lines: rb.lines, hits: rb.hits}
			}
		}
		base := len(lines)
		lines = append(lines, rb.lines...)
		for _, h := range rb.hits {
			hits[base+h.line] = hitTarget{rec: rec, call: h.call, seg: h.seg, summary: h.summary, rail: h.rail}
		}
	}
	if len(v.msgs) == 0 {
		lines = append(lines, "  "+fg(v.th.TextMuted, styledItalic("No messages yet. Type below to start.")))
	}
	v.markLines(lines, hits, w)
	v.placeRail()
	v.lines, v.hits, v.lastTotal = lines, hits, len(lines)
}

// earlierDivider is the rule drawn at the top of a transcript that starts
// after earlier messages: `── N earlier messages ──`, centred in w cells.
func earlierDivider(n, w int, col lipgloss.TerminalColor) string {
	noun := "messages"
	if n == 1 {
		noun = "message"
	}
	label := fmt.Sprintf(" %d earlier %s ", n, noun)
	return fg(col, clip("──"+label+"──", maxInt(w, 1)))
}

// dynamic reports whether a record can still change, so it must not be cached.
func (v *View) dynamic(i int, rec *Record) bool {
	switch rec.Role {
	case roleUser:
		return false
	case roleSystem:
		return i == len(v.msgs)-1 || (rec.Kind == "compact" && !rec.Done)
	}
	return !rec.Done || rec == v.live || rec.hasOpen() || v.tasksLingering(rec)
}

// tasksLingering reports whether a finished record holds a task list that is
// still on screen for its linger time. Its lines change with the clock, so the
// record is drawn fresh until the list hides.
func (v *View) tasksLingering(rec *Record) bool {
	now := v.clock()
	for _, s := range rec.Segments {
		if s.Kind == "tasks" && !s.DoneAt.IsZero() && now.Sub(s.DoneAt) < tasksLinger {
			return true
		}
	}
	return false
}

func (v *View) advanceAll(now time.Time) {
	for _, r := range v.msgs {
		for _, s := range r.Segments {
			if s.Kind == "text" && s.Open {
				advanceSeg(s, now)
			}
		}
	}
}

// windowTop returns the first visible line, pinning to the bottom when sticky.
func (v *View) windowTop(total int) int {
	max0 := total - v.h
	if max0 < 0 {
		max0 = 0
	}
	if v.sticky {
		v.top = max0
	} else if v.top > max0 {
		v.top = max0
	}
	if v.top < 0 {
		v.top = 0
	}
	return v.top
}

// View returns exactly h lines, each exactly w cells.
func (v *View) View() string {
	if v.w <= 0 || v.h <= 0 {
		return ""
	}
	v.build()
	top := v.windowTop(len(v.lines))
	bw := v.bodyW()
	rows := make([]string, v.h)
	for r := range rows {
		s := ""
		if i := top + r; i < len(v.lines) {
			s = v.lines[i]
		}
		rows[r] = fit(s, bw)
	}
	if !v.sticky && top+v.h < len(v.lines) {
		if n := len(v.lines) - v.detachAt; n > 0 {
			rows[v.h-1] = fit(fg(v.th.Brand, fmt.Sprintf("↓ %d new lines — End to jump", n)), bw)
		}
	}
	if v.railOn() {
		rail := v.railColumn(top)
		for r := range rows {
			rows[r] = rail[r] + rows[r]
		}
	}
	return strings.Join(rows, "\n")
}

// ScrollUp moves n lines up and detaches from the bottom.
func (v *View) ScrollUp(n int) {
	if n <= 0 {
		return
	}
	v.build()
	if v.sticky {
		v.detachAt = v.lastTotal
		v.top = v.lastTotal - v.h
		if v.top < 0 {
			v.top = 0
		}
		v.sticky = false
	}
	v.top -= n
	if v.top < 0 {
		v.top = 0
	}
}

// ScrollDown moves n lines down; reaching the bottom re-attaches to it.
func (v *View) ScrollDown(n int) {
	if n <= 0 {
		return
	}
	v.build()
	if v.sticky {
		return
	}
	max0 := v.lastTotal - v.h
	if max0 < 0 {
		max0 = 0
	}
	v.top += n
	if v.top >= max0 {
		v.sticky = true
	}
}

// PageUp scrolls one screen up.
func (v *View) PageUp() { v.ScrollUp(maxInt(1, v.h-1)) }

// PageDown scrolls one screen down.
func (v *View) PageDown() { v.ScrollDown(maxInt(1, v.h-1)) }

// Top scrolls to the first line.
func (v *View) Top() {
	v.build()
	if v.sticky {
		v.detachAt = v.lastTotal
	}
	v.sticky = false
	v.top = 0
}

// Bottom re-attaches to the newest line.
func (v *View) Bottom() { v.sticky = true }

// AtBottom reports whether the newest line is in view.
func (v *View) AtBottom() bool {
	if v.sticky {
		return true
	}
	v.build()
	return v.top >= v.lastTotal-v.h
}

// Wheel acceleration follows the spec's chat profile (MacOSScrollAccel: gain
// 0.35, time constant 110 ms, at most 6 lines a tick). A tick costs 3 lines
// when the wheel is turned slowly. Ticks that arrive close together build up
// a score, and each extra unit of score adds 35% of a tick, up to the cap.
const (
	wheelBase = 3
	wheelMax  = 6
	wheelGain = 0.35
	wheelTau  = 110 * time.Millisecond
)

// wheelStep returns the lines to scroll for this wheel tick and updates the
// burst score. It is called once per wheel event.
func (v *View) wheelStep() int {
	now := v.clock()
	if !v.wheelAt.IsZero() {
		dt := now.Sub(v.wheelAt)
		if dt < 0 {
			dt = 0
		}
		v.wheelScore *= math.Exp(-float64(dt) / float64(wheelTau))
	}
	v.wheelAt = now
	v.wheelScore++
	step := int(math.Round(wheelBase * (1 + wheelGain*(v.wheelScore-1))))
	if step < wheelBase {
		step = wheelBase
	}
	if step > wheelMax {
		step = wheelMax
	}
	return step
}

// HandleMouse handles the wheel (scroll), pointer motion (hover highlight and
// drag selection) and the left button. A press starts a selection. A release
// that did not drag is a click: it toggles the tool row, reasoning block or
// rail fold under the pointer, or toggles every row when the click lands on
// empty space. A release after a drag keeps the selection for ReleasedSelection
// to copy. Y is relative to the top of the view. It reports whether the event
// was used.
func (v *View) HandleMouse(m tea.MouseMsg) bool {
	switch {
	case m.Button == tea.MouseButtonWheelUp:
		v.ScrollUp(v.wheelStep())
		return true
	case m.Button == tea.MouseButtonWheelDown:
		v.ScrollDown(v.wheelStep())
		return true
	case m.Action == tea.MouseActionMotion:
		v.pointerMoved(m)
		return true
	case m.Action == tea.MouseActionPress && m.Button == tea.MouseButtonLeft:
		if v.railOn() && m.X < railW {
			return true // the rail takes clicks in its own columns
		}
		v.build()
		v.selecting, v.sel = true, false
		v.anchor = v.cellAt(m)
		v.cursor = v.anchor
		return true
	case m.Action == tea.MouseActionRelease && m.Button == tea.MouseButtonLeft:
		if !v.selecting {
			return false
		}
		v.selecting = false
		if v.sel {
			v.copyText = v.selectedText()
			v.copyPending = true
			v.gen++
			return true
		}
		v.click(v.anchor.line)
		return true
	}
	return false
}

// pointerMoved extends a drag selection, or moves the hover highlight to the
// tool, reasoning or rail row under the pointer.
func (v *View) pointerMoved(m tea.MouseMsg) {
	if v.selecting && m.Button == tea.MouseButtonLeft {
		v.build()
		v.cursor = v.cellAt(m)
		if v.cursor != v.anchor {
			v.sel = true
		}
		return
	}
	if m.Button != tea.MouseButtonNone {
		return
	}
	v.build()
	v.hoverLine = -1
	if v.railOn() && m.X < railW {
		return
	}
	abs := v.windowTop(len(v.lines)) + m.Y
	if _, ok := v.hits[abs]; ok {
		v.hoverLine = abs
	}
}

// ClearHover drops the hover highlight, for when the pointer leaves the
// transcript.
func (v *View) ClearHover() { v.hoverLine = -1 }

// click acts on a release at absolute line abs: a row that has a fold toggles
// it, and any other row toggles every row (Ctrl+O).
func (v *View) click(abs int) {
	t, ok := v.hits[abs]
	if !ok {
		v.ToggleExpand()
		return
	}
	if t.summary {
		t.rec.ReasonOpen = !t.rec.ReasonOpen
	}
	if t.rail {
		t.rec.RailOpen = !t.rec.RailOpen
	}
	if t.call != nil {
		t.call.Toggled = !t.call.Toggled
	}
	if t.seg != nil {
		t.seg.Toggled = !t.seg.Toggled
	}
	t.rec.cache = nil
	v.gen++
}

// cellAt maps a mouse position to a transcript cell: absolute line and the
// column inside the message body (the rail columns are not part of it).
func (v *View) cellAt(m tea.MouseMsg) cellPos {
	v.build()
	col := m.X
	if v.railOn() {
		col -= railW
	}
	if col < 0 {
		col = 0
	}
	line := v.windowTop(len(v.lines)) + m.Y
	if line >= len(v.lines) {
		line = len(v.lines) - 1
	}
	if line < 0 {
		line = 0
	}
	return cellPos{line: line, col: col}
}

// ReleasedSelection returns the text of a drag selection that has just ended,
// once: the caller copies it. ok is false when no drag ended since the last call.
func (v *View) ReleasedSelection() (string, bool) {
	if !v.copyPending {
		return "", false
	}
	v.copyPending = false
	return v.copyText, true
}

// selectedText is the plain text between the anchor and the cursor, inclusive.
// Each line is cut to its columns and trailing spaces are dropped.
func (v *View) selectedText() string {
	a, b := v.selectionBounds()
	var out []string
	for l := a.line; l <= b.line && l < len(v.lines); l++ {
		s := ansi.Strip(v.lines[l])
		from, to := 0, ansi.StringWidth(s)
		if l == a.line {
			from = a.col
		}
		if l == b.line && b.col+1 < to {
			to = b.col + 1
		}
		if from > to {
			from = to
		}
		out = append(out, strings.TrimRight(ansi.Cut(s, from, to), " "))
	}
	return strings.Join(out, "\n")
}

// selectionBounds returns the selection ordered from its start to its end.
func (v *View) selectionBounds() (cellPos, cellPos) {
	a, b := v.anchor, v.cursor
	if b.line < a.line || (b.line == a.line && b.col < a.col) {
		a, b = b, a
	}
	return a, b
}

// markLines decorates the built lines: the hovered tool row gets the elevated
// background, and a drag selection is shown in reverse video.
func (v *View) markLines(lines []string, hits map[int]hitTarget, w int) {
	if _, ok := hits[v.hoverLine]; ok && v.hoverLine >= 0 && v.hoverLine < len(lines) && !v.sel {
		lines[v.hoverLine] = bgLine(lines[v.hoverLine], w, v.th.BgElevated)
	}
	if !v.sel {
		return
	}
	a, b := v.selectionBounds()
	for l := a.line; l <= b.line && l < len(lines); l++ {
		from, to := 0, -1
		if l == a.line {
			from = a.col
		}
		if l == b.line {
			to = b.col + 1
		}
		lines[l] = reverseCells(lines[l], w, from, to)
	}
}

// reverseCells shows the cells [from, to) of s in reverse video; to < 0 means
// to the end of the line. Resets inside the selection are followed by reverse
// again, as bgLine does for backgrounds.
func reverseCells(s string, w, from, to int) string {
	s = fit(s, w)
	if to < 0 || to > w {
		to = w
	}
	if from > to {
		from = to
	}
	left := ansi.Cut(s, 0, from)
	mid := ansi.Cut(s, from, to)
	right := ansi.Cut(s, to, w)
	mid = strings.ReplaceAll(mid, "\x1b[0m", "\x1b[0m\x1b[7m")
	mid = strings.ReplaceAll(mid, "\x1b[m", "\x1b[m\x1b[7m")
	return left + "\x1b[7m" + mid + "\x1b[27m" + right
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
