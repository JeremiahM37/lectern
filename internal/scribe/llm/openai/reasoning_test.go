package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// requestBody streams req from a provider with the given id against a fake
// chat-completions endpoint and returns the JSON body it received.
func requestBody(t *testing.T, id string, req llm.Request) map[string]any {
	t.Helper()
	var c capture
	srv := newServer(t, &c, func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, `[DONE]`)
	})
	stream(t, New(Options{ID: id, BaseURL: srv.URL}), req)
	return c.body
}

func TestReasoningEffortOnlyForEffortModelsOnOpenAI(t *testing.T) {
	msg := []llm.Message{llm.UserText("x")}
	cases := []struct {
		model string
		want  bool
	}{
		{"gpt-5-mini", true},
		{"gpt-5.4", true},
		{"o3-mini", true},
		{"o4-mini", true},
		{"o1", true},
		{"gpt-4.1", false},
		{"gpt-5-chat-latest", false},
		{"o1-mini", false},
	}
	for _, c := range cases {
		body := requestBody(t, "openai", llm.Request{
			Model: c.model, Messages: msg, Reasoning: "high",
		})
		_, has := body["reasoning_effort"]
		if has != c.want {
			t.Errorf("%s: reasoning_effort sent = %v, want %v (body %v)", c.model, has, c.want, body)
		}
	}
}

func TestReasoningEffortKeptOnCompatibleEndpoints(t *testing.T) {
	// Other OpenAI-compatible endpoints keep the send-when-set behaviour.
	body := requestBody(t, "llmgateway", llm.Request{
		Model: "some/model", Messages: []llm.Message{llm.UserText("x")}, Reasoning: "low",
	})
	if body["reasoning_effort"] != "low" {
		t.Fatalf("reasoning_effort = %v", body["reasoning_effort"])
	}
}

func TestOpenRouterReasoningObject(t *testing.T) {
	msg := []llm.Message{llm.UserText("x")}
	// Effort goes in the object, not as a top-level reasoning_effort.
	body := requestBody(t, "openrouter", llm.Request{Model: "openai/o3", Messages: msg, Reasoning: "xhigh", ThinkingBudget: 4096, ReasoningExclude: true})
	rr, _ := body["reasoning"].(map[string]any)
	if rr["effort"] != "xhigh" || rr["exclude"] != true || rr["max_tokens"] != nil {
		t.Fatalf("reasoning = %v, want effort xhigh and exclude, no max_tokens", rr)
	}
	if _, has := body["reasoning_effort"]; has {
		t.Fatalf("top-level reasoning_effort must not be sent to OpenRouter: %v", body)
	}
	// A budget alone becomes max_tokens.
	body = requestBody(t, "openrouter", llm.Request{Model: "anthropic/claude-sonnet-4.5", Messages: msg, ThinkingBudget: 8192})
	rr, _ = body["reasoning"].(map[string]any)
	if rr["max_tokens"] != float64(8192) || rr["effort"] != nil {
		t.Fatalf("reasoning = %v, want max_tokens 8192", rr)
	}
	// Nothing set: no object at all.
	body = requestBody(t, "openrouter", llm.Request{Model: "x/y", Messages: msg})
	if _, has := body["reasoning"]; has {
		t.Fatalf("reasoning sent with nothing set: %v", body["reasoning"])
	}
}

func TestVerbositySentOnlyForGPT5OnOpenAI(t *testing.T) {
	msg := []llm.Message{llm.UserText("x")}
	cases := []struct {
		provider, model string
		want            bool
	}{
		{"openai", "gpt-5.4", true},
		{"openai", "gpt-5-mini", true},
		{"openai", "gpt-4.1", false},
		{"openai", "gpt-5-chat-latest", false},
		{"openai", "o3", false},
		{"llmgateway", "openai/gpt-5", false},
	}
	for _, c := range cases {
		body := requestBody(t, c.provider, llm.Request{Model: c.model, Messages: msg, Verbosity: "low"})
		if _, has := body["verbosity"]; has != c.want {
			t.Errorf("%s/%s: verbosity sent = %v, want %v", c.provider, c.model, has, c.want)
		}
	}
}

