package tui

import (
	"strings"
	"testing"
)

// /cleartools and /clearthinking write the context_management keys the
// settings screen writes, default off, and reject anything but on or off.
func TestClearToolsCommandTogglesTheKey(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	if f := flashOf(t, clearToolsCommand(m, "")); !strings.Contains(f.text, "off") {
		t.Fatalf("/cleartools with no argument should show the state, got %q", f.text)
	}
	f := flashOf(t, clearToolsCommand(m, "on"))
	if f.kind != "ok" || fb.flat.ContextManagement.ClearToolUses == nil || !*fb.flat.ContextManagement.ClearToolUses {
		t.Fatalf("/cleartools on should store true, got %v (%s)", fb.flat.ContextManagement.ClearToolUses, f.kind)
	}
	clearToolsCommand(m, "OFF")
	if p := fb.flat.ContextManagement.ClearToolUses; p == nil || *p {
		t.Fatalf("/cleartools OFF should store false, got %v", p)
	}
}

func TestClearThinkingCommandTogglesTheKey(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	f := flashOf(t, clearThinkingCommand(m, "on"))
	if f.kind != "ok" || fb.flat.ContextManagement.ClearThinking == nil || !*fb.flat.ContextManagement.ClearThinking {
		t.Fatalf("/clearthinking on should store true, got %v (%s)", fb.flat.ContextManagement.ClearThinking, f.kind)
	}
}

func TestClearCommandRejectsAnUnknownChoice(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	f := flashOf(t, clearThinkingCommand(m, "maybe"))
	if f.kind == "ok" {
		t.Fatalf("/clearthinking maybe should be refused, got %q", f.text)
	}
	if fb.flat.ContextManagement.ClearThinking != nil {
		t.Fatal("a rejected value must not be written")
	}
}

// The two commands are registered, so they show in the palette and run.
func TestClearCommandsAreRegistered(t *testing.T) {
	for _, name := range []string{"cleartools", "clearthinking"} {
		if c, _ := findCommand("/" + name); c == nil {
			t.Errorf("/%s is not registered", name)
		}
	}
}
