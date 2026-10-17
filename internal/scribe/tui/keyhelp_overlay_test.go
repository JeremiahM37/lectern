package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// At 80x30 the help box must clear every row it covers: nothing from the
// screen behind may show past its right border.
func TestHelpOverlayClearsRowsAt80x30(t *testing.T) {
	m, _ := newTestModel(t, 80, 30)
	m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	rows := strings.Split(screen(m), "\n")
	top, bottom := -1, -1
	for i, l := range rows {
		if strings.Contains(l, "╭") && top < 0 {
			top = i
		}
		if strings.Contains(l, "╰") {
			bottom = i
		}
	}
	if top < 0 || bottom <= top {
		t.Fatalf("help box not found:\n%s", strings.Join(rows, "\n"))
	}
	for i := top + 1; i < bottom; i++ {
		l := rows[i]
		idx := strings.LastIndexAny(l, "│╮╯")
		if idx < 0 {
			t.Fatalf("row %d has no right border: %q", i, l)
		}
		if tail := ansi.Strip(l[idx+len("│"):]); strings.TrimSpace(tail) != "" {
			t.Errorf("row %d shows background past the border: %q", i, tail)
		}
	}
}

// The info view is at least the 80-column screen minus the blanked margins,
// so a narrow terminal never leaves a sliver of the screen beside it.
func TestInfoViewLeavesNoSliverAtNarrowWidths(t *testing.T) {
	for _, cols := range []int{40, 60, 80, 100} {
		v := NewInfoView(theme.Default(), "T", "", []string{"x"})
		w, _ := v.Size(cols, 30)
		x := (cols - w) / 2
		if left, right := x, cols-x-w; left > overlayMinMargin && right > overlayMinMargin && cols <= 80 {
			t.Errorf("cols %d: box %d wide leaves margins %d and %d", cols, w, left, right)
		}
	}
}
