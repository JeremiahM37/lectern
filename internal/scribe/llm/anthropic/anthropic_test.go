package anthropic

import (
	"context"
	"encoding/base64"
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

// frame formats one SSE event exactly as the Messages API sends it.
func frame(event, data string) string {
	return "event: " + event + "\ndata: " + data + "\n\n"
}

// messageStart is the opening event of every stream.
const messageStart = `{"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":25,"cache_creation_input_tokens":0,"cache_read_input_tokens":1000,"output_tokens":1}}}`

// newTestServer serves handler and returns a provider pointed at it.
func newTestServer(t *testing.T, handler http.HandlerFunc, opts ...func(*Options)) *Provider {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	o := Options{APIKey: "test-key", BaseURL: srv.URL, HTTPClient: srv.Client()}
	for _, f := range opts {
		f(&o)
	}
	return New(o)
}

// sseHandler writes body as a 200 event stream.
func sseHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}
}

// collect reads ch until it closes, failing the test if it stalls.
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
			t.Fatalf("stream did not close; got %d events so far", len(out))
		}
	}
}

func mustStream(t *testing.T, p *Provider, req llm.Request) []llm.Event {
	t.Helper()
	ch, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	return collect(t, ch)
}

// terminalOf asserts exactly one terminal event, last in the stream.
func terminalOf(t *testing.T, evs []llm.Event) llm.Event {
	t.Helper()
	if len(evs) == 0 {
		t.Fatal("no events")
	}
	for i, ev := range evs[:len(evs)-1] {
		if ev.Type == llm.EvFinish || ev.Type == llm.EvError {
			t.Fatalf("terminal event %s at position %d, not last", ev.Type, i)
		}
	}
	last := evs[len(evs)-1]
	if last.Type != llm.EvFinish && last.Type != llm.EvError {
		t.Fatalf("last event is %s, want a terminal event", last.Type)
	}
	return last
}

func TestTextStream(t *testing.T) {
	body := frame("message_start", messageStart) +
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		frame("ping", `{"type": "ping"}`) +
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`) +
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`) +
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		frame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":7}}`) +
		frame("message_stop", `{"type":"message_stop"}`)

	p := newTestServer(t, sseHandler(body))
	evs := mustStream(t, p, llm.Request{Model: "claude-haiku-4-5", Messages: []llm.Message{llm.UserText("hi")}})

	var text strings.Builder
	for _, ev := range evs[:len(evs)-1] {
		if ev.Type != llm.EvText {
			t.Fatalf("unexpected event %s before finish", ev.Type)
		}
		text.WriteString(ev.Text)
	}
	if text.String() != "Hello" {
		t.Fatalf("text = %q, want Hello", text.String())
	}
	fin := terminalOf(t, evs)
	if fin.Type != llm.EvFinish || fin.FinishReason != llm.FinishStop {
		t.Fatalf("finish = %+v, want stop", fin)
	}
	want := llm.Usage{InputTokens: 25, OutputTokens: 7, CacheReadTokens: 1000}
	if fin.Usage != want {
		t.Fatalf("usage = %+v, want %+v", fin.Usage, want)
	}
}

func TestToolUseFragments(t *testing.T) {
	body := frame("message_start", messageStart) +
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Reading."}}`) +
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		frame("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_01A","name":"read_file","input":{}}}`) +
		frame("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":""}}`) +
		frame("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"pa"}}`) +
		frame("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"th\": \"a.go\""}}`) +
		frame("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"}"}}`) +
		frame("content_block_stop", `{"type":"content_block_stop","index":1}`) +
		frame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":42}}`) +
		frame("message_stop", `{"type":"message_stop"}`)

	p := newTestServer(t, sseHandler(body))
	evs := mustStream(t, p, llm.Request{Model: "claude-haiku-4-5", Messages: []llm.Message{llm.UserText("open a.go")}})

	var start, end *llm.Event
	var deltas strings.Builder
	for i := range evs {
		switch evs[i].Type {
		case llm.EvToolCallStart:
			start = &evs[i]
		case llm.EvToolCallDelta:
			if evs[i].ToolCallID != "toolu_01A" {
				t.Fatalf("delta id = %q", evs[i].ToolCallID)
			}
			deltas.WriteString(evs[i].ArgsDelta)
		case llm.EvToolCallEnd:
			end = &evs[i]
		}
	}
	if start == nil || start.ToolCallID != "toolu_01A" || start.ToolName != "read_file" {
		t.Fatalf("start = %+v", start)
	}
	if deltas.String() != `{"path": "a.go"}` {
		t.Fatalf("accumulated deltas = %q", deltas.String())
	}
	if end == nil || end.ToolName != "read_file" {
		t.Fatalf("end = %+v", end)
	}
	var args map[string]string
	if err := json.Unmarshal(end.Args, &args); err != nil || args["path"] != "a.go" {
		t.Fatalf("end args = %s (err %v)", end.Args, err)
	}
	fin := terminalOf(t, evs)
	if fin.FinishReason != llm.FinishToolCalls {
		t.Fatalf("finish = %s, want tool_calls", fin.FinishReason)
	}
}

