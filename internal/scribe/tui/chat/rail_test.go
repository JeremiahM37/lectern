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

func TestRailHiddenBelowSeventyColumns(t *testing.T) {
	useASCIIIcons(t)
	v, _ := threeTurns(t, 69, 40, 3)
	v.SetCheckpoints([]Checkpoint{{Turn: 1}, {Turn: 2}, {Turn: 3}}, -1)
	if v.railOn() || v.bodyW() != 69 {
		t.Fatalf("rail shown at 69 columns (railOn=%v bodyW=%d)", v.railOn(), v.bodyW())
	}
	checkShape(t, v)
	if got := markerRows(railCells(v), icons.Dot, icons.Icon("checkpoint")); len(got) != 0 {
		t.Errorf("rail glyphs drawn at 69 columns: %v", got)
	}
	if v.FocusRail() {
		t.Error("rail took focus at 69 columns")
	}

	v.SetSize(70, 40)
	if !v.railOn() || v.bodyW() != 70-railW {
		t.Fatalf("rail hidden at 70 columns (railOn=%v bodyW=%d)", v.railOn(), v.bodyW())
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

func TestRailHitFindsMarkerRow(t *testing.T) {
	useASCIIIcons(t)
	v, _ := threeTurns(t, 100, 40, 3)
	v.SetCheckpoints([]Checkpoint{{Turn: 1}, {Turn: 2}, {Turn: 3, Undone: true}}, -1)
	checkShape(t, v)

	out := plain(v)
	row := rowOf(out, "question 2") - 2 // the marker sits on the blank row above "You"
	idx, cp, ok := v.RailHit(1, row)
	if !ok || idx != 1 || cp.Turn != 2 {
		t.Fatalf("click on turn 2 marker: idx=%d turn=%d ok=%v\n%s", idx, cp.Turn, ok, out)
	}
	// The same row but outside the rail columns is not a marker.
	if _, _, ok := v.RailHit(railW, row); ok {
		t.Error("a click past the rail columns hit a marker")
	}
	// A row with no marker.
	if _, _, ok := v.RailHit(1, row+1); ok {
		t.Error("a click on a connector row hit a marker")
	}
	// The undone checkpoint is still reported; the caller decides what to do.
	undone := rowOf(out, "question 3") - 2
	if i, cp, ok := v.RailHit(1, undone); !ok || i != 2 || !cp.Undone {
		t.Errorf("undone marker: idx=%d ok=%v undone=%v", i, ok, cp.Undone)
	}
}

func TestRailHitNeedsRailOnScreen(t *testing.T) {
	useASCIIIcons(t)
	v, _ := threeTurns(t, 69, 40, 3)
	v.SetCheckpoints([]Checkpoint{{Turn: 1}, {Turn: 2}, {Turn: 3}}, -1)
	row := rowOf(plain(v), "question 1") - 2
	if _, _, ok := v.RailHit(1, row); ok {
		t.Error("rail hit reported at 69 columns, where the rail is hidden")
	}
}

func TestSelectRailMovesCursorWithoutFocus(t *testing.T) {
	v, _ := threeTurns(t, 100, 40, 3)
	v.SetCheckpoints([]Checkpoint{{Turn: 1}, {Turn: 2}, {Turn: 3}}, -1)
	v.SelectRail(0)
	if v.RailFocused() {
		t.Error("SelectRail should not take the keyboard")
	}
	// A click browses to a checkpoint (viewed=0), so Alt+C then starts there.
	v.SetCheckpoints([]Checkpoint{{Turn: 1}, {Turn: 2}, {Turn: 3}}, 0)
	v.FocusRail()
	if i, _, _ := v.RailSelected(); i != 0 {
		t.Errorf("focus after browsing to 0 starts on %d", i)
	}
}

func TestRailHitMatchesTheMarkerDrawn(t *testing.T) {
	useASCIIIcons(t)
	// Two checkpoints for turn 1 (a re-run) share a row: the one drawn last wins.
	v, _ := threeTurns(t, 100, 40, 2)
	v.SetCheckpoints([]Checkpoint{{Turn: 1}, {Turn: 2}, {Turn: 1}}, -1)
	checkShape(t, v)
	row := rowOf(plain(v), "question 1") - 2
	idx, _, ok := v.RailHit(1, row)
	if !ok || idx != 2 {
		t.Errorf("shared row hit checkpoint %d (ok=%v), want the one drawn last, 2", idx, ok)
	}
}
