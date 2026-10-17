package tui

import (
	"math"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Wheel acceleration for lists (spec 01 §7.8, the list profile of
// MacOSScrollAccel): gain 0.25, time constant 140 ms, at most 4 rows a tick.
// A tick that follows another quickly builds up a score; each extra unit of
// score adds a quarter of a row, up to the cap. A slow turn moves one row.
const (
	listWheelGain = 0.25
	listWheelTau  = 140 * time.Millisecond
	listWheelMax  = 4
)

// wheelProfile is one acceleration curve: how fast the score turns into rows
// (gain), how quickly an old burst decays (tau), and the most rows one tick may
// move (max).
type wheelProfile struct {
	gain float64
	tau  time.Duration
	max  int
}

// listWheelProfile is the profile for VirtualList, DialogSelect and GroupedList.
var listWheelProfile = wheelProfile{gain: listWheelGain, tau: listWheelTau, max: listWheelMax}

// compactWheelProfile is the compact profile for autocomplete and history
// lists (spec 01 §7.8: A 0.18, tau 160, max 3). Those popups are short, so a
// burst should not jump past the rows that are on screen.
var compactWheelProfile = wheelProfile{gain: 0.18, tau: 160 * time.Millisecond, max: 3}

// listWheel is the burst state for one list.
type listWheel struct {
	at    time.Time
	score float64
}

// step returns the rows to move for a wheel tick at now, on the list profile,
// and updates the score.
func (w *listWheel) step(now time.Time) int {
	return w.stepWith(listWheelProfile, now)
}

// stepCompact returns the rows to move for a wheel tick at now, on the compact
// profile, and updates the score. Autocomplete and history lists call this.
func (w *listWheel) stepCompact(now time.Time) int {
	return w.stepWith(compactWheelProfile, now)
}

// stepWith is step for any profile.
func (w *listWheel) stepWith(p wheelProfile, now time.Time) int {
	if !w.at.IsZero() {
		dt := now.Sub(w.at)
		if dt < 0 {
			dt = 0
		}
		w.score *= math.Exp(-float64(dt) / float64(p.tau))
	}
	w.at = now
	w.score++
	n := int(math.Round(1 + p.gain*(w.score-1)))
	return min(max(n, 1), p.max)
}

// wheel moves the cursor by one accelerated step per tick. dir is -1 up, +1
// down. It returns the cursor-change command, as the keys do.
func (p *Picker) wheel(dir int, now time.Time) tea.Cmd {
	n := p.wheelState.step(now)
	for i := 0; i < n; i++ {
		p.move(dir)
	}
	return p.fireCursor()
}
