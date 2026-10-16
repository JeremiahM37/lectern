package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// TestBrokenProjectConfigIsIgnored checks spec 5.5: a project config that does
// not parse must not stop the session; the global config is used and the
// warning is kept.
func TestBrokenProjectConfigIsIgnored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SCRIBE_HOME", home)
	if err := os.WriteFile(config.GlobalPath(), []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(config.ProjectPath(root)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.ProjectPath(root), []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadWithPreset(root, ""); err == nil {
		t.Fatal("expected the broken project file to fail the normal load")
	}
	cfg, warn, err := startConfig(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Theme != "dark" {
		t.Fatalf("global config not used: %+v", cfg)
	}
	if !strings.Contains(warn, "project config ignored") {
		t.Fatalf("warning = %q", warn)
	}
}
