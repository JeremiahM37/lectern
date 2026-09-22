package hooks_test

// The two agent-side scripts are the only code that runs on the TARGET, and
// hook.py is the gate the whole approval flow depends on: it decides whether a
// tool call proceeds. It had no tests at all. These run the real scripts with
// the real interpreter against a fake control plane.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/hooks"
)

func stage(t *testing.T, name string, body []byte) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable; the scripts run on the target, not here")
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, body, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// runHook executes hook.py exactly as Claude Code would: the tool call on
// stdin, configuration in the environment, meaning in the exit code.
func runHook(t *testing.T, script, url, token string, payload any) (int, string) {
	t.Helper()
	raw, _ := json.Marshal(payload)
	cmd := exec.Command("python3", script)
	cmd.Stdin = strings.NewReader(string(raw))
	cmd.Env = append(os.Environ(),
		"LECTERN_URL="+url, "LECTERN_TOKEN="+token,
		"LECTERN_APPROVAL_TIMEOUT=8")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running the hook: %v", err)
	}
	return code, stderr.String()
}

// fakePlane answers the two endpoints the hook uses, handing out `decision`
// after `pending` polls.
func fakePlane(t *testing.T, decision, note string, pending int32) *httptest.Server {
	t.Helper()
	var polls int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/hook/approval":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["tool_name"] == nil {
				t.Errorf("the hook must forward the tool name: %v", body)
			}
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]any{"id": 7})
		case strings.HasSuffix(r.URL.Path, "/decision"):
			if !strings.Contains(r.URL.Path, "/7/") {
				t.Errorf("polled the wrong approval: %s", r.URL.Path)
			}
			status := "pending"
			if atomic.AddInt32(&polls, 1) > pending {
				status = decision
			}
			json.NewEncoder(w).Encode(map[string]any{"status": status, "note": note})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
}

func TestHookAllowsAnApprovedCall(t *testing.T) {
	script := stage(t, "hook.py", hooks.Hook)
	srv := fakePlane(t, "approved", "", 1)
	defer srv.Close()
	code, stderr := runHook(t, script, srv.URL, "tok", map[string]any{
		"tool_name": "Bash", "tool_input": map[string]any{"command": "pytest -q"}})
	if code != 0 {
		t.Fatalf("an approved call must proceed (exit 0), got %d: %s", code, stderr)
	}
}

// Exit 2 is what blocks the call, and stderr is fed back to the agent as the
// reason — so the operator's words have to survive.
func TestHookBlocksADeniedCallAndExplainsWhy(t *testing.T) {
	script := stage(t, "hook.py", hooks.Hook)
	srv := fakePlane(t, "denied", "not safe, find another way", 0)
	defer srv.Close()
	code, stderr := runHook(t, script, srv.URL, "tok", map[string]any{
		"tool_name": "Bash", "tool_input": map[string]any{"command": "rm -rf /"}})
	if code != 2 {
		t.Fatalf("a denied call must be blocked (exit 2), got %d", code)
	}
	if !strings.Contains(stderr, "not safe, find another way") {
		t.Errorf("the operator's reason must reach the agent: %q", stderr)
	}
}

func TestHookBlocksAnExpiredApproval(t *testing.T) {
	script := stage(t, "hook.py", hooks.Hook)
	srv := fakePlane(t, "expired", "", 0)
	defer srv.Close()
	if code, _ := runHook(t, script, srv.URL, "tok",
		map[string]any{"tool_name": "Bash"}); code != 2 {
		t.Fatalf("an expired approval must not fall open, got %d", code)
	}
}

// The gate has to fail CLOSED. An agent that cannot reach the control plane is
// an agent nobody is watching.
func TestHookBlocksWhenTheControlPlaneIsUnreachable(t *testing.T) {
	script := stage(t, "hook.py", hooks.Hook)
	code, stderr := runHook(t, script, "http://127.0.0.1:1", "tok",
		map[string]any{"tool_name": "Bash"})
	if code != 2 {
		t.Fatalf("unreachable must block (exit 2), got %d", code)
	}
	if !strings.Contains(strings.ToLower(stderr), "unreachable") {
		t.Errorf("it should say why: %q", stderr)
	}
}