func TestToolUseEmptyArgsBecomeObject(t *testing.T) {
	body := frame("message_start", messageStart) +
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_02","name":"list_dir","input":{}}}`) +
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		frame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":3}}`) +
		frame("message_stop", `{"type":"message_stop"}`)

	evs := mustStream(t, newTestServer(t, sseHandler(body)), llm.Request{Model: "m"})
	for _, ev := range evs {
		if ev.Type == llm.EvToolCallEnd {
			if string(ev.Args) != "{}" {
				t.Fatalf("args = %s, want {}", ev.Args)
			}
			return
		}
	}
	t.Fatal("no tool_call_end event")
}

func TestThinkingWithSignature(t *testing.T) {
	body := frame("message_start", messageStart) +
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`) +
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think"}}`) +
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":" about it."}}`) +
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"EqQBCgIYAh"}}`) +
		frame("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		frame("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`) +
		frame("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Done."}}`) +
		frame("content_block_stop", `{"type":"content_block_stop","index":1}`) +
		frame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":20}}`) +
		frame("message_stop", `{"type":"message_stop"}`)

	evs := mustStream(t, newTestServer(t, sseHandler(body)), llm.Request{Model: "m", Reasoning: "low"})

	var reasoning strings.Builder
	var sig string
	var text string
	for _, ev := range evs {
		switch ev.Type {
		case llm.EvReasoning:
			reasoning.WriteString(ev.Text)
		case llm.EvReasoningSig:
			sig = ev.Text
		case llm.EvText:
			text += ev.Text
		}
	}
	if reasoning.String() != "Let me think about it." {
		t.Fatalf("reasoning = %q", reasoning.String())
	}
	if sig != "EqQBCgIYAh" {
		t.Fatalf("signature = %q", sig)
	}
	if text != "Done." {
		t.Fatalf("text = %q", text)
	}
	if fin := terminalOf(t, evs); fin.Type != llm.EvFinish {
		t.Fatalf("terminal = %s (%v)", fin.Type, fin.Err)
	}
}

func TestRateLimitedWithRetryAfter(t *testing.T) {
	p := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your per-minute rate limit"}}`)
	})
	evs := mustStream(t, p, llm.Request{Model: "m"})
	if len(evs) != 1 {
		t.Fatalf("got %d events, want 1", len(evs))
	}
	var ae *llm.APIError
	if !errors.As(evs[0].Err, &ae) {
		t.Fatalf("err = %v, want *llm.APIError", evs[0].Err)
	}
	if ae.StatusCode != 429 || ae.Type != "rate_limit_error" || ae.RetryAfter != 7*time.Second {
		t.Fatalf("api error = %+v", ae)
	}
	if !strings.Contains(ae.Message, "per-minute rate limit") {
		t.Fatalf("message = %q", ae.Message)
	}
	if cat, retry := llm.Classify(evs[0].Err); cat != llm.CatRateLimited || !retry {
		t.Fatalf("classify = %s, %v", cat, retry)
	}
}

func TestOverloaded529(t *testing.T) {
	p := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(529)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	})
	evs := mustStream(t, p, llm.Request{Model: "m"})
	var ae *llm.APIError
	if len(evs) != 1 || !errors.As(evs[0].Err, &ae) {
		t.Fatalf("events = %+v", evs)
	}
	if ae.StatusCode != 529 || ae.Type != "overloaded_error" || ae.RetryAfter != 0 {
		t.Fatalf("api error = %+v", ae)
	}
	if cat, retry := llm.Classify(evs[0].Err); cat != llm.CatOverloaded || !retry {
		t.Fatalf("classify = %s, %v", cat, retry)
	}
}

