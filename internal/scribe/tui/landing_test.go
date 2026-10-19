package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The wordmark is six rows of box and block characters, and every row of it
// is the same width, so it fits 80 columns and its letters stay aligned.
func TestWordmarkFitsEightyColumns(t *testing.T) {
	rows := wordmarkRows()
	if len(rows) != 6 {
		t.Fatalf("wordmark has %d rows, want 6", len(rows))
	}
	w := ansi.StringWidth(rows[0])
	for i, r := range rows {
		if got := ansi.StringWidth(r); got != w {
			t.Fatalf("row %d is %d cells, row 0 is %d: the letters do not line up", i, got, w)
		}
	}
	if w > 80 {
		t.Fatalf("wordmark is %d cells wide, it must fit 80 columns", w)
	}
	if w != wordmarkWidth() || w != landingWordmarkCols {
		t.Fatalf("wordmark width %d, wordmarkWidth %d, landingWordmarkCols %d: they must agree", w, wordmarkWidth(), landingWordmarkCols)
	}
	for _, r := range strings.Join(rows, "") {
		if r != ' ' && !strings.ContainsRune("█╗╔═║╚╝", r) {
			t.Fatalf("wordmark uses %q, which is not a box character", r)
		}
	}
}

// The landing page draws the block wordmark only when the terminal is at least
// 71 columns wide and 30 rows tall; otherwise it draws the small logo.
func TestLandingFallsBackUnderThirtyRows(t *testing.T) {
	first := wordmarkRows()[0]
	cases := []struct {
		name      string
		cols, row int
		wordmark  bool
	}{
		{"80x30 shows the wordmark", 80, 30, true},
		{"80x29 falls back under 30 rows", 80, 29, false},
		{"70x40 falls back when narrower than the wordmark", 70, 40, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, _ := newTestModel(t, c.cols, c.row)
			m.phase = phaseLanding
			out := ansi.Strip(strings.Join(m.viewLanding(c.cols, m.landingRows()), "\n"))
			if got := strings.Contains(out, first); got != c.wordmark {
				t.Fatalf("wordmark drawn = %v, want %v", got, c.wordmark)
			}
			if !c.wordmark && !strings.Contains(out, "LECTERN") {
				t.Fatal("the fallback should be the small logo")
			}
		})
	}
}
