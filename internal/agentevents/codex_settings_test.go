package agentevents

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests run CodexHooksInstallCommand's generated shell/python for
// real, against a real bash + python3, exactly like it runs on a session's
// target — the merge logic's correctness lives entirely in that generated
// script, so a Go-level assertion about the Go string alone would prove
// nothing. See docs/agent-events.md section 2's correction for the real
// codex-cli 0.156.1 probe this mirrors.

func requireShellTools(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
}

// runInstall executes CodexHooksInstallCommand(ask) with HOME=home and
// returns its stdout (the hooks.json path) and full combined output for
// failure messages.
func runInstall(t *testing.T, home string, ask bool) string {
	t.Helper()
	cmd := exec.Command("bash", "-c", CodexHooksInstallCommand(ask))
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install command failed: %v\n%s", err, out)
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		t.Fatalf("install command printed nothing:\n%s", out)
	}
	return path
}

func loadHooksDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("hooks.json is not valid JSON: %v\n%s", err, data)
	}
	return doc
}

func groupsFor(t *testing.T, doc map[string]any, event string) []any {
	t.Helper()
	events, _ := doc["hooks"].(map[string]any)
	groups, _ := events[event].([]any)
	return groups
}

func groupCommands(t *testing.T, group any) []string {
	t.Helper()
	g, _ := group.(map[string]any)
	hooks, _ := g["hooks"].([]any)
	var out []string
	for _, h := range hooks {
		hm, _ := h.(map[string]any)
		if cmd, ok := hm["command"].(string); ok {
			out = append(out, cmd)
		}
	}
	return out
}

// TestCodexHooksInstallMergesWithoutClobberingOtherTools reproduces the real
// situation this machine already has: an unrelated tool's hooks.json exists
// first, and lectern's own install must add its entries alongside it, not
// replace the file.
func TestCodexHooksInstallMergesWithoutClobberingOtherTools(t *testing.T) {
	requireShellTools(t)
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	preexisting := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"sh -c 'echo other-tool-was-here'"}]}]}}`
	if err := os.WriteFile(filepath.Join(codexHome, "hooks.json"), []byte(preexisting), 0o600); err != nil {
		t.Fatal(err)
	}

	path := runInstall(t, home, true)
	if path != filepath.Join(codexHome, "hooks.json") {
		t.Fatalf("install wrote to %q, want %q", path, filepath.Join(codexHome, "hooks.json"))
	}
	doc := loadHooksDoc(t, path)

	sessionStart := groupsFor(t, doc, "SessionStart")
	if len(sessionStart) != 2 {
		t.Fatalf("SessionStart groups = %d, want 2 (other tool + lectern)", len(sessionStart))
	}
	foundOther, foundOurs := false, false
	for _, g := range sessionStart {
		for _, cmd := range groupCommands(t, g) {
			if strings.Contains(cmd, "other-tool-was-here") {
				foundOther = true
			}
			if strings.Contains(cmd, codexHookMarker) {
				foundOurs = true
			}
		}
	}
	if !foundOther {
		t.Error("the pre-existing tool's SessionStart hook was lost")
	}
	if !foundOurs {
		t.Error("lectern's own SessionStart hook was not installed")
	}

	for _, event := range []string{"UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop", "SessionEnd", "PreCompact", "PermissionRequest"} {
		groups := groupsFor(t, doc, event)
		if len(groups) != 1 {
			t.Errorf("%s groups = %d, want 1", event, len(groups))
			continue
		}
		cmds := groupCommands(t, groups[0])
		if len(cmds) != 1 || !strings.Contains(cmds[0], "python3") || !strings.Contains(cmds[0], event) {
			t.Errorf("%s command = %v, want a python3 invocation naming the event", event, cmds)
		}
	}
}

// TestCodexHooksInstallIsIdempotentAndTogglesPermissionRequest runs the
// installer twice with the same ask value (must not duplicate groups), then
// again with ask=false (must remove PermissionRequest but leave every other
// event, and any other tool's entries, alone).
func TestCodexHooksInstallIsIdempotentAndTogglesPermissionRequest(t *testing.T) {
	requireShellTools(t)
	home := t.TempDir()

	path1 := runInstall(t, home, true)
	path2 := runInstall(t, home, true)
	if path1 != path2 {
		t.Fatalf("hooks.json path changed between runs: %q vs %q", path1, path2)
	}
	doc := loadHooksDoc(t, path2)
	if got := len(groupsFor(t, doc, "SessionStart")); got != 1 {
		t.Fatalf("re-running the installer duplicated groups: SessionStart has %d, want 1", got)
	}
	if got := len(groupsFor(t, doc, "PermissionRequest")); got != 1 {
		t.Fatalf("PermissionRequest groups after ask=true = %d, want 1", got)
	}

	runInstall(t, home, false)
	doc = loadHooksDoc(t, path2)
	if got := len(groupsFor(t, doc, "PermissionRequest")); got != 0 {
		t.Fatalf("PermissionRequest groups after ask=false = %d, want 0 (event key should be gone or empty)", got)
	}
	if got := len(groupsFor(t, doc, "SessionStart")); got != 1 {
		t.Fatalf("ask=false must not disturb other events: SessionStart groups = %d, want 1", got)
	}
}

// TestCodexHookScriptForwardsAndReturnsRealHTTPResponse runs the actual
// installed script (not a reimplementation of its logic) against a real
// HTTP server, proving: the right URL/path, the bearer token, the exact
// stdin body forwarded untouched, and the server's JSON response printed
// back to stdout verbatim — the mechanism additionalContext/PermissionRequest
// decisions rely on, confirmed for real against codex-cli 0.156.1 (see
// docs/agent-events.md).
func TestCodexHookScriptForwardsAndReturnsRealHTTPResponse(t *testing.T) {
	requireShellTools(t)
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	home := t.TempDir()
	runInstall(t, home, true)
	scriptPath := filepath.Join(home, ".lectern", "hooks", codexHookMarker)
	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("hook script was not written: %v", err)
	}

	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		buf := make([]byte, 1<<16)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"echoed-ok"}}`))
	}))
	defer srv.Close()

	cmd := exec.Command("python3", scriptPath, "SessionStart", "8")
	cmd.Env = append(os.Environ(),
		"LECTERN_HOOK_TOKEN=test-token-123",
		"LECTERN_HOOK_URL="+srv.URL+"/api/hook/session/42")
	cmd.Stdin = strings.NewReader(`{"session_id":"abc","hook_event_name":"SessionStart"}`)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hook script failed: %v\n%s", err, out)
	}
	if gotPath != "/api/hook/session/42/SessionStart" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer test-token-123" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotBody != `{"session_id":"abc","hook_event_name":"SessionStart"}` {
		t.Errorf("body forwarded = %q", gotBody)
	}
	var resp map[string]any
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("script stdout is not valid JSON: %v\n%s", err, out)
	}
	hso, _ := resp["hookSpecificOutput"].(map[string]any)
	if hso["additionalContext"] != "echoed-ok" {
		t.Errorf("additionalContext round-trip failed: %v", resp)
	}
}

