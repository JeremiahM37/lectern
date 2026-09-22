package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/internal/config"
	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/store"
	"github.com/JeremiahM37/lectern/internal/testutil"
)

var lifecycleRigSeq atomic.Int64

// TestInteractiveMCPManagerLaunchLifecycle proves the API and Manager launch
// the same project MCP configuration through both real executors. It exercises
// all native continuation forms for both supported agents, while the wrapper
// process records the argv and environment that actually reached the target.
// The native histories are fixtures: no model or provider authentication is
// involved.
func TestInteractiveMCPManagerLaunchLifecycle(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		for _, targetKind := range []string{"local", "ssh"} {
			t.Run(agent+"-"+targetKind, func(t *testing.T) {
				requireRealTools(t)
				h := newHarness(t, func(c *config.Config) { c.Mock = false })
				root := t.TempDir()
				captureDir := filepath.Join(root, "captures")
				if err := os.Mkdir(captureDir, 0700); err != nil {
					t.Fatal(err)
				}
				wrapper := writeLifecycleWrapper(t, root)
				repo := filepath.Join(root, "repo")
				// Keep the selected source outside the repository. The launch
				// harness can then prove the destination is a symlink materialized
				// into the exact cwd, while the native-source preservation case is
				// covered independently in internal/skills.
				skillDir := filepath.Join(root, "skill-source", "lifecycle")
				if err := os.MkdirAll(skillDir, 0700); err != nil {
					t.Fatal(err)
				}
				writeMode(t, filepath.Join(skillDir, "SKILL.md"), []byte("name: lifecycle\ndescription: real launch fixture\n"), 0600)
				if err := os.MkdirAll(filepath.Join(repo, ".lectern"), 0700); err != nil {
					t.Fatal(err)
				}
				if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
					t.Fatalf("git init: %v %s", err, out)
				}
				if err := os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("lifecycle seed\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := exec.Command("git", "-C", repo, "add", "seed.txt").Run(); err != nil {
					t.Fatal(err)
				}
				if out, err := exec.Command("git", "-C", repo, "-c", "user.name=Lectern lifecycle", "-c", "user.email=lectern@example.invalid", "commit", "-qm", "seed").CombinedOutput(); err != nil {
					t.Fatalf("commit: %v %s", err, out)
				}
				foreign := []byte(`{"foreign":true,"keep":"exact"}`)
				foreignPath := filepath.Join(repo, ".lectern", "mcp.json")
				writeMode(t, foreignPath, foreign, 0600)
				instructionPath := filepath.Join(repo, "AGENTS.md")
				if agent == "claude" {
					instructionPath = filepath.Join(repo, "CLAUDE.md")
				}
				instruction := []byte("operator instructions must survive every continuation\n")
				writeMode(t, instructionPath, instruction, 0640)

				home := filepath.Join(root, agent+"-home")
				if err := os.Mkdir(home, 0700); err != nil {
					t.Fatal(err)
				}
				configPath, _ := privateConfigFixture(t, agent, home)
				cid := "11111111-1111-4111-8111-111111111111"
				writeNativeLifecycleHistory(t, agent, home, repo, cid)
				before := snapshotFiles(t, []string{foreignPath, instructionPath, configPath,
					filepath.Join(home, "auth.json"),
					filepath.Join(home, "sessions", cid+".jsonl"),
					filepath.Join(home, "projects", claudeProjectSlug(repo), cid+".jsonl"),
					filepath.Join(skillDir, "SKILL.md")})

				var remoteTmuxDir string
				if targetKind == "ssh" {
					var err error
					remoteTmuxDir, err = os.MkdirTemp("/tmp", "lectern-ssh-tmux-")
					if err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(remoteTmuxDir, 0700); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.RemoveAll(remoteTmuxDir) })
				}
				target := insertLifecycleTarget(t, h, targetKind, remoteTmuxDir)
				base := 10000 + lifecycleRigSeq.Add(1)*100
				if _, err := h.App.DB.Exec(`INSERT INTO sessions
					(id, target_id, name, agent, workdir, tmux_session, status, origin, created_at, updated_at, ended_at)
					VALUES (?, ?, 'lifecycle-id-range', 'none', '/', 'never-launched', 'dead', 'lectern', ?, ?, ?)`,
					base, target.ID, store.Now(), store.Now(), store.Now()); err != nil {
					t.Fatal(err)
				}
				targetExecutor, err := h.App.Reg.For(target)
				if err != nil {
					t.Fatal(err)
				}
				if targetKind == "ssh" {
					if result, configErr := targetExecutor.Run(context.Background(), "git config --global --add safe.directory "+shellQuoteForTest(repo), executor.RunOpts{Timeout: 20}); configErr != nil || !result.OK() {
						t.Fatalf("configure remote git safe.directory: %v", configErr)
					}
					// The remote executor runs as root while the test process owns
					// the temp tree. Remove only this test's private runtime before
					// t.TempDir attempts its local cleanup.
					t.Cleanup(func() {
						ex, err := h.App.Reg.For(target)
						if err == nil {
							_, _ = ex.Run(context.Background(), "rm -rf -- "+shellQuoteForTest(filepath.Join(repo, ".lectern", "interactive"))+" "+shellQuoteForTest(filepath.Join(home, ".local"))+"; chmod -R a+rwx -- "+shellQuoteForTest(filepath.Join(repo, ".claude"))+" "+shellQuoteForTest(filepath.Join(repo, ".agents"))+" "+shellQuoteForTest(filepath.Join(filepath.Dir(repo), ".lectern-worktrees"))+" 2>/dev/null || true; git config --global --unset-all safe.directory "+shellQuoteForTest(repo)+" 2>/dev/null || true", executor.RunOpts{Timeout: 20})
						}
					})
				}
				t.Cleanup(func() {
					for id := base + 1; id <= base+10; id++ {
						name := fmt.Sprintf("lec-s%d", id)
						check, checkErr := targetExecutor.Run(context.Background(), "tmux has-session -t "+shellQuoteForTest(name), executor.RunOpts{Timeout: 20})
						if checkErr == nil && check.OK() {
							_, _ = targetExecutor.Run(context.Background(), "tmux kill-session -t "+shellQuoteForTest(name), executor.RunOpts{Timeout: 20})
						}
					}
				})
				envName := "CODEX_HOME"
				if agent == "claude" {
					envName = "CLAUDE_CONFIG_DIR"
				}
				extra := obj{
					"name": agent, "command": wrapper,
					"env":            obj{"CAPTURE_DIR": captureDir, "CAPTURE_SKILL_REL": filepath.ToSlash(filepath.Join(map[string]string{"claude": ".claude", "codex": ".agents"}[agent], "skills", "lifecycle")), "HOME": home, envName: home},
					"resume_args":    []string{"resume", "--last"},
					"resume_id_args": resumeIDArgs(agent),
					"fork_args":      forkArgs(agent),
				}
				h.decode("PUT", "/api/agents", []obj{extra}, 200, nil)
				project, err := h.App.DB.InsertProject(&store.Project{
					Name: "lifecycle-" + agent + "-" + targetKind, TargetID: target.ID,
					RepoPath: repo, DefaultAgent: agent,
					SkillSourcesJSON: store.J([]string{filepath.Dir(skillDir)}),
					MCPJSON:          store.J(obj{"ops_tools": obj{"command": "python3", "args": []string{"-m", "ops"}}}),
				})
				if err != nil {
					t.Fatal(err)
				}
				var available obj
				h.decode("GET", fmt.Sprintf("/api/skills?project_id=%d&agent=%s", project.ID, agent), nil, 200, &available)
				var skillID string
				for _, raw := range available["skills"].([]any) {
					candidate := raw.(map[string]any)
					if candidate["source_path"] == skillDir {
						skillID, _ = candidate["id"].(string)
						break
					}
				}
				if skillID == "" {
					t.Fatalf("lifecycle skill was not discoverable: %v", available)
				}
				h.decode("POST", fmt.Sprintf("/api/projects/%d/skills", project.ID), obj{"agent": agent, "skill_id": skillID}, 201, nil)

				fresh := h.session(obj{"project_id": project.ID, "agent": agent, "name": "fresh"})
				waitCapture(t, captureDir, 0)
				freshCapture := readCapture(t, filepath.Join(captureDir, "0.log"))
				assertLifecycleEnvironment(t, freshCapture, agent, home)
				assertSkillLaunch(t, freshCapture, repo, skillDir)
				assertFreshArgs(t, agent, freshCapture.args)
				var freshMCPPath string
				if agent == "claude" && len(freshCapture.args) > 1 {
					freshMCPPath = freshCapture.args[1]
				}
				assertProjectFiles(t, agent, repo, home, before, foreign, instruction, targetExecutor, freshMCPPath)

				conversations := fmt.Sprintf("/api/sessions/%d/conversations", fresh.id())
				var listing obj
				h.decode("GET", conversations, nil, 200, &listing)
				if !strings.Contains(fmt.Sprint(listing), cid) {
					t.Fatalf("native fixture ID missing from API listing: %v", listing)
				}
				fork := h.post(fmt.Sprintf("/api/sessions/%d/fork", fresh.id()),
					obj{"conversation_id": cid, "name": "forked", "worktree": obj{"branch": "lifecycle-fork"}}, 201)
				waitCapture(t, captureDir, 1)
				forkCapture := readCapture(t, filepath.Join(captureDir, "1.log"))
				assertLifecycleEnvironment(t, forkCapture, agent, home)
				assertSkillLaunch(t, forkCapture, fork.str("workdir"), skillDir)
				assertContinuationArgs(t, agent, forkCapture.args, cid, true)
				killLifecycleSession(t, h, fork)
				h.decode("DELETE", fmt.Sprintf("/api/sessions/%d/worktree", fork.id()), nil, 200, nil)

				killLifecycleSession(t, h, fresh)
				resumed := h.post(fmt.Sprintf("/api/sessions/%d/resume", fresh.id()),
					obj{"conversation_id": cid, "name": "resumed"}, 201)
				if resumed.str("resume_id") != cid {
					t.Fatalf("resume ID was not persisted: %v", resumed)
				}
				waitCapture(t, captureDir, 2)
				resumeCapture := readCapture(t, filepath.Join(captureDir, "2.log"))
				assertLifecycleEnvironment(t, resumeCapture, agent, home)
				assertSkillLaunch(t, resumeCapture, repo, skillDir)
				assertContinuationArgs(t, agent, resumeCapture.args, cid, false)
				killLifecycleSession(t, h, resumed)

				var resumeMCPPath string
				if agent == "claude" && len(resumeCapture.args) > 1 {
					resumeMCPPath = resumeCapture.args[1]
				}
				assertProjectFiles(t, agent, repo, home, before, foreign, instruction, targetExecutor, resumeMCPPath)
			})
		}
	}
}

