package sessions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/isolation"
)

const fakeToken = "f00dfeedcafe0123456789abcdef0123456789abcdef0123"

// The hook token never appears in the launch command. A dead agent's shell
// would otherwise print it, and the process list shows it to anyone.
func TestLaunchCommandReadsSecretsFromAFile(t *testing.T) {
	spec, _ := Find(Builtins(), "claude")
	cmd := spec.LaunchCommand(Start{Workdir: "/r", TmuxName: "lec-s3", EnvPrefix: "LECTERN_HOOK_URL='http://x' ",
		EnvFile: "$HOME/.lectern/hooks/lec-s3.env", EnvFileNames: []string{"LECTERN_HOOK_TOKEN", "OTEL_EXPORTER_OTLP_HEADERS"}})
	for _, want := range []string{`. "$HOME/.lectern/hooks/lec-s3.env" && rm -f "$HOME/.lectern/hooks/lec-s3.env"`,
		"2>&3 3>&-; } 3>&2 2>/dev/null", "unset LECTERN_HOOK_TOKEN OTEL_EXPORTER_OTLP_HEADERS; exec bash"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("launch command lacks %q:\n%s", want, cmd)
		}
	}
}

// writeSecretEnv leaves a private file of export lines; a docker sandbox,
// which sees only its own command line, keeps the old prefix.
func TestSecretEnvFileIsPrivateAndDockerKeepsThePrefix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	secrets := map[string]string{"LECTERN_HOOK_TOKEN": fakeToken, "OTEL_EXPORTER_OTLP_HEADERS": "Authorization=Bearer " + fakeToken}
	file, names, prefix := writeSecretEnv(context.Background(), executor.NewLocal(), "lec-s9", secrets, isolation.Config{})
	if file == "" || prefix != "" || strings.Join(names, ",") != "LECTERN_HOOK_TOKEN,OTEL_EXPORTER_OTLP_HEADERS" {
		t.Fatalf("file=%q names=%v prefix=%q", file, names, prefix)
	}
	path := filepath.Join(home, ".lectern/hooks/lec-s9.env")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("env file: %v %v", info, err)
	}
	// Sourcing it gives the agent its environment back exactly.
	out, err := exec.Command("bash", "-c", `. "$1" && printf '%s|%s' "$LECTERN_HOOK_TOKEN" "$OTEL_EXPORTER_OTLP_HEADERS"`, "x", path).Output()
	if err != nil || string(out) != fakeToken+"|Authorization=Bearer "+fakeToken {
		t.Fatalf("sourced %q %v", out, err)
	}
	_, _, prefix = writeSecretEnv(context.Background(), executor.NewLocal(), "lec-s9", secrets, isolation.Config{Mode: isolation.Docker})
	if !strings.Contains(prefix, fakeToken) {
		t.Fatal("docker lost its secrets")
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
