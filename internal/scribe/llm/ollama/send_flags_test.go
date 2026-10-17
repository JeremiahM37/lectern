package ollama

import (
	"context"
	"reflect"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

func boolp(b bool) *bool { return &b }

// Speed, ToolStreaming and SendReasoning have no Ollama equivalent. Ollama
// already drops reasoning from the history, so setting these must not change
// the request body. The test pins that, so an invented field fails it.
func TestPerformanceFlagsLeaveOllamaBodyUnchanged(t *testing.T) {
	history := []llm.Message{
		llm.UserText("look"),
		{Role: llm.RoleAssistant, Parts: []llm.Part{
			{Type: llm.PartReasoning, Text: "thinking"},
			{Type: llm.PartToolCall, ToolCallID: "c1", ToolName: "list_files"},
		}},
		{Role: llm.RoleTool, Parts: []llm.Part{{Type: llm.PartToolResult, ToolCallID: "c1", Result: "a.txt"}}},
	}
	send := func(req llm.Request) map[string]any {
		t.Helper()
		srv, c := serve(t, 200, `{"done":true,"done_reason":"stop"}`+"\n")
		p := New(Options{BaseURL: srv.URL})
		ch, err := p.Stream(context.Background(), req)
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		collect(t, ch)
		raw, _ := c.snapshot()
		return decodeBody(t, raw)
	}

	base := llm.Request{Model: "qwen3.6:35b-a3b", Messages: history}
	want := send(base)

	flagged := base
	flagged.Speed = "fast"
	flagged.ToolStreaming = boolp(false)
	flagged.SendReasoning = boolp(false)
	if got := send(flagged); !reflect.DeepEqual(got, want) {
		t.Fatalf("performance flags changed the body\n got %v\nwant %v", got, want)
	}
}
