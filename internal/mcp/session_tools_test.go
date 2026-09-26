package mcp

// Unit tests for list_sessions, start_session and send_to_session against
// hand-rolled httptest fake servers (both lectern's own API and, where a
// test needs one, Grimoire's) rather than a real internal/app.App: these
// tools' interesting behavior — filtering/sorting, name resolution, the
// create/poll/attach/send sequencing, and graceful Grimoire degradation —
// is all in this package's own code, not in the server it talks to, so a
// fake that returns exactly the shapes under test gives tighter, faster
// coverage than standing up a mock target and waiting on its tmux poll
// loop. TestClaimWorkReleaseWorkListClaims above already covers the
// real-server end-to-end style for this package.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- small test helpers -----------------------------------------------------

// jsonHandler replies with v marshaled as JSON, at the given status.
func jsonHandler(status int, v any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
}

// decodeBody reads and JSON-decodes a request body for assertions.
func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode body %s: %v", raw, err)
	}
	return m
}

// readUploadedFile parses a single-file multipart upload the way
// internal/api/attachments.go does, for assertions about what start_session/
// send_to_session actually staged.
func readUploadedFile(t *testing.T, r *http.Request) (filename string, content []byte) {
	t.Helper()
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		t.Fatalf("parse multipart: %v", err)
	}
	files := r.MultipartForm.File["file"]
	if len(files) != 1 {
		t.Fatalf("expected exactly one uploaded file, got %d", len(files))
	}
	f, err := files[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return files[0].Filename, data
}

// withFastPolling shrinks the module-level polling knobs for the duration of
// a test, so start_session's create→poll→attach→send sequence against a
// scripted "starting" then "waiting" fake doesn't sit through the real 2s
// production interval.
func withFastPolling(t *testing.T) {
	t.Helper()
	origInterval, origTimeout := pollInterval, sessionReadyTimeout
	pollInterval = 5 * time.Millisecond
	sessionReadyTimeout = 2 * time.Second
	t.Cleanup(func() { pollInterval, sessionReadyTimeout = origInterval, origTimeout })
}

// ---- list_sessions -----------------------------------------------------------

func TestListSessionsFilterAndSort(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects", jsonHandler(200, []map[string]any{
		{"id": 1.0, "name": "alpha"}, {"id": 2.0, "name": "beta"},
	}))
	mux.HandleFunc("GET /api/sessions", jsonHandler(200, []map[string]any{
		{"id": 1.0, "name": "build-x", "agent": "claude", "status": "waiting",
			"project_id": 1.0, "workdir": "/repo/alpha", "updated_at": 300.0},
		{"id": 2.0, "name": "notes-thing", "agent": "codex", "status": "idle",
			"project_id": 2.0, "workdir": "/repo/beta", "updated_at": 200.0},
		{"id": 3.0, "name": "old-run", "agent": "claude", "status": "dead",
			"project_id": 1.0, "workdir": "/repo/alpha", "ended_at": 50.0, "updated_at": 100.0},
	}))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := New(srv.URL, "")

	// Default: only live sessions, most recently updated first.
	raw, err := s.call("list_sessions", map[string]any{})
	if err != nil {
		t.Fatalf("list_sessions: %v", err)
	}
	rows, ok := raw.([]map[string]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("expected 2 live sessions, got %#v", raw)
	}
	if rows[0]["id"] != 1.0 || rows[1]["id"] != 2.0 {
		t.Fatalf("expected id 1 then 2 (most recently updated first), got %v then %v", rows[0]["id"], rows[1]["id"])
	}
	if rows[0]["project"] != "alpha" || rows[0]["waiting_for_input"] != true {
		t.Fatalf("expected row 0 joined to project alpha and waiting_for_input, got %#v", rows[0])
	}

	// include_ended:true brings the dead one back, still sorted.
	raw, err = s.call("list_sessions", map[string]any{"include_ended": true})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ = raw.([]map[string]any)
	if len(rows) != 3 || rows[2]["id"] != 3.0 {
		t.Fatalf("expected the dead session included last, got %#v", rows)
	}

	// query filters by substring across name/project/workdir/agent.
	raw, err = s.call("list_sessions", map[string]any{"query": "beta"})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ = raw.([]map[string]any)
	if len(rows) != 1 || rows[0]["id"] != 2.0 {
		t.Fatalf("expected query %q to match only the beta session, got %#v", "beta", rows)
	}
}

// ---- start_session -------------------------------------------------------

