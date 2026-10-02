package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

// TestLocalRuntimeReceivesItsAgentsHookEvents is the regression test for
// hooks calling back to the default :9110 instead of the runtime's own port.
// A stand-in agent started by `lectern up`'s runtime posts one hook event to
// the address it was given; the runtime must record it.
func TestLocalRuntimeReceivesItsAgentsHookEvents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local runtime currently uses POSIX process locks")
	}
	for _, tool := range []string{"tmux", "curl", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	bin := filepath.Join(t.TempDir(), "lectern")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build local CLI: %v\n%s", err, out)
	}
	state, home, fake, repo, record := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--version" ]; then echo "2.1.0 (Claude Code)"; exit 0; fi
printf '%%s\n' "$LECTERN_HOOK_URL" > %[1]s/url
# Never post anywhere but this test's runtime (a wrong address could be a
# real Lectern on this machine).
runtime=$(sed -n 's/.*"url":"\([^"]*\)".*/\1/p' %[2]s)
case "$LECTERN_HOOK_URL" in
"$runtime"/*) curl -s -o /dev/null -w '%%{http_code}' -X POST -H "Authorization: Bearer $LECTERN_HOOK_TOKEN" \
  -H 'Content-Type: application/json' -d '{"hook_event_name":"UserPromptSubmit","prompt":"hi"}' \
  "$LECTERN_HOOK_URL/UserPromptSubmit" > %[1]s/code ;;
*) echo wrong-address > %[1]s/code ;;
esac
exec sleep 120
`, record, filepath.Join(state, "lectern", "local", "endpoint.json"))
	if err := os.WriteFile(filepath.Join(fake, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	env := append(localTestEnv(t, state), "HOME="+home, "PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		testutil.CleanupTmuxSocket(t, filepath.Join(state, "lectern", "local", "tmux", fmt.Sprintf("tmux-%d", os.Getuid()), "default"))
		_, _ = runLocalCLI(bin, env, "local", "stop")
	})

	up := exec.Command(bin, "up", "--no-browser")
	up.Dir, up.Env = repo, env
	if out, err := up.CombinedOutput(); err != nil {
		t.Fatalf("lectern up: %v\n%s", err, out)
	}
	var endpoint struct {
		URL string `json:"url"`
	}
	data, err := os.ReadFile(filepath.Join(state, "lectern", "local", "endpoint.json"))
	if err != nil || json.Unmarshal(data, &endpoint) != nil {
		t.Fatalf("read endpoint: %v %s", err, data)
	}
	created, err := runLocalCLI(bin, env, "api", "POST", "/sessions", fmt.Sprintf(`{"agent":"claude","workdir":%q}`, repo))
	if err != nil {
		t.Fatalf("create session: %v %s", err, created)
	}
	var session struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(created, &session); err != nil || session.ID == 0 {
		t.Fatalf("session: %v %s", err, created)
	}
	id := strconv.FormatInt(session.ID, 10)
	t.Cleanup(func() { _, _ = runLocalCLI(bin, env, "api", "DELETE", "/sessions/"+id+"?kill=true") })

	deadline := time.Now().Add(30 * time.Second)
	var code, hookURL, view []byte
	for time.Now().Before(deadline) {
		code, _ = os.ReadFile(filepath.Join(record, "code"))
		hookURL, _ = os.ReadFile(filepath.Join(record, "url"))
		view, _ = runLocalCLI(bin, env, "api", "GET", "/sessions/"+id)
		if bytes.Contains(view, []byte(`"state_source":"hook"`)) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	runtimeURL, _ := url.Parse(endpoint.URL)
	gotURL, _ := url.Parse(strings.TrimSpace(string(hookURL)))
	if gotURL == nil || gotURL.Host != runtimeURL.Host {
		t.Fatalf("agent was told to call back to %q; the runtime is %s", hookURL, endpoint.URL)
	}
	if strings.TrimSpace(string(code)) != "200" || !bytes.Contains(view, []byte(`"state_source":"hook"`)) {
		t.Fatalf("runtime did not record the hook event: code=%q session=%s", code, view)
	}

	doctor := exec.Command(bin, "doctor")
	doctor.Env = env
	out, _ := doctor.CombinedOutput()
	if !bytes.Contains(out, []byte("[OK] agent hooks")) {
		t.Fatalf("doctor did not confirm the hook round trip:\n%s", out)
	}
}

// TestLocalRuntimeRefusesOtherWebsites reproduces the audit's attack on a
// real runtime: a page on another site posting a "simple" (no preflight)
// request, and a DNS-rebound name. Neither may create anything.
func TestLocalRuntimeRefusesOtherWebsites(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local runtime currently uses POSIX process locks")
	}
	bin := filepath.Join(t.TempDir(), "lectern")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build local CLI: %v\n%s", err, out)
	}
	state := t.TempDir()
	env := append(localTestEnv(t, state), "HOME="+t.TempDir())
	t.Cleanup(func() { _, _ = runLocalCLI(bin, env, "local", "stop") })
	if out, err := runLocalCLI(bin, env, "api", "GET", "/health"); err != nil {
		t.Fatalf("start runtime: %v %s", err, out)
	}
	var endpoint struct {
		URL string `json:"url"`
	}
	data, _ := os.ReadFile(filepath.Join(state, "lectern", "local", "endpoint.json"))
	if err := json.Unmarshal(data, &endpoint); err != nil {
		t.Fatal(err)
	}
	post := func(host, origin string) int {
		req, _ := http.NewRequest("POST", endpoint.URL+"/api/projects", strings.NewReader(`{"name":"pwned","target_id":1,"repo_path":"/tmp"}`))
		req.Header.Set("Content-Type", "text/plain")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if host != "" {
			req.Host = host
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := post("", "http://evil.example"); code != http.StatusForbidden {
		t.Fatalf("cross-site POST answered %d", code)
	}
	if code := post("evil.example", ""); code != http.StatusMisdirectedRequest {
		t.Fatalf("rebound POST answered %d", code)
	}
	if code := post("", ""); code != http.StatusUnauthorized {
		t.Fatalf("POST with no credential answered %d", code)
	}
	projects, err := runLocalCLI(bin, env, "api", "GET", "/projects")
	if err != nil || bytes.Contains(projects, []byte("pwned")) {
		t.Fatalf("a refused request created a project: %v %s", err, projects)
	}
}