func TestMidStreamErrorEvent(t *testing.T) {
	body := frame("message_start", messageStart) +
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`) +
		frame("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)

	evs := mustStream(t, newTestServer(t, sseHandler(body)), llm.Request{Model: "m"})
	fin := terminalOf(t, evs)
	if fin.Type != llm.EvError {
		t.Fatalf("terminal = %s, want error", fin.Type)
	}
	for _, ev := range evs {
		if ev.Type == llm.EvFinish {
			t.Fatal("stream with error must not emit finish")
		}
	}
	var ae *llm.APIError
	if !errors.As(fin.Err, &ae) || ae.Type != "overloaded_error" || ae.StatusCode != 529 {
		t.Fatalf("err = %#v", fin.Err)
	}
	if cat, _ := llm.Classify(fin.Err); cat != llm.CatOverloaded {
		t.Fatalf("classify = %s", cat)
	}
}

func TestStreamEndsWithoutMessageStop(t *testing.T) {
	body := frame("message_start", messageStart) +
		frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"cut"}}`)
	evs := mustStream(t, newTestServer(t, sseHandler(body)), llm.Request{Model: "m"})
	fin := terminalOf(t, evs)
	if fin.Type != llm.EvError || !errors.Is(fin.Err, io.ErrUnexpectedEOF) {
		t.Fatalf("terminal = %+v", fin)
	}
}

func TestContextCancelMidStream(t *testing.T) {
	released := make(chan struct{})
	p := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, frame("message_start", messageStart)+
			frame("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)+
			frame("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"first"}}`))
		w.(http.Flusher).Flush()
		// Hold the stream open until the client goes away.
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
		close(released)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := p.Stream(ctx, llm.Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	first := <-ch
	if first.Type != llm.EvText || first.Text != "first" {
		t.Fatalf("first event = %+v", first)
	}
	cancel()

	evs := collect(t, ch)
	fin := terminalOf(t, evs)
	if fin.Type != llm.EvError || !errors.Is(fin.Err, context.Canceled) {
		t.Fatalf("terminal = %+v, want EvError(context.Canceled)", fin)
	}
	<-released
}

func TestCancelledBeforeCall(t *testing.T) {
	// A context cancelled before the call must end the stream, not hang.
	p := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch, err := p.Stream(ctx, llm.Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	evs := collect(t, ch)
	fin := terminalOf(t, evs)
	if fin.Type != llm.EvError || !errors.Is(fin.Err, context.Canceled) {
		t.Fatalf("terminal = %+v", fin)
	}
}

func TestOverloadedStatus500WithPlainBody(t *testing.T) {
	p := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "upstream connect error")
	})
	evs := mustStream(t, p, llm.Request{Model: "m"})
	var ae *llm.APIError
	if len(evs) != 1 || !errors.As(evs[0].Err, &ae) {
		t.Fatalf("events = %+v", evs)
	}
	if ae.StatusCode != 502 || ae.Message != "upstream connect error" || ae.Type != "" {
		t.Fatalf("api error = %+v", ae)
	}
}

func TestStopReasonMapping(t *testing.T) {
	cases := map[string]llm.FinishReason{
		"end_turn":      llm.FinishStop,
		"stop_sequence": llm.FinishStop,
		"tool_use":      llm.FinishToolCalls,
		"max_tokens":    llm.FinishLength,
		"refusal":       llm.FinishFilter,
	}
	for stop, want := range cases {
		body := frame("message_start", messageStart) +
			frame("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q,"stop_sequence":null},"usage":{"output_tokens":1}}`, stop)) +
			frame("message_stop", `{"type":"message_stop"}`)
		fin := terminalOf(t, mustStream(t, newTestServer(t, sseHandler(body)), llm.Request{Model: "m"}))
		if fin.FinishReason != want {
			t.Errorf("stop_reason %s -> %s, want %s", stop, fin.FinishReason, want)
		}
	}
}

// captured is the request body and headers of one call. done is closed by
// the handler after it has written the fields, so the test's read is ordered
// after the write (the race detector does not see socket I/O as ordering).
type captured struct {
	body    map[string]any
	headers http.Header
	done    chan struct{}
}

func (c *captured) await(t *testing.T) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never saw the request")
	}
}

