package tui

import (
	"strings"
	"testing"
)

func TestContextClearMemoryResetsRecall(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	got := runFlash(t, m, m.runCommand("/context clear memory"))
	if !strings.Contains(got, "Memory recall reset") {
		t.Fatalf("flash = %q", got)
	}
	if ops := fb.Ops(); len(ops) != 1 || ops[0] != "clear-context memory" {
		t.Fatalf("ops = %v", ops)
	}
}

func TestContextResetClearsBothByDefault(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	got := runFlash(t, m, m.runCommand("/context reset"))
	if !strings.Contains(got, "conversation is kept") {
		t.Fatalf("flash = %q", got)
	}
	if ops := fb.Ops(); len(ops) != 1 || ops[0] != "clear-context " {
		t.Fatalf("ops = %v", ops)
	}
}

func TestContextResetWithTargetClearsOnlyThatTarget(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	runFlash(t, m, m.runCommand("/context reset skills"))
	if ops := fb.Ops(); len(ops) != 1 || ops[0] != "clear-context skills" {
		t.Fatalf("ops = %v", ops)
	}
}

func TestContextTargetsAcceptNearMissWords(t *testing.T) {
	cases := map[string]string{
		"/context clear Memories": "clear-context memory",
		"/context clear SKILL":    "clear-context skills",
		"/context reset all":      "clear-context ",
		"/context clear mem":      "clear-context memory",
	}
	for in, want := range cases {
		m, fb := newTestModel(t, 100, 40)
		runFlash(t, m, m.runCommand(in))
		if ops := fb.Ops(); len(ops) != 1 || ops[0] != want {
			t.Errorf("%s: ops = %v, want %q", in, ops, want)
		}
	}
}

func TestContextUnknownTargetSaysWhatToDo(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	got := runFlash(t, m, m.runCommand("/context reset history"))
	if !strings.Contains(got, `Unknown target "history"`) || !strings.Contains(got, "memory, skills") {
		t.Fatalf("flash = %q, want the unknown word and the valid targets", got)
	}
	if len(fb.Ops()) != 0 {
		t.Fatalf("an unknown target touched the backend: %v", fb.Ops())
	}
}

func TestContextTooManyWordsIsAUsageError(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	got := runFlash(t, m, m.runCommand("/context clear memory skills"))
	if !strings.Contains(got, "Usage: /context clear") {
		t.Fatalf("flash = %q", got)
	}
	if len(fb.Ops()) != 0 {
		t.Fatalf("a usage error touched the backend: %v", fb.Ops())
	}
}

func TestContextWithoutArgsStillOpensTheDashboard(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	m.runCommand("/context")
	if len(fb.Ops()) != 0 {
		t.Fatalf("/context with no argument changed context: %v", fb.Ops())
	}
}
