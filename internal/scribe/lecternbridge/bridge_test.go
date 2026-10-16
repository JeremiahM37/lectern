package lecternbridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

const testToken = "hook-secret"

// fakeLectern mimics the parts of Lectern's per-session hook endpoint the
// bridge uses (POST /api/hook/session/{id}/{event}, bearer-authenticated).
// Its PermissionRequest handler answers from decide; when decide returns nil
// the request is held until the client goes away, and cancelled reports that.
type fakeLectern struct {
	srv       *httptest.Server
	seen      chan recorded
	decide    func(body map[string]any) map[string]any
	cancelled chan struct{}
}

type recorded struct {
	Path string
	Body map[string]any
}

func newFakeLectern(t *testing.T) *fakeLectern {
	f := &fakeLectern{seen: make(chan recorded, 64), cancelled: make(chan struct{}, 8)}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLectern) config() Config {
	return Config{HookURL: f.srv.URL + "/api/hook/session/42", Token: testToken, Client: f.srv.Client()}
}

func (f *fakeLectern) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/api/hook/session/42/") {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	event := strings.TrimPrefix(r.URL.Path, "/api/hook/session/42/")
	f.seen <- recorded{Path: event, Body: body}
	if event != "PermissionRequest" {
		_, _ = w.Write([]byte(`{}`))
		return
	}
	var reply map[string]any
	if f.decide != nil {
		reply = f.decide(body)
	}
	if reply == nil {
		select {
		case <-r.Context().Done():
			f.cancelled <- struct{}{}
			return
		case <-time.After(10 * time.Second):
			reply = map[string]any{}
		}
	}
	_ = json.NewEncoder(w).Encode(reply)
}

// next returns the next event Lectern received.
func (f *fakeLectern) next(t *testing.T) recorded {
	t.Helper()
	select {
	case rec := <-f.seen:
		return rec
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a hook event")
		return recorded{}
	}
}

func decision(behavior string) map[string]any {
	return map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PermissionRequest",
		"decision":      map[string]any{"behavior": behavior},
	}}
}

// localHost answers when release is closed, or reports cancellation of its
// context on cancelled.
type localHost struct {
	interactive bool
	answer      tools.Decision
	release     chan struct{}
	cancelled   chan struct{}
}

func newLocalHost(interactive bool, answer tools.Decision) *localHost {
	return &localHost{interactive: interactive, answer: answer, release: make(chan struct{}), cancelled: make(chan struct{}, 1)}
}

func (h *localHost) Interactive() bool { return h.interactive }

func (h *localHost) Approve(ctx context.Context, _ tools.Approval) tools.Decision {
	select {
	case <-h.release:
		return h.answer
	case <-ctx.Done():
		h.cancelled <- struct{}{}
		return tools.Deny
	}
}

func (h *localHost) Ask(context.Context, tools.Question) (string, error) { return "", nil }
func (h *localHost) Progress(string, string)                             {}

func newTestHost(f *fakeLectern, local tools.Host) *approvalHost {
	return &approvalHost{b: newBridge(f.config()), inner: local}
}

var shellApproval = tools.Approval{Tool: "shell", Kind: "shell", Title: "Run shell command", Detail: "go test ./..."}

func TestTurnEventsReachLecternInOrder(t *testing.T) {
	f := newFakeLectern(t)
	b := newBridge(f.config())

	b.handle(agent.Event{Kind: agent.EvTurnStart, Prompt: "fix the build", Model: "anthropic/claude"})
	b.handle(agent.Event{Kind: agent.EvToolRun, CallID: "c1", Tool: "shell", Args: json.RawMessage(`{"command":"go build"}`)})
	b.handle(agent.Event{Kind: agent.EvToolResult, CallID: "c1", Tool: "shell", Result: &tools.Result{Output: "ok"}})
	b.handle(agent.Event{Kind: agent.EvTurnDone, Text: "Build is green.", Stats: &agent.TurnStats{}})

	var got []recorded
	for range 4 {
		got = append(got, f.next(t))
	}
	for i, w := range []string{"UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"} {
		if got[i].Path != w {
			t.Fatalf("event %d = %q, want %q", i, got[i].Path, w)
		}
	}
	if got[0].Body["prompt"] != "fix the build" {
		t.Errorf("UserPromptSubmit prompt = %v", got[0].Body["prompt"])
	}
	if in := got[1].Body["tool_input"].(map[string]any); in["command"] != "go build" {
		t.Errorf("PreToolUse tool_input = %v", in)
	}
	if in := got[2].Body["tool_input"].(map[string]any); in["command"] != "go build" {
		t.Errorf("PostToolUse should carry the call's input, got %v", in)
	}
	if last := got[3].Body["last_assistant_message"]; last != "Build is green." {
		t.Errorf("Stop last_assistant_message = %v", last)
	}
}

