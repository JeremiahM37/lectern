package chat

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
)

// threeTurns builds a transcript with n finished user turns, "question N"
// and "answer N" each.
func threeTurns(t *testing.T, w, h, n int) (*View, *clock) {
	t.Helper()
	v, c := newView(t, Options{})
	v.SetSize(w, h)
	for i := 1; i <= n; i++ {
		v.AddUser(fmt.Sprintf("question %d", i), 0, at(c), false)
		v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
		text(v, fmt.Sprintf("answer %d", i), c)
		finish(v, c, time.Second)
	}
	return v, c
}

// railCells returns the first railW cells of each row, as plain text.
func railCells(v *View) []string {
	lines := strings.Split(plain(v), "\n")
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Truncate(l, railW, "")
	}
	return out
}

// markerRows returns the rows whose rail cell holds one of glyphs, with the glyph.
func markerRows(cells []string, glyphs ...string) map[int]string {
	got := map[int]string{}
	for r, c := range cells {
		for _, g := range glyphs {
			if strings.Contains(c, g) {
				got[r] = g
			}
		}
	}
	return got
}

// useASCIIIcons makes the icon set deterministic for a test.
func useASCIIIcons(t *testing.T) {
	t.Helper()
	icons.SetNerd(false)
	t.Cleanup(func() { icons.SetNerd(false) })
}

func TestRailMarkersAlignWithTurns(t *testing.T) {
	useASCIIIcons(t)
	v, _ := threeTurns(t, 100, 40, 3)
	v.SetCheckpoints([]Checkpoint{{Turn: 1}, {Turn: 2}, {Turn: 3}}, -1)
	checkShape(t, v)

	out := plain(v)
	cells := railCells(v)
	marks := markerRows(cells, icons.Dot, icons.Icon("checkpoint"))
	if len(marks) != 3 {
		t.Fatalf("want 3 markers, got %d: %v\n%s", len(marks), marks, out)
	}
	// Each marker sits on the blank row above its turn's "You" header.
	for turn := 1; turn <= 3; turn++ {
		you := rowOf(out, fmt.Sprintf("question %d", turn)) - 2
		if _, ok := marks[you]; !ok {
			t.Errorf("turn %d: no marker on row %d; markers at %v\n%s", turn, you, marks, out)
		}
	}
	// Normal, normal, then the latest active one as the diamond.
	last := rowOf(out, "question 3") - 2
	if g := marks[last]; g != icons.Icon("checkpoint") {
		t.Errorf("latest marker = %q, want the checkpoint glyph", g)
	}
	// A faint line joins the markers.
	first := rowOf(out, "question 1") - 2
	if c := cells[first+1]; !strings.Contains(c, "│") {
		t.Errorf("no connector between markers at row %d: %q", first+1, c)
	}
}

func TestRailUndoMarksLastUndone(t *testing.T) {
	useASCIIIcons(t)
	v, _ := threeTurns(t, 100, 40, 3)
	v.SetCheckpoints([]Checkpoint{{Turn: 1}, {Turn: 2}, {Turn: 3, Undone: true}}, -1)
	checkShape(t, v)

	out := plain(v)
	cells := railCells(v)
	undone := rowOf(out, "question 3") - 2
	if g := markerRows(cells, icons.Ring)[undone]; g != icons.Ring {
		t.Errorf("undone marker on row %d = %q, want %q", undone, cells[undone], icons.Ring)
	}
	latest := rowOf(out, "question 2") - 2
	if g := markerRows(cells, icons.Icon("checkpoint"))[latest]; g == "" {
		t.Errorf("turn 2 should be the latest active marker; row %d = %q", latest, cells[latest])
	}
}

func TestRailHiddenBelowEightyColumns(t *testing.T) {
	useASCIIIcons(t)
	v, _ := threeTurns(t, 79, 40, 3)
	v.SetCheckpoints([]Checkpoint{{Turn: 1}, {Turn: 2}, {Turn: 3}}, -1)
	if v.railOn() || v.bodyW() != 79 {
		t.Fatalf("rail shown at 79 columns (railOn=%v bodyW=%d)", v.railOn(), v.bodyW())
	}
	checkShape(t, v)
	if got := markerRows(railCells(v), icons.Dot, icons.Icon("checkpoint")); len(got) != 0 {
		t.Errorf("rail glyphs drawn at 79 columns: %v", got)
	}
	if v.FocusRail() {
		t.Error("rail took focus at 79 columns")
	}

	v.SetSize(80, 40)
	if !v.railOn() || v.bodyW() != 80-railW {
		t.Fatalf("rail hidden at 80 columns (railOn=%v bodyW=%d)", v.railOn(), v.bodyW())
	}
	checkShape(t, v)
}

