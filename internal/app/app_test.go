package app

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Only a brand-new database gets the "ask before risky actions" default; an
// install that already exists keeps the behaviour it had.
func TestAskBeforeRiskyActionsOnlyForNewInstalls(t *testing.T) {
	dir := t.TempDir()
	open := func(path string) *App {
		t.Helper()
		a, err := New(&config.Config{DBPath: path, Mock: true, BaseURL: "http://127.0.0.1:1",
			HostClaudeConfig: filepath.Join(dir, "none.json")}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(a.Close)
		return a
	}
	fresh := open(filepath.Join(dir, "new.db"))
	if got := fresh.DB.Setting("session_permission_mode"); got != "ask" {
		t.Fatalf("new install: %q", got)
	}

	oldPath := filepath.Join(dir, "old.db")
	old, err := store.Open(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	old.Close()
	if got := open(oldPath).DB.Setting("session_permission_mode"); got != "" {
		t.Fatalf("an existing install must keep its default: %q", got)
	}
}
