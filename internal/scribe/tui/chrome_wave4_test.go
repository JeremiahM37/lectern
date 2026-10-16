package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestModelSegmentNamesTheProviderWhenItDiffers(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.cur().model = "anthropic/claude-sonnet-5"
	if got := ansi.Strip(m.modelSegment(m.cur(), 0)); !strings.Contains(got, "anthropic›claude-sonnet-5") {
		t.Fatalf("model segment = %q, want the provider before the model", got)
	}
	m.cur().model = "ollama/ollama"
	if got := ansi.Strip(m.modelSegment(m.cur(), 0)); strings.Contains(got, "ollama›") {
		t.Fatalf("provider should not repeat when it equals the model: %q", got)
	}
}

func TestTabBarBadgesAModelOtherThanTheStartingOne(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.addTab(NewFakeBackend(), "")
	second := m.tabs[len(m.tabs)-1]
	second.defModel = "anthropic/claude-sonnet-5"
	second.model = "openai/gpt-6"
	if out := screen(m); !strings.Contains(out, "[gpt-6]") {
		t.Fatalf("tab bar should badge the model of a tab that changed it:\n%s", out)
	}
	second.model = second.defModel
	if out := screen(m); strings.Contains(out, "[claude-sonnet-5]") {
		t.Fatalf("tab bar should not badge the starting model:\n%s", out)
	}
}

func TestFooterLabelsFollowTheWidth(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	if out := screen(m); !strings.Contains(out, "Editor") || !strings.Contains(out, "^E") {
		t.Fatalf("120 columns should show the editor shortcut with its label:\n%s", out)
	}
	m, _ = newTestModel(t, 40, 30)
	out := screen(m)
	if strings.Contains(out, "Palette") {
		t.Fatalf("40 columns should drop the full labels:\n%s", out)
	}
	if !strings.Contains(out, "^K") {
		t.Fatalf("40 columns should still show the palette key:\n%s", out)
	}
}

func TestFooterTierThreeCarriesTheRailKey(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	if got := ansi.Strip(m.footerKeys(3, footerModes[0])); !strings.Contains(got, "Alt+C Rail") {
		t.Fatalf("tier 3 footer = %q, want the checkpoint rail key", got)
	}
	if got := ansi.Strip(m.footerKeys(2, footerModes[0])); strings.Contains(got, "Alt+C") {
		t.Fatalf("tier 2 should not carry the rail key yet: %q", got)
	}
	found := false
	for _, tip := range tips {
		if strings.Contains(tip, "Alt+C") {
			found = true
		}
	}
	if !found {
		t.Fatal("no footer tip names Alt+C")
	}
}

func TestTipBagIsAPermutationAndRotatesEveryTenSeconds(t *testing.T) {
	order := tipOrder(3)
	seen := make(map[int]bool)
	for _, i := range order {
		seen[i] = true
	}
	if len(order) != len(tips) || len(seen) != len(tips) {
		t.Fatalf("tip bag is not a permutation of %d tips: %v", len(tips), order)
	}
	m, _ := newTestModel(t, 100, 30)
	m.frame = 0
	o0, p0 := m.tipSlot()
	m.frame = tipTicks - 1
	o1, p1 := m.tipSlot()
	if p0 != p1 || len(o0) != len(o1) {
		t.Fatalf("tip changed before 10 s: slot %d then %d", p0, p1)
	}
	m.frame = tipTicks
	_, p2 := m.tipSlot()
	if p2 == p0 {
		t.Fatalf("tip did not change at 10 s")
	}
}

func TestGlitchKeepsWidthAndHidesTheText(t *testing.T) {
	in := "Ctrl+R searches your prompt history"
	out := glitchText(in, 2)
	if ansi.StringWidth(out) != ansi.StringWidth(in) {
		t.Fatalf("glitch changed the width: %q", out)
	}
	if strings.ContainsAny(out, "Ctrl") {
		t.Fatalf("glitch should not show the letters: %q", out)
	}
	if !strings.Contains(out, " ") {
		t.Fatalf("glitch should keep the spaces between words: %q", out)
	}
}

func TestKeyWordsAreRecognisedAndTipTextIsUnchanged(t *testing.T) {
	for _, w := range []string{"Ctrl+R", "Alt+S", "Shift+Enter", "PgUp/PgDn", "^B/^F", "/checkpoints", "!command"} {
		if !isKeyWord(w) {
			t.Errorf("%q should be highlighted as a key or command", w)
		}
	}
	for _, w := range []string{"the", "a", "/", "Scribe"} {
		if isKeyWord(w) {
			t.Errorf("%q should not be highlighted", w)
		}
	}
	m, _ := newTestModel(t, 100, 30)
	tip := tips[0]
	if got := ansi.Strip(renderTip(tip, m.th)); got != tip {
		t.Fatalf("highlighting changed the text: %q != %q", got, tip)
	}
}

func TestFreeWhenTokensUsedAndNothingBilled(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	tb := m.cur()
	tb.model = "openrouter/free-model"
	tb.local = false
	tb.cost = 0
	tb.tokens = 0
	if got := ansi.Strip(m.costSegment(tb)); got != "$0" {
		t.Fatalf("no tokens yet: %q, want $0", got)
	}
	tb.tokens = 120
	if got := ansi.Strip(m.costSegment(tb)); got != "FREE" {
		t.Fatalf("tokens used and no charge: %q, want FREE", got)
	}
	tb.cost = 1.5
	if got := ansi.Strip(m.costSegment(tb)); got != "$1.50" {
		t.Fatalf("billed turn: %q, want $1.50", got)
	}
}

func TestCompactingShowsInTheHeader(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.cur().compacting = true
	if out := screen(m); !strings.Contains(out, "compacting") {
		t.Fatalf("header should show the compacting notice:\n%s", out)
	}
	m.cur().compacting = false
	if out := screen(m); strings.Contains(out, "compacting") {
		t.Fatalf("notice should clear when compaction ends:\n%s", out)
	}
}

func TestSteeringNotesExtraLines(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.cur().steerQ = []string{"first\nsecond\nthird"}
	lines := m.steerLines(m.cur())
	if len(lines) != 1 || !strings.Contains(ansi.Strip(lines[0]), "(+2 lines)") {
		t.Fatalf("steering line = %q, want the (+2 lines) suffix", lines)
	}
}

func TestHeaderNeverOverflowsItsWidth(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	tb := m.cur()
	tb.model = "anthropic/claude-sonnet-5"
	tb.ctxWin, tb.ctxUsed, tb.ctxShown = 200000, 88000, 44
	tb.git.Branch = "feature/parser"
	tb.cost = 0.021
	for w := 50; w <= 140; w++ {
		m.width = w
		if got := ansi.StringWidth(m.viewHeader()); got > w {
			t.Fatalf("width %d: header is %d cells", w, got)
		}
	}
}