func TestStartSessionSimplePrimesWithoutAttachments(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects", jsonHandler(200, []map[string]any{{"id": 5.0, "name": "proj"}}))
	var created map[string]any
	mux.HandleFunc("POST /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		created = decodeBody(t, r)
		jsonHandler(201, map[string]any{"id": 42.0, "name": "sess-1", "status": "starting"})(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := New(srv.URL, "")

	raw, err := s.call("start_session", map[string]any{
		"project": "proj", "prompt": "build the thing",
	})
	if err != nil {
		t.Fatalf("start_session: %v", err)
	}
	if created["project_id"] != 5.0 || created["prime"] != "build the thing" {
		t.Fatalf("expected project_id resolved and prime set, got %#v", created)
	}
	if _, hasFiles := created["files"]; hasFiles {
		t.Fatalf("no attachment fields should reach POST /sessions here: %#v", created)
	}
	out, ok := raw.(map[string]any)
	if !ok || out["id"] != 42.0 || out["attach_hint"] != "lectern attach session 42" {
		t.Fatalf("unexpected result: %#v", raw)
	}
}

func TestStartSessionRequiresExactlyOneTarget(t *testing.T) {
	s := New("http://127.0.0.1:1", "") // unreachable — must never be dialed
	for name, args := range map[string]map[string]any{
		"none":  {"prompt": "x"},
		"both":  {"prompt": "x", "project": "a", "workdir": "/tmp/a"},
		"three": {"prompt": "x", "project": "a", "workdir": "/tmp/a", "scratch": true},
	} {
		if _, err := s.call("start_session", args); err == nil {
			t.Errorf("%s: expected an error requiring exactly one of project/workdir/scratch", name)
		}
	}
	if _, err := s.call("start_session", map[string]any{"scratch": true}); err == nil {
		t.Errorf("expected prompt to be required")
	}
}

func TestStartSessionWithFilesContextAndNotesAttachesThenSends(t *testing.T) {
	withFastPolling(t)

	// A local file the caller is handing over.
	dir := t.TempDir()
	localFile := filepath.Join(dir, "design.txt")
	if err := os.WriteFile(localFile, []byte("the design"), 0o600); err != nil {
		t.Fatal(err)
	}

	grimoire := http.NewServeMux()
	grimoire.HandleFunc("GET /api/notes/projects/plan.md", jsonHandler(200,
		map[string]any{"path": "projects/plan.md", "title": "Plan", "body": "plan body"}))
	var grimoireCreate map[string]any
	var grimoireCreateHeader http.Header
	grimoire.HandleFunc("POST /api/notes", func(w http.ResponseWriter, r *http.Request) {
		grimoireCreate = decodeBody(t, r)
		grimoireCreateHeader = r.Header.Clone()
		jsonHandler(201, map[string]any{"path": "lectern/build-the-thing-brief.md", "title": "x", "body": "y"})(w, r)
	})
	gsrv := httptest.NewServer(grimoire)
	defer gsrv.Close()
	t.Setenv("LECTERN_GRIMOIRE_URL", gsrv.URL)

	var mu sync.Mutex
	statusCalls := 0
	var uploaded []string // filenames, in order received
	var uploadedContent = map[string]string{}
	var sentText string

	mux := http.NewServeMux()
	var created map[string]any
	mux.HandleFunc("POST /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		created = decodeBody(t, r)
		jsonHandler(201, map[string]any{"id": 7.0, "name": "sess-7", "status": "starting", "setup_state": "creating"})(w, r)
	})
	mux.HandleFunc("GET /api/sessions/7", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		statusCalls++
		n := statusCalls
		mu.Unlock()
		if n == 1 {
			jsonHandler(200, map[string]any{"id": 7.0, "name": "sess-7", "status": "starting", "setup_state": "creating"})(w, r)
			return
		}
		jsonHandler(200, map[string]any{"id": 7.0, "name": "sess-7", "status": "waiting"})(w, r)
	})
	mux.HandleFunc("POST /api/sessions/7/attachments", func(w http.ResponseWriter, r *http.Request) {
		name, data := readUploadedFile(t, r)
		mu.Lock()
		uploaded = append(uploaded, name)
		uploadedContent[name] = string(data)
		n := len(uploaded)
		mu.Unlock()
		p := fmt.Sprintf("/repo/.lectern/context/dir%d/%s", n, name)
		jsonHandler(201, map[string]any{"name": name, "path": p, "size": len(data)})(w, r)
	})
	mux.HandleFunc("POST /api/sessions/7/send", func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, r)
		mu.Lock()
		sentText, _ = body["text"].(string)
		mu.Unlock()
		jsonHandler(200, map[string]any{"sent": true})(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := New(srv.URL, "")

	raw, err := s.call("start_session", map[string]any{
		"workdir": "/repo", "name": "Build the thing", "prompt": "build the thing",
		"context": "the plan", "files": []any{localFile}, "notes": []any{"projects/plan.md"},
	})
	if err != nil {
		t.Fatalf("start_session: %v", err)
	}
	if created["prime"] != nil && created["prime"] != "" {
		t.Fatalf("expected no prime when attachments are given, got %#v", created["prime"])
	}
	if statusCalls < 2 {
		t.Fatalf("expected start_session to poll until ready, got %d status calls", statusCalls)
	}
	if len(uploaded) != 3 {
		t.Fatalf("expected 3 uploads (file, context, note), got %v", uploaded)
	}
	if uploadedContent["design.txt"] != "the design" {
		t.Fatalf("local file content mismatch: %q", uploadedContent["design.txt"])
	}
	if uploadedContent["lectern-context.md"] != "the plan" {
		t.Fatalf("context content mismatch: %q", uploadedContent["lectern-context.md"])
	}
	if uploadedContent["plan.md"] != "plan body" {
		t.Fatalf("note content mismatch: %q", uploadedContent["plan.md"])
	}

	if grimoireCreateHeader.Get("X-Grimoire-Agent") != "lectern" {
		t.Fatalf("expected X-Grimoire-Agent header on the note write, got %v", grimoireCreateHeader)
	}
	if grimoireCreate["title"] != "Build the thing — brief" {
		t.Fatalf("expected the session name in the brief title, got %#v", grimoireCreate["title"])
	}
	if grimoireCreate["body"] != "the plan" {
		t.Fatalf("expected the context text as the note body, got %#v", grimoireCreate["body"])
	}

	if !strings.Contains(sentText, "build the thing") || !strings.Contains(sentText, "Context files:") ||
		!strings.Contains(sentText, "/repo/.lectern/context/dir1/design.txt") ||
		!strings.Contains(sentText, "/repo/.lectern/context/dir3/plan.md (Grimoire note: projects/plan.md)") {
		t.Fatalf("unexpected sent message: %q", sentText)
	}

	out, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("expected an object result, got %#v", raw)
	}
	if out["grimoire_note"] != "lectern/build-the-thing-brief.md" {
		t.Fatalf("expected the saved brief's path, got %#v", out["grimoire_note"])
	}
	if out["grimoire_error"] != nil {
		t.Fatalf("did not expect a grimoire error, got %v", out["grimoire_error"])
	}
	paths, _ := out["attached_paths"].([]string)
	if len(paths) != 3 {
		t.Fatalf("expected 3 attached_paths, got %#v", out["attached_paths"])
	}
}

