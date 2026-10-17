package openai

import (
	"reflect"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

func boolp(b bool) *bool { return &b }

// reasoningTurns is two user turns. The first has a tool call with reasoning
// (r1); the second is the turn in progress and has a tool call with reasoning
// (r2). The tool result that follows the first call is a tool-role message.
func reasoningTurns() []llm.Message {
	call := func(id string) llm.Part {
		return llm.Part{Type: llm.PartToolCall, ToolCallID: id, ToolName: "read"}
	}
	result := func(id string) llm.Part {
		return llm.Part{Type: llm.PartToolResult, ToolCallID: id, Result: "ok"}
	}
	return []llm.Message{
		llm.UserText("first question"),
		{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: llm.PartReasoning, Text: "r1"}, call("c1")}},
		{Role: llm.RoleTool, Parts: []llm.Part{result("c1")}},
		llm.UserText("second question"),
		{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: llm.PartReasoning, Text: "r2"}, call("c2")}},
		{Role: llm.RoleTool, Parts: []llm.Part{result("c2")}},
	}
}

// echoed returns reasoning_content for each assistant message, by position.
func echoed(t *testing.T, body map[string]any) map[int]any {
	t.Helper()
	out := map[int]any{}
	msgs, _ := body["messages"].([]any)
	for i, m := range msgs {
		mm := m.(map[string]any)
		if mm["role"] != "assistant" {
			continue
		}
		if rc, ok := mm["reasoning_content"]; ok {
			out[i] = rc
		}
	}
	return out
}

func TestDeepSeekEchoesAllReasoningByDefault(t *testing.T) {
	body := requestBody(t, "deepseek", llm.Request{Model: "deepseek-chat", Messages: reasoningTurns()})
	got := echoed(t, body)
	if got[1] != "r1" || got[4] != "r2" {
		t.Fatalf("default should echo both turns, got %v", got)
	}
}

func TestDeepSeekSendReasoningTrueEchoesAll(t *testing.T) {
	body := requestBody(t, "deepseek", llm.Request{
		Model: "deepseek-chat", Messages: reasoningTurns(), SendReasoning: boolp(true),
	})
	got := echoed(t, body)
	if got[1] != "r1" || got[4] != "r2" {
		t.Fatalf("SendReasoning=true should echo both turns, got %v", got)
	}
}

// With SendReasoning off, the earlier turn loses its reasoning and the turn in
// progress keeps it, as the Anthropic adapter does.
func TestDeepSeekSendReasoningFalseKeepsOnlyTheCurrentTurn(t *testing.T) {
	body := requestBody(t, "deepseek", llm.Request{
		Model: "deepseek-chat", Messages: reasoningTurns(), SendReasoning: boolp(false),
	})
	got := echoed(t, body)
	if _, ok := got[1]; ok {
		t.Errorf("earlier turn kept reasoning_content: %v", got[1])
	}
	if got[4] != "r2" {
		t.Errorf("current turn reasoning_content = %v, want r2", got[4])
	}
}

// A tool-result-only user message continues the turn, so reasoning from before
// it stays in the current turn.
func TestSendReasoningFalseTreatsToolResultUserMessageAsSameTurn(t *testing.T) {
	msgs := reasoningTurns()
	// Put the tool result in a user message, not a tool-role message.
	msgs[2] = llm.Message{Role: llm.RoleUser, Parts: msgs[2].Parts}
	body := requestBody(t, "deepseek", llm.Request{
		Model: "deepseek-chat", Messages: msgs[:3], SendReasoning: boolp(false),
	})
	if got := echoed(t, body); got[1] != "r1" {
		t.Fatalf("tool-result user message should not start a new turn, got %v", got)
	}
}

// Other OpenAI-compatible providers never echo reasoning, so the flag changes
// nothing for them.
func TestSendReasoningFalseDoesNotChangeOtherProviders(t *testing.T) {
	for _, id := range []string{"openai", "openrouter", "groq"} {
		with := requestBody(t, id, llm.Request{Model: "m", Messages: reasoningTurns(), SendReasoning: boolp(false)})
		without := requestBody(t, id, llm.Request{Model: "m", Messages: reasoningTurns()})
		if !reflect.DeepEqual(with, without) {
			t.Errorf("%s: SendReasoning=false changed the body", id)
		}
	}
}

// Speed and ToolStreaming have no OpenAI-compatible equivalent. Setting them
// must leave the wire body exactly as it is when they are unset.
func TestSpeedAndToolStreamingLeaveOpenAIBodyUnchanged(t *testing.T) {
	base := llm.Request{Model: "gpt-4.1", Messages: reasoningTurns()}
	with := base
	with.Speed = "fast"
	with.ToolStreaming = boolp(false)
	for _, id := range []string{"openai", "deepseek", "openrouter"} {
		want := requestBody(t, id, base)
		got := requestBody(t, id, with)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: Speed/ToolStreaming changed the body\n got %v\nwant %v", id, got, want)
		}
	}
}
