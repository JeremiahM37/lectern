package openai

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// thinkingOf sends one DeepSeek request with the given switch and returns the
// thinking field, or nil when it is left out.
func thinkingOf(t *testing.T, id, model, switchVal string) any {
	t.Helper()
	body := requestBody(t, id, llm.Request{
		Model: model, Messages: []llm.Message{llm.UserText("x")}, DeepSeekThinking: switchVal,
	})
	return body["thinking"]
}

func TestDeepSeekThinkingSwitchOnAndOff(t *testing.T) {
	if got := thinkingOf(t, "deepseek", "deepseek-v4-pro", "on"); got == nil || got.(map[string]any)["type"] != "enabled" {
		t.Errorf("on: thinking = %v, want type enabled", got)
	}
	if got := thinkingOf(t, "deepseek", "deepseek-v4-pro", "off"); got == nil || got.(map[string]any)["type"] != "disabled" {
		t.Errorf("off: thinking = %v, want type disabled", got)
	}
}

func TestDeepSeekThinkingDefaultOmitsTheField(t *testing.T) {
	if got := thinkingOf(t, "deepseek", "deepseek-v4-pro", ""); got != nil {
		t.Errorf("default: thinking = %v, want the field left out", got)
	}
}

func TestDeepSeekThinkingOnlyForV4BareIDs(t *testing.T) {
	// Other DeepSeek ids are not documented to take the switch, and gateway
	// ids keep their own reasoning object. None of them gets the field.
	for _, model := range []string{"deepseek-chat", "deepseek-reasoner", "deepseek/deepseek-v4-pro"} {
		if got := thinkingOf(t, "deepseek", model, "off"); got != nil {
			t.Errorf("%s: thinking = %v, want the field left out", model, got)
		}
	}
}

func TestDeepSeekThinkingNotSentToOtherProviders(t *testing.T) {
	for _, id := range []string{"openai", "gemini", "openrouter"} {
		if got := thinkingOf(t, id, "deepseek-v4-pro", "off"); got != nil {
			t.Errorf("provider %s: thinking = %v, want the field left out", id, got)
		}
	}
}

func TestGeminiEffortStyleOnBareIDs(t *testing.T) {
	if ReasoningStyle("gemini-2.5-flash") != llm.ReasoningEffort {
		t.Error("gemini-2.5-flash should take the effort style")
	}
	if ReasoningStyle("google/gemini-2.5-pro") != llm.ReasoningNone {
		t.Error("a gateway id must not take the Gemini effort style")
	}
}

func TestGeminiEffortMapping(t *testing.T) {
	cases := []struct {
		level string
		want  any
	}{
		{"minimal", "minimal"},
		{"low", "low"},
		{"medium", "medium"},
		{"high", "high"},
		{"xhigh", "high"},
		{"max", "high"},
		{"ultra", "high"},
		{"none", nil},
		{"", nil},
	}
	for _, c := range cases {
		if got := effortOf(t, "gemini", "gemini-2.5-flash", c.level); got != c.want {
			t.Errorf("gemini effort %q: reasoning_effort = %v, want %v", c.level, got, c.want)
		}
	}
}

func TestGeminiEffortUsesTheMappingForGemini3Too(t *testing.T) {
	if got := effortOf(t, "gemini", "gemini-3-flash-preview", "xhigh"); got != "high" {
		t.Errorf("gemini-3 xhigh: reasoning_effort = %v, want high", got)
	}
}
