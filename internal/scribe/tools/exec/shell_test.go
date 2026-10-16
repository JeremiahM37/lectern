package exec

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func TestShellSuccessAndExitCode(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	r, _ := newTestRegistry(t, Options{})

	ok := runTool(t, r, "shell", newTestEnv(dir, nil), map[string]any{"command": "echo hello"})
	if ok.IsError || !strings.Contains(ok.Output, "hello") || !strings.Contains(ok.Output, "[exit 0]") {
		t.Fatalf("success case: %+v", ok)
	}

	fail := runTool(t, r, "shell", newTestEnv(dir, nil), map[string]any{"command": "echo oops >&2; exit 3"})
	if !fail.IsError || !strings.Contains(fail.Output, "oops") || !strings.Contains(fail.Output, "[exit 3]") {
		t.Fatalf("failure case: %+v", fail)
	}
	if fail.Meta["exit_code"] != 3 {
		t.Fatalf("exit_code meta = %v, want 3", fail.Meta["exit_code"])
	}
}

func TestShellRunsInWorkdirAndStreams(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	host := &fakeHost{decision: tools.Deny}
	r, _ := newTestRegistry(t, Options{})

	res := runTool(t, r, "shell", newTestEnv(dir, host), map[string]any{
		"command": "pwd; for i in 1 2 3; do echo line$i; done",
	})
	want, _ := filepath.EvalSymlinks(dir)
	if !strings.Contains(res.Output, want) {
		t.Fatalf("pwd output does not show workdir %s: %q", want, res.Output)
	}
	lines := host.progressLines()
	idx := -1
	for _, n := range []string{"line1", "line2", "line3"} {
		next := -1
		for i, l := range lines {
			if l == n {
				next = i
				break
			}
		}
		if next < 0 || next <= idx {
			t.Fatalf("progress lines out of order or missing %s: %v", n, lines)
		}
		idx = next
	}
}

func TestShellTimeoutKillsProcessGroup(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	r, _ := newTestRegistry(t, Options{})
	pidFile := filepath.Join(dir, "child.pid")

	start := time.Now()
	res := runTool(t, r, "shell", newTestEnv(dir, nil), map[string]any{
		"command":    "sleep 60 & echo $! > child.pid; sleep 60",
		"timeout_ms": 1000,
	})
	elapsed := time.Since(start)

	if !res.IsError || res.Meta["timed_out"] != true {
		t.Fatalf("expected a timeout result, got %+v", res)
	}
	if !strings.Contains(res.Output, "timed out") {
		t.Fatalf("timeout message missing: %q", res.Output)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("timeout took %s; the group was not stopped promptly", elapsed)
	}
	pid := readPID(t, pidFile)
	eventually(t, 3*time.Second, "backgrounded sleep to die", func() bool { return !pidAlive(pid) })
}

