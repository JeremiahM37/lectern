package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// sse writes each payload as a "data:" line and flushes between them.
func sse(w http.ResponseWriter, payloads ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	fl, _ := w.(http.Flusher)
	for _, p := range payloads {
		fmt.Fprintf(w, "data: %s\n\n", p)
		if fl != nil {
			fl.Flush()
		}
	}
}

// capture records the last request body and headers the server saw.
type capture struct {
	mu     sync.Mutex
	body   map[string]any
	header http.Header
	path   string
}

func (c *capture) record(r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.body = m
	c.header = r.Header.Clone()
	c.path = r.URL.Path
}

func newServer(t *testing.T, c *capture, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c != nil {
			c.record(r)
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func collect(t *testing.T, ch <-chan llm.Event) []llm.Event {
	t.Helper()
	var out []llm.Event
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatalf("stream did not close; events so far: %+v", out)
		}
	}
}

func stream(t *testing.T, p *Provider, req llm.Request) []llm.Event {
	t.Helper()
	ch, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	return collect(t, ch)
}

func textOf(evs []llm.Event, typ llm.EventType) string {
	var b strings.Builder
	for _, e := range evs {
		if e.Type == typ {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}

func finishOf(t *testing.T, evs []llm.Event) llm.Event {
	t.Helper()
	if len(evs) == 0 {
		t.Fatal("no events")
	}
	last := evs[len(evs)-1]
	if last.Type != llm.EvFinish {
		t.Fatalf("last event = %+v, want finish", last)
	}
	return last
}

func TestPlainTextAndUsage(t *testing.T) {
	var c capture
	srv := newServer(t, &c, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`{"choices":[{"delta":{"role":"assistant","content":"Hel"}}]}`,
			`{"choices":[{"delta":{"content":"lo"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":2}}`,
			`[DONE]`)
	})
	p := New(Options{ID: "openai", BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	evs := stream(t, p, llm.Request{Model: "gpt-x", System: "be terse", Messages: []llm.Message{llm.UserText("hi")}})

	if got := textOf(evs, llm.EvText); got != "Hello" {
		t.Fatalf("text = %q, want Hello", got)
	}
	fin := finishOf(t, evs)
	if fin.FinishReason != llm.FinishStop {
		t.Fatalf("finish = %q", fin.FinishReason)
	}
	if fin.Usage.InputTokens != 12 || fin.Usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v", fin.Usage)
	}
	if c.path != "/v1/chat/completions" {
		t.Fatalf("path = %q", c.path)
	}
	if c.header.Get("Authorization") != "Bearer sk-test" {
		t.Fatalf("auth header = %q", c.header.Get("Authorization"))
	}
	if c.body["stream"] != true {
		t.Fatalf("stream flag = %v", c.body["stream"])
	}
	so, _ := c.body["stream_options"].(map[string]any)
	if so["include_usage"] != true {
		t.Fatalf("stream_options = %v", c.body["stream_options"])
	}
	msgs := c.body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v", msgs)
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "be terse" {
		t.Fatalf("system message = %v", first)
	}
}

func TestNoStreamUsageOmitsOption(t *testing.T) {
	var c capture
	srv := newServer(t, &c, func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, `[DONE]`)
	})
	p := New(Options{ID: "x", BaseURL: srv.URL, NoStreamUsage: true})
	stream(t, p, llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("hi")}})
	if _, ok := c.body["stream_options"]; ok {
		t.Fatalf("stream_options sent despite NoStreamUsage: %v", c.body["stream_options"])
	}
}

