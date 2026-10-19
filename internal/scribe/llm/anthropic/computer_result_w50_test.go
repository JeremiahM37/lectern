package anthropic

import (
	"encoding/base64"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// A tool result that carries an image goes to Anthropic as text plus an image
// block, and a result without one keeps its plain string content.
func TestToolResultWithImageSendsTheImage(t *testing.T) {
	isolate(t)
	req := llm.Request{Model: "claude-sonnet-5-5", Messages: []llm.Message{
		{Role: llm.RoleUser, Parts: []llm.Part{{Type: llm.PartText, Text: "look"}}},
		{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: llm.PartToolCall, ToolCallID: "c1", ToolName: "computer", Args: []byte(`{"action":"screenshot"}`)}}},
		{Role: llm.RoleUser, Parts: []llm.Part{
			{Type: llm.PartToolResult, ToolCallID: "c1", ToolName: "computer", Result: "screenshot",
				Data: []byte("\x89PNG-fake"), MediaType: "image/png"},
		}},
	}}
	m := bodyOf(t, req)
	msgs := m["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	tr := last["content"].([]any)[0].(map[string]any)
	blocks, ok := tr["content"].([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("tool_result content = %#v, want text and image blocks", tr["content"])
	}
	img := blocks[1].(map[string]any)
	src := img["source"].(map[string]any)
	if img["type"] != "image" || src["type"] != "base64" || src["media_type"] != "image/png" {
		t.Fatalf("image block = %#v", img)
	}
	if got, _ := base64.StdEncoding.DecodeString(src["data"].(string)); string(got) != "\x89PNG-fake" {
		t.Fatalf("image data = %q", got)
	}
}

func TestToolResultWithoutImageKeepsItsText(t *testing.T) {
	isolate(t)
	req := llm.Request{Model: "claude-sonnet-5-5", Messages: []llm.Message{
		{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: llm.PartToolCall, ToolCallID: "c1", ToolName: "read", Args: []byte(`{}`)}}},
		{Role: llm.RoleUser, Parts: []llm.Part{{Type: llm.PartToolResult, ToolCallID: "c1", ToolName: "read", Result: "file text"}}},
	}}
	m := bodyOf(t, req)
	msgs := m["messages"].([]any)
	tr := msgs[len(msgs)-1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if tr["content"] != "file text" {
		t.Fatalf("content = %#v, want the string", tr["content"])
	}
}
