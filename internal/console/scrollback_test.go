package console

import (
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"strings"
	"testing"
)

func TestScrollbackStartsAtBottomAndPagesToEarlierOutput(t *testing.T) {
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, fmt.Sprintf("line %03d", i))
	}
	m := newScrollback(strings.Join(lines, "\n"), 80, 24)
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 10})
	if !strings.Contains(m.View(), "line 099") || strings.Contains(m.View(), "line 000") {
		t.Fatal("initial view is not at bottom")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyHome})
	if !strings.Contains(m.View(), "line 000") {
		t.Fatal("Home did not show earliest output")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if strings.Contains(m.View(), "line 000") {
		t.Fatal("page down did not scroll")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("Escape did not close history")
	}
}