func TestXAIEffortOnlyForMiniModels(t *testing.T) {
	msg := []llm.Message{llm.UserText("x")}
	body := requestBody(t, "xai", llm.Request{Model: "grok-3-mini", Messages: msg, Reasoning: "high"})
	if body["reasoning_effort"] != "high" {
		t.Fatalf("grok-3-mini reasoning_effort = %v", body["reasoning_effort"])
	}
	if ReasoningStyle("grok-3-mini") != llm.ReasoningEffort || ReasoningStyle("grok-4") != llm.ReasoningNone {
		t.Fatal("grok-3-mini should take effort, grok-4 should not")
	}
}

func TestOpenRouterModelsReadReasoningFlag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"openai/o3","supported_parameters":["reasoning","max_tokens"]},
			{"id":"meta/llama","supported_parameters":["max_tokens"]}]}`))
	}))
	defer srv.Close()
	ms, err := New(Options{ID: "openrouter", BaseURL: srv.URL}).Models(t.Context())
	if err != nil || len(ms) != 2 {
		t.Fatalf("models = %v, %v", ms, err)
	}
	if !ms[0].Reasoning || ms[1].Reasoning {
		t.Fatalf("reasoning flags = %v, %v", ms[0].Reasoning, ms[1].Reasoning)
	}
}

func TestServiceTierSentWhenSet(t *testing.T) {
	msg := []llm.Message{llm.UserText("x")}
	body := requestBody(t, "openai", llm.Request{Model: "gpt-4.1", Messages: msg, ServiceTier: "flex"})
	if body["service_tier"] != "flex" {
		t.Fatalf("service_tier = %v", body["service_tier"])
	}
	body = requestBody(t, "openai", llm.Request{Model: "gpt-4.1", Messages: msg})
	if _, has := body["service_tier"]; has {
		t.Fatalf("service_tier sent when unset: %v", body["service_tier"])
	}
}

// Truncated argument JSON must still end the call, with the raw text kept.
func TestMalformedToolArgsKeepRawText(t *testing.T) {
	srv := newServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"write_file","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\": \"a.go\", \"content\": \"pack"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"length"}]}`,
			`[DONE]`)
	})
	evs := stream(t, New(Options{ID: "openai", BaseURL: srv.URL}), llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("x")}})

	var end *llm.Event
	for i := range evs {
		if evs[i].Type == llm.EvToolCallEnd {
			end = &evs[i]
		}
	}
	if end == nil {
		t.Fatal("no tool_call_end")
	}
	raw, bad := llm.UnparsedArgs(end.Args)
	if !bad || raw != `{"path": "a.go", "content": "pack` {
		t.Fatalf("args = %s (bad=%v), want the raw text", end.Args, bad)
	}
	if got := finishOf(t, evs).FinishReason; got != llm.FinishLength {
		t.Fatalf("finish = %v", got)
	}
}

// A malformed call in history goes back to the provider as the text the
// model wrote, not as a quoted JSON string.
func TestMalformedHistoryCallRoundTripsRaw(t *testing.T) {
	raw := `{"path": "a.go"`
	msgs := []llm.Message{
		llm.UserText("q"),
		{Role: llm.RoleAssistant, Parts: []llm.Part{
			{Type: llm.PartToolCall, ToolCallID: "c1", ToolName: "read_file", Args: llm.ToolArgs(raw)},
		}},
		{Role: llm.RoleTool, Parts: []llm.Part{{Type: llm.PartToolResult, ToolCallID: "c1", Result: "invalid arguments", IsError: true}}},
	}
	body := requestBody(t, "openai", llm.Request{Model: "gpt-4.1", Messages: msgs})
	raw2, _ := json.Marshal(body["messages"])
	var got []map[string]any
	_ = json.Unmarshal(raw2, &got)
	calls, _ := got[1]["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("messages = %s", raw2)
	}
	fn := calls[0].(map[string]any)["function"].(map[string]any)
	if fn["arguments"] != raw {
		t.Fatalf("arguments = %q, want %q", fn["arguments"], raw)
	}
}

func TestRefusalAndFilterFinishMapToFilter(t *testing.T) {
	for _, reason := range []string{"content_filter", "refusal"} {
		srv := newServer(t, nil, func(w http.ResponseWriter, r *http.Request) {
			sse(w, `{"choices":[{"delta":{},"finish_reason":"`+reason+`"}]}`, `[DONE]`)
		})
		evs := stream(t, New(Options{ID: "openai", BaseURL: srv.URL}), llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("x")}})
		if got := finishOf(t, evs).FinishReason; got != llm.FinishFilter {
			t.Errorf("%s: finish = %v, want content_filter", reason, got)
		}
	}
}
