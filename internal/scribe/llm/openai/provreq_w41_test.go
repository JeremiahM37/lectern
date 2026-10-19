package openai

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

func TestGroqReasoningFormatOnlyForGroq(t *testing.T) {
	msg := []llm.Message{llm.UserText("x")}
	req := llm.Request{Model: "openai/gpt-oss-120b", Messages: msg, ReasoningFormat: "parsed"}

	got := requestBody(t, "groq", req)
	if got["reasoning_format"] != "parsed" {
		t.Fatalf("groq must send reasoning_format, got %v", got["reasoning_format"])
	}
	other := requestBody(t, "openrouter", req)
	if _, ok := other["reasoning_format"]; ok {
		t.Fatalf("a non-Groq provider must not get reasoning_format: %v", other["reasoning_format"])
	}
}

func TestGroqReasoningFormatAbsentByDefault(t *testing.T) {
	req := llm.Request{Model: "llama-3.3-70b", Messages: []llm.Message{llm.UserText("x")}}
	if _, ok := requestBody(t, "groq", req)["reasoning_format"]; ok {
		t.Fatal("unset format must send nothing")
	}
}

func TestDashScopeThinkingShape(t *testing.T) {
	msg := []llm.Message{llm.UserText("x")}
	req := llm.Request{Model: "qwen3-max", Messages: msg, DashScopeThinking: "on", ThinkingBudget: 4096}

	got := requestBody(t, "dashscope", req)
	if got["enable_thinking"] != true {
		t.Fatalf("enable_thinking = %v, want true", got["enable_thinking"])
	}
	if got["thinking_budget"] != float64(4096) {
		t.Fatalf("thinking_budget = %v, want 4096", got["thinking_budget"])
	}
}

func TestDashScopeFieldsNeedTheSwitch(t *testing.T) {
	msg := []llm.Message{llm.UserText("x")}
	// A budget alone does not switch thinking on; the switch must be set.
	got := requestBody(t, "dashscope", llm.Request{Model: "qwen3-max", Messages: msg, ThinkingBudget: 2048})
	if _, ok := got["enable_thinking"]; ok {
		t.Fatalf("enable_thinking sent without the switch: %v", got["enable_thinking"])
	}
	if got["thinking_budget"] != float64(2048) {
		t.Fatalf("budget still sent, got %v", got["thinking_budget"])
	}
}

func TestDashScopeFieldsNotSentToOtherProviders(t *testing.T) {
	msg := []llm.Message{llm.UserText("x")}
	req := llm.Request{Model: "qwen3-max", Messages: msg, DashScopeThinking: "on", ThinkingBudget: 4096}
	got := requestBody(t, "openrouter", req)
	for _, k := range []string{"enable_thinking", "thinking_budget"} {
		if _, ok := got[k]; ok {
			t.Fatalf("%s must be sent only to dashscope, got it on openrouter", k)
		}
	}
}
