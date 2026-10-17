package tui

import (
	"strings"
	"testing"
)

func TestHangWrapIndentsContinuationLines(t *testing.T) {
	out := hangWrap("  ✓ Language servers  gopls, typescript-language-server, pyright, rust-analyzer", 40, 4)
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected a wrap: %q", out)
	}
	for _, l := range lines[1:] {
		if !strings.HasPrefix(l, "    ") || len([]rune(l)) > 40 {
			t.Fatalf("continuation %q should be indented 4 and fit 40", l)
		}
	}
}

func TestFitHintDropsMiddleHintsAndKeepsClose(t *testing.T) {
	h := "↑↓ nav · Tab next category · ←→ fold · Space toggle · Enter run · Esc close"
	got := fitHint(h, 50)
	if len([]rune(got)) > 50 || !strings.HasSuffix(got, "Esc close") || !strings.HasPrefix(got, "↑↓ nav") {
		t.Fatalf("fitHint = %q", got)
	}
	if fitHint("a · b", 80) != "a · b" {
		t.Fatal("a hint that fits must not change")
	}
}
