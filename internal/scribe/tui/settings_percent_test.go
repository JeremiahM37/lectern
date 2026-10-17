package tui

import "testing"

// The Compact-at row shows a percentage, so its editor must take one.
func TestParsePercentMatchesTheRowUnits(t *testing.T) {
	for in, want := range map[string]float64{"80": 0.8, "80%": 0.8, " 75 % ": 0.75, "0.8": 0.8} {
		if got, ok := parsePercent(in); !ok || got < want-1e-9 || got > want+1e-9 {
			t.Errorf("parsePercent(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	if _, ok := parsePercent("lots"); ok {
		t.Error("non-number accepted")
	}
}
