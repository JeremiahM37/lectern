package api_test

// These tests use the real SSH executor against loopback.  The target is
// isolated by HOME and TMUX_TMPDIR, and every agent process is a fixture script;
// no host agent credentials or normal tmux server are involved.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

type remoteAcceptanceRig struct {
	t        *testing.T
	h        *harness
	ex       executor.Executor
	target   *store.Target
	root     string
	home     string
	tmuxDir  string
	tmuxName string
	knownMCP string
	ssh      *testutil.SSHFixture
}

func newRemoteAcceptanceRig(t *testing.T, h *harness) *remoteAcceptanceRig {
	t.Helper()
	dir := t.TempDir()
	sshFixture := testutil.NewSSHFixture(t)
	r := &remoteAcceptanceRig{t: t, h: h,
		root: filepath.Join(dir, "repo"), home: filepath.Join(dir, "home"),
		tmuxDir: filepath.Join(dir, "tmux"), tmuxName: "remote-accept-" + strings.ReplaceAll(t.Name(), "/", "-"), ssh: sshFixture}
	for _, p := range []string{r.root, r.home, r.tmuxDir, filepath.Join(dir, "work")} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	prefix := fmt.Sprintf("env HOME=%s SHELL=/bin/bash TMUX=%s TMUX_TMPDIR=%s GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=safe.directory GIT_CONFIG_VALUE_0=%s sh -c",
		executor.ShellQuote(r.home), executor.ShellQuote(""), executor.ShellQuote(r.tmuxDir), executor.ShellQuote("*"))
	target, err := h.App.DB.InsertTarget(&store.Target{
		Name: "loopback SSH acceptance", Kind: "ssh", Host: "127.0.0.1", User: "test", Port: sshFixture.Port,
		KeyPath: sshFixture.KeyPath, Workroot: filepath.Join(dir, "work"),
		MaxConcurrent: 2, Status: "ok", CommandPrefix: prefix,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.target = target
	r.ex, err = h.App.Reg.For(target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// This private socket can contain only the named fixture session.
		r.ex.Run(ctx, "tmux -f /dev/null kill-session -t ="+executor.ShellQuote(r.tmuxName)+" || true", executor.RunOpts{Timeout: 5})
		// The SSH login is root while the test process owns t.TempDir; remove
		// only exact sessions from this fixture's private socket.
		r.ex.Run(ctx, "socket=$(printf '%s/tmux-%s/default' \"$TMUX_TMPDIR\" \"$(id -u)\"); sessions=$(tmux -S \"$socket\" list-sessions -F '#{session_name}' 2>/dev/null || true); while IFS= read -r name; do [ -n \"$name\" ] && tmux -S \"$socket\" kill-session -t \"=$name\" || true; done <<EOF\n$sessions\nEOF", executor.RunOpts{Timeout: 5})
		for _, path := range []string{r.root, r.home, r.tmuxDir, filepath.Join(filepath.Dir(r.root), "work")} {
			r.ex.Run(ctx, "chmod -R a+rwX "+executor.ShellQuote(path)+" || true", executor.RunOpts{Timeout: 5})
		}
		r.ex.Close()
	})
	r.run("mkdir -p %s %s && git init -q -b main %s", r.root, filepath.Join(r.root, ".lectern"), r.root)
	r.run("git -C %s config user.email acceptance@example.invalid && git -C %s config user.name acceptance && touch %s/README.md && git -C %s add -A && git -C %s commit -qm initial", r.root, r.root, r.root, r.root, r.root)
	r.run("tmux -f /dev/null new-session -d -s %s -c %s bash --norc", r.tmuxName, r.root)
	return r
}

func (r *remoteAcceptanceRig) run(format string, args ...string) executor.Result {
	r.t.Helper()
	cmd := format
	for _, arg := range args {
		cmd = strings.Replace(cmd, "%s", executor.ShellQuote(arg), 1)
	}
	result, err := r.ex.Run(context.Background(), cmd, executor.RunOpts{Timeout: 20})
	if err != nil {
		r.t.Fatalf("remote command: %v", err)
	}
	if !result.OK() {
		r.t.Fatalf("remote command failed (%d): %s\n%s", result.RC, result.Stdout, result.Stderr)
	}
	return result
}

func (r *remoteAcceptanceRig) read(path string) []byte {
	r.t.Helper()
	b, err := r.ex.ReadFile(context.Background(), path, 0)
	if err != nil {
		r.t.Fatalf("read remote %s: %v", path, err)
	}
	return b
}

func (r *remoteAcceptanceRig) waitFile(path string, want []byte) {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got := r.read(path); bytes.Equal(got, want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.t.Fatalf("remote file %s did not contain expected bytes", path)
}

func TestRealLoopbackSSHAttachmentContextAndDelivery(t *testing.T) {
	requireRealTools(t)
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	r := newRemoteAcceptanceRig(t, h)
	sess, err := h.App.DB.InsertSession(&store.Session{TargetID: r.target.ID, Name: "remote upload", Agent: "claude", Workdir: r.root, TmuxSession: r.tmuxName, Status: "idle", Origin: "adopted"})
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("%PDF-1.4\n\x00\xff\n"), 5000)
	code, attached := upload(t, fmt.Sprintf("%s/api/sessions/%d/attachments", h.URL, sess.ID), "context-proof.pdf", data, "")
	if code != 201 {
		t.Fatalf("upload: %d %+v", code, attached)
	}
	path := attached.str("path")
	if !strings.HasPrefix(path, filepath.Join(r.root, ".lectern", "context")+"/") {
		t.Fatalf("attachment escaped remote context: %s", path)
	}
	if got := r.read(path); !bytes.Equal(got, data) {
		t.Fatalf("remote bytes changed: got %d want %d", len(got), len(data))
	}
	stat := r.run("stat -c %a %s", path)
	if strings.TrimSpace(stat.Stdout) != "600" {
		t.Fatalf("remote attachment mode: %q", stat.Stdout)
	}
	if !r.run("git -C %s check-ignore -q %s", r.root, path).OK() {
		t.Fatal("remote attachment is not ignored")
	}
	received := filepath.Join(r.root, "received.pdf")
	h.post(fmt.Sprintf("/api/sessions/%d/send", sess.ID), obj{"text": "cat -- " + executor.ShellQuote(path) + " > " + executor.ShellQuote(received)}, 200)
	r.waitFile(received, data)
}

func TestRealLoopbackSSHRoutineTakeoverPreservesIdentityAndMCP(t *testing.T) {
	requireRealTools(t)
	agentPath := filepath.Join(t.TempDir(), "fake-claude")
	h := newHarness(t, func(c *config.Config) { c.Mock = false; c.ClaudeBin = agentPath })
	r := newRemoteAcceptanceRig(t, h)
	if err := os.WriteFile(agentPath, []byte(takeoverAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := h.App.DB.InsertProject(&store.Project{
		Name: "remote takeover", TargetID: r.target.ID, RepoPath: r.root,
		DefaultBaseBranch: "main", DefaultAgent: "claude", KeepWorktrees: 1,
		EnvJSON: fmt.Sprintf(`{"HOME":%q}`, r.home), MCPJSON: `{"old_tools":{"command":"old-mcp"}}`, StrictMCP: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	projectID := project.ID
	created := h.post("/api/routines", obj{"name": "remote takeover", "prompt": "Keep the running work", "project_ids": []int64{projectID}, "agent": "claude", "dispatch": true}, 201)
	routineID := int64(created.num("id"))
	run := h.post(fmt.Sprintf("/api/routines/%d/run", routineID), obj{}, 200)
	rawTasks, ok := run["tasks"].([]any)
	if !ok || len(rawTasks) != 1 {
		t.Fatalf("run response: %v", run)
	}
	taskID := int64(rawTasks[0].(float64))
	deadline := time.Now().Add(15 * time.Second)
	var att *store.Attempt
	for time.Now().Before(deadline) {
		candidate, e := h.App.DB.LatestAttempt(taskID)
		if e == nil {
			att = candidate
			if candidate.Status == "running" {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if att == nil || att.Status != "running" {
		if att == nil {
			t.Fatal("remote routine never created an attempt")
		}
		t.Fatalf("remote routine never reached running: status=%q result=%s exit=%v worktree=%s", att.Status, att.ResultJSON, att.ExitCode, att.WorktreePath)
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(r.read(filepath.Join(att.WorktreePath, "background-pid"))) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(r.read(filepath.Join(att.WorktreePath, "background-pid"))) == 0 {
		for _, name := range []string{"stderr.log", "events.jsonl", "exit_code", "prompt.md"} {
			t.Logf("remote %s: %s", name, string(r.read(filepath.Join(att.WorktreePath, ".lectern", name))))
		}
		listing, _ := r.ex.Run(context.Background(), "tmux -f /dev/null list-sessions || true", executor.RunOpts{Timeout: 5})
		t.Logf("remote tmux: %s", listing.Stdout)
		t.Fatal("remote background agent did not start")
	}
	staged, err := h.App.DB.Attempt(att.ID)
	if err != nil {
		t.Fatal(err)
	}
	if staged.MCPSnapshot != 1 || !strings.Contains(staged.MCPJSON, "old_tools") {
		t.Fatalf("attempt did not capture original MCP policy: snapshot=%d mcp=%s", staged.MCPSnapshot, staged.MCPJSON)
	}
	if err := h.App.DB.Update("projects", projectID, map[string]any{"mcp_json": `{"new_tools":{"command":"new-mcp"}}`}); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/tasks/%d/takeover", taskID)
	if code := h.status("POST", path, obj{}); code != 202 {
		t.Fatalf("takeover: %d", code)
	}
	tr := waitRemoteTakeover(t, h, taskID)
	sess, err := h.App.DB.Session(*tr.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Workdir != att.WorktreePath {
		t.Fatalf("takeover changed workdir: %s != %s", sess.Workdir, att.WorktreePath)
	}
	logPath := filepath.Join(sess.Workdir, "session-log.txt")
	deadline = time.Now().Add(10 * time.Second)
	var log string
	for time.Now().Before(deadline) {
		log = string(r.read(logPath))
		if strings.Contains(log, "argv:") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(log, "--resume takeover-conversation-123") || strings.Contains(log, "--continue") {
		t.Fatalf("wrong exact resume in remote log: %s", log)
	}
	fields := strings.Fields(log)
	found := false
	for i := range fields {
		if fields[i] == "--mcp-config" && i+1 < len(fields) {
			mcp := string(r.read(fields[i+1]))
			if !strings.Contains(mcp, "old_tools") || strings.Contains(mcp, "new_tools") {
				t.Fatalf("MCP snapshot changed: %s", mcp)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("remote takeover omitted MCP config: %s", log)
	}
	// A repeated operator click after completion is idempotent and must reuse
	// the same session rather than launch another process.
	if code := h.status("POST", path, obj{}); code != 202 {
		t.Fatalf("repeat takeover: %d", code)
	}
	time.Sleep(200 * time.Millisecond)
	log = string(r.read(logPath))
	if strings.Count(log, "argv:") != 1 {
		t.Fatalf("duplicate remote interactive launch: %s", log)
	}
	if code := h.status("POST", fmt.Sprintf("/api/sessions/%d/send", sess.ID), obj{"text": "Now steer remotely"}); code != 200 {
		t.Fatalf("send: %d", code)
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(string(r.read(logPath)), "typed:Now steer remotely") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("remote takeover session did not receive new input")
}

func waitRemoteTakeover(t *testing.T, h *harness, taskID int64) *store.Takeover {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		tr, err := h.App.DB.Takeover(taskID)
		if err != nil {
			t.Fatal(err)
		}
		if tr != nil && tr.Status == "ready" {
			return tr
		}
		if tr != nil && tr.Status == "failed" {
			t.Fatalf("remote takeover failed: %s", tr.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("remote takeover did not complete")
	return nil
}
