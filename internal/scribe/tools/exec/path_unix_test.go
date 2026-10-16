//go:build !windows

package exec

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestShellKeepsTheInheritedPath: commands must find tools the user put on
// PATH. A login shell re-reads /etc/profile, which resets PATH on Debian.
func TestShellKeepsTheInheritedPath(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "scribe-path-probe")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\necho found-it\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	argv := shellArgv("scribe-path-probe")
	cmd := osexec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "found-it") {
		t.Fatalf("the shell lost the inherited PATH: err=%v out=%q", err, out)
	}
}
