package tui

import (
	"errors"
	osexec "os/exec"
	"slices"
	"strings"
	"testing"
)

func TestPassthroughCommandRunsBashInWorkdir(t *testing.T) {
	dir := t.TempDir()
	c := passthroughCommand(dir, "vim notes.md")
	if want := []string{"bash", "-c", "vim notes.md"}; !slices.Equal(c.Args, want) {
		t.Fatalf("Args = %q, want %q", c.Args, want)
	}
	if c.Dir != dir {
		t.Fatalf("Dir = %q, want %q", c.Dir, dir)
	}
}

// The handed-over command is the user's own, on their terminal: it keeps
// their whole environment, credentials included (spec 05 §9.5).
func TestPassthroughCommandKeepsTheUsersEnvironment(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp-test-value")
	env := passthroughCommand(t.TempDir(), "true").Env
	if !slices.Contains(env, "GITHUB_TOKEN=ghp-test-value") {
		t.Fatal("the user's GITHUB_TOKEN should reach their own interactive command")
	}
	if slices.Contains(env, "GIT_TERMINAL_PROMPT=0") {
		t.Fatal("GIT_TERMINAL_PROMPT=0 would stop an interactive git login")
	}
}

func TestPassthroughDoneReportsCleanResume(t *testing.T) {
	msg, ok := passthroughDone("vim notes.md", nil).(flashMsg)
	if !ok {
		t.Fatal("resume did not produce a flash message")
	}
	if msg.kind != "info" || msg.text != "Back from vim notes.md" || msg.chat {
		t.Fatalf("clean resume = %+v", msg)
	}
}

func TestPassthroughDoneReportsExitStatus(t *testing.T) {
	// A real child exit gives the *exec.ExitError the callback receives.
	err := osexec.Command("sh", "-c", "exit 3").Run()
	msg, ok := passthroughDone("less big.log", err).(flashMsg)
	if !ok {
		t.Fatal("resume did not produce a flash message")
	}
	if msg.kind != "info" || msg.chat || msg.text != "less big.log exited with status 3" {
		t.Fatalf("exit status message = %+v", msg)
	}
}

func TestPassthroughDoneReportsStartFailure(t *testing.T) {
	msg := passthroughDone("nosuchthing", errors.New("exec: not found")).(flashMsg)
	if msg.kind != "err" || !strings.HasPrefix(msg.text, "Could not run nosuchthing: ") {
		t.Fatalf("start failure message = %+v", msg)
	}
}

func TestPassthroughDoneTruncatesLongCommand(t *testing.T) {
	long := "cat " + strings.Repeat("x", 200)
	msg := passthroughDone(long, nil).(flashMsg)
	if len(msg.text) > len("Back from ")+passthroughLabelWidth+len("…") {
		t.Fatalf("label not capped: %d bytes", len(msg.text))
	}
	if !strings.HasSuffix(msg.text, "…") {
		t.Fatalf("truncated label has no ellipsis: %q", msg.text)
	}
}

func TestInteractiveCommandIsRegistered(t *testing.T) {
	c, rest := findCommand("/interactive less /etc/hosts")
	if c == nil || c.Name != "interactive" {
		t.Fatalf("/interactive is not registered: %v", c)
	}
	if strings.TrimSpace(rest) != "less /etc/hosts" {
		t.Fatalf("args = %q", rest)
	}
}

func TestRunPassthroughRejectsEmptyCommand(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	cmd := m.runPassthrough("   ")
	if cmd == nil {
		t.Fatal("empty command returned no command")
	}
	msg, ok := cmd().(flashMsg)
	if !ok || msg.kind != "err" || !strings.Contains(msg.text, "/interactive <command>") {
		t.Fatalf("usage message = %#v", msg)
	}
}

func TestRunPassthroughHandsOverTheTerminal(t *testing.T) {
	// The exec command itself is not run here: it would take over the test's
	// terminal. Returning a command at all is what suspends the TUI.
	m, _ := newTestModel(t, 100, 30)
	if cmd := m.runPassthrough("true"); cmd == nil {
		t.Fatal("a command was not handed to tea.ExecProcess")
	}
}
