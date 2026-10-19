package openai

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// summaryHome points HOME at an empty directory so nothing reads the real one.
func summaryHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

// summaryCall streams req from a fake server for provider id and returns the
// path it was posted to, the decoded body and the events.
func summaryCall(t *testing.T, id string, req llm.Request, events ...string) (string, map[string]any, []llm.Event) {
	t.Helper()
	summaryHome(t)
	var c capture
	var path string
	srv := newServer(t, &c, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		sse(w, events...)
	})
	evs := stream(t, New(Options{ID: id, BaseURL: srv.URL}), req)
	return path, c.body, evs
}

var summaryDone = `{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`

func TestSummaryOffKeepsChatCompletionsBody(t *testing.T) {
	msg := []llm.Message{llm.UserText("x")}
	for _, summary := range []string{"", "bogus", "off"} {
		path, body, _ := summaryCall(t, "openai", llm.Request{
			Model: "o4-mini", Messages: msg, Reasoning: "high", ReasoningSummary: summary,
		}, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, `[DONE]`)
		if path != "/chat/completions" {
			t.Errorf("summary %q: path = %s, want /chat/completions", summary, path)
		}
		if _, has := body["reasoning"]; has {
			t.Errorf("summary %q: reasoning object sent: %v", summary, body)
		}
		if _, has := body["store"]; has {
			t.Errorf("summary %q: store sent on chat path: %v", summary, body)
		}
		if body["reasoning_effort"] != "high" {
			t.Errorf("summary %q: reasoning_effort = %v, want high", summary, body["reasoning_effort"])
		}
	}
}

func TestSummaryOnSendsResponsesBodyWithSummary(t *testing.T) {
	read := llm.ToolSpec{Name: "read", Description: "read a file", Schema: json.RawMessage(`{"type":"object"}`)}
	msgs := []llm.Message{
		llm.UserText("look at a"),
		{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: llm.PartReasoning, Text: "hidden"}, {Type: llm.PartToolCall, ToolCallID: "call_1", ToolName: "read", Args: json.RawMessage(`{"path":"a"}`)}}},
		{Role: llm.RoleUser, Parts: []llm.Part{{Type: llm.PartToolResult, ToolCallID: "call_1", Result: "contents"}}},
	}
	path, body, _ := summaryCall(t, "openai", llm.Request{
		Model: "o4-mini", System: "be brief", Messages: msgs, Tools: []llm.ToolSpec{read},
		Reasoning: "medium", ReasoningSummary: "auto", MaxTokens: 500,
	}, summaryDone)

	if path != "/responses" {
		t.Fatalf("path = %s, want /responses", path)
	}
	if _, has := body["messages"]; has {
		t.Errorf("messages field on responses path: %v", body)
	}
	reasoning, _ := body["reasoning"].(map[string]any)
	if reasoning["summary"] != "auto" || reasoning["effort"] != "medium" {
		t.Errorf("reasoning = %v, want summary auto and effort medium", reasoning)
	}
	if body["store"] != false || body["stream"] != true {
		t.Errorf("store/stream = %v/%v, want false/true", body["store"], body["stream"])
	}
	if body["instructions"] != "be brief" {
		t.Errorf("instructions = %v", body["instructions"])
	}
	if body["max_output_tokens"] != float64(500) {
		t.Errorf("max_output_tokens = %v", body["max_output_tokens"])
	}
	if _, has := body["max_completion_tokens"]; has {
		t.Errorf("chat field max_completion_tokens on responses path")
	}

	input, _ := body["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("input has %d items, want 3 (user, function_call, function_call_output): %v", len(input), input)
	}
	first := input[0].(map[string]any)
	if first["type"] != "message" || first["role"] != "user" {
		t.Errorf("item 0 = %v", first)
	}
	call := input[1].(map[string]any)
	if call["type"] != "function_call" || call["call_id"] != "call_1" || call["name"] != "read" || call["arguments"] != `{"path":"a"}` {
		t.Errorf("item 1 = %v", call)
	}
	out := input[2].(map[string]any)
	if out["type"] != "function_call_output" || out["call_id"] != "call_1" || out["output"] != "contents" {
		t.Errorf("item 2 = %v", out)
	}
	if strings.Contains(string(mustJSON(t, body)), "hidden") {
		t.Errorf("reasoning text echoed into input: %v", body)
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "read" {
		t.Errorf("tools = %v", body["tools"])
	}
}