func TestStartSessionGrimoireUnreachableDegradesGracefully(t *testing.T) {
	withFastPolling(t)
	// Nothing listens here; the note write must fail fast and cleanly.
	t.Setenv("LECTERN_GRIMOIRE_URL", "http://127.0.0.1:1")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions", jsonHandler(201, map[string]any{"id": 9.0, "name": "sess-9", "status": "starting"}))
	mux.HandleFunc("GET /api/sessions/9", jsonHandler(200, map[string]any{"id": 9.0, "name": "sess-9", "status": "waiting"}))
	var sentText string
	mux.HandleFunc("POST /api/sessions/9/attachments", func(w http.ResponseWriter, r *http.Request) {
		name, _ := readUploadedFile(t, r)
		jsonHandler(201, map[string]any{"name": name, "path": "/repo/.lectern/context/d/" + name})(w, r)
	})
	mux.HandleFunc("POST /api/sessions/9/send", func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, r)
		sentText, _ = body["text"].(string)
		jsonHandler(200, map[string]any{"sent": true})(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := New(srv.URL, "")

	raw, err := s.call("start_session", map[string]any{
		"workdir": "/repo", "prompt": "go", "context": "the plan",
	})
	if err != nil {
		t.Fatalf("start_session should still succeed when only Grimoire is down: %v", err)
	}
	out, _ := raw.(map[string]any)
	if out["grimoire_note"] != nil {
		t.Fatalf("expected no grimoire_note when Grimoire is unreachable, got %v", out["grimoire_note"])
	}
	errText, _ := out["grimoire_error"].(string)
	if errText == "" {
		t.Fatal("expected a clear grimoire_error explaining the degraded save")
	}
	if !strings.Contains(sentText, "Context files:") {
		t.Fatalf("expected the context file to still be attached and sent, got %q", sentText)
	}
}

func TestStartSessionRejectsMissingLocalFile(t *testing.T) {
	withFastPolling(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions", jsonHandler(201, map[string]any{"id": 1.0, "name": "s", "status": "starting"}))
	mux.HandleFunc("GET /api/sessions/1", jsonHandler(200, map[string]any{"id": 1.0, "name": "s", "status": "waiting"}))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := New(srv.URL, "")

	_, err := s.call("start_session", map[string]any{
		"workdir": "/repo", "prompt": "go", "files": []any{"/no/such/file.txt"},
	})
	if err == nil {
		t.Fatal("expected a clear error for a local file that does not exist")
	}
}

// ---- send_to_session -----------------------------------------------------

func newSendFakeServer(t *testing.T) (*httptest.Server, *sync.Mutex, *[]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var sent []map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions", jsonHandler(200, []map[string]any{
		{"id": 10.0, "name": "alpha-build", "status": "waiting"},
		{"id": 11.0, "name": "alpha-build-2", "status": "idle"},
		{"id": 12.0, "name": "gamma", "status": "dead", "ended_at": 5.0},
	}))
	mux.HandleFunc("GET /api/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("id") {
		case "10":
			jsonHandler(200, map[string]any{"id": 10.0, "name": "alpha-build", "status": "waiting"})(w, r)
		case "20":
			jsonHandler(200, map[string]any{"id": 20.0, "name": "running-one", "status": "running"})(w, r)
		default:
			http.Error(w, "no such session", 404)
		}
	})
	mux.HandleFunc("POST /api/sessions/{id}/send", func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, r)
		mu.Lock()
		sent = append(sent, body)
		mu.Unlock()
		jsonHandler(200, map[string]any{"sent": true})(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &mu, &sent
}