func TestShellCancelKillsProcessGroup(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	r, _ := newTestRegistry(t, Options{})
	pidFile := filepath.Join(dir, "child.pid")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan tools.Result, 1)
	go func() {
		tool, _ := r.Get("shell")
		raw, _ := json.Marshal(map[string]any{"command": "sleep 60 & echo $! > child.pid; sleep 60"})
		done <- tool.Run(ctx, newTestEnv(dir, nil), raw)
	}()
	eventually(t, 5*time.Second, "pid file", func() bool {
		_, err := os.Stat(pidFile)
		return err == nil
	})
	cancel()

	select {
	case res := <-done:
		if !res.IsError || !strings.Contains(res.Output, "cancelled") {
			t.Fatalf("expected a cancelled result, got %+v", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled command did not return")
	}
	pid := readPID(t, pidFile)
	eventually(t, 3*time.Second, "backgrounded sleep to die after cancel", func() bool { return !pidAlive(pid) })
}

func TestShellTimeoutIsClamped(t *testing.T) {
	if got := clampTimeout(0, defaultShellTimeout); got != defaultShellTimeout {
		t.Errorf("zero should give the default, got %s", got)
	}
	if got := clampTimeout(9_999_999, defaultShellTimeout); got != maxShellTimeout {
		t.Errorf("huge timeout should clamp to max, got %s", got)
	}
	if got := clampTimeout(1, defaultShellTimeout); got != time.Second {
		t.Errorf("tiny timeout should clamp to 1s, got %s", got)
	}
}

func TestShellOutputTruncationKeepsHeadAndTail(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	r, _ := newTestRegistry(t, Options{})

	res := runTool(t, r, "shell", newTestEnv(dir, nil), map[string]any{"command": "seq 1 20000"})
	if len(res.Output) > outputLimit+200 {
		t.Fatalf("output is %d bytes, want at most about %d", len(res.Output), outputLimit)
	}
	if !strings.Contains(res.Output, "lines omitted") {
		t.Fatalf("truncation marker missing")
	}
	if !strings.HasPrefix(res.Output, "1\n2\n") {
		t.Fatalf("head not kept: %q", res.Output[:20])
	}
	if !strings.Contains(res.Output, "\n20000\n[exit 0]") {
		t.Fatalf("tail and exit code not kept: %q", res.Output[len(res.Output)-40:])
	}
	if res.Meta["omitted"].(int) == 0 {
		t.Fatalf("omitted meta should be non-zero")
	}
}

func TestTruncateOutputCases(t *testing.T) {
	short := "hello\nworld"
	if got, n := truncateOutput(short); got != short || n != 0 {
		t.Errorf("short input changed: %q (%d)", got, n)
	}

	oneHugeLine := strings.Repeat("x", 100000)
	got, n := truncateOutput(oneHugeLine)
	if len(got) > outputLimit+100 || n != 0 || !strings.Contains(got, "bytes omitted") {
		t.Errorf("single long line: len=%d lines=%d", len(got), n)
	}

	multibyte := strings.Repeat("é", 60000) // 2 bytes per rune
	got, _ = truncateOutput(multibyte)
	if !utf8.ValidString(got) {
		t.Errorf("truncation split a UTF-8 sequence")
	}
}

func TestShellEnvScrubbingEndToEnd(t *testing.T) {
	skipOnWindows(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-should-not-leak")
	t.Setenv("SCRIBE_TEST_TOKEN", "tok-should-not-leak")
	t.Setenv("SCRIBE_TEST_VISIBLE", "visible-value")
	dir := t.TempDir()
	r, _ := newTestRegistry(t, Options{})

	res := runTool(t, r, "shell", newTestEnv(dir, nil), map[string]any{
		"command": `echo "[$ANTHROPIC_API_KEY][$SCRIBE_TEST_TOKEN][$SCRIBE_TEST_VISIBLE]"`,
	})
	if !strings.Contains(res.Output, "[][][visible-value]") {
		t.Fatalf("credentials leaked or visible var missing: %q", res.Output)
	}
}

func TestBackgroundJobLifecycle(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	host := &fakeHost{decision: tools.Deny}
	r, jobs := newTestRegistry(t, Options{})

	start := runTool(t, r, "shell", newTestEnv(dir, host), map[string]any{
		"command":    "for i in 1 2 3; do echo tick$i; sleep 0.05; done",
		"background": true,
	})
	if start.IsError || start.Meta["job_id"] != "bg-1" {
		t.Fatalf("start: %+v", start)
	}
	job, ok := jobs.Get("bg-1")
	if !ok {
		t.Fatal("job bg-1 not registered")
	}
	select {
	case <-job.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("background job did not finish")
	}

	out := runTool(t, r, "shell_output", newTestEnv(dir, nil), map[string]any{"id": "bg-1", "since": 0})
	for _, want := range []string{"tick1", "tick2", "tick3", "exited, exit 0", "[next since=3]"} {
		if !strings.Contains(out.Output, want) {
			t.Fatalf("shell_output missing %q:\n%s", want, out.Output)
		}
	}

	again := runTool(t, r, "shell_output", newTestEnv(dir, nil), map[string]any{"id": "bg-1", "since": 3})
	if strings.Contains(again.Output, "tick") {
		t.Fatalf("since=3 should return no old lines:\n%s", again.Output)
	}

	long := runTool(t, r, "shell", newTestEnv(dir, nil), map[string]any{
		"command": "echo started; sleep 60", "background": true,
	})
	if long.IsError {
		t.Fatalf("start long job: %+v", long)
	}
	killed := runTool(t, r, "shell_kill", newTestEnv(dir, nil), map[string]any{"id": "bg-2"})
	if killed.IsError || !strings.Contains(killed.Output, "killed") {
		t.Fatalf("shell_kill: %+v", killed)
	}
	list := jobs.List()
	if len(list) != 2 || list[1].Status != StatusKilled {
		t.Fatalf("List after kill: %+v", list)
	}
	if missing := runTool(t, r, "shell_output", newTestEnv(dir, nil), map[string]any{"id": "bg-99"}); !missing.IsError {
		t.Fatalf("unknown job should be an error")
	}
}

func TestJobRingKeepsLastLines(t *testing.T) {
	job := &Job{status: StatusRunning, done: make(chan struct{})}
	for i := 1; i <= ringSize+500; i++ {
		job.append(strconv.Itoa(i))
	}
	lines, next, dropped, _, _ := job.Read(0)
	if next != ringSize+500 || dropped != 500 || len(lines) != ringSize {
		t.Fatalf("next=%d dropped=%d kept=%d", next, dropped, len(lines))
	}
	if lines[0] != "501" || lines[len(lines)-1] != strconv.Itoa(ringSize+500) {
		t.Fatalf("window is %s..%s", lines[0], lines[len(lines)-1])
	}
}

func TestKillAllStopsRunningJobs(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	jobs := NewJobs(Options{})
	jobs.start(context.Background(), "sleep 60", dir, ScrubEnv(os.Environ()), 0)
	jobs.start(context.Background(), "sleep 60", dir, ScrubEnv(os.Environ()), 0)

	start := time.Now()
	jobs.KillAll()
	if time.Since(start) > 5*time.Second {
		t.Fatalf("KillAll took %s", time.Since(start))
	}
	for _, info := range jobs.List() {
		if info.Status != StatusKilled {
			t.Fatalf("job %s is %s after KillAll", info.ID, info.Status)
		}
	}
}

func TestShellDestructiveApprovalFlow(t *testing.T) {
	skipOnWindows(t)

	t.Run("denied command does not run", func(t *testing.T) {
		dir := t.TempDir()
		victim := filepath.Join(dir, "victim.txt")
		writeFile(t, victim, "keep me")
		host := &fakeHost{decision: tools.Deny}
		r, _ := newTestRegistry(t, Options{})

		res := runTool(t, r, "shell", newTestEnv(dir, host), map[string]any{
			"command": "rm -rf victim.txt", "description": "clean up",
		})
		if !res.IsError || !strings.Contains(res.Output, "Denied") {
			t.Fatalf("expected denial, got %+v", res)
		}
		if _, err := os.Stat(victim); err != nil {
			t.Fatalf("denied command still removed the file: %v", err)
		}
		if host.approvalCount() != 1 {
			t.Fatalf("approvals = %d, want 1", host.approvalCount())
		}
		a := host.approvals[0]
		if a.Kind != "destructive" || !a.Danger || a.Detail != "rm -rf victim.txt" || !strings.Contains(a.Title, "clean up") {
			t.Fatalf("approval shape wrong: %+v", a)
		}
	})

	t.Run("allowed once runs", func(t *testing.T) {
		dir := t.TempDir()
		victim := filepath.Join(dir, "victim.txt")
		writeFile(t, victim, "delete me")
		host := &fakeHost{decision: tools.AllowOnce}
		r, _ := newTestRegistry(t, Options{})

		res := runTool(t, r, "shell", newTestEnv(dir, host), map[string]any{"command": "rm -rf victim.txt"})
		if res.IsError {
			t.Fatalf("allowed command failed: %+v", res)
		}
		if _, err := os.Stat(victim); !os.IsNotExist(err) {
			t.Fatalf("file should be gone: %v", err)
		}
	})

	t.Run("auto mode skips the prompt", func(t *testing.T) {
		dir := t.TempDir()
		host := &fakeHost{decision: tools.Deny}
		env := newTestEnv(dir, host)
		env.Mode = "auto"
		r, _ := newTestRegistry(t, Options{})

		runTool(t, r, "shell", env, map[string]any{"command": "rm -rf nothing-here"})
		if host.approvalCount() != 0 {
			t.Fatalf("auto mode should not ask")
		}
	})

	t.Run("config allow skips the prompt for a simple command", func(t *testing.T) {
		dir := t.TempDir()
		host := &fakeHost{decision: tools.Deny}
		r, _ := newTestRegistry(t, Options{Allow: []string{"rm -rf build"}})
		runTool(t, r, "shell", newTestEnv(dir, host), map[string]any{"command": "rm -rf build"})
		if host.approvalCount() != 0 {
			t.Fatalf("allowed command prompted")
		}
	})

	t.Run("config allow does not cover chained commands", func(t *testing.T) {
		dir := t.TempDir()
		host := &fakeHost{decision: tools.Deny}
		r, _ := newTestRegistry(t, Options{Allow: []string{"rm -rf build"}})
		res := runTool(t, r, "shell", newTestEnv(dir, host), map[string]any{"command": "rm -rf build; echo hi"})
		if host.approvalCount() != 1 || !res.IsError {
			t.Fatalf("chained command should still prompt and be denied: %+v", res)
		}
	})

	t.Run("no host means denial", func(t *testing.T) {
		dir := t.TempDir()
		r, _ := newTestRegistry(t, Options{})
		res := runTool(t, r, "shell", newTestEnv(dir, nil), map[string]any{"command": "rm -rf x"})
		if !res.IsError || !strings.Contains(res.Output, "No one is available") {
			t.Fatalf("expected denial without a host: %+v", res)
		}
	})
}

func TestShellRefusalsAndDeny(t *testing.T) {
	dir := t.TempDir()
	host := &fakeHost{decision: tools.AllowOnce}
	r, _ := newTestRegistry(t, Options{Deny: []string{"forbidden-token"}})

	for _, cmd := range []string{"pkill -f myserver", "killall node", "tmux kill-server", "tmux kill-session -t work"} {
		res := runTool(t, r, "shell", newTestEnv(dir, host), map[string]any{"command": cmd})
		if !res.IsError || !strings.Contains(res.Output, "refused") {
			t.Errorf("%q should be refused: %+v", cmd, res)
		}
	}
	if host.approvalCount() != 0 {
		t.Errorf("refusals must not reach the approval prompt")
	}

	denied := runTool(t, r, "shell", newTestEnv(dir, host), map[string]any{"command": "echo FORBIDDEN-TOKEN"})
	if !denied.IsError || !strings.Contains(denied.Output, "Denied by config") {
		t.Errorf("deny rule should match case-insensitively: %+v", denied)
	}
}

func TestEmptyCommandIsAnError(t *testing.T) {
	r, _ := newTestRegistry(t, Options{})
	res := runTool(t, r, "shell", newTestEnv(t.TempDir(), nil), map[string]any{"command": "   "})
	if !res.IsError {
		t.Fatalf("empty command should be an error")
	}
}

// readPID reads a pid written by a test command.
func readPID(t *testing.T, path string) int {
	t.Helper()
	eventually(t, 5*time.Second, "pid file "+path, func() bool {
		b, err := os.ReadFile(path)
		return err == nil && strings.TrimSpace(string(b)) != ""
	})
	b, _ := os.ReadFile(path)
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		t.Fatalf("bad pid %q", b)
	}
	return pid
}
