package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// altKey is Alt plus a letter, as bubbletea reports it.
func altKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: true}
}

func TestRailKeysFocusMoveAndRestore(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	settle(m)
	tb := m.cur()

	m.Update(altKey('c'))
	if !tb.chat.RailFocused() {
		t.Fatal("Alt+C did not focus the checkpoint rail")
	}
	// The cursor starts on the latest checkpoint; Up moves to the one before.
	m.Update(key("up"))
	if i, cp, ok := tb.chat.RailSelected(); !ok || cp.Turn != 2 || i != 1 {
		t.Fatalf("after Up: idx=%d turn=%d ok=%v, want turn 2", i, cp.Turn, ok)
	}

	// Enter browses to that turn and offers the restore confirm.
	m.Update(key("enter"))
	if tb.chat.RailFocused() {
		t.Error("rail should give up focus after Enter")
	}
	if tb.browse != 1 {
		t.Errorf("browse index %d, want 1", tb.browse)
	}
	if !strings.Contains(screen(m), "Restore to turn 2?") {
		t.Errorf("restore confirm not offered:\n%s", screen(m))
	}
}

func TestRailEscReturnsKeyboard(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	settle(m)
	m.Update(altKey('c'))
	m.Update(key("esc"))
	if m.cur().chat.RailFocused() {
		t.Error("Esc should leave the rail")
	}
}

func TestRailOtherKeysGoToInput(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	settle(m)
	m.Update(altKey('c'))
	typeText(m, "x")
	if m.cur().chat.RailFocused() {
		t.Error("typing should give the rail up")
	}
	if got := m.cur().in.value(); !strings.Contains(got, "x") {
		t.Errorf("typed key did not reach the input: %q", got)
	}
}
