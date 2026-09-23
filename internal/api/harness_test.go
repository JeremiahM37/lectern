package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/app"
	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

// harness is a whole lectern — real HTTP server, real scheduler, real hook
// endpoints — running against an isolated temp database with the mock executor.
//
// Nothing is stubbed between the API and the "target", so a fake agent exercises
// the same approval and task-filing endpoints a real one does.
type harness struct {
	t   *testing.T
	App *app.App
	URL string
}

func newHarness(t *testing.T, tweak ...func(*config.Config)) *harness {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		DBPath:           filepath.Join(dir, "test.db"),
		Mock:             true,
		TickInterval:     40 * time.Millisecond,
		MockAgentDelay:   40 * time.Millisecond,
		ApprovalPoll:     400 * time.Millisecond,
		ApprovalExpire:   900 * time.Second,
		JanitorDays:      7,
		ClaudeBin:        "claude",
		CodexBin:         "codex",
		GeminiBin:        "gemini",
		VAPIDEmail:       "admin@example.com",
		HostClaudeConfig: filepath.Join(dir, "no-such-claude.json"),
		ClaudeCredsPath:  filepath.Join(dir, "no-such-creds.json"),
		CodexCredsPath:   filepath.Join(dir, "no-such-codex.json"),
	}
	for _, fn := range tweak {
		fn(cfg)
	}
	if !cfg.Mock {
		// Refuse every real-target harness unless the reviewed bwrap runner has
		// established a private process/tmux namespace before app.New starts.
		testutil.RequireIsolated(t)
		// Real executor tests must never inspect or affect the host tmux server.
		isolateTmux(t)
	}
	// bind first so BaseURL is final before the scheduler can read it: the fake
	// agent calls back into this very server through the staged .lectern/env
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.BaseURL = "http://" + ln.Addr().String()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := app.New(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	srv := &httptest.Server{Listener: ln,
		Config: &http.Server{Handler: a.Handler()}}
	srv.Start()
	t.Cleanup(func() {
		srv.Close()
		a.Close()
	})
	return &harness{t: t, App: a, URL: cfg.BaseURL}
}

// mock is the scripted executor every target resolves to in these tests.
func (h *harness) mock() *executor.Mock {
	h.t.Helper()
	ex := h.App.Reg.Any()
	m, ok := ex.(*executor.Mock)
	if !ok {
		h.t.Fatalf("expected a mock executor, got %T", ex)
	}
	return m
}

// ---- HTTP ---------------------------------------------------------------

func (h *harness) request(method, path string, body any, headers map[string]string) (int, []byte) {
	h.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, h.URL+path, rdr)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// rawRequest posts a body exactly as given — no json.Marshal wrapping — with
