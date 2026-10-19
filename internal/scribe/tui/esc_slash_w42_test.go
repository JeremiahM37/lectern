package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// typeKeys sends each rune as a key press.
func typeKeys(m *Model, text string) {
	for _, r := range text {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// Esc with a bare command name in the box clears it, so the next command is
// not prefixed with a second slash (QA41 finding: "//model").
func TestEscClearsABareSlashCommand(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	typeKeys(m, "/mo")
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if got := m.cur().in.value(); got != "" {
		t.Fatalf("input after Esc = %q, want empty", got)
	}
}

// Esc keeps text that is more than a command name: a sentence is not lost.
func TestEscKeepsTypedSentence(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	typeKeys(m, "fix the bug in /tmp")
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if got := m.cur().in.value(); got != "fix the bug in /tmp" {
		t.Fatalf("input after Esc = %q", got)
	}
}
