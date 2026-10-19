package app

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

func TestPerProviderSpeedReachesAnthropicWire(t *testing.T) {
	// Global fast, but this provider has its own standard entry: the wire
	// must not carry speed or the fast-mode beta.
	perf := config.Performance{Speed: "fast", SpeedByProvider: map[string]string{"anthropic": "standard"}}
	req := tuned("claude-opus-5-5", perf, config.Reasoning{})
	applyPerProviderPerformance(&req, "anthropic", perf)
	body := wireBody(t, req)
	if _, ok := body["speed"]; ok {
		t.Fatalf("speed standard for anthropic must send no speed, got %v", body["speed"])
	}

	// Without an entry the global fast still reaches the wire.
	req = tuned("claude-opus-5-5", config.Performance{Speed: "fast"}, config.Reasoning{})
	applyPerProviderPerformance(&req, "anthropic", config.Performance{Speed: "fast"})
	if got := wireBody(t, req)["speed"]; got != "fast" {
		t.Fatalf("global fast must still reach the wire, got %v", got)
	}
}

func TestPerProviderSpeedSetsRequestField(t *testing.T) {
	perf := config.Performance{Speed: "fast", SpeedByProvider: map[string]string{"openai": ""}}
	var r llm.Request
	applyPerProviderPerformance(&r, "openai", perf)
	if r.Speed != "" {
		t.Fatalf("openai entry of empty must override the global, got %q", r.Speed)
	}
	applyPerProviderPerformance(&r, "anthropic", perf)
	if r.Speed != "fast" {
		t.Fatalf("anthropic has no entry, want global fast, got %q", r.Speed)
	}
}

func TestGroqReasoningFormatOnlyForGroq(t *testing.T) {
	perf := config.Performance{GroqReasoningFormat: "hidden"}
	var g llm.Request
	applyPerProviderPerformance(&g, "groq", perf)
	if g.ReasoningFormat != "hidden" {
		t.Fatalf("groq must get the format, got %q", g.ReasoningFormat)
	}
	var o llm.Request
	applyPerProviderPerformance(&o, "openrouter", perf)
	if o.ReasoningFormat != "" {
		t.Fatalf("openrouter must not get the groq format, got %q", o.ReasoningFormat)
	}
	var off llm.Request
	applyPerProviderPerformance(&off, "groq", config.Performance{GroqReasoningFormat: "off"})
	if off.ReasoningFormat != "" {
		t.Fatalf("off must send nothing, got %q", off.ReasoningFormat)
	}
}

func TestDashScopeThinkingFromProviderSettings(t *testing.T) {
	var on llm.Request
	applyProviderReasoning(&on, "dashscope", config.Reasoning{Effort: "high", Budget: 4096}, llm.ReasoningEffort)
	if on.DashScopeThinking != "on" || on.ThinkingBudget != 4096 {
		t.Fatalf("effort plus budget: got switch %q budget %d", on.DashScopeThinking, on.ThinkingBudget)
	}

	var off llm.Request
	applyProviderReasoning(&off, "dashscope", config.Reasoning{}, llm.ReasoningEffort)
	if off.DashScopeThinking != "" || off.ThinkingBudget != 0 {
		t.Fatalf("no settings must leave the request unchanged, got %+v", off)
	}

	var explicitOff llm.Request
	applyProviderReasoning(&explicitOff, "dashscope", config.Reasoning{Effort: "off"}, llm.ReasoningEffort)
	if explicitOff.DashScopeThinking != "" {
		t.Fatalf("effort off must not switch thinking on, got %q", explicitOff.DashScopeThinking)
	}

	var other llm.Request
	applyProviderReasoning(&other, "openai", config.Reasoning{Effort: "high", Budget: 4096}, llm.ReasoningEffort)
	if other.DashScopeThinking != "" {
		t.Fatalf("dashscope fields must stay off other providers, got %q", other.DashScopeThinking)
	}
}
