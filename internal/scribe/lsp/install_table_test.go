package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakePath makes a PATH holding only empty executables named names. Nothing
// here is ever run; the files only satisfy exec.LookPath.
func fakePath(t *testing.T, names ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GOBIN", "")
	t.Setenv("GOPATH", t.TempDir())
}

func TestInstallCatalogCommands(t *testing.T) {
	got := InstallCatalog([]string{"go", "typescript", "python", "rust"})
	if len(got) != 4 {
		t.Fatalf("want 4 entries, got %d: %+v", len(got), got)
	}
	want := map[string]string{
		"gopls":                      "go install golang.org/x/tools/gopls@latest",
		"typescript-language-server": "npm install -g typescript typescript-language-server",
		"pyright-langserver":         "npm install -g pyright",
		"rust-analyzer":              "rustup component add rust-analyzer",
	}
	for _, e := range got {
		if e.Command() != want[e.Server] {
			t.Errorf("%s: command %q, want %q", e.Server, e.Command(), want[e.Server])
		}
		if e.Runnable() != (e.Server != "rust-analyzer") {
			t.Errorf("%s: runnable=%v", e.Server, e.Runnable())
		}
	}
}

func TestManualInstallHasNoArgv(t *testing.T) {
	e, ok := ServerInstallFor("rust-analyzer")
	if !ok {
		t.Fatal("rust-analyzer should be catalogued")
	}
	if e.Runnable() || e.Argv != nil || e.Tool() != "" {
		t.Fatalf("manual entry must not carry an argv: %+v", e)
	}
}

func TestServerInstallForNameOrLanguage(t *testing.T) {
	for _, name := range []string{"gopls", "go"} {
		e, ok := ServerInstallFor(name)
		if !ok || e.Server != "gopls" {
			t.Errorf("%q: got %+v ok=%v", name, e, ok)
		}
	}
	if _, ok := ServerInstallFor("no-such-server"); ok {
		t.Error("unknown name should not match")
	}
	if _, ok := ServerInstallFor("  "); ok {
		t.Error("blank name should not match")
	}
}

func TestRunnableArgvIsACopy(t *testing.T) {
	e, _ := ServerInstallFor("gopls")
	e.Argv[0] = "rm"
	again, _ := ServerInstallFor("gopls")
	if again.Argv[0] != "go" {
		t.Fatalf("catalogue was mutated through a returned entry: %v", again.Argv)
	}
}

func TestFindOnPathUsesOnlyPath(t *testing.T) {
	fakePath(t, "gopls")
	name, path, ok := FindOnPath("go")
	if !ok || name != "gopls" || filepath.Base(path) != "gopls" {
		t.Fatalf("installed go server: name=%q path=%q ok=%v", name, path, ok)
	}
	if _, _, ok := FindOnPath("rust"); ok {
		t.Error("rust-analyzer is not on the fake PATH; should be not installed")
	}
	if _, _, ok := FindOnPath("no-such-language"); ok {
		t.Error("unknown language should not be found")
	}
}

func TestFindOnPathFallsBackToLaterCandidate(t *testing.T) {
	// python's first candidate is pyright-langserver; pylsp is the third.
	fakePath(t, "pylsp")
	name, _, ok := FindOnPath("python")
	if !ok || name != "pylsp" {
		t.Fatalf("got name=%q ok=%v, want pylsp", name, ok)
	}
}

func TestMissingPathIsNotInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GOBIN", "")
	t.Setenv("GOPATH", t.TempDir())
	if _, _, ok := FindOnPath("go"); ok {
		t.Fatal("empty PATH should show nothing installed")
	}
	e, ok := ServerInstallFor("gopls")
	if !ok || strings.TrimSpace(e.Command()) == "" {
		t.Fatal("command must still be shown when not installed")
	}
}
