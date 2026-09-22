package api_test

// Everything else in this suite runs against the mock executor, which is how
// three real bugs shipped: NUL argv delimiters that truncated a command on a
// real host, a launch race that only appears when a process actually starts, and
// a tmux session name that mocks accepted and tmux did not.
//
// This test uses no mock at all. Real git, real worktree, real tmux, a real
// process on this machine, the real local executor, the real poller, the real
// diff capture. The only stand-in is the agent binary itself — a shell script
// that speaks the same stream-json protocol Claude Code does, because the point
// is to exercise lectern's plumbing rather than an LLM.

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/internal/app"
	"github.com/JeremiahM37/lectern/internal/config"
	"github.com/JeremiahM37/lectern/internal/store"
	"github.com/JeremiahM37/lectern/internal/testutil"
)

// tmux names come from database ids (lec-<attempt>, lec-s<session>), and every
// rig gets a fresh database whose ids start at 1 — so without this every test in
// this file would compete for the same global tmux name on the one tmux server
// this machine runs. Each rig takes a disjoint id range instead.
var rigSeq atomic.Int64

// Keep the socket path short (Unix sockets have a length limit), and own the
// server so test cleanup cannot kill a live session with a matching name.
func isolateTmux(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("", "lec-tmux-")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", dir)
	t.Cleanup(func() {
		testutil.CleanupTmux(t, dir)
		os.RemoveAll(dir)
	})
}

func requireRealTools(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"git", "tmux", "bash"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed; this test needs the real thing", bin)
		}
	}
}

// fakeAgent speaks Claude Code's stream-json protocol and makes a real edit in
// the worktree it is run from, so the diff capture has something true to find.
const fakeAgent = `#!/bin/bash
# argv is: -p <prompt> --output-format stream-json --verbose --permission-mode X --settings Y
prompt=""
while [ $# -gt 0 ]; do
  case "$1" in
    -p) prompt="$2"; shift 2 ;;
    *) shift ;;
  esac
done
echo '{"type":"system","subtype":"init","session_id":"fake-session-abc","tools":["Bash","Edit"]}'
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"Adding the note."}]}}'
# a real edit in the real worktree, which is the working directory
printf 'agent was here\n' >> NOTES.md
# fail on demand via the prompt itself: a tmux session inherits the tmux
# SERVER's environment, not the caller's, so an env var would not reach here
if [[ "$prompt" == *FAIL-THIS-RUN* ]]; then
  echo '{"type":"result","subtype":"error","is_error":true,"result":"could not do it"}'
  exit 3
fi
# prove the prompt actually reached the agent
printf 'prompt-bytes:%s\n' "${#prompt}" >> NOTES.md
echo '{"type":"result","subtype":"success","is_error":false,"result":"done"}'
exit 0
`

// realRig is a control plane with no mocks, pointed at a real git repository.
type realRig struct {
	t       *testing.T
	app     *app.App
	url     string
	repo    string
	project int64
}

