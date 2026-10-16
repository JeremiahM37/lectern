package lsp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// A server turned off in settings is never started, and asking for it says so.
func TestDisabledServerIsRefusedNotStarted(t *testing.T) {
	dir := goProject(t)
	m := NewManager(dir)
	m.SetDisabled([]string{" GO "})
	_, err := m.sessionFor(context.Background(), filepath.Join(dir, "a.go"))
	if err == nil || !strings.Contains(err.Error(), "turned off in settings") {
		t.Fatalf("expected a turned-off error, got %v", err)
	}
	if len(m.running()) != 0 {
		t.Fatal("a disabled server must not be started")
	}
	m.SetDisabled(nil)
	if m.isDisabled("go") {
		t.Fatal("SetDisabled(nil) should re-enable every server")
	}
}

// Warm with nothing to start returns at once and leaves no session behind.
func TestWarmSkipsDisabledAndUnknownLanguages(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	m.SetDisabled([]string{"go"})
	m.Warm(context.Background(), []string{"go", "not-a-language"})
	if len(m.running()) != 0 {
		t.Fatal("Warm must not start disabled or unknown servers")
	}
}
