package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The demo agent, in ask mode, asks for approval before its first write, so a
// new user sees the whole loop with nothing installed.
func TestDemoAgentAsksForApproval(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local runtime test uses a POSIX shell")
	}
	for _, tool := range []string{"curl", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	bin := filepath.Join(t.TempDir(), "lectern")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	state, home, repo := t.TempDir(), t.TempDir(), t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	env := append(localTestEnv(state), "HOME="+home, "LECTERN_SESSION_BACKEND=pty")
	t.Cleanup(func() {
		_, _ = runLocalCLI(bin, env, "api", "DELETE", "/sessions/1?kill=true")
		_, _ = runLocalCLI(bin, env, "local", "stop")
	})
	created, err := runLocalCLI(bin, env, "api", "POST", "/sessions", fmt.Sprintf(`{"agent":"demo","workdir":%q,"permission_mode":"ask"}`, repo))
	if err != nil {
		t.Fatalf("create: %v %s", err, created)
	}
	var s struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(created, &s)
	id := strconv.FormatInt(s.ID, 10)
	time.Sleep(2 * time.Second)
	if out, err := runLocalCLI(bin, env, "api", "POST", "/sessions/"+id+"/send", `{"text":"hello demo"}`); err != nil {
		t.Fatalf("send: %v %s", err, out)
	}
	var pending []byte
	approval := regexp.MustCompile(`"id":(\d+),"session_id":` + id + `,"tool_name":"Write"`)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && !approval.Match(pending) {
		time.Sleep(300 * time.Millisecond)
		pending, _ = runLocalCLI(bin, env, "api", "GET", "/approvals")
	}
	m := approval.FindSubmatch(pending)
	if m == nil {
		t.Fatalf("the demo did not ask for approval: %s", pending)
	}
	if _, err := os.Stat(filepath.Join(repo, "demo-notes.md")); err == nil {
		t.Fatal("the demo wrote before it was approved")
	}
	if out, err := runLocalCLI(bin, env, "api", "POST", "/approvals/"+string(m[1])+"/decision", `{"decision":"approved"}`); err != nil {
		t.Fatalf("approve: %v %s", err, out)
	}
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(filepath.Join(repo, "demo-notes.md")); err == nil && bytes.Contains(data, []byte("hello demo")) {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatal("the demo did not write after approval")
}

// Starting an agent that is not on the machine is refused before any session
// exists, naming the agents that are installed and how to install it.
func TestStartingAnUninstalledAgentIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local runtime test uses a POSIX shell")
	}
	bin := filepath.Join(t.TempDir(), "lectern")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	state, home, fake, dir := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(fake, "claude-for-test"), []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := append(localTestEnv(state), "HOME="+home, "LECTERN_SESSION_BACKEND=pty",
		"LECTERN_CLAUDE_BIN=claude-for-test", "LECTERN_CODEX_BIN=codex-not-installed-here", "LECTERN_GEMINI_BIN=gemini-not-installed-here",
		"PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() { _, _ = runLocalCLI(bin, env, "local", "stop") })
	out, err := runLocalCLI(bin, env, "api", "POST", "/sessions", fmt.Sprintf(`{"agent":"codex","workdir":%q}`, dir))
	if err == nil || !bytes.Contains(out, []byte("codex isn't installed on this computer")) ||
		!bytes.Contains(out, []byte("Installed there: claude.")) || !bytes.Contains(out, []byte("npm install -g @openai/codex")) {
		t.Fatalf("uninstalled agent: %v %s", err, out)
	}
	// The web app gets a code it can act on.
	var endpoint struct{ URL, Token string }
	data, _ := os.ReadFile(filepath.Join(state, "lectern", "local", "endpoint.json"))
	_ = json.Unmarshal(data, &endpoint)
	req, _ := http.NewRequest("POST", endpoint.URL+"/api/sessions", strings.NewReader(fmt.Sprintf(`{"agent":"codex","workdir":%q}`, dir)))
	req.Header.Set("Authorization", "Bearer "+endpoint.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 422 || !bytes.Contains(body, []byte(`"code":"agent_not_installed"`)) || !bytes.Contains(body, []byte(`"installed":["claude"]`)) {
		t.Fatalf("API answer: %d %s", res.StatusCode, body)
	}
	if list, _ := runLocalCLI(bin, env, "api", "GET", "/sessions?all=1"); bytes.Contains(list, []byte(`"agent":"codex"`)) {
		t.Fatalf("a session was created anyway: %s", list)
	}
	// An installed one still starts.
	if out, err := runLocalCLI(bin, env, "api", "POST", "/sessions", fmt.Sprintf(`{"agent":"claude","workdir":%q}`, dir)); err != nil {
		t.Fatalf("installed agent refused: %v %s", err, out)
	}
	_, _ = runLocalCLI(bin, env, "api", "DELETE", "/sessions/1?kill=true")
}