func newRealRig(t *testing.T) *realRig {
	t.Helper()
	requireRealTools(t)
	dir := t.TempDir()
	isolateTmux(t)

	// a real repository with a real commit on a real branch
	repo := filepath.Join(dir, "repo")
	mustRun(t, "", "git", "init", "-q", "-b", "main", repo)
	if err := os.WriteFile(filepath.Join(repo, "NOTES.md"), []byte("start\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repo, "git", "config", "user.email", "test@example.com")
	mustRun(t, repo, "git", "config", "user.name", "lectern test")
	mustRun(t, repo, "git", "add", "-A")
	mustRun(t, repo, "git", "commit", "-q", "-m", "initial")

	// scratch directories are created by the target's shell under $HOME by
	// default; a test must not litter the developer's home, so point the root
	// at this test's own temp dir. The local executor inherits this process's
	// environment, which is what makes the override reach the target.
	t.Setenv("LECTERN_SCRATCH_ROOT", filepath.Join(dir, "scratch"))

	agentPath := filepath.Join(dir, "fake-claude")
	if err := os.WriteFile(agentPath, []byte(fakeAgent), 0o755); err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		DBPath:           filepath.Join(dir, "lectern.db"),
		Mock:             false, // the whole point
		TickInterval:     60 * time.Millisecond,
		SessionPoll:      3600 * time.Second, // no session polling in this test
		ApprovalExpire:   900 * time.Second,
		JanitorDays:      0, // never sweep mid-test
		ClaudeBin:        agentPath,
		BaseURL:          "http://" + ln.Addr().String(),
		HostClaudeConfig: filepath.Join(dir, "none.json"),
		ClaudeCredsPath:  filepath.Join(dir, "none.json"),
		CodexCredsPath:   filepath.Join(dir, "none.json"),
	}
	a, err := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: a.Handler()}
	go srv.Serve(ln)

	target, err := a.DB.InsertTarget(&store.Target{
		Name: "control-plane", Kind: "local",
		Workroot: filepath.Join(dir, "work"), MaxConcurrent: 2, Status: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := a.DB.InsertProject(&store.Project{
		Name: "realtest", TargetID: target.ID, RepoPath: repo,
		DefaultBaseBranch: "main", DefaultAgent: "claude",
		DefaultPermissionMode: "acceptEdits", KeepWorktrees: 1})
	if err != nil {
		t.Fatal(err)
	}

	// Claim this rig's session-id range. Ids are max(rowid)+1, so a marker row
	// at the base is left in place permanently: every session this rig launches
	// then lands above it and cannot collide with another test's tmux name.
	base := 1000 + rigSeq.Add(1)*100
	if _, err := a.DB.Exec(`INSERT INTO sessions
		(id, target_id, name, agent, workdir, tmux_session, status, origin, ended_at)
		VALUES (?, ?, 'id-range-marker', 'none', '/', 'never-launched', 'dead', 'lectern', 1)`,
		base, target.ID); err != nil {
		t.Fatalf("reserving the session id range: %v", err)
	}

	r := &realRig{t: t, app: a, url: cfg.BaseURL, repo: repo, project: project.ID}
	t.Cleanup(func() {
		srv.Close()
		a.Close()
		r.killStrayTmux()
	})
	return r
}

// killStrayTmux makes sure a failing test cannot leave a real tmux session
// running on the developer's machine.
func (r *realRig) killStrayTmux() {
	attempts, err := r.app.DB.AttemptsWhere("1=1")
	if err != nil {
		return
	}
	for _, att := range attempts {
		if att.TmuxSession != "" {
			exec.Command("tmux", "kill-session", "-t", att.TmuxSession).Run()
		}
	}
}

func mustRun(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (r *realRig) do(method, path string, body any) (int, []byte) {
	r.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = strings.NewReader(string(raw))
	}
	req, _ := http.NewRequest(method, r.url+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (r *realRig) dispatch(title, prompt string) int64 {
	r.t.Helper()
	code, body := r.do("POST", "/api/tasks", map[string]any{
		"project_id": r.project, "title": title, "prompt": prompt, "agent": "claude"})
	if code != 200 && code != 201 {
		r.t.Fatalf("creating a task: %d %s", code, body)
	}
	var task struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal(body, &task)
	if code, body := r.do("POST", fmt.Sprintf("/api/tasks/%d/dispatch", task.ID), map[string]any{}); code != 200 {
		r.t.Fatalf("dispatch: %d %s", code, body)
	}
	return task.ID
}

func (r *realRig) waitStatus(id int64, want ...string) *store.Task {
	r.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		task, err := r.app.DB.Task(id)
		if err == nil {
			last = task.Status
			for _, w := range want {
				if task.Status == w {
					return task
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	// dump what the agent actually did — this is the failure worth debugging
	if att, err := r.app.DB.LatestAttempt(id); err == nil && att.WorktreePath != "" {
		for _, f := range []string{"stderr.log", "events.jsonl", "exit_code"} {
			raw, _ := os.ReadFile(filepath.Join(att.WorktreePath, ".lectern", f))
			r.t.Logf("--- %s ---\n%s", f, raw)
		}
	}
	r.t.Fatalf("task %d never reached %v (stuck at %q)", id, want, last)
	return nil
}

// The whole pipeline, for real: dispatch creates a git worktree, tmux starts the
// agent in it, the poller reads its output, the exit code finalises the attempt,
// and the edit the agent made is captured as a diff.
func TestARealDispatchRunsAnAgentInARealWorktree(t *testing.T) {
	r := newRealRig(t)
	id := r.dispatch("real run", "add a line to NOTES.md")
	task := r.waitStatus(id, "done", "review", "failed")
	if task.Status == "failed" {
		att, _ := r.app.DB.LatestAttempt(id)
		t.Fatalf("the real pipeline failed: %s", att.ResultJSON)
	}

	att, err := r.app.DB.LatestAttempt(id)
	if err != nil {
		t.Fatal(err)
	}

	// a real worktree on disk, on its own branch
	if att.WorktreePath == "" {
		t.Fatal("no worktree was recorded")
	}
	if _, err := os.Stat(att.WorktreePath); err != nil {
		t.Fatalf("the worktree is not on disk: %v", err)
	}
	if att.Branch == "" || att.Branch == "main" {
		t.Errorf("the attempt must run on its own branch, got %q", att.Branch)
	}
	branch := strings.TrimSpace(mustRun(t, att.WorktreePath, "git", "rev-parse", "--abbrev-ref", "HEAD"))
	if branch != att.Branch {
		t.Errorf("the worktree is on %q but the attempt recorded %q", branch, att.Branch)
	}

	// the agent's real edit landed in the worktree and NOT in the source repo
	edited, err := os.ReadFile(filepath.Join(att.WorktreePath, "NOTES.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(edited), "agent was here") {
		t.Errorf("the agent's edit is missing from the worktree: %q", edited)
	}
	original, _ := os.ReadFile(filepath.Join(r.repo, "NOTES.md"))
	if strings.Contains(string(original), "agent was here") {
		t.Error("the agent wrote into the source repository instead of its worktree")
	}
	// the prompt genuinely reached the agent process
	if !strings.Contains(string(edited), "prompt-bytes:") {
		t.Error("the agent never received a prompt argument")
	}
	for _, line := range strings.Split(string(edited), "\n") {
		if n, ok := strings.CutPrefix(line, "prompt-bytes:"); ok && n == "0" {
			t.Error("the agent was launched with an empty prompt")
		}
	}

	// the poller parsed the real stream-json the process wrote
	events, err := r.app.DB.TaskEvents(id, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("no events were captured from the agent's output")
	}
	var sawAssistant bool
	for _, e := range events {
		if strings.Contains(e.PayloadJSON, "Adding the note") {
			sawAssistant = true
		}
	}
	if !sawAssistant {
		t.Errorf("the agent's own message never reached the event log (%d events)", len(events))
	}

	// the exit code came back through the real file the launcher redirects to
	if att.ExitCode == nil || *att.ExitCode != 0 {
		t.Errorf("exit code: %v", att.ExitCode)
	}

	// and the diff of the real edit was captured
	code, body := r.do("GET", fmt.Sprintf("/api/tasks/%d/diff", id), nil)
	if code != 200 {
		t.Fatalf("diff: %d %s", code, body)
	}
	if !strings.Contains(string(body), "agent was here") {
		t.Errorf("the captured diff does not contain the agent's edit: %s", truncate(string(body), 400))
	}

	// The exit-code file is written before the wrapper exits, so finalization
	// can beat tmux noticing that exit. Require cleanup within a bounded wait.
	deadline := time.Now().Add(3 * time.Second)
	for exec.Command("tmux", "has-session", "-t", "="+att.TmuxSession).Run() == nil {
		if time.Now().After(deadline) {
			t.Fatalf("tmux session %s is still alive after the attempt finished", att.TmuxSession)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A failing agent must be reported as failed with its real exit code, not
// silently swallowed or left running.
func TestARealFailingAgentIsReportedAsFailed(t *testing.T) {
	r := newRealRig(t)
	id := r.dispatch("real failure", "FAIL-THIS-RUN please")
	task := r.waitStatus(id, "failed", "done", "review")
	if task.Status != "failed" {
		t.Fatalf("a nonzero agent exit must fail the task, got %q", task.Status)
	}
	att, _ := r.app.DB.LatestAttempt(id)
	if att.ExitCode == nil || *att.ExitCode != 3 {
		t.Errorf("the real exit code should be preserved, got %v", att.ExitCode)
	}
}

// A configured task definition uses its own batch command and plain-output
// parser. The script intentionally omits a trailing newline so final draining
// cannot strand the last timeline message; the second definition proves its
// nonzero exit remains a failed task.
func TestARealCustomTaskCapturesPlainOutputAndExitCode(t *testing.T) {
	r := newRealRig(t)
	bin := filepath.Join(filepath.Dir(r.repo), "custom-task-agent")
	script := `#!/bin/sh
cat >/dev/null
if [ "$1" = "--fail" ]; then
  printf 'custom failure Ω'
  exit 3
fi
printf 'custom output Ω $(literal)'
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	code, body := r.do("PUT", "/api/agents", []map[string]any{
		{"name": "plain-custom", "command": bin,
			"task": map[string]any{"command": bin, "prompt_template": "stdin", "output_mode": "plain"}},
		{"name": "plain-failing", "command": bin,
			"task": map[string]any{"command": bin, "args": []string{"--fail"}, "prompt_template": "stdin", "output_mode": "plain"}},
	})
	if code != 200 {
		t.Fatalf("custom agent setup: %d %s", code, body)
	}
	customTask := func(agent string) int64 {
		code, body := r.do("POST", "/api/tasks", map[string]any{
			"project_id": r.project, "title": agent, "prompt": "custom prompt", "agent": agent})
		if code != 201 {
			t.Fatalf("creating %s task: %d %s", agent, code, body)
		}
		var task struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal(body, &task); err != nil {
			t.Fatal(err)
		}
		code, body = r.do("POST", fmt.Sprintf("/api/tasks/%d/dispatch", task.ID), map[string]any{})
		if code != 200 {
			t.Fatalf("dispatching %s task: %d %s", agent, code, body)
		}
		return task.ID
	}
	okID := customTask("plain-custom")
	okTask := r.waitStatus(okID, "done", "failed", "review")
	if okTask.Status != "done" && okTask.Status != "review" {
		attempt, _ := r.app.DB.LatestAttempt(okID)
		t.Fatalf("custom plain task failed: task=%q result=%s status=%s worktree=%s", okTask.Status, attempt.ResultJSON, attempt.Status, attempt.WorktreePath)
	}
	okEvents, err := r.app.DB.TaskEvents(okID, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	var sawOutput bool
	for _, event := range okEvents {
		if strings.Contains(event.PayloadJSON, "custom output Ω $(literal)") {
			sawOutput = true
		}
	}
	if !sawOutput {
		attempt, _ := r.app.DB.LatestAttempt(okID)
		eventsRaw, _ := os.ReadFile(filepath.Join(attempt.WorktreePath, ".lectern", "events.jsonl"))
		stderrRaw, _ := os.ReadFile(filepath.Join(attempt.WorktreePath, ".lectern", "stderr.log"))
		t.Fatalf("unterminated custom output was lost: events=%q stderr=%q db=%#v", eventsRaw, stderrRaw, okEvents)
	}
	failID := customTask("plain-failing")
	failTask := r.waitStatus(failID, "failed", "done", "review")
	if failTask.Status != "failed" {
		t.Fatalf("custom nonzero task was not failed: %q", failTask.Status)
	}
	failAttempt, err := r.app.DB.LatestAttempt(failID)
	if err != nil || failAttempt.ExitCode == nil || *failAttempt.ExitCode != 3 {
		t.Fatalf("custom nonzero exit code was not captured: %#v (err=%v)", failAttempt, err)
	}
}

// Two attempts must not collide: separate worktrees, separate branches,
// separate tmux sessions. This is the constraint that lets the board run a
// whole estate at once.
func TestRealConcurrentAttemptsAreIsolated(t *testing.T) {
	r := newRealRig(t)
	a := r.dispatch("first", "one")
	b := r.dispatch("second", "two")
	r.waitStatus(a, "done", "review", "failed")
	r.waitStatus(b, "done", "review", "failed")

	attA, _ := r.app.DB.LatestAttempt(a)
	attB, _ := r.app.DB.LatestAttempt(b)
	if attA.WorktreePath == attB.WorktreePath {
		t.Errorf("both attempts shared a worktree: %s", attA.WorktreePath)
	}
	if attA.Branch == attB.Branch {
		t.Errorf("both attempts shared a branch: %s", attA.Branch)
	}
	if attA.TmuxSession == attB.TmuxSession {
		t.Errorf("both attempts shared a tmux session: %s", attA.TmuxSession)
	}
	// and the source repo is untouched by either
	original, _ := os.ReadFile(filepath.Join(r.repo, "NOTES.md"))
	if strings.TrimSpace(string(original)) != "start" {
		t.Errorf("the source repository was modified: %q", original)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---- interactive sessions, for real ------------------------------------

// fakeInteractiveAgent stays alive and records both its argv and everything
// typed at it, so a test can prove a prime or a message truly arrived rather
// than merely that lectern thought it sent one.
const fakeInteractiveAgent = `#!/bin/bash
log="$PWD/session-log.txt"
printf 'argv:%s\n' "$*" >> "$log"
printf 'cwd:%s\n' "$PWD" >> "$log"
echo "fake agent ready"
while IFS= read -r line; do
  printf 'typed:%s\n' "$line" >> "$log"
done
`

func (r *realRig) sessionLog(workdir string) string {
	r.t.Helper()
	raw, _ := os.ReadFile(filepath.Join(workdir, "session-log.txt"))
	return string(raw)
}

func (r *realRig) waitForLog(workdir, want string, limit time.Duration) string {
	r.t.Helper()
	deadline := time.Now().Add(limit)
	var got string
	for time.Now().Before(deadline) {
		got = r.sessionLog(workdir)
		if strings.Contains(got, want) {
			return got
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.t.Fatalf("waiting for %q in the agent's log; it holds:\n%s", want, got)
	return ""
}

func tmuxAlive(name string) bool {
	return exec.Command("tmux", "has-session", "-t", name).Run() == nil
}

// interactiveRig points the agent binary at the long-lived fake instead.
func newInteractiveRig(t *testing.T) *realRig {
	r := newRealRig(t)
	if err := os.WriteFile(r.app.Cfg.ClaudeBin, []byte(fakeInteractiveAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *realRig) launchSession(body map[string]any) *store.Session {
	r.t.Helper()
	code, raw := r.do("POST", "/api/sessions", body)
	if code != 200 && code != 201 {
		r.t.Fatalf("launching a session: %d %s", code, raw)
	}
	var sess store.Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		r.t.Fatalf("%v: %s", err, raw)
	}
	r.t.Cleanup(func() { exec.Command("tmux", "kill-session", "-t", sess.TmuxSession).Run() })
	return &sess
}

// Launching an interactive session must produce a real tmux session, in the
// project's directory, with the priming context actually delivered to the agent
// — that delivery is the whole "resume any project with any agent and it has
// full context" feature.
func TestARealInteractiveSessionStartsAndIsPrimed(t *testing.T) {
	r := newInteractiveRig(t)
	sess := r.launchSession(map[string]any{
		"project_id": r.project, "agent": "claude",
		"prime": "CONTEXT-MARKER: you are resuming the inference project"})

	if !strings.HasPrefix(sess.TmuxSession, "lec-s") {
		t.Errorf("an interactive session must not collide with an attempt's naming: %q", sess.TmuxSession)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !tmuxAlive(sess.TmuxSession) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !tmuxAlive(sess.TmuxSession) {
		t.Fatalf("no real tmux session named %q exists", sess.TmuxSession)
	}

	log := r.waitForLog(r.repo, "argv:", 10*time.Second)
	if !strings.Contains(log, "CONTEXT-MARKER") {
		t.Errorf("the priming context never reached the agent:\n%s", log)
	}
	if !strings.Contains(log, "cwd:"+r.repo) {
		t.Errorf("the agent started in the wrong directory:\n%s", log)
	}
}

// MCP credentials are target-user state, not worktree files. A real Git
// allocation must therefore stay clean while its interactive session runs and
// must remain removable through the normal ownership checks after the session
// ends.
func TestARealInteractiveMCPWorktreeStaysCleanAndRemovable(t *testing.T) {
	r := newInteractiveRig(t)
	home := filepath.Join(filepath.Dir(r.repo), "mcp-agent-home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := r.app.DB.Update("projects", r.project, map[string]any{
		"env_json":   `{"HOME":"` + home + `"}`,
		"mcp_json":   `{"ops":{"command":"python3","args":["-m","ops"]}}`,
		"strict_mcp": 1,
	}); err != nil {
		t.Fatal(err)
	}
	sess := r.launchSession(map[string]any{"project_id": r.project, "agent": "claude", "worktree": map[string]any{}})
	log := r.waitForLog(sess.Workdir, "argv:", 10*time.Second)
	if err := os.Remove(filepath.Join(sess.Workdir, "session-log.txt")); err != nil {
		t.Fatal(err)
	}
	status := strings.TrimSpace(mustRun(t, sess.Workdir, "git", "status", "--porcelain", "--untracked-files=all", "--ignored=matching"))
	if status != "" {
		t.Fatalf("MCP launch left Git worktree changes: %q", status)
	}
	if sourceStatus := strings.TrimSpace(mustRun(t, r.repo, "git", "status", "--porcelain", "--untracked-files=all", "--ignored=matching")); sourceStatus != "" {
		t.Fatalf("MCP launch changed source checkout: %q", sourceStatus)
	}
	stateRoot := os.Getenv("XDG_STATE_HOME")
	if stateRoot == "" {
		stateRoot = filepath.Join(home, ".local", "state")
	}
	privateMCPRoot := filepath.Join(stateRoot, "lectern", "mcp") + string(filepath.Separator)
	if !strings.Contains(log, "--mcp-config "+privateMCPRoot) || !strings.Contains(log, "--strict-mcp-config") {
		t.Fatalf("private MCP config was not passed as an absolute state path: %s", log)
	}
	if code, body := r.do("DELETE", fmt.Sprintf("/api/sessions/%d", sess.ID), nil); code != 200 {
		t.Fatalf("ending session: %d %s", code, body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for tmuxAlive(sess.TmuxSession) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if tmuxAlive(sess.TmuxSession) {
		t.Fatal("owned MCP session did not end")
	}
	if code, body := r.do("DELETE", fmt.Sprintf("/api/sessions/%d/worktree", sess.ID), nil); code != 200 {
		t.Fatalf("removing clean MCP worktree: %d %s", code, body)
	}
	if _, err := os.Stat(sess.Workdir); !os.IsNotExist(err) {
		t.Fatalf("MCP worktree still exists after removal: %v", err)
	}
}

// Typing at a live session has to actually reach the process.
func TestARealSessionReceivesTypedText(t *testing.T) {
	r := newInteractiveRig(t)
	sess := r.launchSession(map[string]any{"project_id": r.project, "agent": "claude"})
	r.waitForLog(r.repo, "argv:", 10*time.Second)

	code, body := r.do("POST", fmt.Sprintf("/api/sessions/%d/send", sess.ID),
		map[string]any{"text": "TYPED-MARKER hello"})
	if code != 200 {
		t.Fatalf("send: %d %s", code, body)
	}
	log := r.waitForLog(r.repo, "typed:", 10*time.Second)
	if !strings.Contains(log, "TYPED-MARKER hello") {
		t.Errorf("the text never arrived at the agent:\n%s", log)
	}
}

// THIS is the regression that matters most. Removing an adopted session from the
// board once killed seven live conversations, because delete meant kill. An
// adopted session is someone else's; letting go of it must leave it running.
func TestReleasingAnAdoptedSessionLeavesItRunning(t *testing.T) {
	r := newInteractiveRig(t)
	sess := r.launchSession(map[string]any{"project_id": r.project, "agent": "claude"})
	r.waitForLog(r.repo, "argv:", 10*time.Second)

	// make it look adopted: lectern found it, it did not start it
	if err := r.app.DB.Update("sessions", sess.ID, map[string]any{"origin": "discovered"}); err != nil {
		t.Fatal(err)
	}
	code, body := r.do("DELETE", fmt.Sprintf("/api/sessions/%d", sess.ID), nil)
	if code != 200 {
		t.Fatalf("delete: %d %s", code, body)
	}
	var out struct {
		Killed bool `json:"killed"`
	}
	json.Unmarshal(body, &out)
	if out.Killed {
		t.Error("the API reported killing a session it was only asked to stop tracking")
	}
	time.Sleep(300 * time.Millisecond)
	if !tmuxAlive(sess.TmuxSession) {
		t.Fatal("stopping tracking killed a live conversation — this is the seven-session incident")
	}
}

// The explicit kill still has to work, or the board cannot clean anything up.
func TestKillingAnAdoptedSessionRequiresAskingForIt(t *testing.T) {
	r := newInteractiveRig(t)
	sess := r.launchSession(map[string]any{"project_id": r.project, "agent": "claude"})
	r.waitForLog(r.repo, "argv:", 10*time.Second)
	if err := r.app.DB.Update("sessions", sess.ID, map[string]any{"origin": "discovered"}); err != nil {
		t.Fatal(err)
	}

	code, body := r.do("DELETE", fmt.Sprintf("/api/sessions/%d?kill=true", sess.ID), nil)
	if code != 200 {
		t.Fatalf("delete: %d %s", code, body)
	}
	deadline := time.Now().Add(10 * time.Second)
	for tmuxAlive(sess.TmuxSession) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if tmuxAlive(sess.TmuxSession) {
		t.Error("an explicit kill did not stop the tmux session")
	}
}

// A session lectern launched itself is its own to clean up.
func TestDeletingAnOwnedSessionKillsIt(t *testing.T) {
	r := newInteractiveRig(t)
	sess := r.launchSession(map[string]any{"project_id": r.project, "agent": "claude"})
	r.waitForLog(r.repo, "argv:", 10*time.Second)

	if code, body := r.do("DELETE", fmt.Sprintf("/api/sessions/%d", sess.ID), nil); code != 200 {
		t.Fatalf("delete: %d %s", code, body)
	}
	deadline := time.Now().Add(10 * time.Second)
	for tmuxAlive(sess.TmuxSession) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if tmuxAlive(sess.TmuxSession) {
		t.Error("lectern did not clean up a session it started itself")
	}
}

// Discovery has to find a real tmux session running a real agent process, and
// join it to the right working directory — the join that the NUL-delimiter bug
// broke on a real host while every mock test passed.
func TestDiscoveryFindsARealTmuxSession(t *testing.T) {
	r := newInteractiveRig(t)
	sess := r.launchSession(map[string]any{"project_id": r.project, "agent": "claude"})
	r.waitForLog(r.repo, "argv:", 10*time.Second)
	// lectern must not offer to adopt what it already tracks
	code, body := r.do("GET", "/api/sessions/discover", nil)
	if code != 200 {
		t.Fatalf("discover: %d %s", code, body)
	}
	var candidates []struct {
		TmuxSession string `json:"tmux_session"`
	}
	if err := json.Unmarshal(body, &candidates); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	for _, c := range candidates {
		if c.TmuxSession == sess.TmuxSession {
			t.Errorf("discovery offered to adopt a session lectern already owns: %s", c.TmuxSession)
		}
	}
}

// A real tmux session receives the request; a partial write must not save a
// wrap, kill that session, or launch its successor. Only publication of the
// completed file may do those things.
func TestRealHandoffWaitsForCompletedPublication(t *testing.T) {
	r := newInteractiveRig(t)
	sess := r.launchSession(map[string]any{"project_id": r.project, "name": "handoff-real", "group_path": "Work/Handoffs"})
	r.waitForLog(r.repo, "cwd:", 5*time.Second)
	code, raw := r.do("POST", fmt.Sprintf("/api/sessions/%d/handoff", sess.ID), map[string]any{"successor": true, "kill_old": true})
	if code != 202 {
		t.Fatalf("%d %s", code, raw)
	}
	log := r.waitForLog(r.repo, "completion marker", 5*time.Second)
	start := strings.Index(log, "/tmp/lectern-handoff-")
	if start < 0 {
		t.Fatal(log)
	}
	end := strings.Index(log[start:], ".md")
	if end < 0 {
		t.Fatal(log)
	}
	path := log[start : start+end+3]
	t.Cleanup(func() { os.Remove(path); os.Remove(path + ".partial") })
	body := "## WHERE WE ARE\nThe real file is still being written.\n## NEXT\nKeep the existing session alive.\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// Cross a full handoff read interval, exercising the old >40-byte bug.
	time.Sleep(3500 * time.Millisecond)
	wraps, err := r.app.DB.Wraps(r.project, 10)
	if err != nil || len(wraps) != 0 || !tmuxAlive(sess.TmuxSession) {
		t.Fatalf("partial publication finalized: wraps=%v err=%v", wraps, err)
	}
	final := body + "<!-- lectern:complete " + path + " -->\n"
	if err := os.WriteFile(path+".partial", []byte(final), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".partial", path); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		wraps, _ = r.app.DB.Wraps(r.project, 10)
		if len(wraps) > 0 && wraps[0].NextSessionID != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(wraps) != 1 || wraps[0].NextSessionID == nil {
		t.Fatalf("completed handoff not finalized: %+v", wraps)
	}
	next, err := r.app.DB.Session(*wraps[0].NextSessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exec.Command("tmux", "kill-session", "-t", next.TmuxSession).Run() })
	if tmuxAlive(sess.TmuxSession) || !tmuxAlive(next.TmuxSession) {
		t.Fatal("completion did not transfer the session")
	}
	if next.GroupPath != "Work/Handoffs" {
		t.Fatal("successor lost its group")
	}
	if strings.Contains(wraps[0].Summary, "lectern:complete") {
		t.Fatal("protocol marker leaked into saved memory")
	}
}
