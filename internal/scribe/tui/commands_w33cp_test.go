package tui

import (
	"strings"
	"testing"
)

// /pruning with no argument shows the current target and the choices.
func TestPruningWithNoArgumentShowsCurrentAndChoices(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	fb.flat.ContextManagement.PruningTarget = "subagents"
	f := flashOf(t, pruningCommand(m, ""))
	if f.kind != "info" || !strings.Contains(f.text, "Tool result pruning: subagents") || !strings.Contains(f.text, "none, main, subagents, both") {
		t.Fatalf("status should show subagents and the choices, got %q (%s)", f.text, f.kind)
	}
}

// The default reads as none before anything is set.
func TestPruningDefaultReadsAsNone(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	f := flashOf(t, pruningCommand(m, ""))
	if !strings.Contains(f.text, "Tool result pruning: none") {
		t.Fatalf("default should read none, got %q", f.text)
	}
}

// Setting a target saves it under context_management.pruning_target and
// confirms; the match ignores case.
func TestPruningSetsTheTarget(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	f := flashOf(t, pruningCommand(m, "Main"))
	if fb.flat.ContextManagement.Pruning() != "main" || f.kind != "ok" {
		t.Fatalf("/pruning Main should save main and confirm, got %q (%s)", fb.flat.ContextManagement.Pruning(), f.kind)
	}
	if !strings.Contains(f.text, "Tool result pruning set to main") {
		t.Fatalf("confirmation text: %q", f.text)
	}
}

// none clears the stored value, as /speed off does.
func TestPruningNoneClearsTheValue(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	pruningCommand(m, "both")
	pruningCommand(m, "none")
	if fb.flat.ContextManagement.PruningTarget != "" {
		t.Fatalf("/pruning none should store the empty value, got %q", fb.flat.ContextManagement.PruningTarget)
	}
}

// A bad choice lists the valid ones and leaves the setting alone.
func TestPruningRejectsUnknownChoice(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	pruningCommand(m, "main")
	f := flashOf(t, pruningCommand(m, "turbo"))
	if f.kind != "err" || !strings.Contains(f.text, "Valid: none, main, subagents, both") {
		t.Fatalf("a bad target should list the valid choices, got %q (%s)", f.text, f.kind)
	}
	if fb.flat.ContextManagement.Pruning() != "main" {
		t.Fatalf("a bad target changed the setting to %q", fb.flat.ContextManagement.Pruning())
	}
}

// /pruning is registered as a command.
func TestPruningIsRegistered(t *testing.T) {
	if c, _ := findCommand("/pruning"); c == nil {
		t.Fatal("/pruning should be registered")
	}
}
