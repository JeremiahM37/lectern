package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// visibleValues lists the picker's visible rows in order, by Value.
func visibleValues(p *Picker) []string {
	var out []string
	for _, i := range p.visible {
		out = append(out, p.Items[i].Value)
	}
	return out
}

func TestNameMatchOutranksDescriptionOnlyMatch(t *testing.T) {
	undo := PickItem{Value: "undo", Label: "/undo", Desc: "Revert the last turn"}
	jobs := PickItem{Value: "jobs", Label: "/jobs", Desc: "Background work and undoing"}
	su, ok1 := matchItem(undo, "und")
	sj, ok2 := matchItem(jobs, "und")
	if !ok1 || !ok2 {
		t.Fatalf("both should match: undo=%v jobs=%v", ok1, ok2)
	}
	if su <= sj {
		t.Fatalf("name match %d should outrank description-only match %d", su, sj)
	}
}

func TestDescriptionMatchNeedsThreeCharacters(t *testing.T) {
	jobs := PickItem{Value: "jobs", Label: "/jobs", Desc: "Background work"}
	if _, ok := matchItem(jobs, "ba"); ok {
		t.Fatal("a 2-character query must not match description text")
	}
	if _, ok := matchItem(jobs, "bac"); !ok {
		t.Fatal("a 3-character query should match description text")
	}
}

func TestGroupTextDoesNotMatchOnItsOwn(t *testing.T) {
	// Every palette row in query mode has Group "Results"; a query that is a
	// subsequence of that word must not pull them all in.
	cost := PickItem{Value: "cost", Label: "/cost", Desc: "Spend so far", Group: "Results"}
	if _, ok := matchItem(cost, "res"); ok {
		t.Fatal("group text alone must not match")
	}
	if _, ok := matchItem(cost, "cos"); !ok {
		t.Fatal("the name should still match")
	}
}

func TestNameTiersPrefixThenWordThenSubstring(t *testing.T) {
	q := "diff"
	prefix := PickItem{Value: "a", Label: "/diffstat"}
	word := PickItem{Value: "b", Label: "/git diff"}
	sub := PickItem{Value: "c", Label: "/rediffed"}
	sp, _ := matchItem(prefix, q)
	sw, _ := matchItem(word, q)
	ss, _ := matchItem(sub, q)
	if !(sp > sw && sw > ss) {
		t.Fatalf("want prefix > word start > substring, got %d, %d, %d", sp, sw, ss)
	}
}

func TestSlashPrefixMatchesWithoutSlash(t *testing.T) {
	if _, ok := matchItem(PickItem{Value: "undo", Label: "/undo"}, "und"); !ok {
		t.Fatal("und should match /undo")
	}
}

func TestPickerTypedFilterRanksNamesFirst(t *testing.T) {
	p := NewPicker(theme.Default(), "Palette", "")
	p.TypeToFilter = true
	p.SetItems([]PickItem{
		{Value: "jobs", Label: "/jobs", Desc: "Background work"},
		{Value: "cost", Label: "/cost", Desc: "Show spend and budget"},
		{Value: "undo", Label: "/undo", Desc: "Revert the last turn"},
	})
	for _, r := range "und" {
		p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	got := visibleValues(p)
	if len(got) == 0 || got[0] != "undo" {
		t.Fatalf("visible = %v, want undo first", got)
	}
	if it, ok := p.Selected(); !ok || it.Value != "undo" {
		t.Fatalf("cursor should sit on the best match, got %+v", it)
	}
}