func TestTurnFailureSendsStopFailure(t *testing.T) {
	f := newFakeLectern(t)
	b := newBridge(f.config())
	b.handle(agent.Event{Kind: agent.EvTurnDone, Stats: &agent.TurnStats{Error: "rate limited"}})
	if got := f.next(t); got.Path != "StopFailure" || got.Body["error"] != "rate limited" {
		t.Fatalf("got %+v, want StopFailure with the error", got)
	}
	b.handle(agent.Event{Kind: agent.EvTurnDone, Stats: &agent.TurnStats{Error: "cancelled", Aborted: true}})
	if got := f.next(t); got.Path != "Stop" {
		t.Fatalf("aborted turn should report Stop (idle), got %q", got.Path)
	}
}

func TestSubagentEventsAreNotForwarded(t *testing.T) {
	f := newFakeLectern(t)
	b := newBridge(f.config())
	b.handle(agent.Event{Kind: agent.EvTurnDone, AgentID: "explore-1", Text: "sub result"})
	b.handle(agent.Event{Kind: agent.EvTurnDone, Text: "main"})
	if got := f.next(t); got.Path != "Stop" || got.Body["last_assistant_message"] != "main" {
		t.Fatalf("subagent turn leaked to Lectern: %+v", got)
	}
}

func TestStepFinishPostsStatuslineWithCostAndContext(t *testing.T) {
	f := newFakeLectern(t)
	b := newBridge(f.config())
	b.handle(agent.Event{Kind: agent.EvTurnStart, Model: "deepseek/v4"})
	b.handle(agent.Event{Kind: agent.EvContext, ContextUsed: 50000, ContextWindow: 200000})
	b.handle(agent.Event{Kind: agent.EvStepFinish, CostUSD: 0.42})

	f.next(t) // UserPromptSubmit
	got := f.next(t)
	if got.Path != "statusline" {
		t.Fatalf("path = %q, want statusline", got.Path)
	}
	if cost := got.Body["cost"].(map[string]any); cost["total_cost_usd"] != 0.42 {
		t.Errorf("cost = %v", cost)
	}
	cw := got.Body["context_window"].(map[string]any)
	if cw["used_percentage"] != float64(25) || cw["context_window_size"] != float64(200000) {
		t.Errorf("context_window = %v", cw)
	}
	if m := got.Body["model"].(map[string]any); m["id"] != "deepseek/v4" {
		t.Errorf("model = %v", m)
	}
}

func TestRemoteAllowWinsAndDismissesLocalPrompt(t *testing.T) {
	f := newFakeLectern(t)
	f.decide = func(map[string]any) map[string]any { return decision("allow") }
	local := newLocalHost(true, tools.Deny)
	h := newTestHost(f, local)

	if got := h.Approve(context.Background(), shellApproval); got != tools.AllowOnce {
		t.Fatalf("Approve = %v, want AllowOnce from the remote allow", got)
	}
	rec := f.next(t)
	if rec.Path != "PermissionRequest" || rec.Body["tool_name"] != "Bash" {
		t.Fatalf("PermissionRequest body = %+v", rec)
	}
	if in := rec.Body["tool_input"].(map[string]any); in["command"] != "go test ./..." {
		t.Errorf("tool_input = %v", in)
	}
	select {
	case <-local.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("local prompt was not dismissed after the remote answer")
	}
}

func TestRemoteDenyWins(t *testing.T) {
	f := newFakeLectern(t)
	f.decide = func(map[string]any) map[string]any { return decision("deny") }
	local := newLocalHost(true, tools.AllowOnce)
	h := newTestHost(f, local)
	if got := h.Approve(context.Background(), shellApproval); got != tools.Deny {
		t.Fatalf("Approve = %v, want Deny from the remote deny", got)
	}
}

