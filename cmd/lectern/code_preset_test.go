package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An unknown --preset is refused before any session opens, and the error
// names the presets that do exist.
func TestCodePresetFlagRefusesUnknownName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SCRIBE_HOME", home)
	dir := filepath.Join(home, "presets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fast.json"), []byte(`{"mode":"auto"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	code := codeTUI([]string{"--preset", "slow", "--cwd", t.TempDir()}, &errOut)
	if code != 2 {
		t.Fatalf("exit %d, want 2; stderr: %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), `no preset "slow"`) || !strings.Contains(errOut.String(), "fast") {
		t.Fatalf("the error should name the missing preset and the ones that exist: %q", errOut.String())
	}
}

// --preset needs a name.
func TestCodePresetFlagNeedsValue(t *testing.T) {
	var errOut bytes.Buffer
	if code := codeTUI([]string{"--preset"}, &errOut); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}
