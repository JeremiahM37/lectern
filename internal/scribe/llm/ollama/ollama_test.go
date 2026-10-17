package ollama

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// capture serves one canned response and records the last request body.
type capture struct {
	mu     sync.Mutex
	body   []byte
	path   string
	auth   string
	header http.Header
}

func (c *capture) snapshot() ([]byte, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.body, c.path
}

func serve(t *testing.T, status int, reply string) (*httptest.Server, *capture) {
	t.Helper()
	c := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.body, c.path, c.header = b, r.URL.Path, r.Header.Clone()
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

func collect(t *testing.T, ch <-chan llm.Event) []llm.Event {
	t.Helper()
	var out []llm.Event
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func decodeBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, raw)
	}
	return m
}

func TestRequestBodyNativeShape(t *testing.T) {
	srv, c := serve(t, 200, `{"done":true,"done_reason":"stop"}`+"\n")
	p := New(Options{BaseURL: srv.URL + "/", NumCtx: func(string) int { return 32768 }, Headers: map[string]string{"X-Extra": "1"}})
	temp := 0.5
	schema := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
	req := llm.Request{
		Model:       "qwen3.6:35b-a3b",
		System:      "be careful",
		Tools:       []llm.ToolSpec{{Name: "list_files", Description: "list", Schema: schema}},
		Temperature: &temp,
		MaxTokens:   100,
		Reasoning:   "medium",
		Headers:     map[string]string{"X-Session": "s1"},
		Messages: []llm.Message{
			llm.UserText("look"),
			{Role: llm.RoleUser, Parts: []llm.Part{{Type: llm.PartText, Text: "see"}, {Type: llm.PartImage, MediaType: "image/png", Data: []byte("PNG")}}},
			{Role: llm.RoleAssistant, Parts: []llm.Part{
				{Type: llm.PartReasoning, Text: "hidden"},
				{Type: llm.PartText, Text: "checking"},
				{Type: llm.PartToolCall, ToolCallID: "x1", ToolName: "list_files", Args: json.RawMessage(`{"path":"pkg"}`)},
			}},
			{Role: llm.RoleTool, Parts: []llm.Part{{Type: llm.PartToolResult, ToolCallID: "x1", ToolName: "list_files", Result: "a.go"}}},
			{Role: llm.RoleTool, Parts: []llm.Part{{Type: llm.PartToolResult, ToolCallID: "x2", ToolName: "list_files", Result: "boom", IsError: true}}},
		},
	}
	ch, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collect(t, ch)

	raw, path := c.snapshot()
	if path != "/api/chat" {
		t.Fatalf("path = %q, want /api/chat", path)
	}
	if got := c.header.Get("X-Extra"); got != "1" || c.header.Get("X-Session") != "s1" {
		t.Fatalf("headers not sent: X-Extra=%q X-Session=%q", got, c.header.Get("X-Session"))
	}
	b := decodeBody(t, raw)
	if b["model"] != "qwen3.6:35b-a3b" || b["stream"] != true {
		t.Fatalf("model/stream = %v / %v", b["model"], b["stream"])
	}
	if b["think"] != true {
		t.Fatalf("think = %v, want true", b["think"])
	}
	opts := b["options"].(map[string]any)
	if opts["num_ctx"] != float64(32768) || opts["num_predict"] != float64(100) || opts["temperature"] != 0.5 {
		t.Fatalf("options = %v", opts)
	}

	tools := b["tools"].([]any)
	fn := tools[0].(map[string]any)
	if fn["type"] != "function" || fn["function"].(map[string]any)["name"] != "list_files" {
		t.Fatalf("tool shape = %v", fn)
	}

	msgs := b["messages"].([]any)
	if len(msgs) != 6 {
		t.Fatalf("messages = %d, want 6: %v", len(msgs), msgs)
	}
	sys := msgs[0].(map[string]any)
	if sys["role"] != "system" || sys["content"] != "be careful" {
		t.Fatalf("system first = %v", sys)
	}
	if msgs[1].(map[string]any)["content"] != "look" {
		t.Fatalf("user = %v", msgs[1])
	}
	img := msgs[2].(map[string]any)
	if img["role"] != "user" || img["content"] != "see" {
		t.Fatalf("user+image = %v", img)
	}
	wantImg := base64.StdEncoding.EncodeToString([]byte("PNG"))
	if imgs := img["images"].([]any); len(imgs) != 1 || imgs[0] != wantImg {
		t.Fatalf("images = %v, want [%s]", imgs, wantImg)
	}
	as := msgs[3].(map[string]any)
	if as["role"] != "assistant" || as["content"] != "checking" {
		t.Fatalf("assistant = %v (reasoning must not be sent)", as)
	}
	call := as["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if call["name"] != "list_files" {
		t.Fatalf("call name = %v", call["name"])
	}
	if args, ok := call["arguments"].(map[string]any); !ok || args["path"] != "pkg" {
		t.Fatalf("arguments must be an object, got %v", call["arguments"])
	}
	tm := msgs[4].(map[string]any)
	if tm["role"] != "tool" || tm["content"] != "a.go" || tm["tool_name"] != "list_files" {
		t.Fatalf("tool message = %v", tm)
	}
	if em := msgs[5].(map[string]any); em["content"] != "error: boom" {
		t.Fatalf("error result = %v", em)
	}
}

