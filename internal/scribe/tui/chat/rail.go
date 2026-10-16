package chat

import (
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
)

// Checkpoint is one marker on the rail: a turn that changed files.
type Checkpoint struct {
	Turn   int // 1-based user turn, counted the way JumpToTurn counts them
	Undone bool
}

const (
	railW = 3 // caret, marker, gap
	// railMinWidth is the transcript width below which the rail is hidden. It
	// is the width inside the frame and beside the side panels, not the
	// terminal width: at an 80-column terminal the transcript is already
	// narrower than 80, so a threshold of 80 hid the rail there.
	railMinWidth = 70
)

// SetCheckpoints replaces the rail's markers. viewed is the index of the
// checkpoint being browsed, or -1 for the live view.
func (v *View) SetCheckpoints(cps []Checkpoint, viewed int) {
	v.cps = append([]Checkpoint(nil), cps...)
	if viewed < 0 || viewed >= len(v.cps) {
		viewed = -1
	}
	v.railViewed = viewed
	v.railSel = clamp(v.railSel, 0, len(v.cps)-1)
}

// railOn reports whether the rail is drawn: there is a checkpoint and the
// transcript is wide enough to spare the columns.
func (v *View) railOn() bool { return len(v.cps) > 0 && v.w >= railMinWidth }

// bodyW is the width left for messages once the rail takes its columns.
func (v *View) bodyW() int {
	if v.railOn() {
		return maxInt(1, v.w-railW)
	}
	return maxInt(1, v.w)
}

// RailFocused reports whether the rail owns the arrow keys.
func (v *View) RailFocused() bool { return v.railFocus && v.railOn() }

// FocusRail gives the rail the keyboard, starting on the browsed checkpoint
// or else the latest active one. It reports false when there is no rail.
func (v *View) FocusRail() bool {
	if !v.railOn() {
		return false
	}
	v.railFocus = true
	switch {
	case v.railViewed >= 0:
		v.railSel = v.railViewed
	case v.railLatest() >= 0:
		v.railSel = v.railLatest()
	default:
		v.railSel = len(v.cps) - 1
	}
	return true
}

// BlurRail returns the keyboard to the input.
func (v *View) BlurRail() { v.railFocus = false }

// RailMove steps the rail cursor by d (clamped at both ends) and scrolls the
// chat to that turn.
func (v *View) RailMove(d int) {
	if !v.RailFocused() {
		return
	}
	v.railSel = clamp(v.railSel+d, 0, len(v.cps)-1)
	v.JumpToTurn(v.cps[v.railSel].Turn)
}

// RailSelected returns the index and checkpoint under the rail cursor.
func (v *View) RailSelected() (int, Checkpoint, bool) {
	if !v.RailFocused() {
		return -1, Checkpoint{}, false
	}
	return v.railSel, v.cps[v.railSel], true
}

// railLatest is the index of the newest checkpoint that is not undone, or -1.
func (v *View) railLatest() int {
	for i := len(v.cps) - 1; i >= 0; i-- {
		if !v.cps[i].Undone {
			return i
		}
	}
	return -1
}

// railRunning reports whether checkpoint i belongs to the turn in progress.
func (v *View) railRunning(i int) bool {
	cp := v.cps[i]
	return v.busy && !cp.Undone && cp.Turn == v.railUser && i == len(v.cps)-1
}

// placeRail records the first rendered row of the user turn behind each
// checkpoint. build calls it after recStart is known.
func (v *View) placeRail() {
	v.railAt = make([]int, len(v.cps))
	for i := range v.railAt {
		v.railAt[i] = -1
	}
	n := 0
	for i, r := range v.msgs {
		if r.Role != roleUser || r.Steering {
			continue
		}
		n++
		for c, cp := range v.cps {
			if cp.Turn == n {
				v.railAt[c] = v.recStart[i]
			}
		}
	}
	v.railUser = n
}

// railColumn returns h cells, each exactly railW wide, for the rows that
// start at absolute line top.
func (v *View) railColumn(top int) []string {
	h := v.h
	caret := make([]string, h)
	mark := make([]string, h)
	// A faint line joins each pair of markers that both have a row.
	for i := 0; i+1 < len(v.railAt); i++ {
		a, b := v.railAt[i], v.railAt[i+1]
		if a < 0 || b < 0 {
			continue
		}
		for abs := maxInt(a+1, top); abs < b && abs < top+h; abs++ {
			mark[abs-top] = fg(v.th.TextFaint, "│")
		}
	}
	// Markers above or below the window get an arrow on the edge row.
	above, below := false, false
	for _, a := range v.railAt {
		above = above || (a >= 0 && a < top)
		below = below || a >= top+h
	}
	if above && h > 0 {
		mark[0] = fg(v.th.TextMuted, "▲")
	}
	if below && h > 0 {
		mark[h-1] = fg(v.th.TextMuted, "▼")
	}
	now := v.clock()
	last := v.railLatest()
	for i, cp := range v.cps {
		a := v.railAt[i]
		if a < top || a >= top+h {
			continue
		}
		mark[a-top] = v.railGlyph(i, cp, last, now)
		if i == v.railSel && v.RailFocused() {
			caret[a-top] = fg(v.th.Brand, icons.Icon("arrow"))
		}
	}
	out := make([]string, h)
	for r := range out {
		c, m := caret[r], mark[r]
		if c == "" {
			c = " "
		}
		if m == "" {
			m = " "
		}
		out[r] = fit(c, 1) + fit(m, 1) + " "
	}
	return out
}

// railGlyph picks the marker for checkpoint i. Priority: running, browsed,
// undone, latest active, then any other turn that changed files.
func (v *View) railGlyph(i int, cp Checkpoint, last int, now time.Time) string {
	th := v.th
	switch {
	case v.railRunning(i):
		return fg(th.BrandSecondary, render.Frame(render.Braille, frameAt(now)))
	case i == v.railViewed:
		return fg(th.Warning, icons.Dot)
	case cp.Undone:
		return fg(th.TextFaint, icons.Ring)
	case i == last && v.railViewed < 0:
		return fg(th.Brand, icons.Icon("checkpoint"))
	case i == last:
		return fg(th.TextMuted, icons.Icon("checkpoint"))
	}
	return fg(th.TextMuted, icons.Dot)
}

func clamp(x, lo, hi int) int {
	if hi < lo {
		return lo
	}
	return maxInt(lo, minInt(x, hi))
}

// RailHit reports which checkpoint marker is drawn on view row y (relative to
// the top of the view) when x falls in the rail columns. It returns the
// checkpoint's index in the list given to SetCheckpoints.
func (v *View) RailHit(x, y int) (int, Checkpoint, bool) {
	if !v.railOn() || x < 0 || x >= railW || y < 0 || y >= v.h {
		return -1, Checkpoint{}, false
	}
	v.build()
	abs := v.windowTop(len(v.lines)) + y
	// railColumn draws the later checkpoint over an earlier one on the same
	// row, so the click must report the same one: the last match.
	hit := -1
	for i, a := range v.railAt {
		if a == abs {
			hit = i
		}
	}
	if hit < 0 {
		return -1, Checkpoint{}, false
	}
	return hit, v.cps[hit], true
}

// SelectRail moves the rail cursor to checkpoint i without taking the keyboard.
func (v *View) SelectRail(i int) {
	if i >= 0 && i < len(v.cps) {
		v.railSel = i
	}
}