func TestToolCallsSplitAcrossChunks(t *testing.T) {
	srv := newServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`{"choices":[{"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"read_file","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"grep","arguments":"{\"pat"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"tern\":\"x\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":9}}`,
			`[DONE]`)
	})
	p := New(Options{ID: "openai", BaseURL: srv.URL})
	evs := stream(t, p, llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("go")}})

	starts := map[string]string{}
	deltas := map[string]string{}
	ends := map[string]string{}
	for _, e := range evs {
		switch e.Type {
		case llm.EvToolCallStart:
			if _, dup := starts[e.ToolCallID]; dup {
				t.Fatalf("duplicate start for %s", e.ToolCallID)
			}
			starts[e.ToolCallID] = e.ToolName
		case llm.EvToolCallDelta:
			deltas[e.ToolCallID] += e.ArgsDelta
		case llm.EvToolCallEnd:
			ends[e.ToolCallID] = string(e.Args)
			if e.ToolName != starts[e.ToolCallID] {
				t.Fatalf("end name %q does not match start %q", e.ToolName, starts[e.ToolCallID])
			}
		}
	}
	if starts["call_a"] != "read_file" || starts["call_b"] != "grep" {
		t.Fatalf("starts = %v", starts)
	}
	if deltas["call_a"] != `{"path":"a.go"}` {
		t.Fatalf("deltas a = %q", deltas["call_a"])
	}
	if ends["call_a"] != `{"path":"a.go"}` {
		t.Fatalf("end a = %s", ends["call_a"])
	}
	if ends["call_b"] != `{"pattern":"x"}` {
		t.Fatalf("end b = %s", ends["call_b"])
	}
	if fin := finishOf(t, evs); fin.FinishReason != llm.FinishToolCalls || fin.Usage.OutputTokens != 9 {
		t.Fatalf("finish = %+v", fin)
	}
}

func TestEmptyArgsBecomeEmptyObject(t *testing.T) {
	srv := newServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"list_dir","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`[DONE]`)
	})
	p := New(Options{ID: "openai", BaseURL: srv.URL})
	evs := stream(t, p, llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("go")}})
	for _, e := range evs {
		if e.Type == llm.EvToolCallEnd && string(e.Args) != "{}" {
			t.Fatalf("args = %s, want {}", e.Args)
		}
	}
}

func TestMissingFinishReasonWithToolCalls(t *testing.T) {
	srv := newServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"fs","arguments":"{}"}}]}}]}`,
			`[DONE]`)
	})
	p := New(Options{ID: "ollama", BaseURL: srv.URL})
	evs := stream(t, p, llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("go")}})
	fin := finishOf(t, evs)
	if fin.FinishReason != llm.FinishToolCalls {
		t.Fatalf("finish = %q, want tool_calls", fin.FinishReason)
	}
	var ended bool
	for _, e := range evs {
		if e.Type == llm.EvToolCallEnd && e.ToolCallID == "c1" {
			ended = true
		}
	}
	if !ended {
		t.Fatal("tool call was not closed")
	}
}

func TestReasoningFields(t *testing.T) {
	srv := newServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`{"choices":[{"delta":{"reasoning_content":"step one "}}]}`,
			`{"choices":[{"delta":{"reasoning":"step two"}}]}`,
			`{"choices":[{"delta":{"content":"answer"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`[DONE]`)
	})
	p := New(Options{ID: "openrouter", BaseURL: srv.URL})
	evs := stream(t, p, llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("q")}})
	if got := textOf(evs, llm.EvReasoning); got != "step one step two" {
		t.Fatalf("reasoning = %q", got)
	}
	if got := textOf(evs, llm.EvText); got != "answer" {
		t.Fatalf("text = %q", got)
	}
}

func TestCustomReasoningField(t *testing.T) {
	srv := newServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`{"choices":[{"delta":{"reasoning_text":"thinking"}}]}`,
			`{"choices":[{"delta":{"content":"ok"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`[DONE]`)
	})
	p := New(Options{ID: "x", BaseURL: srv.URL, ReasoningField: "reasoning_text"})
	evs := stream(t, p, llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("q")}})
	if got := textOf(evs, llm.EvReasoning); got != "thinking" {
		t.Fatalf("reasoning = %q", got)
	}
}

func TestInlineThinkTagsSplitAcrossChunks(t *testing.T) {
	srv := newServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`{"choices":[{"delta":{"content":"<thi"}}]}`,
			`{"choices":[{"delta":{"content":"nk>plan it"}}]}`,
			`{"choices":[{"delta":{"content":" out</th"}}]}`,
			`{"choices":[{"delta":{"content":"ink>The answer is 4."}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`[DONE]`)
	})
	p := New(Options{ID: "ollama", BaseURL: srv.URL})
	evs := stream(t, p, llm.Request{Model: "qwen", Messages: []llm.Message{llm.UserText("2+2")}})
	if got := textOf(evs, llm.EvReasoning); got != "plan it out" {
		t.Fatalf("reasoning = %q", got)
	}
	if got := textOf(evs, llm.EvText); got != "The answer is 4." {
		t.Fatalf("text = %q", got)
	}
	for _, e := range evs {
		if strings.Contains(e.Text, "<") {
			t.Fatalf("tag leaked into event: %+v", e)
		}
	}
}

