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

// advanceSeg moves the reveal of an open text segment up to now.
func advanceSeg(s *Segment, now time.Time) {
	n := len([]rune(s.Text))
	if !s.Open {
		s.shown = n
		return
	}
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
	s.shown = next
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
