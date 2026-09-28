package api_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

// helperModes runs a real-target test with the target-side helpers in
// Python, and again through a real lectern binary with python3 made
// unusable, as on an agent machine that has only Lectern.
var helperModes = []string{"python", "go"}

func useHelpers(t *testing.T, h *harness, targetID int64, mode string) {
	t.Helper()
	if mode != "go" {
		return
	}
	target, err := h.App.DB.Target(targetID)
	if err != nil {
		t.Fatal(err)
	}
	ex, err := h.App.Reg.For(target)
	if err != nil {
		t.Fatal(err)
	}
	executor.SetTargetEnv(ex, executor.TargetEnv{Lectern: testutil.LecternBinary(t)})
	shim := t.TempDir()
	if err := os.WriteFile(filepath.Join(shim, "python3"), []byte("#!/bin/sh\necho 'python3 was used' >&2\nexit 97\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
}
