package tui

import (
	"strings"
	"testing"
)

func TestContextPanelShowsBreakdownAndBudget(t *testing.T) {
	m, _ := newTestModel(t, 120, 40)
	lines := strings.Join(contextLines(m.cur()), "\n")
	for _, want := range []string{"What fills it", "Tool definitions", "5.1k", "184k of 1.0M daily budget (18%)", "/compact"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("panel lacks %q:\n%s", want, lines)
		}
	}
	if got := shareBar(1, 1000, 20); got != "█"+strings.Repeat("░", 19) {
		t.Fatalf("a tiny share still shows one cell: %q", got)
	}
}
