package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestMaskRunesKeepsLastFour(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  string
	}{
		{"abc", 20, "•••"},               // short: all bullets, never the whole value
		{"abcd", 20, "••••"},             // exactly the keep length is still hidden
		{"abcdefghij", 20, "••••••ghij"}, // six hidden, four clear
		{"abcdefghij", 6, "••ghij"},      // narrow: leftmost bullets dropped, tail kept
		{"abcdefghij", 0, "ghij"},        // no room at all: only the tail
		{"", 20, ""},
	}
	for _, c := range cases {
		if got := maskRunes([]rune(c.in), c.width); got != c.want {
			t.Errorf("maskRunes(%q, %d) = %q, want %q", c.in, c.width, got, c.want)
		}
	}
}

func TestMaskedPromptShowsOnlyTheTail(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	p := NewTextPrompt(m.th, "API key for OpenAI", "paste the key", true, nil).WithValue("sk-test-1234567890")
	out := ansi.Strip(p.View(60, 8))
	if !strings.Contains(out, "7890") {
		t.Fatalf("the last four characters should show: %q", out)
	}
	if strings.Contains(out, "sk-test") || strings.Contains(out, "1234") {
		t.Fatalf("the rest of the key must stay hidden: %q", out)
	}
}

func TestUnmaskedPromptShowsEverything(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	p := NewTextPrompt(m.th, "Router", "", false, nil).WithValue("anthropic/claude")
	if out := ansi.Strip(p.View(60, 8)); !strings.Contains(out, "anthropic/claude") {
		t.Fatalf("a plain field should show its value: %q", out)
	}
}
