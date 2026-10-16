package app

import (
	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// applyReasoning sets the reasoning fields of one request from the provider's
// settings. Only the controls the model's reasoning style takes are applied:
// effort for "effort" and "adaptive", the token budget for "budget". Service
// tier is sent whenever it is set. Empty settings leave the request alone, so
// the provider default (or the global reasoning level) still applies.
func applyReasoning(r *llm.Request, rs config.Reasoning, style string) {
	switch style {
	case llm.ReasoningBudget:
		if rs.Budget > 0 {
			r.ThinkingBudget = rs.Budget
		}
	case llm.ReasoningAdaptive, llm.ReasoningEffort:
		if rs.Effort != "" {
			r.Reasoning = rs.Effort
		}
	}
	if rs.ServiceTier != "" {
		r.ServiceTier = rs.ServiceTier
	}
}

// tune is the agent.Options.Tune hook. It reads the settings of the
// provider the request is going to on every request, so a change in the
// config, a model switch or a fallback to another provider takes effect on
// the next request without reopening the session.
func (a *App) tune(m agent.Model, r *llm.Request) {
	provider, _ := llm.SplitModelRef(m.Ref)
	a.mu.Lock()
	rs := a.Cfg.ReasoningByProvider[provider]
	a.mu.Unlock()
	applyReasoning(r, rs, m.Info.ReasoningStyle)
}