// Unconfigured means "not running under lectern" — a developer running the CLI
// by hand must not be blocked by a hook that has nothing to ask.
func TestHookIsInertWhenUnconfigured(t *testing.T) {
	script := stage(t, "hook.py", hooks.Hook)
	if code, _ := runHook(t, script, "", "", map[string]any{"tool_name": "Bash"}); code != 0 {
		t.Fatalf("an unconfigured hook must not block local runs, got %d", code)
	}
}

func TestHookToleratesGarbageOnStdin(t *testing.T) {
	script := stage(t, "hook.py", hooks.Hook)
	srv := fakePlane(t, "approved", "", 0)
	defer srv.Close()
	cmd := exec.Command("python3", script)
	cmd.Stdin = strings.NewReader("not json at all")
	cmd.Env = append(os.Environ(), "LECTERN_URL="+srv.URL, "LECTERN_TOKEN=tok")
	err := cmd.Run()
	if err != nil {
		t.Fatalf("malformed input must not block the agent: %v", err)
	}
}

// It long-polls: the operator may take minutes, and the hook must keep waiting
// rather than treating "still pending" as a decision.
func TestHookKeepsPollingWhilePending(t *testing.T) {
	script := stage(t, "hook.py", hooks.Hook)
	srv := fakePlane(t, "approved", "", 3)
	defer srv.Close()
	start := time.Now()
	if code, _ := runHook(t, script, srv.URL, "tok",
		map[string]any{"tool_name": "Bash"}); code != 0 {
		t.Fatalf("it gave up before the decision arrived, got %d", code)
	}
	if time.Since(start) > 30*time.Second {
		t.Error("polling took implausibly long")
	}
}

// lec.py is how an agent files follow-up work and leaves durable notes.
func TestAgentKitFilesTasksAndNotes(t *testing.T) {
	script := stage(t, "lec.py", hooks.ADK)
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		body["_path"] = r.URL.Path
		got = append(got, body)
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"task_id": 12, "note_id": 34})
	}))
	defer srv.Close()

	// the kit reads its credentials from the staged env file beside it
	envPath := filepath.Join(filepath.Dir(script), "env")
	os.WriteFile(envPath, []byte(fmt.Sprintf("ADK_URL=%s\nADK_TOKEN=tok-abc\n", srv.URL)), 0o644)

	run := func(args ...string) int {
		cmd := exec.Command("python3", append([]string{script}, args...)...)
		if err := cmd.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return ee.ExitCode()
			}
			t.Fatal(err)
		}
		return 0
	}
	if rc := run("add-task", "add tests", "write tests for health()", "--dispatch"); rc != 0 {
		t.Fatalf("filing a task: rc=%d", rc)
	}
	if rc := run("add-note", "auth uses bcrypt"); rc != 0 {
		t.Fatalf("leaving a note: rc=%d", rc)
	}
	if len(got) != 2 {
		t.Fatalf("expected two calls, got %d", len(got))
	}
	if got[0]["_path"] != "/api/hook/tasks" || got[0]["title"] != "add tests" ||
		got[0]["dispatch"] != true || got[0]["token"] != "tok-abc" {
		t.Errorf("task call: %v", got[0])
	}
	if got[1]["_path"] != "/api/hook/notes" || got[1]["note"] != "auth uses bcrypt" {
		t.Errorf("note call: %v", got[1])
	}
}

func TestAgentKitRefusesWithoutCredentials(t *testing.T) {
	script := stage(t, "lec.py", hooks.ADK)
	cmd := exec.Command("python3", script, "add-note", "x")
	err := cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() == 0 {
		t.Fatal("without a staged env file it must fail rather than post nowhere")
	}
}
