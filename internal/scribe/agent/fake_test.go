package agent

import (
	"context"
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

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/openai"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// ---- scripted OpenAI-compatible server --------------------------------------
//
// The tests drive the agent through the real openai adapter. fakeLLM is an
// httptest server that speaks chat-completions SSE and answers each request
// with a scripted reply. Every request body is recorded so tests can assert
// what the agent sent.

type sseCall struct{ id, name, args string }

type sseUsage struct{ prompt, completion, cached int }

// sseTurn is one scripted model response.
type sseTurn struct {
	text   []string // text deltas, in order
	calls  []sseCall
	finish string // default: "tool_calls" when there are calls, else "stop"
	usage  *sseUsage
}

// reply writes one HTTP response.
type reply func(w http.ResponseWriter, r *http.Request)

func sseLine(v any) string {
	j, _ := json.Marshal(v)
	return "data: " + string(j) + "\n\n"
}

// sseBody renders a scripted turn as the wire stream a real server sends.
func sseBody(t sseTurn) string {
	var b strings.Builder
	choice := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{"index": 0, "delta": delta, "finish_reason": finish}
	}
	for _, s := range t.text {
		b.WriteString(sseLine(map[string]any{"choices": []any{choice(map[string]any{"content": s}, nil)}}))
	}
	for i, c := range t.calls {
		args := c.args
		if args == "" {
			args = "{}"
		}
		half := len(args) / 2
		start := map[string]any{"index": i, "id": c.id, "type": "function", "function": map[string]any{"name": c.name, "arguments": ""}}
		b.WriteString(sseLine(map[string]any{"choices": []any{choice(map[string]any{"tool_calls": []any{start}}, nil)}}))
		// Arguments arrive in two fragments, as they do from real servers.
		for _, frag := range []string{args[:half], args[half:]} {
			d := map[string]any{"index": i, "function": map[string]any{"arguments": frag}}
			b.WriteString(sseLine(map[string]any{"choices": []any{choice(map[string]any{"tool_calls": []any{d}}, nil)}}))
		}
	}
	finish := t.finish
	if finish == "" {
		finish = "stop"
		if len(t.calls) > 0 {
			finish = "tool_calls"
		}
	}
	b.WriteString(sseLine(map[string]any{"choices": []any{choice(map[string]any{}, finish)}}))
	if t.usage != nil {
		u := map[string]any{"prompt_tokens": t.usage.prompt, "completion_tokens": t.usage.completion}
		if t.usage.cached > 0 {
			u["prompt_tokens_details"] = map[string]any{"cached_tokens": t.usage.cached}
		}
		b.WriteString(sseLine(map[string]any{"choices": []any{}, "usage": u}))
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// turn answers with a complete scripted stream.
func turn(t sseTurn) reply {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sseBody(t))
	}
}

// apiError answers with a non-2xx status and a JSON error body.
func apiError(status int, retryAfter, body string) reply {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

// wireBody is the subset of a chat-completions request the tests inspect.
type wireBody struct {
	Model    string    `json:"model"`
	Messages []wireMsg `json:"messages"`
	Tools    []struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
	MaxTokens int  `json:"max_tokens"`
	Stream    bool `json:"stream"`
}

type wireMsg struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	ToolCalls []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
	ToolCallID string `json:"tool_call_id"`
}

// text returns the message content as a string (empty for null).
func (m wireMsg) text() string {
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	if string(m.Content) == "null" {
		return ""
	}
	return string(m.Content)
}

type recordedReq struct {
	body wireBody
	raw  []byte
}

// fakeLLM is the scripted server. pick chooses the reply for request n
// (0-based, in arrival order).
type fakeLLM struct {
	t    *testing.T
	srv  *httptest.Server
	quit chan struct{}

	mu          sync.Mutex
	pick        func(n int, req recordedReq) reply
	reqs        []recordedReq
	inflight    int
	maxInflight int
}

func newFake(t *testing.T) *fakeLLM {
	t.Helper()
	f := &fakeLLM{t: t, quit: make(chan struct{})}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	// Cleanups run last-in first-out: release hung handlers, then close.
	t.Cleanup(f.srv.Close)
	t.Cleanup(func() { close(f.quit) })
	return f
}

func (f *fakeLLM) setPick(p func(int, recordedReq) reply) {
	f.mu.Lock()
	f.pick = p
	f.mu.Unlock()
}