func TestSummaryNotSentWhereNotSupported(t *testing.T) {
	msg := []llm.Message{llm.UserText("x")}
	cases := []struct{ provider, model string }{
		{"openai", "gpt-4.1"}, // not a reasoning model
		{"openai", "gpt-5-chat-latest"},
		{"deepseek", "o4-mini"},   // other provider id
		{"llmgateway", "o4-mini"}, // gateway with an OpenAI-looking model
	}
	for _, c := range cases {
		path, body, _ := summaryCall(t, c.provider, llm.Request{
			Model: c.model, Messages: msg, ReasoningSummary: "detailed",
		}, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, `[DONE]`)
		if path != "/chat/completions" {
			t.Errorf("%s/%s: path = %s, want /chat/completions", c.provider, c.model, path)
		}
		if _, has := body["reasoning"]; has {
			t.Errorf("%s/%s: reasoning sent: %v", c.provider, c.model, body)
		}
	}
}

func TestResponsesStreamSurfacesSummaryAsReasoning(t *testing.T) {
	evs := []string{
		`{"type":"response.reasoning_summary_part.added"}`,
		`{"type":"response.reasoning_summary_text.delta","delta":"**Plan**"}`,
		`{"type":"response.reasoning_summary_text.delta","delta":"\n\nCheck the files"}`,
		`{"type":"response.reasoning_summary_part.added"}`,
		`{"type":"response.reasoning_summary_text.delta","delta":"**Next**"}`,
		`{"type":"response.output_text.delta","delta":"Hi"}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":4},"output_tokens":20,"output_tokens_details":{"reasoning_tokens":15}}}}`,
	}
	_, _, got := summaryCall(t, "openai", llm.Request{
		Model: "o4-mini", Messages: []llm.Message{llm.UserText("x")}, ReasoningSummary: "auto",
	}, evs...)

	if r := textOf(got, llm.EvReasoning); r != "**Plan**\n\nCheck the files\n\n**Next**" {
		t.Errorf("reasoning text = %q", r)
	}
	if txt := textOf(got, llm.EvText); txt != "Hi" {
		t.Errorf("answer text = %q, want Hi", txt)
	}
	fin := finishOf(t, got)
	if fin.FinishReason != llm.FinishStop {
		t.Errorf("finish = %v, want stop", fin.FinishReason)
	}
	u := fin.Usage
	if u.InputTokens != 6 || u.CacheReadTokens != 4 || u.OutputTokens != 20 || u.ReasoningTokens != 15 {
		t.Errorf("usage = %+v", u)
	}
}

func TestResponsesStreamToolCall(t *testing.T) {
	evs := []string{
		`{"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","call_id":"call_9","name":"read","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"path\":"}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"\"a\"}"}`,
		`{"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":"{\"path\":\"a\"}"}`,
		`{"type":"response.output_item.done","item":{"id":"fc_1","type":"function_call","call_id":"call_9","name":"read","arguments":"{\"path\":\"a\"}"}}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`,
	}
	_, _, got := summaryCall(t, "openai", llm.Request{
		Model: "o4-mini", Messages: []llm.Message{llm.UserText("x")}, ReasoningSummary: "auto",
	}, evs...)

	var start, end *llm.Event
	ends := 0
	for i := range got {
		switch got[i].Type {
		case llm.EvToolCallStart:
			start = &got[i]
		case llm.EvToolCallEnd:
			ends++
			end = &got[i]
		}
	}
	if start == nil || start.ToolCallID != "call_9" || start.ToolName != "read" {
		t.Fatalf("start = %+v", start)
	}
	if ends != 1 || end.ToolCallID != "call_9" || string(end.Args) != `{"path":"a"}` {
		t.Fatalf("end events = %d, last = %+v", ends, end)
	}
	if fin := finishOf(t, got); fin.FinishReason != llm.FinishToolCalls {
		t.Errorf("finish = %v, want tool_calls", fin.FinishReason)
	}
}

func TestResponsesStreamErrorIsTerminal(t *testing.T) {
	_, _, got := summaryCall(t, "openai", llm.Request{
		Model: "o4-mini", Messages: []llm.Message{llm.UserText("x")}, ReasoningSummary: "auto",
	}, `{"type":"response.reasoning_summary_text.delta","delta":"thinking"}`,
		`{"type":"error","message":"rate limit hit"}`)
	last := got[len(got)-1]
	if last.Type != llm.EvError || last.Err == nil || !strings.Contains(last.Err.Error(), "rate limit hit") {
		t.Fatalf("last event = %+v, want EvError with message", last)
	}
}

func TestResponsesStreamWithoutCompletionIsAnError(t *testing.T) {
	_, _, got := summaryCall(t, "openai", llm.Request{
		Model: "o4-mini", Messages: []llm.Message{llm.UserText("x")}, ReasoningSummary: "auto",
	}, `{"type":"response.output_text.delta","delta":"cut"}`)
	last := got[len(got)-1]
	if last.Type != llm.EvError {
		t.Fatalf("last event = %+v, want EvError for a stream that never completed", last)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