// a bearer token, for endpoints whose caller is another program sending its
// own already-serialized JSON rather than this harness's usual `obj`/struct
// bodies. Used by the agent-hook tests (hooks_agentevents_test.go), which
// post real Claude Code fixture payloads byte-for-byte and authenticate with
// a session's hook_token rather than the general API bearer.
func (h *harness) rawRequest(method, path, body, bearerToken string) (int, []byte) {
	h.t.Helper()
	req, err := http.NewRequest(method, h.URL+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// status returns just the response code — for the "this must be rejected" cases.
func (h *harness) status(method, path string, body any) int {
	code, _ := h.request(method, path, body, nil)
	return code
}

// decode runs a request, asserts the status, and unmarshals into out.
func (h *harness) decode(method, path string, body any, want int, out any) {
	h.t.Helper()
	code, raw := h.request(method, path, body, nil)
	if code != want {
		h.t.Fatalf("%s %s: got %d want %d — %s", method, path, code, want, raw)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			h.t.Fatalf("%s %s: decoding %s: %v", method, path, raw, err)
		}
	}
}

// obj is one JSON object as a map, for assertions that do not deserve a type.
type obj map[string]any

func (o obj) str(key string) string  { s, _ := o[key].(string); return s }
func (o obj) num(key string) float64 { f, _ := o[key].(float64); return f }
func (o obj) id() int64              { return int64(o.num("id")) }
func (o obj) sub(key string) obj {
	m, _ := o[key].(map[string]any)
	return obj(m)
}
func (o obj) list(key string) []obj {
	items, _ := o[key].([]any)
	out := make([]obj, 0, len(items))
	for _, i := range items {
		if m, ok := i.(map[string]any); ok {
			out = append(out, obj(m))
		}
	}
	return out
}

func (h *harness) get(path string) obj {
	h.t.Helper()
	var out obj
	h.decode("GET", path, nil, 200, &out)
	return out
}

func (h *harness) getList(path string) []obj {
	h.t.Helper()
	var out []obj
	h.decode("GET", path, nil, 200, &out)
	return out
}

func (h *harness) post(path string, body any, want int) obj {
	h.t.Helper()
	var out obj
	h.decode("POST", path, body, want, &out)
	return out
}

func (h *harness) patch(path string, body any, want int) obj {
	h.t.Helper()
	var out obj
	h.decode("PATCH", path, body, want, &out)
	return out
}

// ---- fixtures -----------------------------------------------------------

// seeded is the demo board mock mode ships with.
func (h *harness) seededProjectID() int64 {
	h.t.Helper()
	projects := h.getList("/api/projects")
	if len(projects) == 0 {
		h.t.Fatal("mock mode should have seeded a demo project")
	}
	return projects[0].id()
}

func (h *harness) firstTargetID() int64 {
	h.t.Helper()
	targets := h.getList("/api/targets")
	if len(targets) == 0 {
		h.t.Fatal("no targets")
	}
	return targets[0].id()
}

// project creates a project on the seeded target with arbitrary extra fields.
func (h *harness) project(name string, extra obj) obj {
	h.t.Helper()
	body := obj{"name": name, "target_id": h.firstTargetID(),
		"repo_path": "/mock/" + name}
	for k, v := range extra {
		body[k] = v
	}
	return h.post("/api/projects", body, 201)
}

// task creates a task; extra overrides any field.
func (h *harness) task(projectID int64, title, prompt string, extra obj) obj {
	h.t.Helper()
	body := obj{"project_id": projectID, "title": title, "prompt": prompt}
	for k, v := range extra {
		body[k] = v
	}
	return h.post("/api/tasks", body, 201)
}

// run creates a task, dispatches it, and waits for it to leave the board's
// active states.
func (h *harness) run(projectID int64, title, prompt string, extra obj) obj {
	h.t.Helper()
	t := h.task(projectID, title, prompt, extra)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", t.id()), obj{}, 200)
	h.waitStatus(t.id(), "review")
	return h.get(fmt.Sprintf("/api/tasks/%d", t.id()))
}

// ---- waiting ------------------------------------------------------------

const waitTimeout = 20 * time.Second

// waitUntil polls a condition. Everything in this service is asynchronous by
// design, so an explicit poll is the honest way to test it.
func (h *harness) waitUntil(what string, fn func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s", what)
}

func (h *harness) taskStatus(id int64) string {
	return h.get(fmt.Sprintf("/api/tasks/%d", id)).str("status")
}

func (h *harness) waitStatus(id int64, want string) obj {
	h.t.Helper()
	h.waitUntil(fmt.Sprintf("task %d to reach %s", id, want),
		func() bool { return h.taskStatus(id) == want })
	return h.get(fmt.Sprintf("/api/tasks/%d", id))
}

func (h *harness) pendingApprovals() []obj {
	return h.getList("/api/approvals?status=pending")
}

// waitApproval blocks until the gated agent has actually asked for permission.
func (h *harness) waitApproval(taskID int64) obj {
	h.t.Helper()
	var found obj
	h.waitUntil(fmt.Sprintf("a pending approval on task %d", taskID), func() bool {
		for _, a := range h.pendingApprovals() {
			if int64(a.num("task_id")) == taskID {
				found = a
				return true
			}
		}
		return false
	})
	return found
}

// launchCmd is the tmux command the mock target was actually asked to run.
func (h *harness) launchCmd() string {
	h.t.Helper()
	for _, c := range h.mock().CmdLog() {
		if len(c) > 16 && c[:16] == "tmux new-session" {
			return c
		}
	}
	h.t.Fatalf("no launch command was issued; log: %v", h.mock().CmdLog())
	return ""
}

// staged returns the one file staged on the target whose path ends with suffix.
func (h *harness) staged(suffix string) []byte {
	h.t.Helper()
	files := h.mock().Files()
	for path, data := range files {
		if len(path) >= len(suffix) && path[len(path)-len(suffix):] == suffix {
			return data
		}
	}
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	h.t.Fatalf("nothing staged at *%s (have: %v)", suffix, keys)
	return nil
}

func (h *harness) stagedJSON(suffix string) obj {
	h.t.Helper()
	var out obj
	if err := json.Unmarshal(h.staged(suffix), &out); err != nil {
		h.t.Fatalf("staged %s is not JSON: %v", suffix, err)
	}
	return out
}

// cmdLogHas reports whether any command issued to the target contains want.
func (h *harness) cmdLogHas(want string) bool {
	for _, c := range h.mock().CmdLog() {
		if contains(c, want) {
			return true
		}
	}
	return false
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || bytes.Contains([]byte(haystack), []byte(needle))
}

func writeFile(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func httpGet(url string) (*http.Response, error) { return http.Get(url) }

// request2 runs a request, asserts the status, and returns the object body.
func (h *harness) request2(method, path string, body any, want int) obj {
	h.t.Helper()
	var out obj
	h.decode(method, path, body, want, &out)
	return out
}