func TestPartialTagAtEndIsFlushedAsText(t *testing.T) {
	srv := newServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`{"choices":[{"delta":{"content":"a <b"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`[DONE]`)
	})
	p := New(Options{ID: "x", BaseURL: srv.URL})
	evs := stream(t, p, llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("q")}})
	if got := textOf(evs, llm.EvText); got != "a <b" {
		t.Fatalf("text = %q", got)
	}
}

func TestCachedTokensSplitFromInput(t *testing.T) {
	srv := newServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`{"choices":[{"delta":{"content":"x"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":40},"completion_tokens_details":{"reasoning_tokens":3}}}`,
			`[DONE]`)
	})
	p := New(Options{ID: "openai", BaseURL: srv.URL})
	evs := stream(t, p, llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("q")}})
	u := finishOf(t, evs).Usage
	want := llm.Usage{InputTokens: 60, OutputTokens: 7, CacheReadTokens: 40, ReasoningTokens: 3}
	if u != want {
		t.Fatalf("usage = %+v, want %+v", u, want)
	}
}

func TestDeepSeekCacheFieldsAndUsage(t *testing.T) {
	srv := newServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`{"choices":[{"delta":{"content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":50,"completion_tokens":1,"prompt_cache_hit_tokens":30,"prompt_cache_miss_tokens":20}}`,
			`[DONE]`)
	})
	p := New(Options{ID: "deepseek", BaseURL: srv.URL})
	evs := stream(t, p, llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("q")}})
	u := finishOf(t, evs).Usage
	if u.InputTokens != 20 || u.CacheReadTokens != 30 {
		t.Fatalf("usage = %+v", u)
	}
}

func TestAPIError401(t *testing.T) {
	srv := newServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`)
	})
	p := New(Options{ID: "openai", BaseURL: srv.URL, APIKey: "bad"})
	ch, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("q")}})
	if ch != nil {
		t.Fatal("expected no channel on HTTP error")
	}
	var ae *llm.APIError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v (%T), want *llm.APIError", err, err)
	}
	if ae.StatusCode != 401 || ae.Message != "Incorrect API key provided" || ae.Type != "invalid_request_error" || ae.Provider != "openai" {
		t.Fatalf("APIError = %+v", ae)
	}
	if cat, retry := llm.Classify(err); cat != llm.CatAuth || retry {
		t.Fatalf("Classify = %q, %v", cat, retry)
	}
}

func TestAPIError429RetryAfter(t *testing.T) {
	srv := newServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, "slow down")
	})
	p := New(Options{ID: "groq", BaseURL: srv.URL})
	_, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("q")}})
	var ae *llm.APIError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v", err)
	}
	if ae.RetryAfter != 7*time.Second || ae.Message != "slow down" {
		t.Fatalf("APIError = %+v", ae)
	}
}

func TestInStreamErrorIsTerminal(t *testing.T) {
	srv := newServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`{"choices":[{"delta":{"content":"partial"}}]}`,
			`{"error":{"message":"server overloaded","type":"overloaded_error"}}`)
	})
	p := New(Options{ID: "x", BaseURL: srv.URL})
	evs := stream(t, p, llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("q")}})
	last := evs[len(evs)-1]
	if last.Type != llm.EvError {
		t.Fatalf("last = %+v, want error", last)
	}
	if cat, _ := llm.Classify(last.Err); cat != llm.CatOverloaded {
		t.Fatalf("category = %q", cat)
	}
}

func TestStreamEndingWithoutFinishIsError(t *testing.T) {
	srv := newServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices":[{"delta":{"content":"cut o"}}]}`)
	})
	p := New(Options{ID: "x", BaseURL: srv.URL})
	evs := stream(t, p, llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("q")}})
	last := evs[len(evs)-1]
	if last.Type != llm.EvError {
		t.Fatalf("last = %+v, want error", last)
	}
	if cat, retry := llm.Classify(last.Err); cat != llm.CatStream || !retry {
		t.Fatalf("Classify = %q, %v", cat, retry)
	}
}