func insertLifecycleTarget(t *testing.T, h *harness, kind, tmuxDir string) *store.Target {
	t.Helper()
	if kind == "local" {
		target, err := h.App.DB.InsertTarget(&store.Target{Name: "lifecycle local", Kind: "local"})
		if err != nil {
			t.Fatal(err)
		}
		return target
	}
	prefix := ""
	if tmuxDir != "" {
		prefix = "mkdir -m 700 -p " + shellQuoteForTest(tmuxDir) + " && env SHELL=/bin/bash TMUX_TMPDIR=" + shellQuoteForTest(tmuxDir) + " sh -c"
	}
	sshFixture := testutil.NewSSHFixture(t)
	target, err := h.App.DB.InsertTarget(&store.Target{
		Name: "lifecycle ssh", Kind: "ssh", Host: "127.0.0.1", User: "test", Port: sshFixture.Port,
		KeyPath: sshFixture.KeyPath, CommandPrefix: prefix,
	})
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func writeLifecycleWrapper(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "agent-wrapper.sh")
	body := `#!/bin/sh
set -eu
n=$(find "$CAPTURE_DIR" -maxdepth 1 -name '*.log' -type f | wc -l)
out="$CAPTURE_DIR/$n.log"
tmp="$CAPTURE_DIR/.$n.capture.$$"
{
  printf 'CLAUDE_CONFIG_DIR=%s\n' "${CLAUDE_CONFIG_DIR-}"
  printf 'CODEX_HOME=%s\n' "${CODEX_HOME-}"
  printf 'PWD=%s\n' "$(pwd)"
  skill_target=$(realpath .claude/skills/lifecycle 2>/dev/null || realpath .agents/skills/lifecycle 2>/dev/null || true)
  printf 'SKILL_TARGET=%s\n' "$skill_target"
  for arg in "$@"; do printf 'ARG=%s\n' "$arg"; done
} > "$tmp"
# Publish only after the complete capture has been written. waitCapture polls
# for the final name, so exposing it before the final line lets the test read
# a partial lifecycle record under load.
mv -- "$tmp" "$out"
exec sleep 600
`
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func privateConfigFixture(t *testing.T, agent, home string) (string, []byte) {
	t.Helper()
	name := "auth.json"
	body := []byte(`{"private":"claude-auth","keep":true}`)
	if agent == "codex" {
		name = "config.toml"
		body = []byte("model = \"fixture\"\n")
		writeMode(t, filepath.Join(home, "auth.json"), []byte(`{"token":"fixture"}`), 0600)
	}
	path := filepath.Join(home, name)
	writeMode(t, path, body, 0600)
	return path, body
}

func writeNativeLifecycleHistory(t *testing.T, agent, home, repo, cid string) {
	t.Helper()
	dir := filepath.Join(home, "sessions")
	if agent == "claude" {
		dir = filepath.Join(home, "projects", claudeProjectSlug(repo))
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if agent == "codex" {
		rows = append(rows, map[string]any{"type": "session_meta", "payload": obj{"id": cid, "cwd": repo, "source": "cli"}})
		rows = append(rows, map[string]any{"type": "response_item", "payload": obj{"type": "message", "role": "user", "content": []obj{{"type": "input_text", "text": "lifecycle fixture"}}}})
	} else {
		rows = append(rows, map[string]any{"type": "user", "sessionId": cid, "cwd": repo, "message": obj{"role": "user", "content": "lifecycle fixture"}})
	}
	var body strings.Builder
	for _, row := range rows {
		data, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		body.Write(data)
		body.WriteByte('\n')
	}
	writeMode(t, filepath.Join(dir, cid+".jsonl"), []byte(body.String()), 0600)
}

func claudeProjectSlug(workdir string) string {
	var b strings.Builder
	for _, c := range workdir {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

func resumeIDArgs(agent string) []string {
	if agent == "claude" {
		return []string{"--resume", "{id}"}
	}
	return []string{"resume", "{id}"}
}

func forkArgs(agent string) []string {
	if agent == "claude" {
		return []string{"--resume", "{id}", "--fork-session"}
	}
	return []string{"fork", "{id}"}
}

type lifecycleCapture struct {
	env  map[string]string
	args []string
}

func waitCapture(t *testing.T, dir string, index int) {
	t.Helper()
	path := filepath.Join(dir, fmt.Sprintf("%d.log", index))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for wrapper capture %s", path)
}

func readCapture(t *testing.T, path string) lifecycleCapture {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := lifecycleCapture{env: map[string]string{}}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "ARG="):
			out.args = append(out.args, strings.TrimPrefix(line, "ARG="))
		case strings.Contains(line, "="):
			parts := strings.SplitN(line, "=", 2)
			out.env[parts[0]] = parts[1]
		}
	}
	return out
}

func assertLifecycleEnvironment(t *testing.T, got lifecycleCapture, agent, home string) {
	t.Helper()
	key := "CODEX_HOME"
	if agent == "claude" {
		key = "CLAUDE_CONFIG_DIR"
	}
	if got.env[key] != home {
		t.Fatalf("%s was not preserved: got %q want %q", key, got.env[key], home)
	}
}

func assertSkillLaunch(t *testing.T, got lifecycleCapture, workdir, source string) {
	t.Helper()
	if got.env["PWD"] != workdir {
		t.Fatalf("agent launched in %q, want %q", got.env["PWD"], workdir)
	}
	if got.env["SKILL_TARGET"] != source {
		t.Fatalf("selected skill unavailable at launched cwd: got %q want %q", got.env["SKILL_TARGET"], source)
	}
}

func assertFreshArgs(t *testing.T, agent string, args []string) {
	t.Helper()
	if agent == "claude" {
		if len(args) < 2 || args[0] != "--mcp-config" || !strings.Contains(args[1], "/lectern/mcp/") {
			t.Fatalf("fresh Claude MCP argv: %q", args)
		}
		for _, arg := range args {
			if arg == "--resume" || arg == "--fork-session" || arg == "resume" || arg == "fork" {
				t.Fatalf("fresh Claude launch unexpectedly had continuation arg %q: %q", arg, args)
			}
		}
		return
	}
	want := []string{"-c", `mcp_servers.ops_tools.args=["-m","ops"]`, "-c", `mcp_servers.ops_tools.command="python3"`}
	assertArgsContainOrdered(t, args, want)
}

func assertContinuationArgs(t *testing.T, agent string, args []string, cid string, fork bool) {
	t.Helper()
	if agent == "claude" {
		if len(args) < 4 || args[0] != "--mcp-config" || !strings.Contains(args[1], "/lectern/mcp/") {
			t.Fatalf("Claude MCP argv: %q", args)
		}
		start := 0
		for start < len(args[2:]) && args[2+start] != "--resume" {
			start++
		}
		if start+1 >= len(args[2:]) || (args[2+start+1] != cid && !(fork && strings.HasSuffix(args[2+start+1], "/"+cid+".jsonl"))) {
			t.Fatalf("Claude continuation ID: %q", args)
		}
		if fork {
			assertArgsContainOrdered(t, args[2+start:], []string{"--resume", args[2+start+1], "--fork-session"})
		}
		return
	}
	want := []string{"-c", `mcp_servers.ops_tools.args=["-m","ops"]`, "-c", `mcp_servers.ops_tools.command="python3"`}
	if fork {
		want = append(want, "fork", cid)
	} else {
		want = append(want, "resume", cid)
	}
	assertArgsContainOrdered(t, args, want)
}

func assertArgsContainOrdered(t *testing.T, got, want []string) {
	t.Helper()
	start := 0
	for _, item := range want {
		found := -1
		for i := start; i < len(got); i++ {
			if got[i] == item {
				found = i
				break
			}
		}
		if found < 0 {
			t.Fatalf("argv %q missing ordered item %q after %d; want %q", got, item, start, want)
		}
		start = found + 1
	}
}

func killLifecycleSession(t *testing.T, h *harness, session obj) {
	t.Helper()
	if session.str("tmux_session") != "" {
		h.decode("DELETE", fmt.Sprintf("/api/sessions/%d", session.id()), nil, 200, nil)
	}
}

type fileSnapshot struct {
	path string
	body []byte
	perm os.FileMode
}

func snapshotFiles(t *testing.T, paths []string) []fileSnapshot {
	t.Helper()
	out := make([]fileSnapshot, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, fileSnapshot{path: path, body: body, perm: info.Mode().Perm()})
	}
	return out
}