func (f *fakeLLM) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body wireBody
	if err := json.Unmarshal(raw, &body); err != nil {
		f.t.Errorf("request body is not JSON: %v", err)
	}
	rec := recordedReq{body: body, raw: raw}

	f.mu.Lock()
	n := len(f.reqs)
	f.reqs = append(f.reqs, rec)
	f.inflight++
	if f.inflight > f.maxInflight {
		f.maxInflight = f.inflight
	}
	pick := f.pick
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inflight--
		f.mu.Unlock()
	}()

	if r.URL.Path != "/v1/chat/completions" {
		f.t.Errorf("unexpected path %s", r.URL.Path)
		http.NotFound(w, r)
		return
	}
	if pick == nil {
		f.t.Errorf("request #%d arrived but the test set no script", n+1)
		apiError(500, "", `{"error":{"message":"no script"}}`)(w, r)
		return
	}
	pick(n, rec)(w, r)
}

// requests returns a copy of everything the server has received.
func (f *fakeLLM) requests() []recordedReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedReq(nil), f.reqs...)
}

func (f *fakeLLM) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

// inFlight returns the current and peak number of requests being served.
func (f *fakeLLM) inFlight() (cur, peak int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inflight, f.maxInflight
}

// scripted answers request n with replies[n]; running past the script fails
// the test.
func scripted(t *testing.T, replies ...reply) func(int, recordedReq) reply {
	return func(n int, _ recordedReq) reply {
		if n >= len(replies) {
			t.Errorf("unexpected model request #%d (script has %d)", n+1, len(replies))
			return apiError(500, "", `{"error":{"message":"unexpected request"}}`)
		}
		return replies[n]
	}
}

// hang writes headers (and an optional text delta), then goes silent until
// the client gives up or the test ends.
func (f *fakeLLM) hang(partial string) reply {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if partial != "" {
			_, _ = io.WriteString(w, sseLine(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": partial}}}}))
		}
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-f.quit:
		case <-time.After(20 * time.Second):
		}
	}
}

// gated holds next until gate is closed.
func (f *fakeLLM) gated(gate <-chan struct{}, next reply) reply {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		case <-f.quit:
			return
		}
		next(w, r)
	}
}

// priced is a model with prices, so cost accounting is observable.
var priced = llm.ModelInfo{ContextWindow: 100000, InputPerMTok: 3, OutputPerMTok: 15, CacheReadPerMTok: 0.3}

// fakeModel points a Model at the fake server through the real openai adapter.
func fakeModel(base, ref string, info llm.ModelInfo) Model {
	_, id := llm.SplitModelRef(ref)
	return Model{
		Ref:      ref,
		ID:       id,
		Info:     info,
		Provider: openai.New(openai.Options{ID: "fake", BaseURL: base + "/v1"}),
	}
}

// baseOptions returns options for a plain agent on f. Tests override what
// they need.
func baseOptions(f *fakeLLM, reg *tools.Registry, log *eventLog, workdir string) Options {
	return Options{
		Model:        fakeModel(f.srv.URL, "fake/model-a", priced),
		Tools:        reg,
		Env:          tools.Env{Workdir: workdir},
		System:       func(mode string) string { return "SYS " + mode },
		Sink:         log.sink,
		StallTimeout: 5 * time.Second,
	}
}

// ---- request inspection -----------------------------------------------------

func lastUserText(req recordedReq) string {
	for i := len(req.body.Messages) - 1; i >= 0; i-- {
		if m := req.body.Messages[i]; m.Role == "user" {
			return m.text()
		}
	}
	return ""
}

func msgsWithRole(req recordedReq, role string) []wireMsg {
	var out []wireMsg
	for _, m := range req.body.Messages {
		if m.Role == role {
			out = append(out, m)
		}
	}
	return out
}

// toolMsg finds the role=tool message answering callID.
func toolMsg(t *testing.T, req recordedReq, callID string) wireMsg {
	t.Helper()
	for _, m := range msgsWithRole(req, "tool") {
		if m.ToolCallID == callID {
			return m
		}
	}
	t.Fatalf("no tool message for %s in request with messages %s", callID, req.raw)
	return wireMsg{}
}

func systemText(req recordedReq) string {
	if s := msgsWithRole(req, "system"); len(s) > 0 {
		return s[0].text()
	}
	return ""
}

// ---- events -----------------------------------------------------------------

