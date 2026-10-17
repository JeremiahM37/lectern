package tui

import (
	"strings"
	"testing"
)

// groupRows counts the palette rows in one group.
func groupRows(items []PickItem, group string) int {
	n := 0
	for _, it := range items {
		if it.Group == group {
			n++
		}
	}
	return n
}

// cursorGroup is the group of the highlighted palette row.
func cursorGroup(p *Picker) string {
	return p.Items[p.visible[p.cursor]].Group
}

func TestPaletteFoldsCursorGroupWithArrowKeys(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	m, _ := newTestModel(t, 100, 40)
	m.openPalette()
	p := topLive(t, m).Picker
	group := cursorGroup(p)
	n := groupRows(p.Items, group)
	if n < 2 {
		t.Fatalf("need a group with several rows to fold, %q has %d", group, n)
	}

	send(m, "left")
	if !p.folded[group] || len(p.visible) != len(p.Items)-n+1 {
		t.Fatalf("left should fold %q to one row: folded=%v visible=%d of %d", group, p.folded[group], len(p.visible), len(p.Items))
	}
	if got := cursorGroup(p); got != group {
		t.Fatalf("the folded row keeps the cursor in %q, cursor is in %q", group, got)
	}

	send(m, "right")
	if p.folded[group] || len(p.visible) != len(p.Items) {
		t.Fatalf("right should unfold %q: folded=%v visible=%d of %d", group, p.folded[group], len(p.visible), len(p.Items))
	}
}

func TestPaletteSpaceTogglesFoldWhenUnfiltered(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	m, _ := newTestModel(t, 100, 40)
	m.openPalette()
	p := topLive(t, m).Picker
	group := cursorGroup(p)

	send(m, " ")
	if !p.folded[group] {
		t.Fatalf("Space should fold %q when the filter is empty", group)
	}
	send(m, " ")
	if p.folded[group] {
		t.Fatalf("a second Space should unfold %q", group)
	}
}

func TestPaletteFilterKeepsSpaceAsText(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	m, _ := newTestModel(t, 100, 40)
	m.openPalette()
	typeText(m, "ver")
	p := topLive(t, m).Picker

	send(m, " ")
	if p.query != "ver " {
		t.Fatalf("Space while filtering must type into the query, query = %q", p.query)
	}
	send(m, "left")
	if len(p.folded) != 0 {
		t.Fatalf("a filtered palette must not fold groups, folded = %v", p.folded)
	}
}

func TestPaletteFooterNamesTheFoldKeys(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	m, _ := newTestModel(t, 100, 40)
	m.openPalette()
	lp := topLive(t, m)
	if !strings.Contains(lp.Footer(), "←→ fold") || !strings.Contains(lp.Footer(), "Space toggle") {
		t.Fatalf("unfiltered footer %q should name the fold keys", lp.Footer())
	}
	typeText(m, "ver")
	if strings.Contains(topLive(t, m).Footer(), "fold") {
		t.Fatalf("filtered footer %q should not offer folding", topLive(t, m).Footer())
	}
}

func TestPlaceholderKeepsSteerWhileBrowsingMidTurn(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	tb := m.cur()
	tb.cps = []CheckpointInfo{{Turn: 3}, {Turn: 7}}
	tb.browse = 1
	tb.busy = true
	// A message sent mid-turn steers that turn, so the rewind prompt would lie.
	if ph, _ := inputPlaceholder(tb, m.th); ph != "'/' for commands · or steer by sending a new message" {
		t.Fatalf("busy placeholder while browsing = %q", ph)
	}
}
