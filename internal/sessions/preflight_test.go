package sessions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

func TestExecutableWordNeverEvaluatesShell(t *testing.T) {
	for input, want := range map[string]string{
		"claude --model opus": "claude", "'/tmp/my agent' --flag": "/tmp/my agent",
		`"/tmp/my agent"`: "/tmp/my agent", `ENV=x claude`: "", `$(touch /tmp/unwanted)`: "",
		`"$HOME/bin/agent"`: "", "": "",
	} {
		if got := executableWord(input); got != want {
			t.Errorf("%q: got %q want %q", input, got, want)
		}
	}
}

func TestAgentPreflightUsesTargetPathAndRejectsMissingBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "test-agent")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ex := &executor.Local{}
	spec := Spec{Name: "test", Command: "test-agent", Env: map[string]string{"PATH": dir}}
	if err := checkAgentExecutable(context.Background(), ex, spec, nil); err != nil {
		t.Fatal(err)
	}
	spec.Command = "missing-lectern-agent"
	if err := checkAgentExecutable(context.Background(), ex, spec, nil); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("missing executable: %v", err)
	}
	spec.Command = "test-agent"
	if err := checkAgentExecutable(context.Background(), ex, spec, map[string]string{"PATH": "/does-not-exist"}); err == nil {
		t.Fatal("launch PATH override was ignored")
	}
}

func TestLaunchEnvironmentSurvivesCrashWithoutLeakingCredentials(t *testing.T) {
	dir := t.TempDir()
	const secret = "test-hook-secret-must-not-appear-in-diagnostics"
	envFile, cleanup, err := stageLaunchEnvironment(context.Background(), &executor.Local{}, "LECTERN_HOOK_TOKEN='"+secret+"'")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	defer os.RemoveAll(filepath.Dir(envFile))
	info, err := os.Stat(filepath.Dir(envFile))
	if err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("launch directory must be private: %v %v", info, err)
	}
	for name, script := range map[string]string{
		"tmux":           "#!/bin/sh\nwhile [ \"$#\" -gt 0 ] && [ \"$1\" != -- ]; do shift; done\n[ \"$1\" = -- ] && shift\nexec \"$@\"\n",
		"crashing-agent": "#!/bin/sh\n[ -n \"$LECTERN_HOOK_TOKEN\" ] || exit 97\nprintf 'ENV_RECEIVED\\n'\nkill -ABRT $$\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	command := (Spec{Name: "crashing-agent", Command: "crashing-agent"}).LaunchCommand(Start{Workdir: dir, TmuxName: "test-crash", EnvFile: envFile})
	if strings.Contains(command, secret) {
		t.Fatal("credential in process command")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("launch wrapper: %v: %s", err, output)
	}
	if !strings.Contains(string(output), "ENV_RECEIVED") || strings.Contains(string(output), secret) {
		t.Fatalf("crash output: %s", output)
	}
	if _, err := os.Stat(envFile); !os.IsNotExist(err) {
		t.Fatalf("launch credentials left after exit: %v", err)
	}
}