func TestCancelEndsStream(t *testing.T) {
	release := make(chan struct{})
	srv := newServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices":[{"delta":{"content":"start"}}]}`)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	defer close(release)
	p := New(Options{ID: "x", BaseURL: srv.URL})
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := p.Stream(ctx, llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("q")}})
	if err != nil {
		t.Fatal(err)
	}
	first := <-ch
	if first.Type != llm.EvText {
		t.Fatalf("first = %+v", first)
	}
	cancel()
	evs := collect(t, ch)
	if len(evs) == 0 {
		t.Fatal("no terminal event after cancel")
	}
	last := evs[len(evs)-1]
	if last.Type != llm.EvError || !errors.Is(last.Err, context.Canceled) {
		t.Fatalf("last = %+v, want context.Canceled error", last)
	}
}

func TestRequestTranslation(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G'}
	msgs := []llm.Message{
		llm.UserText("read a.go"),
		{Role: llm.RoleAssistant, Parts: []llm.Part{
			{Type: llm.PartReasoning, Text: "I should read it"},
			{Type: llm.PartToolCall, ToolCallID: "c1", ToolName: "read_file", Args: json.RawMessage(`{"path":"a.go"}`)},
			{Type: llm.PartToolCall, ToolCallID: "c2", ToolName: "list_dir"},
		}},
		{Role: llm.RoleTool, Parts: []llm.Part{
			{Type: llm.PartToolResult, ToolCallID: "c1", ToolName: "read_file", Result: "package a"},
			{Type: llm.PartToolResult, ToolCallID: "c2", ToolName: "list_dir", Result: "no such dir", IsError: true},
			{Type: llm.PartImage, MediaType: "image/png", Data: png},
		}},
		{Role: llm.RoleUser, Parts: []llm.Part{{Type: llm.PartText, Text: "what is this?"}, {Type: llm.PartImage, Data: png}}},
	}
	var c capture
	srv := newServer(t, &c, func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, `[DONE]`)
	})
	p := New(Options{ID: "deepseek", BaseURL: srv.URL})
	stream(t, p, llm.Request{
		Model: "deepseek-reasoner", System: "sys", Messages: msgs, MaxTokens: 100,
		Reasoning: "high", ToolChoice: "required",
		Tools: []llm.ToolSpec{{Name: "read_file", Description: "read", Schema: json.RawMessage(`{"type":"object"}`)}},
	})

	if c.body["reasoning_effort"] != "high" || c.body["max_tokens"] != float64(100) {
		t.Fatalf("body options = %v", c.body)
	}
	if _, ok := c.body["max_completion_tokens"]; ok {
		t.Fatal("max_completion_tokens sent to non-openai provider")
	}
	if c.body["tool_choice"] != "required" {
		t.Fatalf("tool_choice = %v", c.body["tool_choice"])
	}
	tools := c.body["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "read_file" || fn["parameters"] == nil {
		t.Fatalf("tool = %v", fn)
	}

	got := c.body["messages"].([]any)
	if len(got) != 7 {
		t.Fatalf("got %d messages: %v", len(got), got)
	}
	asst := got[2].(map[string]any)
	if asst["role"] != "assistant" || asst["content"] != nil {
		t.Fatalf("assistant = %v", asst)
	}
	if asst["reasoning_content"] != "I should read it" {
		t.Fatalf("deepseek reasoning_content not echoed: %v", asst["reasoning_content"])
	}
	calls := asst["tool_calls"].([]any)
	c1 := calls[0].(map[string]any)
	if c1["id"] != "c1" || c1["type"] != "function" {
		t.Fatalf("tool call = %v", c1)
	}
	if args := c1["function"].(map[string]any)["arguments"]; args != `{"path":"a.go"}` {
		t.Fatalf("arguments = %v", args)
	}
	if args := calls[1].(map[string]any)["function"].(map[string]any)["arguments"]; args != "{}" {
		t.Fatalf("empty args = %v", args)
	}
	tool1 := got[3].(map[string]any)
	if tool1["role"] != "tool" || tool1["tool_call_id"] != "c1" || tool1["content"] != "package a" {
		t.Fatalf("tool1 = %v", tool1)
	}
	tool2 := got[4].(map[string]any)
	if tool2["content"] != "error: no such dir" || tool2["tool_call_id"] != "c2" {
		t.Fatalf("tool2 = %v", tool2)
	}
	imgTurn := got[5].(map[string]any)
	if imgTurn["role"] != "user" {
		t.Fatalf("tool image turn = %v", imgTurn)
	}
	parts := imgTurn["content"].([]any)
	url := parts[0].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("image url = %q", url)
	}
}

