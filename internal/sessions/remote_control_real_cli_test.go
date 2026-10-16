package sessions

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Opt-in check against the installed claude (LECTERN_REAL_CLI_TEST=1). It
// connects nothing: it only confirms that the flag Lectern passes and the
// screen text it reads are still what this claude uses.
func TestRemoteControlRealClaudeStillMatches(t *testing.T) {
	if os.Getenv("LECTERN_REAL_CLI_TEST") != "1" {
		t.Skip("set LECTERN_REAL_CLI_TEST=1 to run against the installed claude")
	}
	bin, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude is not installed")
	}
	help, err := exec.Command(bin, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("claude --help: %v", err)
	}
	if !strings.Contains(string(help), "--remote-control [name]") {
		t.Fatalf("claude no longer offers --remote-control [name]:\n%s", help)
	}
	real, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(real)
	if err != nil {
		t.Skipf("cannot read the claude binary: %v", err)
	}
	for _, text := range []string{"/remote-control is active", "Enable Remote Control", "Never mind"} {
		if !bytes.Contains(raw, []byte(text)) {
			t.Errorf("claude no longer prints %q; update remote_control.go's screen checks", text)
		}
	}
}
