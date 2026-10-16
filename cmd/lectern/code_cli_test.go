package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/version"
)

// --version prints the version and exits 0 with or without a terminal, and
// never starts the UI.
func TestCodeVersionFlagWorksAtTerminalAndInPipe(t *testing.T) {
	for _, flag := range []string{"--version", "-v"} {
		rec := withTUI(t)
		var out, errOut bytes.Buffer
		// codeCommand with a non-terminal stdin is the pipe case; the version
		// flag must not depend on which one it is.
		if code := codeCommand([]string{flag}, strings.NewReader(""), &out, &errOut); code != 0 {
			t.Fatalf("%s: exit %d err %q", flag, code, errOut.String())
		}
		if strings.TrimSpace(out.String()) != version.Version {
			t.Errorf("%s: out %q, want %q", flag, out.String(), version.Version)
		}
		if rec.ran {
			t.Errorf("%s started the UI", flag)
		}
	}
}

// --cwd is checked before anything touches the folder: a missing folder is
// reported as such (and not created), and a file is not a folder.
func TestCodeTUIRejectsBadCwd(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "plain.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing")
	cases := []struct{ cwd, want string }{
		{missing, "--cwd: "},
		{file, "is not a directory"},
	}
	for _, c := range cases {
		rec := withTUI(t)
		var errOut bytes.Buffer
		if code := codeTUI([]string{"--cwd", c.cwd}, &errOut); code != 1 {
			t.Errorf("%s: exit %d, want 1 (err %q)", c.cwd, code, errOut.String())
		}
		if !strings.Contains(errOut.String(), c.want) {
			t.Errorf("%s: err %q, want it to contain %q", c.cwd, errOut.String(), c.want)
		}
		if rec.ran {
			t.Errorf("%s: the UI started", c.cwd)
		}
	}
	if _, err := os.Stat(missing); err == nil {
		t.Errorf("--cwd created %s", missing)
	}
}
