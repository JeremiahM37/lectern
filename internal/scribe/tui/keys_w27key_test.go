package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Ctrl+Y copies the selection when there is one and the focused message when
// there is not (spec 06 §8.2). These cases pin that precedence.
func TestPickCtrlYTextPrefersSelection(t *testing.T) {
	text, fromSel := pickCtrlYText("picked words", true, "whole message", true)
	if text != "picked words" || !fromSel {
		t.Fatalf("selection present: got (%q, %v)", text, fromSel)
	}
}

func TestPickCtrlYTextFallsBackToFocusedMessage(t *testing.T) {
	cases := map[string]struct {
		selection    string
		hasSelection bool
	}{
		"no selection":           {"", false},
		"selection not reported": {"ignored", false},
		"blank selection":        {"   \n  ", true},
	}
	for name, c := range cases {
		text, fromSel := pickCtrlYText(c.selection, c.hasSelection, "whole message", true)
		if text != "whole message" || fromSel {
			t.Errorf("%s: got (%q, %v), want the focused message", name, text, fromSel)
		}
	}
}

func TestPickCtrlYTextNothingToCopy(t *testing.T) {
	if text, _ := pickCtrlYText("", false, "", false); text != "" {
		t.Fatalf("empty transcript and no selection copied %q", text)
	}
	if text, _ := pickCtrlYText("", false, "", true); text != "" {
		t.Fatalf("an empty focused message copied %q", text)
	}
}

// A view that cannot report a selection must read as no selection, so Ctrl+Y
// falls back to the message rather than failing.
func TestSelectedTextOfFreshViewIsEmpty(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	if text, ok := selectedTextOf(m.cur().chat); ok && text != "" {
		t.Fatalf("fresh transcript reports selection %q", text)
	}
}

func TestCtrlYCopiesFocusedMessageAndFlashesIt(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.cur().chat.AddUser("hello from the focused message", 0, time.Now(), false)
	if _, handled := m.handleGlobalKey(tea.KeyMsg{Type: tea.KeyCtrlY}); !handled {
		t.Fatal("Ctrl+Y was not handled")
	}
	if !strings.HasPrefix(m.flash, "Copied the message (") {
		t.Fatalf("flash = %q, want the message copy", m.flash)
	}
	if !strings.Contains(m.flash, "1 line)") {
		t.Fatalf("flash = %q, want a line count", m.flash)
	}
}

func TestCtrlYWithNothingToCopyFlashesNothing(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	if _, handled := m.handleGlobalKey(tea.KeyMsg{Type: tea.KeyCtrlY}); !handled {
		t.Fatal("Ctrl+Y was not handled")
	}
	if m.flash != "Nothing to copy" {
		t.Fatalf("flash = %q, want %q", m.flash, "Nothing to copy")
	}
}
