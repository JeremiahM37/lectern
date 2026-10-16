package chat

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// build renders every record (finished ones from cache), records where each
// record starts, and collects the clickable rows.
func (v *View) build() {
	w := v.w
	if w < 1 {
		w = 1
	}
	now := v.clock()
	v.advanceAll(now)
	lines := []string{}
	hits := map[int]hitTarget{}
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
			hits[base+h.line] = hitTarget{rec: rec, call: h.call, seg: h.seg, summary: h.summary}
		}
	}
	if len(v.msgs) == 0 {
		lines = append(lines, "  "+fg(v.th.TextMuted, styledItalic("No messages yet. Type below to start.")))
	}
	v.lines, v.hits, v.lastTotal = lines, hits, len(lines)
}

// dynamic reports whether a record can still change, so it must not be cached.
func (v *View) dynamic(i int, rec *Record) bool {
	switch rec.Role {
	case roleUser:
		return false
	case roleSystem:
		return i == len(v.msgs)-1 || (rec.Kind == "compact" && !rec.Done)
	}
	return !rec.Done || rec == v.live || rec.hasOpen()
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
	rows := make([]string, v.h)
	for r := range rows {
		s := ""
		if i := top + r; i < len(v.lines) {
			s = v.lines[i]
		}
		rows[r] = fit(s, v.w)
	}
	if !v.sticky && top+v.h < len(v.lines) {
		if n := len(v.lines) - v.detachAt; n > 0 {
			rows[v.h-1] = fit(fg(v.th.Brand, fmt.Sprintf("↓ %d new lines — End to jump", n)), v.w)
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

// HandleMouse handles the wheel (scroll) and a left click (toggle the tool
// row or reasoning block under the pointer). Y is relative to the top of the
// view. It reports whether the event was used.
func (v *View) HandleMouse(m tea.MouseMsg) bool {
	switch {
	case m.Button == tea.MouseButtonWheelUp:
		v.ScrollUp(3)
		return true
	case m.Button == tea.MouseButtonWheelDown:
		v.ScrollDown(3)
		return true
	case m.Action == tea.MouseActionPress && m.Button == tea.MouseButtonLeft:
		v.build()
		abs := v.windowTop(len(v.lines)) + m.Y
		t, ok := v.hits[abs]
		if !ok {
			return false
		}
		if t.summary {
			t.rec.ReasonOpen = !t.rec.ReasonOpen
		}
		if t.call != nil {
			t.call.Toggled = !t.call.Toggled
		}
		if t.seg != nil {
			t.seg.Toggled = !t.seg.Toggled
		}
		t.rec.cache = nil
		return true
	}
	return false
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
