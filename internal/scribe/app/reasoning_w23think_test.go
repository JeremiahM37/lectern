package app

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

func TestDeepSeekThinkingSettingReachesTheRequest(t *testing.T) {
	r := llm.Request{}
	applyProviderReasoning(&r, "deepseek", config.Reasoning{Thinking: "off"}, llm.ReasoningEffort)
	if r.DeepSeekThinking != "off" {
		t.Errorf("DeepSeekThinking = %q, want off", r.DeepSeekThinking)
	}
}

func TestDeepSeekThinkingUnsetLeavesTheRequestAlone(t *testing.T) {
	r := llm.Request{}
	applyProviderReasoning(&r, "deepseek", config.Reasoning{}, llm.ReasoningEffort)
	if r.DeepSeekThinking != "" {
		t.Errorf("DeepSeekThinking = %q, want empty", r.DeepSeekThinking)
	}
}

func TestDeepSeekThinkingNotSetForAModelWithoutTheStyle(t *testing.T) {
	r := llm.Request{}
	applyProviderReasoning(&r, "deepseek", config.Reasoning{Thinking: "on"}, llm.ReasoningNone)
	if r.DeepSeekThinking != "" {
		t.Errorf("deepseek-chat got DeepSeekThinking = %q, want empty", r.DeepSeekThinking)
	}
}

func TestDeepSeekThinkingNotSetForOtherProviders(t *testing.T) {
	r := llm.Request{}
	applyProviderReasoning(&r, "openai", config.Reasoning{Thinking: "on"}, llm.ReasoningEffort)
	if r.DeepSeekThinking != "" {
		t.Errorf("openai got DeepSeekThinking = %q, want empty", r.DeepSeekThinking)
	}
}