func TestRailFocusMovesAndSelects(t *testing.T) {
	useASCIIIcons(t)
	v, _ := threeTurns(t, 100, 40, 3)
	v.SetCheckpoints([]Checkpoint{{Turn: 1}, {Turn: 2}, {Turn: 3}}, -1)

	if !v.FocusRail() {
		t.Fatal("rail did not take focus")
	}
	if i, cp, ok := v.RailSelected(); !ok || i != 2 || cp.Turn != 3 {
		t.Fatalf("focus starts on latest: got idx=%d turn=%d ok=%v", i, cp.Turn, ok)
	}
	v.RailMove(-1)
	v.RailMove(-1)
	v.RailMove(-1) // clamps at the first checkpoint
	if i, cp, _ := v.RailSelected(); i != 0 || cp.Turn != 1 {
		t.Fatalf("after moving up: idx=%d turn=%d, want 0/1", i, cp.Turn)
	}
	cells := railCells(v)
	first := rowOf(plain(v), "question 1") - 2
	if !strings.HasPrefix(cells[first], icons.Icon("arrow")) {
		t.Errorf("caret not on the selected marker row %d: %q", first, cells[first])
	}
	v.BlurRail()
	if v.RailFocused() {
		t.Error("rail still focused after blur")
	}
	if _, _, ok := v.RailSelected(); ok {
		t.Error("RailSelected reports a selection with no focus")
	}
}

func TestRailViewedCheckpointIsWarningDot(t *testing.T) {
	useASCIIIcons(t)
	v, _ := threeTurns(t, 100, 40, 3)
	v.SetCheckpoints([]Checkpoint{{Turn: 1}, {Turn: 2}, {Turn: 3}}, 0)
	out := plain(v)
	cells := railCells(v)
	viewed := rowOf(out, "question 1") - 2
	if g := markerRows(cells, icons.Dot)[viewed]; g != icons.Dot {
		t.Errorf("browsed checkpoint row %d = %q, want %q", viewed, cells[viewed], icons.Dot)
	}
	latest := rowOf(out, "question 3") - 2
	if g := markerRows(cells, icons.Icon("checkpoint"))[latest]; g == "" {
		t.Errorf("latest should still show the diamond while browsing an older turn: %q", cells[latest])
	}
}

func TestRailJumpScrollsToTurn(t *testing.T) {
	useASCIIIcons(t)
	v, _ := threeTurns(t, 100, 6, 6)
	v.SetCheckpoints([]Checkpoint{{Turn: 1}, {Turn: 2}, {Turn: 3}, {Turn: 4}, {Turn: 5}, {Turn: 6}}, -1)
	v.FocusRail()
	v.RailMove(-1)
	v.RailMove(-1)
	v.RailMove(-1)
	_, cp, _ := v.RailSelected()
	if cp.Turn != 3 {
		t.Fatalf("selected turn %d, want 3", cp.Turn)
	}
	out := plain(v)
	if !strings.Contains(strings.Join(strings.Split(out, "\n")[:3], "\n"), "question 3") {
		t.Errorf("chat did not scroll to turn 3:\n%s", out)
	}
	checkShape(t, v)
}

func TestRailRunningTurnShowsSpinner(t *testing.T) {
	useASCIIIcons(t)
	v, c := threeTurns(t, 100, 40, 2)
	v.AddUser("question 3", 0, at(c), false)
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	text(v, "working", c)
	v.SetCheckpoints([]Checkpoint{{Turn: 1}, {Turn: 2}, {Turn: 3}}, -1)
	cells := railCells(v)
	running := rowOf(plain(v), "question 3") - 2
	if strings.ContainsAny(cells[running], icons.Dot+icons.Ring+icons.Icon("checkpoint")) {
		t.Errorf("running turn should show a spinner, got %q", cells[running])
	}
	if strings.TrimSpace(cells[running]) == "" {
		t.Errorf("no spinner on row %d", running)
	}
}
