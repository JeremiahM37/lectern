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

// listWheel is the burst state for one picker.
type listWheel struct {
	at    time.Time
	score float64
}

// step returns the rows to move for a wheel tick at now and updates the score.
func (w *listWheel) step(now time.Time) int {
	if !w.at.IsZero() {
		dt := now.Sub(w.at)
		if dt < 0 {
			dt = 0
		}
		w.score *= math.Exp(-float64(dt) / float64(listWheelTau))
	}
	w.at = now
	w.score++
	n := int(math.Round(1 + listWheelGain*(w.score-1)))
	return min(max(n, 1), listWheelMax)
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
