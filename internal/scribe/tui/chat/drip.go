package chat

import (
	"math"
	"time"
	"unicode"
)

// The streaming drip reveals text at a smoothed rate instead of in network
// bursts. One step is 32 ms; velocity eases toward a target that grows with
// the amount buffered.
const (
	dripTick     = 32 * time.Millisecond
	dripFloor    = 0.8
	dripCap      = 16.0
	maxDripSteps = 64
)

// Fade durations for streaming text (spec 01 §2.12): the text eases in to its
// streaming opacity when a segment opens, and to full strength when it closes.
const (
	fadeIn  = 300 * time.Millisecond
	fadeOut = 250 * time.Millisecond
)

// stampFade records when an open text segment was first seen open and when it
// was first seen closed. Only a segment that was seen open is stamped closed,
// so a restored transcript (never seen open) does not fade out on load.
func stampFade(s *Segment, now time.Time) {
	if s.Kind != "text" {
		return
	}
	if s.Open {
		if s.openedAt.IsZero() {
			s.openedAt = now
		}
		return
	}
	if !s.openedAt.IsZero() && s.closedAt.IsZero() {
		s.closedAt = now
	}
}

// advanceSeg moves the reveal of an open text segment up to now.
func advanceSeg(s *Segment, now time.Time) {
	n := len([]rune(s.Text))
	if !s.Open {
		s.shown = n
		s.fed = n
		return
	}
	if n < s.fed {
		// The text got shorter than what was fed. The reveal assumes growth,
		// so the drip starts over rather than guessing where the text moved.
		s.shown, s.vel, s.dripAt = 0, dripFloor, time.Time{}
	}
	s.fed = n
	if s.dripAt.IsZero() {
		s.dripAt = now
		if s.vel == 0 {
			s.vel = dripFloor
		}
		return
	}
	steps := int(now.Sub(s.dripAt) / dripTick)
	if steps <= 0 {
		return
	}
	if steps > maxDripSteps {
		steps = maxDripSteps
		s.dripAt = now
	} else {
		s.dripAt = s.dripAt.Add(time.Duration(steps) * dripTick)
	}
	for i := 0; i < steps; i++ {
		dripStep(s)
	}
}

func dripStep(s *Segment) {
	rs := []rune(s.Text)
	if s.shown > len(rs) {
		s.shown = len(rs)
		return
	}
	buffered := len(rs) - s.shown
	if buffered <= 0 {
		s.vel += (dripFloor - s.vel) * 0.4
		return
	}
	target := dripFloor + float64(buffered)*0.08
	if target > dripCap {
		target = dripCap
	}
	ease := 0.15
	if buffered > 60 {
		ease = 0.55
	}
	s.vel += (target - s.vel) * ease
	n := int(math.Round(s.vel))
	if n < 1 {
		n = 1
	}
	if n > buffered {
		n = buffered
	}
	next := s.shown + n
	if snapped := snapWord(rs, next); snapped > s.shown {
		next = snapped
	}
	if next > len(rs) {
		next = len(rs)
	}
	// Never reveal half a grapheme or half an escape sequence. Holding back
	// is safe: the next tick reconsiders with more text, and the reveal never
	// moves backwards.
	next = safeCut(rs, next, s.shown)
	s.shown = next
}

// safeCut moves a reveal point off a grapheme or an ANSI escape it would
// split. It prefers the nearer clean point but never returns a value at or
// below floor (the text already revealed), so the reveal never moves back.
// When no clean point lies beyond floor it returns floor and waits for more.
func safeCut(rs []rune, i, floor int) int {
	if i <= 0 || i >= len(rs) {
		return i
	}
	if e, end := openEscape(rs, i); e >= 0 {
		back := e > floor
		fwd := end >= 0
		switch {
		case back && (!fwd || end-i > i-e):
			i = e
		case fwd:
			i = end
		default:
			return floor
		}
	}
	for i > floor && i < len(rs) && (joinsPrev(rs[i]) || rs[i-1] == zwj) {
		i--
	}
	return i
}

const (
	esc = 0x1b
	bel = 0x07
	zwj = 0x200D
)

// joinsPrev reports whether r belongs to the grapheme of the rune before it:
// a combining mark, a variation selector, or an emoji skin-tone modifier.
func joinsPrev(r rune) bool {
	return unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Mc, r) ||
		r == zwj || r == 0xFE0F || (r >= 0x1F3FB && r <= 0x1F3FF)
}

// openEscape finds the escape sequence that spans position i, if any. It
// returns its start and end (end is -1 when the sequence is not terminated
// yet), or start -1 when no sequence spans i.
func openEscape(rs []rune, i int) (start, end int) {
	for e := i - 1; e >= 0; e-- {
		if rs[e] != esc {
			continue
		}
		if x := escapeEnd(rs, e); x < 0 || x > i {
			return e, x
		}
		break
	}
	return -1, 0
}

// escapeEnd returns the index just past the escape sequence starting at e, or
// -1 when it has not finished arriving.
func escapeEnd(rs []rune, e int) int {
	if e+1 >= len(rs) {
		return -1
	}
	switch rs[e+1] {
	case '[': // CSI: parameters, then a final byte in 0x40-0x7E
		for k := e + 2; k < len(rs); k++ {
			if rs[k] >= 0x40 && rs[k] <= 0x7E {
				return k + 1
			}
		}
	case ']': // OSC: ends at BEL or ESC \
		for k := e + 2; k < len(rs); k++ {
			if rs[k] == bel {
				return k + 1
			}
			if rs[k] == esc && k+1 < len(rs) && rs[k+1] == '\\' {
				return k + 2
			}
		}
	default:
		return e + 2
	}
	return -1
}

// snapWord moves a reveal point to a word boundary: up to 8 runes ahead, or
// else up to 4 runes behind. It never splits a word when it can help it.
func snapWord(rs []rune, i int) int {
	if i <= 0 || i >= len(rs) {
		return i
	}
	if isBreak(rs[i-1]) || isBreak(rs[i]) {
		return i
	}
	for j := i + 1; j < len(rs) && j <= i+8; j++ {
		if isBreak(rs[j]) {
			return j
		}
	}
	for k := i - 1; k >= 0 && k >= i-4; k-- {
		if isBreak(rs[k]) {
			return k + 1
		}
	}
	return i
}

func isBreak(r rune) bool {
	return unicode.IsSpace(r) || r == ',' || r == '.' || r == ';' || r == ':' || r == '!' || r == '?' || r == ')'
}

// visibleText is the text revealed so far (all of it when not streaming).
func visibleText(s *Segment) string {
	if !s.Open {
		return s.Text
	}
	rs := []rune(s.Text)
	if s.shown > len(rs) {
		return s.Text
	}
	return string(rs[:s.shown])
}