func TestLocalAnswerFirstCancelsRemoteWait(t *testing.T) {
	f := newFakeLectern(t) // decide is nil: Lectern holds the request
	local := newLocalHost(true, tools.AllowOnce)
	// Answer only once the request is in flight at Lectern, so there is a
	// held request to cancel.
	go func() {
		time.Sleep(200 * time.Millisecond)
		close(local.release)
	}()
	h := newTestHost(f, local)

	if got := h.Approve(context.Background(), shellApproval); got != tools.AllowOnce {
		t.Fatalf("Approve = %v, want the local AllowOnce", got)
	}
	if rec := f.next(t); rec.Path != "PermissionRequest" {
		t.Fatalf("first event = %q, want PermissionRequest", rec.Path)
	}
	select {
	case <-f.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("Lectern's held request was not cancelled after the local answer")
	}
}

func TestNoRemoteOpinionFallsBackToLocal(t *testing.T) {
	f := newFakeLectern(t)
	f.decide = func(map[string]any) map[string]any { return map[string]any{} } // bypass mode
	local := newLocalHost(true, tools.AllowSession)
	close(local.release)
	h := newTestHost(f, local)
	if got := h.Approve(context.Background(), shellApproval); got != tools.AllowSession {
		t.Fatalf("Approve = %v, want the local answer", got)
	}
}

func TestHeadlessHostWaitsForLectern(t *testing.T) {
	f := newFakeLectern(t)
	f.decide = func(map[string]any) map[string]any {
		time.Sleep(50 * time.Millisecond)
		return decision("allow")
	}
	// A headless host answers at once with Deny; it must not win the race.
	headless := newLocalHost(false, tools.Deny)
	close(headless.release)
	h := newTestHost(f, headless)
	if got := h.Approve(context.Background(), shellApproval); got != tools.AllowOnce {
		t.Fatalf("Approve = %v, want the remote AllowOnce", got)
	}
}

func TestHeadlessHostDeniesWhenLecternHasNoOpinion(t *testing.T) {
	f := newFakeLectern(t)
	f.decide = func(map[string]any) map[string]any { return map[string]any{} }
	headless := newLocalHost(false, tools.AllowOnce)
	close(headless.release)
	h := newTestHost(f, headless)
	if got := h.Approve(context.Background(), shellApproval); got != tools.AllowOnce {
		t.Fatalf("Approve = %v, want the local host's own rule", got)
	}
}

func TestRefusedTokenIsNoOpinion(t *testing.T) {
	f := newFakeLectern(t)
	cfg := f.config()
	cfg.Token = "wrong"
	local := newLocalHost(true, tools.AllowOnce)
	close(local.release)
	h := &approvalHost{b: newBridge(cfg), inner: local}
	if got := h.Approve(context.Background(), shellApproval); got != tools.AllowOnce {
		t.Fatalf("Approve = %v, want the local answer when Lectern refuses the token", got)
	}
}

func TestAttachOutsideLecternIsAnError(t *testing.T) {
	t.Setenv(EnvHookURL, "")
	t.Setenv(EnvHookToken, "")
	local := newLocalHost(true, tools.AllowOnce)
	host, err := Attach(nil, local)
	if err != ErrNoSession {
		t.Fatalf("Attach err = %v, want ErrNoSession", err)
	}
	if host != local {
		t.Fatal("Attach outside Lectern must hand back the local host unchanged")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv(EnvHookURL, "")
	t.Setenv(EnvHookToken, "")
	if _, ok := FromEnv(); ok {
		t.Fatal("FromEnv reported a session with no environment")
	}
	t.Setenv(EnvHookURL, "http://127.0.0.1:9110/api/hook/session/7/")
	t.Setenv(EnvHookToken, "tok")
	cfg, ok := FromEnv()
	if !ok || cfg.HookURL != "http://127.0.0.1:9110/api/hook/session/7" || cfg.Token != "tok" {
		t.Fatalf("FromEnv = %+v, %v", cfg, ok)
	}
}

func TestApprovalToolShapes(t *testing.T) {
	name, in := approvalTool(tools.Approval{Tool: "write_file", Kind: "write_outside", Detail: "/etc/hosts"})
	if name != "write_file" || in["file_path"] != "/etc/hosts" {
		t.Errorf("write_outside: %s %v", name, in)
	}
	name, in = approvalTool(tools.Approval{Tool: "mcp__x", Kind: "mcp", Detail: "args"})
	if name != "mcp__x" || in["detail"] != "args" {
		t.Errorf("generic: %s %v", name, in)
	}
}

func TestClipKeepsRunesWhole(t *testing.T) {
	got := clip(strings.Repeat("é", 10), 5) // 2 bytes per rune
	if !strings.HasPrefix(got, "éé") || strings.ContainsRune(got, '�') {
		t.Fatalf("clip = %q", got)
	}
}
