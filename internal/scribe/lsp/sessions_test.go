package lsp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSessionsReportsStartingAndFailed(t *testing.T) {
	m := NewManager(t.TempDir())
	if got := m.Sessions(); len(got) != 0 {
		t.Fatalf("fresh manager: Sessions = %v, want none", got)
	}
	spec := serverSpecs["go"]
	m.noteStarting("go\x00/a", spec, "/a")
	m.noteFailed("go\x00/b", spec, "/b", errors.New("gopls exploded"))
	m.noteStarting("rust\x00/r", serverSpecs["rust"], "/r")

	got := m.Sessions()
	if len(got) != 3 {
		t.Fatalf("Sessions = %d entries, want 3: %+v", len(got), got)
	}
	// Sorted by language, then root.
	if got[0].Language != "go" || got[0].Root != "/a" || got[0].State != StateStarting {
		t.Errorf("entry 0 = %+v", got[0])
	}
	if got[1].Root != "/b" || got[1].State != StateFailed || got[1].LastError != "gopls exploded" {
		t.Errorf("entry 1 = %+v", got[1])
	}
	if got[2].Language != "rust" {
		t.Errorf("entry 2 = %+v", got[2])
	}
}

func TestSessionsFailedStartIsListed(t *testing.T) {
	// With no gopls reachable the start fails, and the failure is listed.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Hide every server: an empty PATH and HOME mean no gopls, even if this
	// machine has one.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	m := NewManager(dir)
	defer m.Shutdown()
	abs := filepath.Join(dir, "main.go")
	if _, err := m.sessionFor(context.Background(), abs); err == nil {
		t.Fatal("expected a start failure with no gopls installed")
	}
	got := m.Sessions()
	if len(got) != 1 || got[0].State != StateFailed || got[0].LastError == "" {
		t.Fatalf("Sessions = %+v, want one failed go session with an error", got)
	}
	if got[0].PID != 0 || got[0].OpenDocs != 0 {
		t.Errorf("failed session reports a process: %+v", got[0])
	}
}

func TestSessionsRunningThenClearedByShutdown(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := findServer(serverSpecs["go"], dir); !ok {
		t.Skip("no gopls on this machine")
	}
	m := NewManager(dir)
	abs := filepath.Join(dir, "main.go")
	if err := os.WriteFile(abs, []byte("package x\n\nfunc F() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.sessionFor(context.Background(), abs); err != nil {
		t.Fatalf("start gopls: %v", err)
	}
	got := m.Sessions()
	if len(got) != 1 || got[0].State != StateRunning || got[0].PID <= 0 || got[0].Command == "" {
		t.Fatalf("Sessions = %+v, want one running gopls with a pid and command", got)
	}
	if got[0].Root != dir {
		t.Errorf("root = %q, want %q", got[0].Root, dir)
	}
	m.Shutdown()
	if got := m.Sessions(); len(got) != 0 {
		t.Errorf("after Shutdown: Sessions = %+v, want none", got)
	}
}
