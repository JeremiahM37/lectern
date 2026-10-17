package openai

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// effortOf sends one request and returns the reasoning_effort field, or nil
// when the field is left out.
func effortOf(t *testing.T, id, model, reasoning string) any {
	t.Helper()
	body := requestBody(t, id, llm.Request{
		Model: model, Messages: []llm.Message{llm.UserText("x")}, Reasoning: reasoning,
	})
	return body["reasoning_effort"]
}

func TestDeepSeekV4EffortUsesHighOrMax(t *testing.T) {
	cases := []struct {
		level string
		want  any
	}{
		{"high", "high"},
		{"max", "max"},
		{"low", "low"},
		{"minimal", "low"},
		{"medium", "high"},
		{"xhigh", "high"},
		{"ultra", "max"},
		{"", nil},
		{"none", nil},
	}
	for _, c := range cases {
		if got := effortOf(t, "deepseek", "deepseek-v4-pro", c.level); got != c.want {
			t.Errorf("deepseek-v4-pro effort %q: reasoning_effort = %v, want %v", c.level, got, c.want)
		}
	}
}

func TestDeepSeekV4EffortNotSentWithoutTheStyle(t *testing.T) {
	// The mapping is for V4 only. The other DeepSeek ids keep the generic
	// send-when-set path, which is unchanged.
	if ReasoningStyle("deepseek-chat") != llm.ReasoningNone {
		t.Fatal("deepseek-chat should take no effort")
	}
	if got := effortOf(t, "deepseek", "deepseek-chat", "high"); got != "high" {
		t.Errorf("deepseek-chat with a set level: reasoning_effort = %v, want the unchanged pass-through high", got)
	}
}

func TestDeepSeekGatewayIDKeepsNoEffortStyle(t *testing.T) {
	// OpenRouter lists DeepSeek models as "deepseek/deepseek-v4-pro". Those
	// keep their own reasoning object and must not pick up the Think rows.
	if ReasoningStyle("deepseek/deepseek-v4-pro") != llm.ReasoningNone {
		t.Fatal("a gateway id must not take the DeepSeek effort style")
	}
	if ReasoningStyle("deepseek-v4-pro") != llm.ReasoningEffort {
		t.Fatal("bare deepseek-v4-pro should take effort")
	}
}

func TestXAIEffortForEveryGrokModelThatTakesIt(t *testing.T) {
	for _, id := range []string{"grok-3-mini", "grok-3-mini-fast", "grok-4.5", "grok-4.6", "grok-4.7", "grok-4.7-fast"} {
		if ReasoningStyle(id) != llm.ReasoningEffort {
			t.Errorf("%s should take effort", id)
		}
		if got := effortOf(t, "xai", id, "high"); got != "high" {
			t.Errorf("%s: reasoning_effort = %v, want high", id, got)
		}
	}
}

func TestXAIEffortLeftOutForGrokModelsThatDoNotTakeIt(t *testing.T) {
	// grok-4.20-multi-agent reuses reasoning.effort for the agent count, and
	// the plain grok-4 ids are not listed as taking the field.
	for _, id := range []string{"grok-4", "grok-4-0709", "grok-4.20-multi-agent", "grok-4.2"} {
		if ReasoningStyle(id) != llm.ReasoningNone {
			t.Errorf("%s should take no effort", id)
		}
		if got := effortOf(t, "xai", id, "high"); got != nil {
			t.Errorf("%s: reasoning_effort = %v, want it left out", id, got)
		}
	}
}

func TestReasoningControlsUnchangedForOtherProviders(t *testing.T) {
	// The style additions must not reach OpenAI, Groq or OpenRouter.
	if ReasoningStyle("gpt-4.1") != llm.ReasoningNone {
		t.Error("gpt-4.1 should still take no effort")
	}
	if got := effortOf(t, "openai", "gpt-4.1", "high"); got != nil {
		t.Errorf("openai gpt-4.1: reasoning_effort = %v, want it left out", got)
	}
	if got := effortOf(t, "groq", "deepseek-v4-pro", "high"); got != "high" {
		t.Errorf("groq: reasoning_effort = %v, want the unchanged pass-through high", got)
	}
}
