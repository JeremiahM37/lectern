package agentevents

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// The installers run as Go helpers on a target with a lectern binary and as
// the Python heredocs everywhere else.
func TestInstallersPickTheTargetsHelper(t *testing.T) {
	plain := executor.NewMock(0)
	withLectern := executor.NewMock(0)
	executor.SetTargetEnv(withLectern, executor.TargetEnv{Lectern: "/opt/lectern"})

	if got, want := ClaudeSettingsInstall(plain, "lec-1", "http://h/x", true), ClaudeSettingsInstallCommand("lec-1", "http://h/x", true); got != want {
		t.Errorf("claude fallback changed:\n%s", got)
	}
	if got, want := ClaudeSettingsInstall(withLectern, "lec-1", "http://h/x", true),
		"/opt/lectern helper claude-settings-install lec-1 http://h/x 1 /opt/lectern"; got != want {
		t.Errorf("claude helper: %q, want %q", got, want)
	}
	if got, want := CodexHooksInstall(plain, false), CodexHooksInstallCommand(false); got != want {
		t.Errorf("codex fallback changed:\n%s", got)
	}
	if got, want := CodexHooksInstall(withLectern, false),
		`/opt/lectern helper codex-hooks-install 0 "$HOME"/.lectern/hooks/lectern-codex-hook.py /opt/lectern`; got != want {
		t.Errorf("codex helper: %q, want %q", got, want)
	}
}
