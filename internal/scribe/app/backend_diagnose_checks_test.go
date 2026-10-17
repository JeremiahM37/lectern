package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

func TestHomeCheckStates(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	if c := homeCheck(); c.State != "ok" {
		t.Fatalf("writable home = %+v", c)
	}
	t.Setenv("SCRIBE_HOME", filepath.Join(t.TempDir(), "missing"))
	if c := homeCheck(); c.State != "warn" || c.Fix == "" {
		t.Fatalf("missing home = %+v", c)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCRIBE_HOME", file)
	if c := homeCheck(); c.State != "fail" || c.Fix == "" {
		t.Fatalf("home that is a file = %+v", c)
	}
}

// A settings file that does not parse is a fail row that names the file.
func TestHooksChecksReportUnparsableSettings(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(root, ".claude", "settings.json")
	if err := os.WriteFile(bad, []byte(`{"hooks": `), 0o600); err != nil {
		t.Fatal(err)
	}
	var fail *tui.DiagnoseCheck
	for _, c := range hooksChecks(root) {
		if c.State == "fail" {
			c := c
			fail = &c
		}
	}
	if fail == nil || !strings.Contains(fail.Fix, bad) {
		t.Fatalf("unparsable settings = %+v", fail)
	}

	if err := os.WriteFile(bad, []byte(`{"hooks": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range hooksChecks(root) {
		if c.State == "fail" {
			t.Fatalf("valid settings reported as failing: %+v", c)
		}
	}
}

// A project language with no server row yet gets an info row; a disabled one
// says so.
func TestLSPGapChecksNameTheMissingLanguages(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &App{Workdir: dir}
	rows := lspGapChecks(a, nil)
	if len(rows) != 1 || rows[0].Name != "go" || rows[0].Detail != "not started yet" {
		t.Fatalf("gap rows = %+v", rows)
	}
	a.Cfg = config.Config{DisabledLSPServers: []string{"go"}}
	rows = lspGapChecks(a, nil)
	if len(rows) != 1 || rows[0].Detail != "disabled in config" {
		t.Fatalf("disabled gap rows = %+v", rows)
	}
	if rows := lspGapChecks(&App{Workdir: dir}, []tui.LSPSession{{Language: "go"}}); len(rows) != 0 {
		t.Fatalf("a language with a row should add nothing: %+v", rows)
	}
}
