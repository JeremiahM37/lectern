package executor

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

func TestPctWrapQuoting(t *testing.T) {
	cmd := Wrap("101", "git -C '/root/lec demo' diff", "/root")
	if !strings.HasPrefix(cmd, "sudo pct exec 101 -- bash -c ") {
		t.Fatalf("prefix: %s", cmd)
	}
	if !strings.Contains(cmd, "cd /root") {
		t.Errorf("cwd missing: %s", cmd)
	}
	// the single-quoted payload survives the shell quoting round trip
	if !strings.Contains(cmd, "lec demo") {
		t.Errorf("payload lost: %s", cmd)
	}
}

// recordingRunner captures the command a ReadFile turns into.
type recordingRunner struct{ last string }

func (r *recordingRunner) Run(_ context.Context, cmd string, _ RunOpts) (Result, error) {
	r.last = cmd
	return Result{0, "data", ""}, nil
}

// TestReadFileUsesFastTail guards a real performance bug: `dd bs=1` costs one
// syscall per byte, so re-reading a growing agent log on every poll crawled.
func TestReadFileUsesFastTail(t *testing.T) {
	ssh := NewSSH("h", "root", 22, "", "")
	rec := &recordingRunner{}
	ssh.runner = rec.Run
	if _, err := ssh.ReadFile(context.Background(), "/log", 100); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.last, "tail -c +101") || strings.Contains(rec.last, "dd ") {
		t.Fatalf("ssh read command: %s", rec.last)
	}

	pct := NewPct("9")
	rec2 := &recordingRunner{}
	pct.runner = rec2.Run
	if _, err := pct.ReadFile(context.Background(), "/log", 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec2.last, "tail -c +1") || strings.Contains(rec2.last, "dd ") {
		t.Fatalf("pct read command: %s", rec2.last)
	}
}

func TestShellQuoteEscapesSingleQuotes(t *testing.T) {
	if got := ShellQuote(`it's`); got != `'it'\''s'` {
		t.Fatalf("got %s", got)
	}
}

// A host whose SSH lands somewhere other than the work — a Windows box with its
// toolchain in WSL — probes as "no tmux, no python3" and can run nothing. A
// per-target wrapper makes it an ordinary target.
func TestSSHWrapperWrapsEveryCommand(t *testing.T) {
	ssh := NewSSH("h", "root", 22, "", "wsl -e bash -lc")
	rec := &recordingRunner{}
	ssh.runner = rec.Run
	if _, err := ssh.Run(context.Background(), "git status",
		RunOpts{Cwd: "/srv/repo"}); err != nil {
		t.Fatal(err)
	}
	got := rec.last
	if !strings.HasPrefix(got, "wsl -e bash -lc ") {
		t.Fatalf("not wrapped: %s", got)
	}
	if !strings.Contains(got, "cd /srv/repo && git status") {
		t.Fatalf("inner command lost: %s", got)
	}
	// the whole inner command must survive as ONE argument
	if strings.Count(got, "wsl -e bash -lc") != 1 {
		t.Errorf("wrapper applied more than once: %s", got)
	}

	plain := NewSSH("h", "root", 22, "", "")
	rec2 := &recordingRunner{}
	plain.runner = rec2.Run
	plain.Run(context.Background(), "git status", RunOpts{})
	if rec2.last != "git status" {
		t.Fatalf("an unwrapped target must be untouched: %s", rec2.last)
	}
}

// A Windows host hands the SSH command line to cmd.exe, which does not
// understand POSIX quoting — and anything that survives that is then
// $-expanded by the outer bash. Base64 passes through both untouched.
func TestSSHWrapperTemplates(t *testing.T) {
	cmd := `git commit -m "it's $(date)"`

	win := NewSSH("h", "u", 22, "", `wsl -e bash -lc "echo {b64} | base64 -d | bash"`)
	rec := &recordingRunner{}
	win.runner = rec.Run
	win.Run(context.Background(), cmd, RunOpts{})
	got := rec.last
	if strings.Contains(got, "$(date)") || strings.Contains(got, "'") {
		t.Fatalf("the command must not survive as shell syntax: %s", got)
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(got,
		`wsl -e bash -lc "echo `), ` | base64 -d | bash"`)
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("payload is not base64: %q", payload)
	}
	if string(decoded) != cmd {
		t.Fatalf("round trip lost the command: %q", decoded)
	}

	posix := NewSSH("h", "u", 22, "", "sudo -u dev {cmd}")
	rec2 := &recordingRunner{}
	posix.runner = rec2.Run
	posix.Run(context.Background(), "ls", RunOpts{})
	if rec2.last != "sudo -u dev ls" {
		t.Fatalf("{cmd}: %s", rec2.last)
	}
}
