package tui

import (
	"math"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// runEase steps the shown cost toward target until it stops moving, and
// returns the frame count. It fails the test if the easing does not settle.
func runEase(t *testing.T, shown, target float64) (float64, int) {
	t.Helper()
	for frame := 1; frame <= 2000; frame++ {
		next := easeCost(shown, target)
		if next == shown {
			return shown, frame
		}
		shown = next
	}
	t.Fatalf("easing from %v toward %v did not settle", shown, target)
	return 0, 0
}

// The shown cost must end on the real value, not a rounded one, so a sub-cent
// figure still reads exactly as the provider billed it.
func TestCostEaseConvergesOnTheRealValue(t *testing.T) {
	for _, target := range []float64{0.004, 0.123, 0.5, 1.23, 4.5678, 12.345} {
		got, _ := runEase(t, 0, target)
		if got != target {
			t.Errorf("easing to %v settled on %v", target, got)
		}
	}
}

// Easing down, as a new session does when the cost resets, also lands exactly.
func TestCostEaseFallsToZeroExactly(t *testing.T) {
	got, _ := runEase(t, 1.5, 0)
	if got != 0 {
		t.Fatalf("easing down to zero settled on %v", got)
	}
}

// No frame may move past the real value, in either direction.
func TestCostEaseNeverOvershoots(t *testing.T) {
	for shown := 0.0; shown <= 3; shown += 0.0137 {
		for target := 0.0; target <= 3; target += 0.0291 {
			next := easeCost(shown, target)
			lo, hi := math.Min(shown, target), math.Max(shown, target)
			if next < lo-1e-12 || next > hi+1e-12 {
				t.Fatalf("easeCost(%v, %v) = %v, outside [%v, %v]", shown, target, next, lo, hi)
			}
		}
	}
}

// While the gap is a cent or more, each frame moves by whole cents, so the figure
// ticks rather than moving in fractions of a cent. The final frame may land on
// the real value, which can carry a sub-cent part.
func TestCostEaseStepsAreWholeCents(t *testing.T) {
	for _, c := range []struct{ shown, target float64 }{{0, 1.234}, {0.05, 0.9}, {2, 0.37}, {0.6302, 0.6402}} {
		next := easeCost(c.shown, c.target)
		if next == c.target {
			continue
		}
		cents := (next - c.shown) * 100
		if math.Abs(cents-math.Round(cents)) > 1e-6 {
			t.Errorf("easeCost(%v, %v) = %v, a step of %v cents", c.shown, c.target, next, cents)
		}
	}
}

// The first frame closes 35% of the gap, rounded to a cent.
func TestCostEaseClosesThirtyFivePercentPerFrame(t *testing.T) {
	if got := easeCost(0, 1.0); got != 0.35 {
		t.Fatalf("first frame from 0 toward 1.00 = %v, want 0.35", got)
	}
	// 35% of the remaining 0.65 is 0.2275, which rounds to 0.23 cents.
	if got := easeCost(0.35, 1.0); math.Abs(got-0.58) > 1e-9 {
		t.Fatalf("second frame = %v, want 0.58", got)
	}
}

// Within a cent the easing snaps to the real value rather than stalling a
// fraction of a cent short of it.
func TestCostEaseSnapsWithinACent(t *testing.T) {
	if got := easeCost(0.10, 0.105); got != 0.105 {
		t.Fatalf("within a cent: got %v, want the real 0.105", got)
	}
	if got := easeCost(1.2345, 1.23); got != 1.23 {
		t.Fatalf("within a cent (down): got %v, want 1.23", got)
	}
}

// A gap of exactly one cent must still move, or the figure would stop one cent
// short: 35% of a cent rounds to nothing, so the step is forced to a cent.
func TestCostEaseNeverStallsAOneCentGap(t *testing.T) {
	if got := easeCost(0, 0.01); got != 0.01 {
		t.Fatalf("one-cent gap: got %v, want 0.01", got)
	}
	if got := easeCost(0.5, 0.52); got == 0.5 || got > 0.52 {
		t.Fatalf("two-cent gap: got %v, want a step up to at most 0.52", got)
	}
}

// Rendered text follows the real value once easing has settled, so the header
// and the cost figure agree.
func TestCostSegmentAfterEasingShowsRealValue(t *testing.T) {
	settled, _ := runEase(t, 0, 0.123)
	if got := formatUSD(settled, 3); got != "$0.123" {
		t.Fatalf("settled figure formats as %q, want $0.123", got)
	}
}

// Each tip added for spec §9 names a key or command that the code handles.
// The test sends the key to the global handler: a key it does not know returns
// handled=false.
func TestNewFooterTipKeysAreHandled(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	keys := map[string]tea.KeyMsg{
		"Esc":    {Type: tea.KeyEsc},
		"Ctrl+X": {Type: tea.KeyCtrlX},
		"Ctrl+L": {Type: tea.KeyCtrlL},
		"Ctrl+S": {Type: tea.KeyCtrlS},
		"Ctrl+Y": {Type: tea.KeyCtrlY},
		"Ctrl+W": {Type: tea.KeyCtrlW},
		"Alt+1":  {Type: tea.KeyRunes, Runes: []rune{'1'}, Alt: true},
		"Alt+5":  {Type: tea.KeyRunes, Runes: []rune{'5'}, Alt: true},
	}
	for name, km := range keys {
		if _, handled := m.handleGlobalKey(km); !handled {
			t.Errorf("%s is named in a footer tip but the global handler does not take it", name)
		}
	}
}

// The six tips added for spec §9 are present, and the bag still covers them all.
func TestFooterTipsIncludeTheSpecCount(t *testing.T) {
	if len(tips) != 33 {
		t.Fatalf("footer has %d tips, spec 01 §9 asks for 33 base hints", len(tips))
	}
	for _, want := range []string{"Esc or Ctrl+X stops", "Ctrl+L opens the model picker", "Alt+1 to Alt+5", "Ctrl+W closes"} {
		found := false
		for _, tip := range tips {
			if strings.Contains(tip, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no footer tip mentions %q", want)
		}
	}
	seen := map[int]bool{}
	for _, i := range tipOrder(5) {
		seen[i] = true
	}
	if len(seen) != len(tips) {
		t.Fatalf("tip bag covers %d of %d tips", len(seen), len(tips))
	}
}