func TestSendToSessionResolvesByNumericID(t *testing.T) {
	srv, mu, sent := newSendFakeServer(t)
	s := New(srv.URL, "")
	raw, err := s.call("send_to_session", map[string]any{"session": "10", "message": "hello"})
	if err != nil {
		t.Fatalf("send_to_session: %v", err)
	}
	out, _ := raw.(map[string]any)
	if out["sent"] != true || out["session_id"] != int64(10) {
		t.Fatalf("unexpected result: %#v", raw)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*sent) != 1 || (*sent)[0]["text"] != "hello" {
		t.Fatalf("expected exactly one send with text=hello, got %#v", *sent)
	}
}

func TestSendToSessionResolvesUniqueExactName(t *testing.T) {
	srv, mu, sent := newSendFakeServer(t)
	s := New(srv.URL, "")
	if _, err := s.call("send_to_session", map[string]any{"session": "alpha-build", "message": "hi"}); err != nil {
		t.Fatalf("send_to_session: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*sent) != 1 {
		t.Fatalf("expected the exact-name match to resolve unambiguously, got %#v", *sent)
	}
}

func TestSendToSessionAmbiguousNameListsCandidates(t *testing.T) {
	srv, _, _ := newSendFakeServer(t)
	s := New(srv.URL, "")
	_, err := s.call("send_to_session", map[string]any{"session": "alpha", "message": "hi"})
	if err == nil {
		t.Fatal("expected an ambiguous-name error")
	}
	if !strings.Contains(err.Error(), "alpha-build") || !strings.Contains(err.Error(), "alpha-build-2") {
		t.Fatalf("expected both candidates named in the error, got: %v", err)
	}
}

func TestSendToSessionMissingNameErrors(t *testing.T) {
	srv, _, _ := newSendFakeServer(t)
	s := New(srv.URL, "")
	_, err := s.call("send_to_session", map[string]any{"session": "does-not-exist", "message": "hi"})
	if err == nil {
		t.Fatal("expected a not-found error")
	}
}

func TestSendToSessionRefusesEndedSession(t *testing.T) {
	srv, _, _ := newSendFakeServer(t)
	s := New(srv.URL, "")
	_, err := s.call("send_to_session", map[string]any{"session": "gamma", "message": "hi"})
	if err == nil || !strings.Contains(err.Error(), "ended") {
		t.Fatalf("expected a clear ended-session refusal, got: %v", err)
	}
}

func TestSendToSessionRequiresMessageOrAttachment(t *testing.T) {
	srv, _, _ := newSendFakeServer(t)
	s := New(srv.URL, "")
	_, err := s.call("send_to_session", map[string]any{"session": "10"})
	if err == nil {
		t.Fatal("expected an error when neither message nor files/notes/context are given")
	}
}

func TestSendToSessionInterruptSendsEscapeFirst(t *testing.T) {
	srv, mu, sent := newSendFakeServer(t)
	s := New(srv.URL, "")
	if _, err := s.call("send_to_session", map[string]any{
		"session": "20", "message": "stop and look at this", "interrupt": true,
	}); err != nil {
		t.Fatalf("send_to_session: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*sent) != 2 {
		t.Fatalf("expected an escape key send followed by the text, got %#v", *sent)
	}
	if (*sent)[0]["key"] != "escape" {
		t.Fatalf("expected the first send to be the escape key, got %#v", (*sent)[0])
	}
	if (*sent)[1]["text"] != "stop and look at this" {
		t.Fatalf("expected the second send to carry the message, got %#v", (*sent)[1])
	}
}

func TestSendToSessionGrimoireUnreachableDegradesGracefully(t *testing.T) {
	t.Setenv("LECTERN_GRIMOIRE_URL", "http://127.0.0.1:1")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/20", jsonHandler(200, map[string]any{"id": 20.0, "name": "running-one", "status": "running"}))
	mux.HandleFunc("POST /api/sessions/20/attachments", func(w http.ResponseWriter, r *http.Request) {
		name, _ := readUploadedFile(t, r)
		jsonHandler(201, map[string]any{"name": name, "path": "/repo/.lectern/context/d/" + name})(w, r)
	})
	var sentText string
	mux.HandleFunc("POST /api/sessions/20/send", func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, r)
		sentText, _ = body["text"].(string)
		jsonHandler(200, map[string]any{"sent": true})(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := New(srv.URL, "")

	raw, err := s.call("send_to_session", map[string]any{
		"session": "20", "message": "here's the plan", "context": "the plan",
		"save_context_to_grimoire": true,
	})
	if err != nil {
		t.Fatalf("send_to_session should still succeed when only Grimoire is down: %v", err)
	}
	out, _ := raw.(map[string]any)
	if out["sent"] != true {
		t.Fatalf("expected the message to still be sent, got %#v", raw)
	}
	if out["grimoire_note"] != nil {
		t.Fatalf("expected no grimoire_note when Grimoire is unreachable, got %v", out["grimoire_note"])
	}
	if s, _ := out["grimoire_error"].(string); s == "" {
		t.Fatal("expected a clear grimoire_error")
	}
	if !strings.Contains(sentText, "here's the plan") || !strings.Contains(sentText, "Context files:") {
		t.Fatalf("unexpected sent text: %q", sentText)
	}
}

// TestStartSessionValidatesAgentAgainstTheRegistry proves start_session
// checks `agent` against the live registry (GET /api/agents) and, on an
// unknown name, fails with the valid names listed — before ever reaching
// POST /sessions — rather than relying on that endpoint's generic "define it
// in /api/agents" 422.
func TestStartSessionValidatesAgentAgainstTheRegistry(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/agents", jsonHandler(200, []map[string]any{
		{"name": "claude", "builtin": true},
		{"name": "codex", "builtin": true},
		{"name": "aider", "command": "aider"},
	}))
	mux.HandleFunc("GET /api/projects", jsonHandler(200, []map[string]any{{"id": 5.0, "name": "proj"}}))
	posted := false
	mux.HandleFunc("POST /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		posted = true
		jsonHandler(201, map[string]any{"id": 1.0, "name": "s"})(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := New(srv.URL, "")

	_, err := s.call("start_session", map[string]any{
		"project": "proj", "prompt": "x", "agent": "not-a-real-agent",
	})
	if err == nil {
		t.Fatal("expected an error for an unregistered agent name")
	}
	if !strings.Contains(err.Error(), "not-a-real-agent") {
		t.Fatalf("error should name the bad value: %v", err)
	}
	for _, want := range []string{"claude", "codex", "aider"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error should list valid agent names (missing %q): %v", want, err)
		}
	}
	if posted {
		t.Fatal("POST /api/sessions must not be reached for an invalid agent name")
	}

	// A registered custom agent must still work.
	if _, err := s.call("start_session", map[string]any{
		"project": "proj", "prompt": "x", "agent": "aider",
	}); err != nil {
		t.Fatalf("a registered agent name should be accepted: %v", err)
	}
	if !posted {
		t.Fatal("expected POST /api/sessions to be reached for a valid agent")
	}
}