func TestOmitsNumCtxAndThinkWhenUnset(t *testing.T) {
	srv, c := serve(t, 200, `{"done":true,"done_reason":"stop"}`+"\n")
	p := New(Options{BaseURL: srv.URL})
	ch, err := p.Stream(context.Background(), llm.Request{Model: "m", Reasoning: "off", Messages: []llm.Message{llm.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch)
	raw, _ := c.snapshot()
	b := decodeBody(t, raw)
	if _, ok := b["think"]; ok {
		t.Fatalf("think sent for reasoning=off: %v", b["think"])
	}
	if _, ok := b["options"].(map[string]any)["num_ctx"]; ok {
		t.Fatalf("num_ctx sent with no NumCtx: %s", raw)
	}
}

func TestStreamTextThinkingToolCallAndUsage(t *testing.T) {
	reply := strings.Join([]string{
		`{"message":{"role":"assistant","content":"","thinking":"plan "},"done":false}`,
		`{"message":{"role":"assistant","content":"Listing"},"done":false}`,
		`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"list_files","arguments":{"path":"pkg"}}},{"function":{"name":"read_file","arguments":{"path":"a"}}}]},"done":false}`,
		`{"model":"m","done":true,"done_reason":"stop","prompt_eval_count":1234,"eval_count":56}`,
	}, "\n") + "\n"
	srv, _ := serve(t, 200, reply)
	p := New(Options{BaseURL: srv.URL})
	ch, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("go")}})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)

	want := []llm.EventType{
		llm.EvReasoning, llm.EvText,
		llm.EvToolCallStart, llm.EvToolCallDelta, llm.EvToolCallEnd,
		llm.EvToolCallStart, llm.EvToolCallDelta, llm.EvToolCallEnd,
		llm.EvFinish,
	}
	if len(evs) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(evs), len(want), evs)
	}
	for i, ev := range evs {
		if ev.Type != want[i] {
			t.Fatalf("event %d = %s, want %s", i, ev.Type, want[i])
		}
	}
	if evs[0].Text != "plan " || evs[1].Text != "Listing" {
		t.Fatalf("text deltas = %q / %q", evs[0].Text, evs[1].Text)
	}
	if evs[2].ToolCallID != "call_0" || evs[2].ToolName != "list_files" {
		t.Fatalf("first call start = %+v", evs[2])
	}
	if string(evs[4].Args) != `{"path":"pkg"}` {
		t.Fatalf("first call args = %s", evs[4].Args)
	}
	if evs[5].ToolCallID != "call_1" || evs[7].ToolName != "read_file" {
		t.Fatalf("second call = %+v / %+v", evs[5], evs[7])
	}
	fin := evs[8]
	if fin.FinishReason != llm.FinishToolCalls {
		t.Fatalf("finish = %s, want tool_calls", fin.FinishReason)
	}
	if fin.Usage.InputTokens != 1234 || fin.Usage.OutputTokens != 56 {
		t.Fatalf("usage = %+v", fin.Usage)
	}
}

func TestStreamPlainTextFinishReasons(t *testing.T) {
	srv, _ := serve(t, 200, `{"message":{"content":"done"},"done":true,"done_reason":"length","prompt_eval_count":3,"eval_count":1}`+"\n")
	p := New(Options{BaseURL: srv.URL})
	ch, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("x")}})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)
	if len(evs) != 2 || evs[0].Type != llm.EvText || evs[1].FinishReason != llm.FinishLength {
		t.Fatalf("events = %+v", evs)
	}
}

