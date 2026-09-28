package api_test

// Claude Code's fullscreen renderer captures the mouse, which breaks native
// selection, links and Lectern's clickable paths in its sessions. Lectern
// launches it with CLAUDE_CODE_DISABLE_MOUSE=1 unless told otherwise; these
// tests follow that setting through launches, overrides and a continuation.

import (
	"context"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/sessions"
)

// launchOf is the tmux command that started the session with this row.
func (h *harness) launchOf(sess obj) string {
	h.t.Helper()
	launched := ""
	for _, cmd := range h.mock().CmdLog() {
		if strings.HasPrefix(cmd, "tmux new-session") && strings.Contains(cmd, sess.str("tmux_session")+" ") {
			launched = cmd
		}
	}
	if launched == "" {
		h.t.Fatalf("no launch for %s", sess.str("tmux_session"))
	}
	return launched
}

func TestClaudeLaunchesLeaveTheMouseToTheTerminal(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	on, off := "CLAUDE_CODE_DISABLE_MOUSE=1 ", "CLAUDE_CODE_DISABLE_MOUSE=0 "

	// On by default, and only for Claude Code: other CLIs do not read it.
	claude := h.session(obj{"project_id": pid, "name": "claude", "agent": "claude"})
	if cmd := h.launchOf(claude); !strings.Contains(cmd, on) {
		t.Errorf("claude launch lacks the variable:\n%s", cmd)
	}
	codex := h.session(obj{"project_id": pid, "name": "codex", "agent": "codex"})
	if cmd := h.launchOf(codex); strings.Contains(cmd, "CLAUDE_CODE_DISABLE_MOUSE") {
		t.Errorf("codex was given Claude's variable:\n%s", cmd)
	}

	// Turned off globally: Claude keeps the mouse.
	h.request2("PUT", "/api/settings", obj{"claude_terminal_mouse": "0"}, 200)
	quiet := h.session(obj{"project_id": pid, "name": "quiet"})
	if cmd := h.launchOf(quiet); strings.Contains(cmd, "CLAUDE_CODE_DISABLE_MOUSE") {
		t.Errorf("setting off, variable still set:\n%s", cmd)
	}

	// A project can turn it back on for itself, and rejects nonsense.
	h.patch("/api/projects/"+itoa(pid), obj{"claude_terminal_mouse": "1"}, 200)
	if code := h.status("PATCH", "/api/projects/"+itoa(pid), obj{"claude_terminal_mouse": "yes"}); code != 422 {
		t.Errorf("invalid override accepted: %d", code)
	}
	forced := h.session(obj{"project_id": pid, "name": "forced"})
	if cmd := h.launchOf(forced); !strings.Contains(cmd, on) {
		t.Errorf("project override on ignored:\n%s", cmd)
	}

	// An explicit value in the project's env wins over Lectern's.
	h.patch("/api/projects/"+itoa(pid), obj{"env": obj{"CLAUDE_CODE_DISABLE_MOUSE": "0"}}, 200)
	explicit := h.session(obj{"project_id": pid, "name": "explicit"})
	if cmd := h.launchOf(explicit); !strings.Contains(cmd, off) || strings.Contains(cmd, on) {
		t.Errorf("explicit env value not respected:\n%s", cmd)
	}
	h.patch("/api/projects/"+itoa(pid), obj{"env": obj{}, "claude_terminal_mouse": ""}, 200)

	// A continuation re-decides a value Lectern injected: the first session
	// was launched with it on, and the setting is now off.
	row, err := h.App.DB.Session(claude.id())
	if err != nil {
		t.Fatal(err)
	}
	config, err := h.App.Sessions.SessionLaunchConfiguration(row)
	if err != nil || !config.TerminalMouse || config.Spec.Env[sessions.EnvClaudeDisableMouse] != "1" {
		t.Fatalf("saved configuration does not record the injection: %v %+v", err, config)
	}
	next, err := h.App.Sessions.Launch(context.Background(), sessions.LaunchOpts{Configuration: config, TargetID: row.TargetID, ProjectID: row.ProjectID, Name: "continued", Agent: row.Agent, Workdir: row.Workdir})
	if err != nil {
		t.Fatal(err)
	}
	if cmd := h.launchOf(obj{"tmux_session": next.TmuxSession}); strings.Contains(cmd, "CLAUDE_CODE_DISABLE_MOUSE") {
		t.Errorf("continuation kept a stale injected value:\n%s", cmd)
	}
}
