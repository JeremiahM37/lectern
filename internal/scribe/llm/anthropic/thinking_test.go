package anthropic

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// bodyFor sends req to a fake server and returns the JSON body it received.
func bodyFor(t *testing.T, req llm.Request) map[string]any {
	t.Helper()
	p, c := captureServer(t)
	if len(req.Messages) == 0 {
		req.Messages = []llm.Message{llm.UserText("x")}
	}
	mustStream(t, p, req)
	c.await(t)
	return c.body
}

func TestReasoningStyleByModelID(t *testing.T) {
	cases := map[string]string{
		"claude-opus-5-5":            llm.ReasoningAdaptive,
		"claude-sonnet-5-5":          llm.ReasoningAdaptive,
		"claude-haiku-5-5":           llm.ReasoningAdaptive,
		"claude-fable-5-1":           llm.ReasoningAdaptive,
		"claude-mythos-5":            llm.ReasoningAdaptive,
		"claude-opus-4-7":            llm.ReasoningAdaptive,
		"claude-opus-4-8-20261101":   llm.ReasoningAdaptive,
		"claude-opus-4-6":            llm.ReasoningBudget,
		"claude-opus-4-5":            llm.ReasoningBudget,
		"claude-sonnet-4-5":          llm.ReasoningBudget,
		"claude-sonnet-4-5-20250929": llm.ReasoningBudget,
		"claude-haiku-4-5-20251001":  llm.ReasoningBudget,
		"claude-3-7-sonnet-20250219": llm.ReasoningBudget,
		"claude-3-5-sonnet-20241022": llm.ReasoningNone,
		"claude-3-haiku-20240307":    llm.ReasoningNone,
		"claude-some-future-model":   llm.ReasoningBudget,
	}
	for id, want := range cases {
		if got := ReasoningStyle(id); got != want {
			t.Errorf("ReasoningStyle(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestBodyAdaptiveFamilies(t *testing.T) {
	temp := 0.5
	for _, model := range []string{"claude-opus-5-5", "claude-haiku-5-5", "claude-fable-5-1", "claude-opus-4-7"} {
		body := bodyFor(t, llm.Request{Model: model, Reasoning: "medium", Temperature: &temp, MaxTokens: 1000})

		th, _ := body["thinking"].(map[string]any)
		if th["type"] != "adaptive" {
			t.Fatalf("%s thinking = %v, want adaptive", model, body["thinking"])
		}
		if _, has := th["budget_tokens"]; has {
			t.Fatalf("%s adaptive thinking must not carry budget_tokens: %v", model, th)
		}
		oc, _ := body["output_config"].(map[string]any)
		if oc["effort"] != "medium" {
			t.Fatalf("%s output_config = %v, want effort medium", model, body["output_config"])
		}
		if _, has := body["temperature"]; has {
			t.Fatalf("%s must not send temperature", model)
		}
	}
}

func TestBodyBudgetFamilies(t *testing.T) {
	temp := 0.5
	cases := []struct {
		model  string
		req    llm.Request
		budget float64
	}{
		{"claude-sonnet-4-5", llm.Request{Reasoning: "low"}, 2048},
		{"claude-opus-4-6", llm.Request{Reasoning: "high"}, 24576},
		{"claude-sonnet-4-5-20250929", llm.Request{ThinkingBudget: 4000, Reasoning: "low"}, 4000},
		{"claude-sonnet-4-5", llm.Request{ThinkingBudget: 500, Reasoning: "low"}, 1024},
		{"claude-sonnet-4-5", llm.Request{ThinkingBudget: 6000}, 6000},
	}
	for _, c := range cases {
		req := c.req
		req.Model, req.Temperature, req.MaxTokens = c.model, &temp, 1000
		body := bodyFor(t, req)

		th, _ := body["thinking"].(map[string]any)
		if th["type"] != "enabled" || th["budget_tokens"] != c.budget {
			t.Fatalf("%s %+v thinking = %v, want enabled budget %v", c.model, c.req, body["thinking"], c.budget)
		}
		if _, has := body["output_config"]; has {
			t.Fatalf("%s budget model must not send output_config", c.model)
		}
		if _, has := body["temperature"]; has {
			t.Fatalf("%s must not send temperature with thinking", c.model)
		}
		if mt := body["max_tokens"].(float64); mt <= c.budget {
			t.Fatalf("%s max_tokens %v must exceed budget %v", c.model, mt, c.budget)
		}
	}
}

func TestBodyNoThinkingWithoutReasoning(t *testing.T) {
	temp := 0.3
	// Adaptive model, no reasoning asked: no thinking, and no temperature
	// because the model rejects it.
	body := bodyFor(t, llm.Request{Model: "claude-opus-5-5", Temperature: &temp})
	if _, has := body["thinking"]; has {
		t.Fatalf("thinking sent without Reasoning: %v", body["thinking"])
	}
	if _, has := body["temperature"]; has {
		t.Fatal("adaptive model must not receive temperature")
	}

	// Older model, no reasoning: temperature passes through.
	body = bodyFor(t, llm.Request{Model: "claude-sonnet-4-5", Temperature: &temp})
	if body["temperature"] != 0.3 {
		t.Fatalf("temperature = %v, want 0.3", body["temperature"])
	}
}

func TestAdaptiveBudgetPicksEffort(t *testing.T) {
	body := bodyFor(t, llm.Request{Model: "claude-haiku-5-5", ThinkingBudget: 5000})
	oc, _ := body["output_config"].(map[string]any)
	if oc["effort"] != "medium" {
		t.Fatalf("output_config = %v, want effort medium for a 5000 budget", body["output_config"])
	}
	body = bodyFor(t, llm.Request{Model: "claude-haiku-5-5", ThinkingBudget: 20000})
	if oc, _ := body["output_config"].(map[string]any); oc["effort"] != "high" {
		t.Fatalf("output_config = %v, want effort high", body["output_config"])
	}
}

func TestServiceTierPassedWhenSet(t *testing.T) {
	body := bodyFor(t, llm.Request{Model: "claude-haiku-5-5", ServiceTier: "standard_only"})
	if body["service_tier"] != "standard_only" {
		t.Fatalf("service_tier = %v", body["service_tier"])
	}
	body = bodyFor(t, llm.Request{Model: "claude-haiku-5-5"})
	if _, has := body["service_tier"]; has {
		t.Fatalf("service_tier sent when unset: %v", body["service_tier"])
	}
}
