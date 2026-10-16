package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestOverlayBlanksNarrowMargins(t *testing.T) {
	base := make([]string, 5)
	for i := range base {
		base[i] = strings.Repeat("x", 20)
	}
	box := []string{"[" + strings.Repeat("-", 14) + "]", "[" + strings.Repeat(" ", 14) + "]"}
	out := overlay(base, box, 20, 5)
	// a 16-wide box on 20 columns leaves 2 columns each side: both blanked
	for _, row := range []int{1, 2} {
		got := ansi.Strip(out[row])
		if strings.Contains(got, "x") {
			t.Fatalf("row %d keeps a sliver of the screen behind: %q", row, got)
		}
		if ansi.StringWidth(out[row]) != 20 {
			t.Fatalf("row %d is %d wide", row, ansi.StringWidth(out[row]))
		}
	}
	if got := ansi.Strip(out[0]); got != strings.Repeat("x", 20) {
		t.Fatalf("rows above the box are untouched, got %q", got)
	}
}

func TestOverlayKeepsWideMarginsWithAGutter(t *testing.T) {
	base := make([]string, 3)
	for i := range base {
		base[i] = strings.Repeat("x", 40)
	}
	box := []string{"[" + strings.Repeat("-", 8) + "]"}
	out := overlay(base, box, 40, 3)
	got := ansi.Strip(out[1])
	// box at x=15..24; one blank column each side, the rest of the screen stays
	want := strings.Repeat("x", 14) + " " + "[--------]" + " " + strings.Repeat("x", 14)
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}
