package openai

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

func TestExtraParamsMergeIntoBody(t *testing.T) {
	body := requestBody(t, "myproxy", llm.Request{
		Model:    "some-model",
		Messages: []llm.Message{llm.UserText("hi")},
		ExtraParams: map[string]json.RawMessage{
			"top_k":           json.RawMessage(`5`),
			"enable_thinking": json.RawMessage(`true`),
		},
	})
	if body["top_k"] != float64(5) || body["enable_thinking"] != true {
		t.Fatalf("extra params not in body: %v", body)
	}
	if body["model"] != "some-model" {
		t.Errorf("model = %v", body["model"])
	}
}

func TestExtraParamsOverrideAdapterFields(t *testing.T) {
	// An extra field wins over a field the adapter set, so a provider switch
	// reaches the wire even when Scribe already sends that key.
	temp := 0.2
	body := requestBody(t, "myproxy", llm.Request{
		Model:       "m",
		Temperature: &temp,
		Messages:    []llm.Message{llm.UserText("hi")},
		ExtraParams: map[string]json.RawMessage{"temperature": json.RawMessage(`0.9`)},
	})
	if body["temperature"] != 0.9 {
		t.Fatalf("temperature = %v, want the extra value 0.9", body["temperature"])
	}
}

func TestExtraParamsNeverOverrideCoreFields(t *testing.T) {
	body := requestBody(t, "myproxy", llm.Request{
		Model:    "real-model",
		Messages: []llm.Message{llm.UserText("the real prompt")},
		Tools: []llm.ToolSpec{{Name: "read_file", Description: "d",
			Schema: json.RawMessage(`{"type":"object"}`)}},
		ExtraParams: map[string]json.RawMessage{
			"model":    json.RawMessage(`"evil-model"`),
			"messages": json.RawMessage(`[]`),
			"tools":    json.RawMessage(`[]`),
			"stream":   json.RawMessage(`false`),
		},
	})
	if body["model"] != "real-model" {
		t.Errorf("model overridden: %v", body["model"])
	}
	if body["stream"] != true {
		t.Errorf("stream overridden: %v", body["stream"])
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		t.Errorf("messages overridden: %v", body["messages"])
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Errorf("tools overridden: %v", body["tools"])
	}
}

func TestNoExtraParamsLeavesBodyUnchanged(t *testing.T) {
	var c capture
	srv := newServer(t, &c, func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, `[DONE]`)
	})
	stream(t, New(Options{ID: "myproxy", BaseURL: srv.URL}), llm.Request{
		Model: "m", Messages: []llm.Message{llm.UserText("hi")},
	})
	if _, ok := c.body["top_k"]; ok {
		t.Fatalf("unexpected field in body: %v", c.body)
	}
	if c.body["model"] != "m" || c.body["stream"] != true {
		t.Fatalf("core fields changed: %v", c.body)
	}
}
