package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/lsp"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// A language the user turned off is shown as info, and no server is started.
func TestLSPProbeBlockSkipsDisabledServer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &App{Workdir: dir, Cfg: config.Config{DisabledLSPServers: []string{"go"}}}
	a.ext.lsp = lsp.NewManager(dir)
	defer a.ext.lsp.Shutdown()
	hb := lspProbeBlock(a, "go")
	if hb.Name != "LSP go (tier 1)" || len(hb.Probes) != 1 || hb.Probes[0].State != "info" ||
		!strings.Contains(hb.Probes[0].Reason, "turned off") {
		t.Fatalf("disabled server block = %+v", hb)
	}
}

// A project file the server cannot start for is a failing probe with a fix.
func TestLSPProbeBlockFailsWhenServerMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	a := &App{Workdir: dir}
	a.ext.lsp = lsp.NewManager(dir)
	defer a.ext.lsp.Shutdown()
	hb := lspProbeBlock(a, "go")
	p := hb.Probes[0]
	if p.State != "fail" || !strings.HasPrefix(p.Reason, "did not start") || p.Fix == "" || p.Ms != -1 {
		t.Fatalf("missing server probe = %+v", p)
	}
}

// A language with no file in the project has nothing to ask, so it is info.
func TestLSPProbeBlockNoSampleFile(t *testing.T) {
	dir := t.TempDir()
	a := &App{Workdir: dir}
	a.ext.lsp = lsp.NewManager(dir)
	defer a.ext.lsp.Shutdown()
	hb := lspProbeBlock(a, "rust")
	if len(hb.Probes) != 1 || hb.Probes[0].State != "info" || !strings.Contains(hb.Probes[0].Reason, "no rust file") {
		t.Fatalf("no sample file block = %+v", hb)
	}
}

// Without a language server manager there is nothing to probe.
func TestHealthLanguagesNeedsManager(t *testing.T) {
	if got := healthLanguages(&App{Workdir: t.TempDir()}); got != nil {
		t.Fatalf("no manager should probe nothing, got %v", got)
	}
}

// The streaming check reports the quick rows and each language's block, and
// returns the same rows at the end, in the popup's order.
func TestHealthChecksStreamReportsEachBlock(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	a := &App{Workdir: dir, Cfg: config.Config{}}
	a.ext.lsp = lsp.NewManager(dir)
	defer a.ext.lsp.Shutdown()
	b := &scribeBackend{app: a}
	var reports int
	final := b.HealthChecksStream(func(bs []tui.HealthBackend) { reports++ })
	if reports < 2 {
		t.Fatalf("reports = %d, want the quick batch and the language block", reports)
	}
	var lspSeen bool
	for _, hb := range final {
		if hb.Name == "LSP go (tier 1)" {
			lspSeen = true
		}
	}
	if !lspSeen {
		t.Fatalf("final rows lack the language block: %v", names(final))
	}
}
