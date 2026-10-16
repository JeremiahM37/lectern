package app

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

func TestApplyReasoningPicksFieldsByStyle(t *testing.T) {
	rs := config.Reasoning{Effort: "medium", Budget: 8000, ServiceTier: "auto"}
	cases := []struct {
		style  string
		effort string
		budget int
		tier   string
		why    string
	}{
		{llm.ReasoningBudget, "", 8000, "auto", "budget models take the budget, not effort"},
		{llm.ReasoningAdaptive, "medium", 0, "auto", "adaptive models take effort, budget-style thinking is rejected"},
		{llm.ReasoningEffort, "medium", 0, "auto", "effort-style models take effort"},
		{llm.ReasoningNone, "", 0, "auto", "no reasoning controls, tier still applies"},
	}
	for _, c := range cases {
		var r llm.Request
		applyReasoning(&r, rs, c.style)
		if r.Reasoning != c.effort || r.ThinkingBudget != c.budget || r.ServiceTier != c.tier {
			t.Errorf("style %q (%s): got effort=%q budget=%d tier=%q", c.style, c.why,
				r.Reasoning, r.ThinkingBudget, r.ServiceTier)
		}
	}
}

func TestApplyReasoningEmptySettingsLeaveRequestAlone(t *testing.T) {
	r := llm.Request{Reasoning: "high", ThinkingBudget: 0}
	applyReasoning(&r, config.Reasoning{}, llm.ReasoningBudget)
	if r.Reasoning != "high" || r.ThinkingBudget != 0 || r.ServiceTier != "" {
		t.Fatalf("empty settings must not overwrite the request: %+v", r)
	}
}

func TestTuneFollowsTheModelTheRequestGoesTo(t *testing.T) {
	a := &App{Cfg: config.Config{ReasoningByProvider: map[string]config.Reasoning{
		"anthropic": {Effort: "high"},
		"deepseek":  {Budget: 4000},
	}}}
	var r llm.Request
	a.tune(agent.Model{Ref: "anthropic/claude-opus-5-5", Info: llm.ModelInfo{ReasoningStyle: llm.ReasoningAdaptive}}, &r)
	if r.Reasoning != "high" || r.ThinkingBudget != 0 {
		t.Fatalf("primary: %+v", r)
	}
	// A fallback to another provider must use that provider's settings,
	// not the ones of the model the session started with.
	r = llm.Request{}
	a.tune(agent.Model{Ref: "deepseek/r", Info: llm.ModelInfo{ReasoningStyle: llm.ReasoningBudget}}, &r)
	if r.Reasoning != "" || r.ThinkingBudget != 4000 {
		t.Fatalf("fallback: %+v", r)
	}
}
