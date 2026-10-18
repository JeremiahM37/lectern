package app

import (
	"time"

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

// applyPerformance sets the request options of the Performance block (the
// Claude tab's thinking, effort, cache lifetime and sequential-tools rows).
// A provider's own reasoning setting wins over the Performance effort, so
// the per-provider Think tab stays the more specific control. Empty fields
// leave the request alone.
func applyPerformance(r *llm.Request, p config.Performance, rs config.Reasoning, style string) {
	switch p.Thinking {
	case "off":
		r.Reasoning = ""
		r.ThinkingBudget = 0
	case "enabled":
		if style == llm.ReasoningBudget && r.Reasoning == "" && r.ThinkingBudget == 0 {
			r.Reasoning = "medium"
		}
	case "adaptive":
		if style == llm.ReasoningAdaptive && r.Reasoning == "" {
			r.Reasoning = "medium"
		}
	}
	if p.Effort != "" && rs.Effort == "" && (style == llm.ReasoningEffort || style == llm.ReasoningAdaptive) && p.Thinking != "off" {
		r.Reasoning = p.Effort
	}
	if p.CacheTTL != "" {
		r.CacheTTL = p.CacheTTL
	}
	if config.Bool(p.DisableParallelToolUse, false) {
		r.DisableParallelToolUse = true
	}
	// Adapters that do not know these leave them alone; unset means the
	// request is unchanged.
	r.Speed = p.Speed
	r.ToolStreaming = p.ToolStreaming
	r.SendReasoning = p.SendReasoning
}

// tune is the agent.Options.Tune hook. It reads the settings of the
// provider the request is going to on every request, so a change in the
// config, a model switch or a fallback to another provider takes effect on
// the next request without reopening the session.
func (a *App) tune(m agent.Model, r *llm.Request) {
	provider, _ := llm.SplitModelRef(m.Ref)
	a.mu.Lock()
	rs := a.Cfg.ReasoningByProvider[provider]
	perf := a.Cfg.Performance
	cm := a.Cfg.ContextManagement
	sessionID := a.Meta.ID
	a.mu.Unlock()
	r.SessionID = sessionID
	applyReasoning(r, rs, m.Info.ReasoningStyle)
	applyPerformance(r, perf, rs, m.Info.ReasoningStyle)
	applyProviderReasoning(r, provider, rs, m.Info.ReasoningStyle)
	applyContextManagement(r, cm, provider, m.Info.ContextWindow)
}

// applyContextManagement sets the Anthropic context edits from the Claude tab.
// Only Anthropic requests carry them. Subagents are built without this hook,
// so they never get the edits, which is what the spec asks for. Both edits are
// off unless their setting is on, so an unset config leaves the request alone.
func applyContextManagement(r *llm.Request, cm config.ContextManagement, provider string, window int) {
	if provider != "anthropic" {
		return
	}
	r.ClearThinking = config.Bool(cm.ClearThinking, false)
	r.ClearToolUses = config.Bool(cm.ClearToolUses, false)
	r.ContextWindow = window
}

// applyProviderReasoning sets the controls that only one provider takes. The
// OpenRouter tab's effort, token budget and hide-reasoning rows apply to every
// model that lists reasoning (its style is not one of the controlled styles,
// so the generic path above does not cover it). The OpenAI verbosity row
// applies only to effort-style models, the same models the adapter sends it to.
func applyProviderReasoning(r *llm.Request, provider string, rs config.Reasoning, style string) {
	switch provider {
	case "openrouter":
		if rs.Effort != "" {
			r.Reasoning = rs.Effort
		}
		if rs.Budget > 0 {
			r.ThinkingBudget = rs.Budget
		}
		r.ReasoningExclude = rs.Exclude
	case "openai":
		if rs.Verbosity != "" && style == llm.ReasoningEffort {
			r.Verbosity = rs.Verbosity
		}
	case "deepseek":
		// The adapter sends the switch only to the V4 ids; the style check
		// keeps the request unchanged for every other DeepSeek model.
		if rs.Thinking != "" && style == llm.ReasoningEffort {
			r.DeepSeekThinking = rs.Thinking
		}
	}
}

// applyWatchdog sets the stream watchdog thresholds from /watchdog. Zero
// leaves the agent's defaults (180 s first token, 120 s between chunks,
// 900 s whole call).
func applyWatchdog(o *agent.Options, w config.Watchdog) {
	if w.FirstToken > 0 {
		o.FirstTokenTimeout = time.Duration(w.FirstToken) * time.Second
	}
	if w.InterToken > 0 {
		o.InterTokenTimeout = time.Duration(w.InterToken) * time.Second
	}
	if w.Step > 0 {
		o.StepTimeout = time.Duration(w.Step) * time.Second
	}
}
