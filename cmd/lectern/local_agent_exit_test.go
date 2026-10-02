package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// exitingAgent asks for permission, as a gated tool call does, then dies
// while the request is pending — the audit's agent killed by Ctrl+\\.
const exitingAgent = `#!/bin/sh
[ "$1" = --version ] && { echo 2.1.0; exit 0; }
curl -s -o /dev/null -X POST -H "Authorization: Bearer $LECTERN_HOOK_TOKEN" -H 'Content-Type: application/json' \
  -d '{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"echo hi"}}' \
  "$LECTERN_HOOK_URL/PermissionRequest" &
sleep 3
kill -QUIT $$
`

// TestAgentExitIsStoppedOnEveryBackend: an agent that exits leaves its
// terminal at a shell prompt. The session must read Ended · agent exited
// (Stopped, with Revive), never Idle.
func TestAgentExitIsStoppedOnEveryBackend(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local runtime test uses a POSIX shell agent")
	}
	for _, backendName := range []string{"pty", "tmux"} {
		t.Run(backendName, func(t *testing.T) {
			if backendName == "tmux" {
				if _, err := exec.LookPath("tmux"); err != nil {
					t.Skip("tmux is not installed")
				}
			}
			if _, err := exec.LookPath("git"); err != nil {
				t.Skip("git is not installed")
			}
			bin := filepath.Join(t.TempDir(), "lectern")
			if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
				t.Fatalf("build: %v\n%s", err, out)
			}
			state, home, fake, repo := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
			// Exits by itself shortly after starting.
			if err := os.WriteFile(filepath.Join(fake, "claude"), []byte(exitingAgent), 0o755); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
				t.Fatalf("git init: %v %s", err, out)
			}
			env := append(localTestEnv(state), "HOME="+home, "LECTERN_SESSION_BACKEND="+backendName,
				"LECTERN_SESSION_POLL=0.5",
				"PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Cleanup(func() {
				_, _ = runLocalCLI(bin, env, "api", "DELETE", "/sessions/1?kill=true")
				_, _ = runLocalCLI(bin, env, "local", "stop")
			})
			created, err := runLocalCLI(bin, env, "api", "POST", "/sessions", fmt.Sprintf(`{"agent":"claude","workdir":%q,"permission_mode":"ask"}`, repo))
			if err != nil {
				t.Fatalf("create: %v %s", err, created)
			}
			var s struct {
				ID int64 `json:"id"`
			}
			_ = json.Unmarshal(created, &s)
			id := strconv.FormatInt(s.ID, 10)
			deadline := time.Now().Add(90 * time.Second)
			var view []byte
			for time.Now().Before(deadline) {
				view, _ = runLocalCLI(bin, env, "api", "GET", "/sessions/"+id)
				if bytes.Contains(view, []byte(`"state_reason":"agent_exited"`)) {
					break
				}
				time.Sleep(500 * time.Millisecond)
			}
			if !bytes.Contains(view, []byte(`"state":"ended"`)) || !bytes.Contains(view, []byte(`"state_reason":"agent_exited"`)) {
				t.Fatalf("an exited agent is not shown as stopped: %s", view)
			}
			// The approval it was blocked on went with it.
			deadline = time.Now().Add(10 * time.Second)
			var pending []byte
			for time.Now().Before(deadline) {
				pending, _ = runLocalCLI(bin, env, "api", "GET", "/approvals?status=pending")
				if !bytes.Contains(pending, []byte(`"session_id":`+id)) {
					break
				}
				time.Sleep(300 * time.Millisecond)
			}
			if bytes.Contains(pending, []byte(`"session_id":`+id)) {
				t.Fatalf("approval outlived its agent: %s", pending)
			}
			if expired, _ := runLocalCLI(bin, env, "api", "GET", "/approvals?status=expired"); !bytes.Contains(expired, []byte(`"session_id":`+id)) {
				t.Fatalf("the agent's approval was never expired: %s", expired)
			}
		})
	}
}
