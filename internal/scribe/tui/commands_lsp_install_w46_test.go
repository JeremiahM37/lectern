package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lspInstallSetup gives the fake backend a Go project and a PATH holding only
// the named fake executables, and replaces the install runner with a counter.
// No test here runs a real install: the runner is the only thing that could.
func lspInstallSetup(t *testing.T, path ...string) (*Model, *FakeBackend, *[][]string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	for _, n := range path {
		if err := os.WriteFile(filepath.Join(bin, n), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GOBIN", "")
	t.Setenv("GOPATH", t.TempDir())

	var calls [][]string
	prev := lspInstallRun
	lspInstallRun = func(_ context.Context, argv []string) (string, error) {
		calls = append(calls, append([]string(nil), argv...))
		return "", nil
	}
	t.Cleanup(func() { lspInstallRun = prev })

	m, fb := newTestModel(t, 100, 30)
	fb.workdir = dir
	return m, fb, &calls
}

func TestLSPInstallIsRegisteredAsASubcommand(t *testing.T) {
	cmd, args := findCommand("/lsp install gopls")
	if cmd == nil || cmd.Name != "lsp install" || args != "gopls" {
		t.Fatalf("/lsp install gopls resolved to %+v args %q", cmd, args)
	}
}

func TestLSPInstallListingNamesInstallAndState(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GOBIN", "")
	t.Setenv("GOPATH", t.TempDir())
	got := lspInstallListing(dir)
	for _, want := range []string{
		"Language servers for this project (go)",
		"gopls  go  not installed",
		"install: go install golang.org/x/tools/gopls@latest",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("listing missing %q:\n%s", want, got)
		}
	}
}

func TestLSPInstallListingShowsInstalledPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gopls"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GOBIN", "")
	t.Setenv("GOPATH", t.TempDir())
	if got := lspInstallListing(dir); !strings.Contains(got, "gopls  go  installed ("+filepath.Join(bin, "gopls")+")") {
		t.Errorf("installed server not shown as installed:\n%s", got)
	}
}

func TestLSPInstallListingWithNoLanguages(t *testing.T) {
	if got := lspInstallListing(t.TempDir()); !strings.Contains(got, "No language with a known server") {
		t.Errorf("empty project listing: %q", got)
	}
}

// The confirm gate: a runnable install opens the confirm modal and runs nothing.
func TestLSPInstallAsksBeforeRunning(t *testing.T) {
	m, _, calls := lspInstallSetup(t, "go")
	before := len(m.modals)
	if cmd := m.runCommand("/lsp install gopls"); cmd != nil {
		t.Fatalf("install must not return a command before confirm, got one")
	}
	if len(m.modals) != before+1 {
		t.Fatalf("want a confirm modal pushed, modal count %d -> %d", before, len(m.modals))
	}
	if _, ok := m.modals[len(m.modals)-1].(*Confirm); !ok {
		t.Fatalf("top modal is %T, want *Confirm", m.modals[len(m.modals)-1])
	}
	if len(*calls) != 0 {
		t.Fatalf("install ran before confirm: %v", *calls)
	}
}

func TestLSPInstallNoKeepsItOff(t *testing.T) {
	m, _, calls := lspInstallSetup(t, "go")
	m.runCommand("/lsp install gopls")
	send(m, "n")
	if len(m.modals) != 0 {
		t.Errorf("declined confirm should close, %d modals left", len(m.modals))
	}
	if len(*calls) != 0 {
		t.Fatalf("declined install still ran: %v", *calls)
	}
}

func TestLSPInstallYesRunsTheCatalogueCommand(t *testing.T) {
	m, _, calls := lspInstallSetup(t, "go")
	m.runCommand("/lsp install gopls")
	cmd := send(m, "y")
	if cmd == nil {
		t.Fatal("yes should start the install")
	}
	if len(*calls) != 0 {
		t.Fatalf("the install must run in the background, not inside the key handler: %v", *calls)
	}
	m.Update(cmd()) // the background job's result reaches the event loop
	want := []string{"go", "install", "golang.org/x/tools/gopls@latest"}
	if len(*calls) != 1 || strings.Join((*calls)[0], " ") != strings.Join(want, " ") {
		t.Fatalf("runner calls %v, want one call %v", *calls, want)
	}
}

func TestLSPInstallAlreadyInstalledDoesNotAsk(t *testing.T) {
	m, _, calls := lspInstallSetup(t, "go", "gopls")
	before := len(m.modals)
	m.runCommand("/lsp install gopls")
	if len(m.modals) != before || len(*calls) != 0 {
		t.Fatalf("installed server should not prompt or run: modals %d, calls %v", len(m.modals), *calls)
	}
}

func TestLSPInstallManualServerIsShownNotRun(t *testing.T) {
	m, _, calls := lspInstallSetup(t, "go")
	before := len(m.modals)
	m.runCommand("/lsp install rust-analyzer")
	if len(m.modals) != before || len(*calls) != 0 {
		t.Fatalf("manual install should only show text: modals %d, calls %v", len(m.modals), *calls)
	}
}

func TestLSPInstallMissingToolDoesNotAsk(t *testing.T) {
	m, _, calls := lspInstallSetup(t) // no go on PATH
	before := len(m.modals)
	m.runCommand("/lsp install gopls")
	if len(m.modals) != before || len(*calls) != 0 {
		t.Fatalf("missing go should not prompt or run: modals %d, calls %v", len(m.modals), *calls)
	}
}

func TestLSPInstallUnknownServerDoesNotAsk(t *testing.T) {
	m, _, calls := lspInstallSetup(t, "go")
	before := len(m.modals)
	m.runCommand("/lsp install no-such-server")
	if len(m.modals) != before || len(*calls) != 0 {
		t.Fatalf("unknown server should do nothing: modals %d, calls %v", len(m.modals), *calls)
	}
}

func TestLSPInstallTailKeepsLastLines(t *testing.T) {
	out := "a\n\n b \nc\nd\n"
	if got := lspInstallTail(out, 2); got != "c\nd" {
		t.Errorf("tail = %q", got)
	}
	if got := lspInstallTail("", 3); got != "" {
		t.Errorf("empty tail = %q", got)
	}
}
