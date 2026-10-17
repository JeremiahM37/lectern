package app

import (
	"os"
	"path/filepath"
	"testing"
)

// The backend reports Git missing when no git is on PATH, and nothing once a
// git is found. The check is a real PATH lookup, so the test points PATH at
// an empty directory and then at one holding a stand-in git.
func TestMissingRequiredFollowsPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	b := &scribeBackend{}
	got := b.MissingRequired()
	if len(got) != 1 || got[0] != "Git" {
		t.Fatalf("no git on PATH: MissingRequired() = %v, want [Git]", got)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if got := b.MissingRequired(); len(got) != 0 {
		t.Fatalf("git on PATH: MissingRequired() = %v, want none", got)
	}
}
