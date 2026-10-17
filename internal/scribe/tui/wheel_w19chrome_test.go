package tui

import (
	"math"
	"testing"
	"time"
)

// The compact profile is the one spec 01 §7.8 gives for autocomplete and
// history: gain 0.18, time constant 160 ms, at most 3 rows.
func TestCompactWheelProfileMatchesSpec(t *testing.T) {
	if compactWheelProfile.gain != 0.18 || compactWheelProfile.tau != 160*time.Millisecond || compactWheelProfile.max != 3 {
		t.Fatalf("compact profile = %+v, want gain 0.18, tau 160ms, max 3", compactWheelProfile)
	}
}

// A fast burst on the compact profile reaches its cap of three rows and never
// goes past it.
func TestCompactWheelAcceleratesToItsCap(t *testing.T) {
	var w listWheel
	now := time.Unix(2000, 0)
	if n := w.stepCompact(now); n != 1 {
		t.Fatalf("first tick moved %d rows, want 1", n)
	}
	var last int
	for i := 0; i < 20; i++ {
		now = now.Add(5 * time.Millisecond)
		last = w.stepCompact(now)
		if last < 1 || last > 3 {
			t.Fatalf("burst tick %d moved %d rows, want 1 to 3", i, last)
		}
	}
	if last != 3 {
		t.Fatalf("burst settled at %d rows, want the compact cap 3", last)
	}
}

// A slow tick on the compact profile moves one row.
func TestCompactWheelSlowTickMovesOneRow(t *testing.T) {
	var w listWheel
	now := time.Unix(2000, 0)
	w.stepCompact(now)
	if n := w.stepCompact(now.Add(time.Second)); n != 1 {
		t.Fatalf("slow tick moved %d rows, want 1", n)
	}
}

// The compact profile decays with its own 160 ms constant. A tick 160 ms after
// the last one keeps exp(-1) of the earlier score.
func TestCompactWheelDecaysWithItsOwnTau(t *testing.T) {
	var w listWheel
	now := time.Unix(2000, 0)
	w.stepCompact(now)
	w.stepCompact(now.Add(160 * time.Millisecond))
	want := 1 + math.Exp(-1)
	if math.Abs(w.score-want) > 1e-9 {
		t.Fatalf("score after 160 ms = %v, want %v", w.score, want)
	}
}

// The list profile is unchanged by the compact one: its 140 ms constant and cap
// of four still apply to list wheels.
func TestListWheelKeepsItsOwnProfile(t *testing.T) {
	var w listWheel
	now := time.Unix(2000, 0)
	w.step(now)
	w.step(now.Add(140 * time.Millisecond))
	want := 1 + math.Exp(-1)
	if math.Abs(w.score-want) > 1e-9 {
		t.Fatalf("list score after 140 ms = %v, want %v", w.score, want)
	}
	var last int
	for i := 0; i < 20; i++ {
		now = now.Add(5 * time.Millisecond)
		last = w.step(now)
	}
	if last != listWheelMax {
		t.Fatalf("list burst settled at %d rows, want %d", last, listWheelMax)
	}
}
