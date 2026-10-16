package tui

import (
	"strings"
	"testing"
)

func TestTabKeyCyclesTabsWhenInputIsIdle(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	send(m, "ctrl+t")
	typeText(m, "review")
	send(m, "enter")
	if m.active != 1 {
		t.Fatalf("second tab not active: %d", m.active)
	}
	send(m, "tab")
	if m.active != 0 {
		t.Errorf("tab should cycle to the first tab, active=%d", m.active)
	}
	send(m, "tab")
	if m.active != 1 {
		t.Errorf("tab should wrap to the second tab, active=%d", m.active)
	}
}

func TestTabKeyLeftToCompletionWhenPopupOpen(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	send(m, "ctrl+t")
	typeText(m, "review")
	send(m, "enter")
	send(m, "alt+1")
	typeText(m, "/")
	if m.cur().in.popupKind() == "" {
		t.Skip("no slash popup in this build; the completion case is not reachable")
	}
	send(m, "tab")
	if m.active != 0 {
		t.Errorf("tab switched tabs while a completion was open (active=%d)", m.active)
	}
}

func TestTabKeyWithOneTabIsNotSwallowed(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	send(m, "tab")
	if m.active != 0 || len(m.tabs) != 1 {
		t.Errorf("single tab: active=%d tabs=%d", m.active, len(m.tabs))
	}
}

func TestHelpListsCheckpointRailAndTabs(t *testing.T) {
	text := strings.Join(keyHelpLines(), "\n")
	for _, want := range []string{
		"Alt+C",
		"checkpoint rail",
		"Click a marker",
		"Tab                 accept a completion, or next tab",
		"Alt+1..5 switch",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("key help is missing %q", want)
		}
	}
}

func TestHelpScreenShowsAltC(t *testing.T) {
	m, _ := newTestModel(t, 100, 60)
	settle(m)
	m.openHelp()
	// The list is longer than the box; page down until the checkpoint block shows.
	for i := 0; i < 4 && !strings.Contains(screen(m), "Alt+C"); i++ {
		send(m, "pgdown")
	}
	if !strings.Contains(screen(m), "Alt+C") {
		t.Errorf("help screen does not show Alt+C:\n%s", screen(m))
	}
}

func TestKeyHelpLinesFitTheBoxAtEightyColumns(t *testing.T) {
	for _, line := range keyHelpLines() {
		if n := len([]rune(line)); n > keyHelpMaxWidth {
			t.Errorf("help line is %d columns, wraps in the 80-column box: %q", n, line)
		}
	}
}