func assertProjectFiles(t *testing.T, agent, repo, home string, before []fileSnapshot, foreign, instruction []byte, ex executor.Executor, mcpPath string) {
	t.Helper()
	if got, err := os.ReadFile(filepath.Join(repo, ".lectern", "mcp.json")); err != nil || string(got) != string(foreign) {
		t.Fatalf("foreign MCP config changed: %q %v", got, err)
	}
	name := "AGENTS.md"
	if agent == "claude" {
		name = "CLAUDE.md"
	}
	if got, err := os.ReadFile(filepath.Join(repo, name)); err != nil || string(got) != string(instruction) {
		t.Fatalf("%s instructions changed: %q %v", name, got, err)
	}
	for _, want := range before {
		got, err := os.ReadFile(want.path)
		if err != nil {
			t.Fatalf("protected file %s disappeared: %v", want.path, err)
		}
		info, err := os.Stat(want.path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want.body) || info.Mode().Perm() != want.perm {
			t.Fatalf("protected file %s changed (mode %o -> %o)", want.path, want.perm, info.Mode().Perm())
		}
	}
	if agent == "claude" {
		if mcpPath == "" {
			t.Fatal("Claude launch did not publish a private MCP runtime")
		}
		modeResult, modeErr := ex.Run(context.Background(), "stat -c %a "+shellQuoteForTest(mcpPath), executor.RunOpts{Timeout: 20})
		if modeErr != nil || !modeResult.OK() || strings.TrimSpace(modeResult.Stdout) != "600" {
			t.Fatalf("Claude MCP runtime is not private: %s (%q, %v)", mcpPath, strings.TrimSpace(modeResult.Stdout), modeErr)
		}
		var payload map[string]any
		data, readErr := ex.Run(context.Background(), "cat "+shellQuoteForTest(mcpPath), executor.RunOpts{Timeout: 20})
		want := `{"mcpServers":{"ops_tools":{"args":["-m","ops"],"command":"python3"}}}`
		if readErr != nil || !data.OK() || strings.TrimSpace(data.Stdout) != want || json.Unmarshal([]byte(data.Stdout), &payload) != nil || payload["mcpServers"] == nil {
			t.Fatalf("Claude MCP runtime %s is invalid: %v (stdout=%q stderr=%q)", mcpPath, readErr, data.Stdout, data.Stderr)
		}
		parentResult, parentErr := ex.Run(context.Background(), "stat -c %a "+shellQuoteForTest(filepath.Dir(mcpPath)), executor.RunOpts{Timeout: 20})
		if parentErr != nil || !parentResult.OK() || strings.TrimSpace(parentResult.Stdout) != "700" {
			t.Fatalf("Claude MCP runtime parent %s is not private: %q (%v)", filepath.Dir(mcpPath), strings.TrimSpace(parentResult.Stdout), parentErr)
		}
	}
	_ = home
}

func writeMode(t *testing.T, path string, body []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func shellQuoteForTest(s string) string {
	return fmt.Sprintf("'%s'", strings.ReplaceAll(s, "'", "'\\''"))
}
