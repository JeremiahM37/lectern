package sessions

import (
	"os/exec"
	"strings"
	"testing"
)

const fakeToken = "f00dfeedcafe0123456789abcdef0123456789abcdef0123"

// The hook token never appears in the launch command. A dead agent's shell
// would otherwise print it, and the process list shows it to anyone. The
// agent's subshell loads and deletes the private file, and the shell's own
// report of a killed agent goes nowhere.
func TestLaunchCommandReadsSecretsFromAFile(t *testing.T) {
	spec, _ := Find(Builtins(), "claude")
	cmd := spec.LaunchCommand(Start{Workdir: "/r", TmuxName: "lec-s3", EnvFile: "/tmp/lectern-launch-env.abc/env"})
	for _, want := range []string{`set -a; . /tmp/lectern-launch-env.abc/env || exit; set +a; rm -f /tmp/lectern-launch-env.abc/env`,
		"2>&3 3>&-; } 3>&2 2>/dev/null", "rmdir /tmp/lectern-launch-env.abc 2>/dev/null; exec bash"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("launch command lacks %q:\n%s", want, cmd)
		}
	}
	if strings.Contains(cmd, fakeToken) {
		t.Fatal("secret in the launch command")
	}
}

// The shell's own report of an agent killed by a signal goes nowhere, while
// what the agent writes to stderr still shows.
func TestADeadAgentLeavesNoCommandLineOnScreen(t *testing.T) {
	inner := `{ SECRET=` + fakeToken + ` sh -c 'echo agent-error >&2; kill -QUIT $$' 2>&3 3>&-; } 3>&2 2>/dev/null; echo after`
	out, _ := exec.Command("bash", "-c", inner).CombinedOutput()
	if strings.Contains(string(out), fakeToken) || !strings.Contains(string(out), "agent-error") || !strings.Contains(string(out), "after") {
		t.Fatalf("pane would show: %q", out)
	}
}