func TestHTTPErrorStatus(t *testing.T) {
	srv, _ := serve(t, 404, `{"error":"model \"nope\" not found, try pulling it first"}`)
	p := New(Options{BaseURL: srv.URL})
	_, err := p.Stream(context.Background(), llm.Request{Model: "nope", Messages: []llm.Message{llm.UserText("x")}})
	var ae *llm.APIError
	if err == nil || !asAPIError(err, &ae) {
		t.Fatalf("err = %v, want *llm.APIError", err)
	}
	if ae.StatusCode != 404 || ae.Provider != "ollama" {
		t.Fatalf("api error = %+v", ae)
	}
	if !strings.Contains(ae.Message, "lectern code --list-models ollama") {
		t.Fatalf("missing model hint: %q", ae.Message)
	}
	if cat, _ := llm.Classify(err); cat != llm.CatConfig {
		t.Fatalf("category = %s, want Config Error", cat)
	}
}

func TestHTTPErrorPlainBody(t *testing.T) {
	srv, _ := serve(t, 500, "server exploded")
	p := New(Options{BaseURL: srv.URL})
	_, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("x")}})
	var ae *llm.APIError
	if err == nil || !asAPIError(err, &ae) || ae.StatusCode != 500 || ae.Message != "server exploded" {
		t.Fatalf("err = %v", err)
	}
}

func TestErrorLineMidStream(t *testing.T) {
	srv, _ := serve(t, 200, `{"message":{"content":"par"},"done":false}`+"\n"+`{"error":"runner crashed"}`+"\n")
	p := New(Options{BaseURL: srv.URL})
	ch, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("x")}})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)
	last := evs[len(evs)-1]
	if last.Type != llm.EvError {
		t.Fatalf("last event = %s, want error", last.Type)
	}
	if !strings.Contains(last.Err.Error(), "runner crashed") {
		t.Fatalf("err = %v", last.Err)
	}
}

func TestStreamEndedBeforeDone(t *testing.T) {
	srv, _ := serve(t, 200, `{"message":{"content":"cut"},"done":false}`+"\n")
	p := New(Options{BaseURL: srv.URL})
	ch, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("x")}})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)
	last := evs[len(evs)-1]
	if last.Type != llm.EvError || !strings.Contains(last.Err.Error(), "ended before") {
		t.Fatalf("last = %+v", last)
	}
}

func TestUnreachableNamesOllama(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close()
	p := New(Options{BaseURL: base})
	_, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("x")}})
	if err == nil || !strings.Contains(err.Error(), "check that ollama is running") {
		t.Fatalf("err = %v", err)
	}
}

func TestModelsParsesTagsAndShow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			io.WriteString(w, `{"models":[{"name":"qwen3.6:35b-a3b","model":"qwen3.6:35b-a3b"},{"name":"tiny:latest","model":"tiny:latest"},{"name":"broken:1"}]}`)
		case "/api/show":
			var body struct {
				Model string `json:"model"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			switch body.Model {
			case "qwen3.6:35b-a3b":
				io.WriteString(w, `{"model_info":{"general.architecture":"qwen35moe","qwen35moe.context_length":262144},"capabilities":["completion","tools","thinking","vision"]}`)
			case "tiny:latest":
				w.WriteHeader(http.StatusInternalServerError)
			default:
				http.Error(w, "nope", http.StatusNotFound)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := New(Options{BaseURL: srv.URL})
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("models = %+v", models)
	}
	q := models[0]
	if q.ID != "qwen3.6:35b-a3b" || q.ContextWindow != 262144 || !q.Tools || !q.Reasoning || !q.Vision {
		t.Fatalf("qwen = %+v", q)
	}
	if models[1].ID != "tiny:latest" || models[1].ContextWindow != 0 {
		t.Fatalf("failed show must leave window 0: %+v", models[1])
	}
	if models[2].ID != "broken:1" {
		t.Fatalf("third = %+v", models[2])
	}
}

func TestIDDefaultsToOllama(t *testing.T) {
	if got := New(Options{}).ID(); got != "ollama" {
		t.Fatalf("ID = %q", got)
	}
}

func asAPIError(err error, target **llm.APIError) bool {
	ae, ok := err.(*llm.APIError)
	if ok {
		*target = ae
	}
	return ok
}