// captureServer records the one request it receives and answers with a
// minimal successful stream.
func captureServer(t *testing.T, opts ...func(*Options)) (*Provider, *captured) {
	t.Helper()
	c := &captured{done: make(chan struct{})}
	p := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		c.headers = r.Header.Clone()
		if err := json.Unmarshal(raw, &c.body); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		close(c.done)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, frame("message_stop", `{"type":"message_stop"}`))
	}, opts...)
	return p, c
}

// blocksOf returns the content blocks of a message as maps.
func blocksOf(t *testing.T, msg any) []map[string]any {
	t.Helper()
	m, ok := msg.(map[string]any)
	if !ok {
		t.Fatalf("message is %T", msg)
	}
	raw, ok := m["content"].([]any)
	if !ok {
		t.Fatalf("content is %T", m["content"])
	}
	out := make([]map[string]any, 0, len(raw))
	for _, b := range raw {
		out = append(out, b.(map[string]any))
	}
	return out
}

func TestRequestBodyTranslation(t *testing.T) {
	p, c := captureServer(t)
	temp := 0.2
	img := []byte{0x89, 'P', 'N', 'G'}

	req := llm.Request{
		Model:       "claude-haiku-4-5",
		System:      "You are Scribe.",
		CacheSystem: true,
		MaxTokens:   1234,
		Temperature: &temp,
		ToolChoice:  "required",
		Tools: []llm.ToolSpec{{
			Name:        "read_file",
			Description: "Read a file",
			Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		}},
		Messages: []llm.Message{
			llm.UserText("first"),
			llm.UserText("second"), // merges with the one above
			{Role: llm.RoleAssistant, Parts: []llm.Part{
				{Type: llm.PartText, Text: "Let me look."},
				{Type: llm.PartToolCall, ToolCallID: "toolu_1", ToolName: "read_file", Args: json.RawMessage(`{"path":"a.go"}`)},
			}},
			{Role: llm.RoleTool, Parts: []llm.Part{
				{Type: llm.PartToolResult, ToolCallID: "toolu_1", ToolName: "read_file", Result: "package a"},
				{Type: llm.PartToolResult, ToolCallID: "toolu_2", ToolName: "read_file", Result: "no such file", IsError: true},
			}},
			{Role: llm.RoleUser, Parts: []llm.Part{
				{Type: llm.PartImage, MediaType: "image/png", Data: img},
				{Type: llm.PartText, Text: "what is this?"},
			}},
		},
	}
	mustStream(t, p, req)
	c.await(t)

	if c.body["model"] != "claude-haiku-4-5" || c.body["stream"] != true {
		t.Fatalf("model/stream = %v / %v", c.body["model"], c.body["stream"])
	}
	if c.body["max_tokens"] != float64(1234) {
		t.Fatalf("max_tokens = %v", c.body["max_tokens"])
	}
	if c.body["temperature"] != 0.2 {
		t.Fatalf("temperature = %v", c.body["temperature"])
	}
	if tc, _ := c.body["tool_choice"].(map[string]any); tc["type"] != "any" {
		t.Fatalf("tool_choice = %v, want type any", c.body["tool_choice"])
	}

	// System: one text block with an ephemeral cache breakpoint.
	sys, _ := c.body["system"].([]any)
	if len(sys) != 1 {
		t.Fatalf("system = %v", c.body["system"])
	}
	sb := sys[0].(map[string]any)
	if sb["text"] != "You are Scribe." || sb["cache_control"].(map[string]any)["type"] != "ephemeral" {
		t.Fatalf("system block = %v", sb)
	}

	// Tools carry input_schema.
	tools, _ := c.body["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "read_file" {
		t.Fatalf("tools = %v", c.body["tools"])
	}
	if _, ok := tools[0].(map[string]any)["input_schema"]; !ok {
		t.Fatal("tool has no input_schema")
	}

	// Alternation: user(merged), assistant, user(tool results + image + text).
	msgs, _ := c.body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want 3 (merged, alternating)", len(msgs))
	}
	roles := []string{msgs[0].(map[string]any)["role"].(string), msgs[1].(map[string]any)["role"].(string), msgs[2].(map[string]any)["role"].(string)}
	if roles[0] != "user" || roles[1] != "assistant" || roles[2] != "user" {
		t.Fatalf("roles = %v", roles)
	}

	first := blocksOf(t, msgs[0])
	if len(first) != 2 || first[0]["text"] != "first" || first[1]["text"] != "second" {
		t.Fatalf("merged user blocks = %v", first)
	}
	if _, marked := first[1]["cache_control"]; marked {
		t.Fatal("cache breakpoint must only be on the last message")
	}

	asst := blocksOf(t, msgs[1])
	if asst[1]["type"] != "tool_use" || asst[1]["id"] != "toolu_1" {
		t.Fatalf("tool_use block = %v", asst[1])
	}
	if in, _ := asst[1]["input"].(map[string]any); in["path"] != "a.go" {
		t.Fatalf("tool_use input = %v", asst[1]["input"])
	}

	// Tool results fold into the user message that follows, ahead of the
	// later text and image blocks.
	last := blocksOf(t, msgs[2])
	types := make([]string, 0, len(last))
	for _, b := range last {
		types = append(types, b["type"].(string))
	}
	if strings.Join(types, ",") != "tool_result,tool_result,image,text" {
		t.Fatalf("last message block types = %v", types)
	}
	if last[0]["tool_use_id"] != "toolu_1" || last[0]["content"] != "package a" {
		t.Fatalf("first tool_result = %v", last[0])
	}
	if last[1]["is_error"] != true {
		t.Fatalf("second tool_result is_error = %v", last[1]["is_error"])
	}
	src := last[2]["source"].(map[string]any)
	if src["media_type"] != "image/png" || src["data"] != base64.StdEncoding.EncodeToString(img) {
		t.Fatalf("image source = %v", src)
	}

	// The last block of the last message is the cache breakpoint.
	if last[3]["cache_control"].(map[string]any)["type"] != "ephemeral" {
		t.Fatalf("last block has no cache_control: %v", last[3])
	}
	for _, b := range last[:3] {
		if _, marked := b["cache_control"]; marked {
			t.Fatalf("unexpected cache_control on %v", b["type"])
		}
	}

	// Headers.
	if c.headers.Get("x-api-key") != "test-key" || c.headers.Get("anthropic-version") != "2023-06-01" {
		t.Fatalf("headers = %v", c.headers)
	}
	if c.headers.Get("Authorization") != "" {
		t.Fatal("Authorization must not be set without AuthToken")
	}
}