func TestReasoningDroppedForNonDeepSeek(t *testing.T) {
	var c capture
	srv := newServer(t, &c, func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, `[DONE]`)
	})
	p := New(Options{ID: "openai", BaseURL: srv.URL})
	stream(t, p, llm.Request{Model: "m", Messages: []llm.Message{
		llm.UserText("q"),
		{Role: llm.RoleAssistant, Parts: []llm.Part{
			{Type: llm.PartReasoning, Text: "secret"},
			{Type: llm.PartToolCall, ToolCallID: "c1", ToolName: "t", Args: json.RawMessage(`{}`)},
		}},
		{Role: llm.RoleTool, Parts: []llm.Part{{Type: llm.PartToolResult, ToolCallID: "c1", Result: "ok"}}},
	}})
	raw, _ := json.Marshal(c.body)
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "reasoning_content") {
		t.Fatalf("reasoning leaked into request: %s", raw)
	}
	if _, ok := c.body["max_completion_tokens"]; ok {
		t.Fatal("unexpected max_completion_tokens")
	}
}

func TestOpenAIUsesMaxCompletionTokens(t *testing.T) {
	var c capture
	srv := newServer(t, &c, func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, `[DONE]`)
	})
	p := New(Options{ID: "openai", BaseURL: srv.URL})
	stream(t, p, llm.Request{Model: "gpt-5", MaxTokens: 512, Messages: []llm.Message{llm.UserText("q")}})
	if c.body["max_completion_tokens"] != float64(512) {
		t.Fatalf("body = %v", c.body)
	}
	if _, ok := c.body["max_tokens"]; ok {
		t.Fatal("max_tokens sent to openai")
	}
}

func TestExtraHeadersAndRequestOverride(t *testing.T) {
	var c capture
	srv := newServer(t, &c, func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, `[DONE]`)
	})
	p := New(Options{ID: "llmgateway", BaseURL: srv.URL, Headers: map[string]string{"X-Title": "scribe", "X-Session": "old"}})
	stream(t, p, llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("q")},
		Headers: map[string]string{"X-Session": "new"}})
	if c.header.Get("X-Title") != "scribe" || c.header.Get("X-Session") != "new" {
		t.Fatalf("headers = %v", c.header)
	}
	if c.header.Get("Authorization") != "" {
		t.Fatalf("auth sent without key: %q", c.header.Get("Authorization"))
	}
}

func TestModelsRemote(t *testing.T) {
	srv := newServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"object":"list","data":[{"id":"alpha"},{"id":"beta"}]}`)
	})
	p := New(Options{ID: "x", BaseURL: srv.URL + "/v1", Models: []string{"static-only"}})
	ms, err := p.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].ID != "alpha" || ms[1].ID != "beta" {
		t.Fatalf("models = %+v", ms)
	}
}

func TestModelsFallsBackToStatic(t *testing.T) {
	srv := newServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	p := New(Options{ID: "x", BaseURL: srv.URL, Models: []string{"m1", "m2"}})
	ms, err := p.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].ID != "m1" {
		t.Fatalf("models = %+v", ms)
	}
}

func TestModelsErrorWithoutStatic(t *testing.T) {
	srv := newServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"nope"}`)
	})
	p := New(Options{ID: "x", BaseURL: srv.URL})
	_, err := p.Models(context.Background())
	var ae *llm.APIError
	if !errors.As(err, &ae) || ae.StatusCode != 401 {
		t.Fatalf("err = %v", err)
	}
}

func TestSplitterUnit(t *testing.T) {
	var sp splitter
	var got []piece
	for _, s := range []string{"x<", "/th", "ink>", "<think>a", "b</think>c"} {
		got = append(got, sp.push(s)...)
	}
	got = append(got, sp.flush()...)
	var text, reason strings.Builder
	for _, p := range got {
		if p.reasoning {
			reason.WriteString(p.text)
		} else {
			text.WriteString(p.text)
		}
	}
	if text.String() != "x</think>c" || reason.String() != "ab" {
		t.Fatalf("text=%q reason=%q", text.String(), reason.String())
	}
}
