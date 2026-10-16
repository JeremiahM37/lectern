package session

import (
	"encoding/json"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

func call(id string) llm.Part {
	return llm.Part{Type: llm.PartToolCall, ToolCallID: id, ToolName: "read", Args: json.RawMessage(`{}`)}
}

func result(id string) llm.Part {
	return llm.Part{Type: llm.PartToolResult, ToolCallID: id, Result: "ok"}
}

func TestValidateRestoredKeepsAWellFormedHistory(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Parts: []llm.Part{{Type: llm.PartText, Text: "hi"}}},
		{Role: llm.RoleAssistant, Parts: []llm.Part{call("a")}},
		{Role: llm.RoleTool, Parts: []llm.Part{result("a")}},
	}
	out, dropped := ValidateRestored(msgs)
	if dropped != 0 || len(out) != 3 {
		t.Fatalf("well-formed history changed: %d messages, %d dropped", len(out), dropped)
	}
}

func TestValidateRestoredDropsOrphanResults(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleTool, Parts: []llm.Part{result("ghost")}},
		{Role: llm.RoleUser, Parts: []llm.Part{{Type: llm.PartText, Text: "go"}}},
		{Role: llm.RoleAssistant, Parts: []llm.Part{call("a"), result("zzz")}},
	}
	out, dropped := ValidateRestored(msgs)
	if dropped != 2 {
		t.Fatalf("dropped = %d, want 2 (the orphan result before the call and the one with no call)", dropped)
	}
	if len(out) != 2 {
		t.Fatalf("messages = %d, want the orphan-only tool message gone", len(out))
	}
	if out[1].Parts[0].Type != llm.PartToolCall {
		t.Fatalf("assistant message lost its call: %+v", out[1])
	}
}

func TestValidateRestoredKeepsUnansweredCall(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Parts: []llm.Part{{Type: llm.PartText, Text: "go"}}},
		{Role: llm.RoleAssistant, Parts: []llm.Part{call("a")}},
	}
	out, dropped := ValidateRestored(msgs)
	if dropped != 0 || len(out) != 2 {
		t.Fatalf("an interrupted call must stay so Continue can finish it: %d dropped, %d kept", dropped, len(out))
	}
}

func TestValidateRestoredDropsUnknownRole(t *testing.T) {
	msgs := []llm.Message{
		{Role: "wizard", Parts: []llm.Part{{Type: llm.PartText, Text: "x"}}},
		{Role: llm.RoleUser, Parts: []llm.Part{{Type: llm.PartText, Text: "y"}}},
	}
	out, dropped := ValidateRestored(msgs)
	if dropped != 1 || len(out) != 1 || out[0].Role != llm.RoleUser {
		t.Fatalf("unknown role not dropped: %+v (dropped %d)", out, dropped)
	}
}

func TestValidateRestoredEmpty(t *testing.T) {
	if out, n := ValidateRestored(nil); out != nil || n != 0 {
		t.Fatalf("nil history: %v, %d", out, n)
	}
}