// TestCodexHookScriptDegradesSilentlyWhenUnreachable proves the hook can
// never be mistaken for a request to block a tool call or turn: no
// LECTERN_HOOK_URL/TOKEN, and an unreachable URL, must both print "{}" and
// exit 0 well within the per-event timeout.
func TestCodexHookScriptDegradesSilentlyWhenUnreachable(t *testing.T) {
	requireShellTools(t)
	home := t.TempDir()
	runInstall(t, home, true)
	scriptPath := filepath.Join(home, ".lectern", "hooks", codexHookMarker)

	t.Run("no env", func(t *testing.T) {
		cmd := exec.Command("python3", scriptPath, "SessionStart", "2")
		cmd.Env = os.Environ()
		cmd.Stdin = strings.NewReader(`{}`)
		start := time.Now()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("script must exit 0, got %v: %s", err, out)
		}
		if time.Since(start) > 3*time.Second {
			t.Fatal("script with no env should return immediately, not wait for a timeout")
		}
		if strings.TrimSpace(string(out)) != "{}" {
			t.Fatalf("stdout = %q, want {}", out)
		}
	})

	t.Run("unreachable url", func(t *testing.T) {
		cmd := exec.Command("python3", scriptPath, "SessionStart", "1")
		cmd.Env = append(os.Environ(),
			"LECTERN_HOOK_TOKEN=t", "LECTERN_HOOK_URL=http://127.0.0.1:1/nope")
		cmd.Stdin = strings.NewReader(`{}`)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("script must exit 0 even when lectern is unreachable, got %v: %s", err, out)
		}
		if strings.TrimSpace(string(out)) != "{}" {
			t.Fatalf("stdout = %q, want {}", out)
		}
	})
}
