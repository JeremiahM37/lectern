package gitbash

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFindsBashBesideGit(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"cmd/git.exe", "bin/bash.exe"} {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	look := func(string) (string, error) { return filepath.Join(root, "cmd", "git.exe"), nil }
	got, err := find(look, func(string) string { return "" })
	if err != nil || got != filepath.Join(root, "bin", "bash.exe") {
		t.Fatalf("find = %q, %v", got, err)
	}
	override := func(k string) string {
		if k == Env {
			return `D:\tools\bash.exe`
		}
		return ""
	}
	if got, _ := find(look, override); got != `D:\tools\bash.exe` {
		t.Fatalf("override = %q", got)
	}
	none := func(string) (string, error) { return "", errors.New("no git") }
	if _, err := find(none, func(string) string { return "" }); err == nil {
		t.Fatal("found a bash with no Git installed")
	}
}

func TestNativePath(t *testing.T) {
	for in, want := range map[string]string{"/c/Users/me/x": `C:\Users\me\x`, "/d": `D:\`, `C:\x`: `C:\x`, "relative/x": "relative/x", "/usr/bin": "/usr/bin"} {
		if got := NativePath(in); got != want {
			t.Errorf("NativePath(%q) = %q, want %q", in, got, want)
		}
	}
}
