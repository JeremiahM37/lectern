package headless

import (
	"strings"
	"testing"
)

func TestRunWithoutGitStopsWithTheReason(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	res := invoke(t, []string{"--cwd", t.TempDir(), "say hello"}, "")
	if res.code != 1 || !strings.Contains(res.stderr, "git is required") {
		t.Fatalf("exit %d, stderr %q; want 1 and the git message", res.code, res.stderr)
	}
}