func TestRequestDropsUnsignedReasoningAndFixesToolInput(t *testing.T) {
	p, c := captureServer(t)
	req := llm.Request{
		Model: "claude-sonnet-4-5",
		Messages: []llm.Message{
			llm.UserText("go"),
			{Role: llm.RoleAssistant, Parts: []llm.Part{
				{Type: llm.PartReasoning, Text: "no signature here"},
				{Type: llm.PartReasoning, Text: "signed thought", Signature: "sig123"},
				{Type: llm.PartToolCall, ToolCallID: "t1", ToolName: "a", Args: nil},
				{Type: llm.PartToolCall, ToolCallID: "t2", ToolName: "b", Args: json.RawMessage("not json")},
			}},
			llm.UserText("continue"),
		},
	}
	mustStream(t, p, req)
	c.await(t)

	msgs := c.body["messages"].([]any)
	asst := blocksOf(t, msgs[1])
	if len(asst) != 3 {
		t.Fatalf("assistant blocks = %v, want thinking + 2 tool_use", asst)
	}
	if asst[0]["type"] != "thinking" || asst[0]["signature"] != "sig123" || asst[0]["thinking"] != "signed thought" {
		t.Fatalf("thinking block = %v", asst[0])
	}
	for _, b := range asst[1:] {
		if in, _ := b["input"].(map[string]any); in == nil || len(in) != 0 {
			t.Fatalf("tool_use input = %v, want {}", b["input"])
		}
	}
}

func TestThinkingBudgetAndTemperature(t *testing.T) {
	temp := 0.7

	p, c := captureServer(t)
	mustStream(t, p, llm.Request{Model: "m", Reasoning: "medium", Temperature: &temp, MaxTokens: 1000,
		Messages: []llm.Message{llm.UserText("x")}})
	c.await(t)
	th, _ := c.body["thinking"].(map[string]any)
	if th["type"] != "enabled" || th["budget_tokens"] != float64(8192) {
		t.Fatalf("thinking = %v", c.body["thinking"])
	}
	if _, has := c.body["temperature"]; has {
		t.Fatal("temperature must be omitted with thinking")
	}
	if mt := c.body["max_tokens"].(float64); mt <= 8192 {
		t.Fatalf("max_tokens %v must exceed the budget", mt)
	}

	p2, c2 := captureServer(t)
	mustStream(t, p2, llm.Request{Model: "m", Temperature: &temp, Messages: []llm.Message{llm.UserText("x")}})
	c2.await(t)
	if _, has := c2.body["thinking"]; has {
		t.Fatal("no thinking expected without Reasoning")
	}
	if c2.body["temperature"] != 0.7 {
		t.Fatalf("temperature = %v", c2.body["temperature"])
	}
	if c2.body["max_tokens"] != float64(defaultMaxTokens) {
		t.Fatalf("default max_tokens = %v", c2.body["max_tokens"])
	}
}