type eventLog struct {
	mu  sync.Mutex
	evs []Event
}

func (l *eventLog) sink(e Event) {
	l.mu.Lock()
	l.evs = append(l.evs, e)
	l.mu.Unlock()
}

func (l *eventLog) snapshot() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Event(nil), l.evs...)
}

func (l *eventLog) kinds() []EventKind {
	var out []EventKind
	for _, e := range l.snapshot() {
		out = append(out, e.Kind)
	}
	return out
}

func (l *eventLog) of(k EventKind) []Event {
	var out []Event
	for _, e := range l.snapshot() {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

// waitUntil polls cond until it holds or the test fails.
func waitUntil(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ---- test tools -------------------------------------------------------------

// readTool reads a real file under the workdir.
type readTool struct{}

func (readTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "read",
		Description: "Read a file in the project.",
		Schema:      tools.Schema(map[string]any{"path": tools.Str("file path")}, "path"),
	}
}
func (readTool) ReadOnly() bool { return true }
func (readTool) Run(_ context.Context, env *tools.Env, args json.RawMessage) tools.Result {
	var in struct {
		Path string `json:"path"`
	}
	if err := tools.Decode(args, &in); err != nil {
		return tools.Errorf("%v", err)
	}
	b, err := os.ReadFile(filepath.Join(env.Workdir, in.Path))
	if err != nil {
		return tools.Errorf("read %s: %v", in.Path, err)
	}
	return tools.Result{Output: string(b), Summary: fmt.Sprintf("%d bytes", len(b))}
}

// writeTool writes a real file under the workdir and reports it as edited.
type writeTool struct{}

func (writeTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "write_file",
		Description: "Write a file in the project.",
		Schema: tools.Schema(map[string]any{
			"path":    tools.Str("file path"),
			"content": tools.Str("full file content"),
		}, "path", "content"),
	}
}
func (writeTool) ReadOnly() bool { return false }
func (writeTool) Run(_ context.Context, env *tools.Env, args json.RawMessage) tools.Result {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := tools.Decode(args, &in); err != nil {
		return tools.Errorf("%v", err)
	}
	if err := os.WriteFile(filepath.Join(env.Workdir, in.Path), []byte(in.Content), 0o644); err != nil {
		return tools.Errorf("write %s: %v", in.Path, err)
	}
	return tools.Result{Output: "wrote " + in.Path, Files: []string{in.Path}}
}

// fnTool is a tool whose behaviour the test supplies.
type fnTool struct {
	name string
	ro   bool
	run  func(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result
}

func (t fnTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        t.name,
		Description: t.name + " test tool",
		Schema:      tools.Schema(map[string]any{"id": tools.Str("label")}),
	}
}
func (t fnTool) ReadOnly() bool { return t.ro }
func (t fnTool) Run(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
	return t.run(ctx, env, args)
}

// label reads the "id" argument of a fnTool call.
func label(args json.RawMessage) string {
	var in struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(args, &in)
	return in.ID
}

// barrier releases every waiter once n have arrived.
type barrier struct {
	mu      sync.Mutex
	n       int
	arrived int
	open    chan struct{}
}

func newBarrier(n int) *barrier { return &barrier{n: n, open: make(chan struct{})} }

// wait returns true when all n parties arrived within timeout.
func (b *barrier) wait(timeout time.Duration) bool {
	b.mu.Lock()
	b.arrived++
	if b.arrived == b.n {
		close(b.open)
	}
	b.mu.Unlock()
	select {
	case <-b.open:
		return true
	case <-time.After(timeout):
		return false
	}
}

// recorder notes the order of tool bodies and the peak number running.
type recorder struct {
	mu      sync.Mutex
	order   []string
	running int
	peak    int
}

func (r *recorder) enter(name string) {
	r.mu.Lock()
	r.running++
	if r.running > r.peak {
		r.peak = r.running
	}
	r.order = append(r.order, name)
	r.mu.Unlock()
}

func (r *recorder) exit() {
	r.mu.Lock()
	r.running--
	r.mu.Unlock()
}

func (r *recorder) snapshot() ([]string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.order...), r.peak
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// hasUserNotice reports whether a request's user messages include text.
func hasUserNotice(req recordedReq, text string) bool {
	for _, m := range msgsWithRole(req, "user") {
		if strings.Contains(m.text(), text) {
			return true
		}
	}
	return false
}
