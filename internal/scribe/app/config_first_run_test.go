package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// isolateScribeHome keeps HOME and the Scribe home inside a temp dir.
func isolateScribeHome(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("LECTERN_HOME", "")
	t.Setenv("SCRIBE_HOME", filepath.Join(root, "scribe"))
	return root
}

// A first start writes the global config (spec 02 5.5) and gives no warning.
func TestStartConfigCreatesGlobalOnFirstRun(t *testing.T) {
	isolateScribeHome(t)
	cfg, warn, err := startConfig(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if warn != "" {
		t.Fatalf("unexpected warning on first run: %q", warn)
	}
	if _, err := os.Stat(config.GlobalPath()); err != nil {
		t.Fatalf("global config not created: %v", err)
	}
	if cfg.AgentFeatures == nil {
		t.Fatal("first-run config is missing the default agent_features")
	}
}

// A read-only home must not stop startup (spec 02 5.5). The session runs on
// built-in defaults and the warning names the path and the reason. Root
// ignores directory permissions, so this runs only as a normal user.
func TestStartConfigWarnsWhenHomeIsReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	isolateScribeHome(t)
	home := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(home, 0o700) })
	t.Setenv("SCRIBE_HOME", home)

	_, warn, err := startConfig(t.TempDir(), "")
	if err != nil {
		t.Fatalf("startup failed on a read-only home: %v", err)
	}
	if !strings.Contains(warn, "could not create") || !strings.Contains(warn, home) {
		t.Fatalf("warning = %q, want the failed path named", warn)
	}
	if _, err := os.Stat(config.GlobalPath()); err == nil {
		t.Fatal("a config file appeared in a read-only home")
	}
}

// A home that is a regular file is not a read-only home: nothing under it can
// be read, so startup stops with an error that names the config path. This
// pins that behaviour so it is a decision, not an accident.
func TestStartConfigFailsWhenHomeIsAFile(t *testing.T) {
	isolateScribeHome(t)
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCRIBE_HOME", blocker)

	_, _, err := startConfig(t.TempDir(), "")
	if err == nil {
		t.Fatal("expected startup to fail when the home is a file")
	}
	if !strings.Contains(err.Error(), "config.json") {
		t.Fatalf("error does not name the config file: %v", err)
	}
}

// Two startup notes are joined so that neither is lost.
func TestJoinWarnings(t *testing.T) {
	if got := joinWarnings("", "b"); got != "b" {
		t.Fatalf("empty first: %q", got)
	}
	if got := joinWarnings("a", ""); got != "a" {
		t.Fatalf("empty second: %q", got)
	}
	if got := joinWarnings("a", "b"); got != "a; b" {
		t.Fatalf("both: %q", got)
	}
}