func TestAuthTokenUsesBearer(t *testing.T) {
	p, c := captureServer(t, func(o *Options) {
		o.AuthToken = "oauth-token"
		o.ExtraHeaders = map[string]string{"anthropic-beta": "oauth-2025-04-20"}
	})
	mustStream(t, p, llm.Request{Model: "m", Headers: map[string]string{"X-Session": "s1"}, Messages: []llm.Message{llm.UserText("x")}})
	c.await(t)
	if c.headers.Get("Authorization") != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q", c.headers.Get("Authorization"))
	}
	if c.headers.Get("x-api-key") != "" {
		t.Fatal("x-api-key must not be sent with AuthToken")
	}
	if c.headers.Get("anthropic-beta") != "oauth-2025-04-20" || c.headers.Get("X-Session") != "s1" {
		t.Fatalf("extra headers missing: %v", c.headers)
	}
}

func TestModelsPaginatedAndEnriched(t *testing.T) {
	var mu sync.Mutex
	var afters []string
	p := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		afters = append(afters, r.URL.Query().Get("after_id"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("after_id") == "" {
			_, _ = io.WriteString(w, `{"data":[{"type":"model","id":"claude-haiku-4-5-20251001","display_name":"Claude Haiku 4.5","created_at":"2025-10-01T00:00:00Z"}],"has_more":true,"first_id":"claude-haiku-4-5-20251001","last_id":"claude-haiku-4-5-20251001"}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"type":"model","id":"claude-experimental-9","display_name":"Experimental 9"}],"has_more":false,"first_id":"claude-experimental-9","last_id":"claude-experimental-9"}`)
	})

	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(afters) != 2 || afters[0] != "" || afters[1] != "claude-haiku-4-5-20251001" {
		t.Fatalf("pagination cursors = %q", afters)
	}

	byID := map[string]llm.ModelInfo{}
	for _, m := range models {
		if _, dup := byID[m.ID]; dup {
			t.Fatalf("duplicate model %s", m.ID)
		}
		byID[m.ID] = m
	}

	// A dated id keeps its own id but gets the alias' prices.
	haiku := byID["claude-haiku-4-5-20251001"]
	if haiku.InputPerMTok != 1 || haiku.OutputPerMTok != 5 || haiku.ContextWindow != 200000 {
		t.Fatalf("haiku = %+v", haiku)
	}
	if haiku.CacheReadPerMTok != 0.1 || haiku.CacheWritePerMTok != 1.25 {
		t.Fatalf("haiku cache prices = %v / %v", haiku.CacheReadPerMTok, haiku.CacheWritePerMTok)
	}
	if haiku.Name != "Claude Haiku 4.5" {
		t.Fatalf("name = %q", haiku.Name)
	}

	// Unknown models from the endpoint are kept without prices.
	exp := byID["claude-experimental-9"]
	if exp.Name != "Experimental 9" || exp.InputPerMTok != 0 {
		t.Fatalf("experimental = %+v", exp)
	}

	// Static entries the endpoint did not list are still offered.
	opus := byID["claude-opus-4-1"]
	if opus.InputPerMTok != 15 || opus.OutputPerMTok != 75 {
		t.Fatalf("opus = %+v", opus)
	}
	if _, ok := byID["claude-opus-5-5"]; !ok {
		t.Fatal("static table entry claude-opus-5-5 missing")
	}
}

func TestModelsErrorFallsBackToStaticTable(t *testing.T) {
	p := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	})
	models, err := p.Models(context.Background())
	var ae *llm.APIError
	if !errors.As(err, &ae) || ae.StatusCode != 401 {
		t.Fatalf("err = %v", err)
	}
	if len(models) != len(KnownModels()) || len(models) == 0 {
		t.Fatalf("got %d models, want the static table", len(models))
	}
}
