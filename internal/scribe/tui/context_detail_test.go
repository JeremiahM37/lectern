package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
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

func TestInfoPanelFitsItsText(t *testing.T) {
	v := NewInfoView(theme.Resolve(""), "Claims", "", []string{"one", "two", "three"})
	if _, h := v.Size(120, 45); h != 6 {
		t.Fatalf("a three-line panel should be 6 rows (minimum), got %d", h)
	}
	long := make([]string, 80)
	for i := range long {
		long[i] = "line"
	}
	if _, h := NewInfoView(theme.Resolve(""), "Log", "", long).Size(120, 45); h != 30 {
		t.Fatalf("a long panel should stop at 30 rows, got %d", h)
	}
}

func TestSecretLinesStayOutOfHistory(t *testing.T) {
	for _, line := range []string{
		"/remote token set 1234567890:AAH9xYzAbCdEfGhIjKlMnOpQrStUvWxYz012",
		"/keys anthropic sk-ant-abcdefghijklmnopqrstu",
		"please use 1234567890:AAH9xYzAbCdEfGhIjKlMnOpQrStUvWxYz012 for the bot",
	} {
		if !secretLine(line) {
			t.Errorf("%q should be kept out of history", line)
		}
	}
	for _, line := range []string{"/remote token status", "fix the bug in main.go", "/keys"} {
		if secretLine(line) {
			t.Errorf("%q is not a secret", line)
		}
	}
}

// Typing a filter puts the cursor on the best match, so Enter picks what the
// user typed, not the row the cursor was on before.
func TestPickerFilterMovesCursorToBestMatch(t *testing.T) {
	var picked string
	p := NewPicker(theme.Resolve(""), "Model", "")
	p.Items = []PickItem{{Label: "gemma4:e4b"}, {Label: "qwen3:0.6b"}, {Label: "qwen3.6:35b-a3b"}}
	p.OnSelect = func(it PickItem) tea.Cmd { picked = it.Label; return nil }
	p.refilter()
	p.TypeToFilter = true
	p.cursor = 1 // the current model (qwen3:0.6b), as the model picker opens on it
	for _, r := range "qwen3.6" {
		p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if picked != "qwen3.6:35b-a3b" {
		t.Fatalf("picked %q", picked)
	}
}
