package tui

import (
	"sort"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// contextHistory is what /context shows about this session that the running
// Usage() total cannot: which models answered, what subagents cost, and how
// often the conversation was compacted. It is folded from the session's UI
// transcript, which is written as the session runs and survives a resume.
type contextHistory struct {
	// Models are the main agent's model calls grouped by model, busiest first.
	Models []modelUsage

	// Subagents counts the dispatch calls that reported subagent spend.
	// Their tokens and cost are already inside the session totals; these
	// figures only say how much of them came from subagents.
	Subagents     int
	SubagentUsage llm.Usage
	SubagentCost  float64

	// Compactions counts the compactions that actually ran (skipped ones
	// are not counted). Last is the most recent of them, if any.
	Compactions int
	Last        compaction
}

// modelUsage is the main agent's usage on one model.
type modelUsage struct {
	Ref   string
	Calls int
	Usage llm.Usage
	// CostUSD sums the steps' own costs (StepCostUSD), so it is per model.
	CostUSD float64
}

// compaction is one finished compaction, by message count.
type compaction struct {
	Before, After int
	Model         string
}

// foldHistory builds the /context history from a session transcript.
func foldHistory(evs []agent.Event) contextHistory {
	var h contextHistory
	byModel := map[string]*modelUsage{}
	for _, e := range evs {
		if e.AgentID != "" {
			// Subagent events belong to the dispatch, not the main agent's
			// model table; their spend arrives on the dispatch result.
			continue
		}
		switch e.Kind {
		case agent.EvStepFinish:
			if e.Model == "" {
				continue
			}
			m := byModel[e.Model]
			if m == nil {
				m = &modelUsage{Ref: e.Model}
				byModel[e.Model] = m
			}
			m.Calls++
			m.Usage.Add(e.Usage)
			m.CostUSD += e.StepCostUSD
		case agent.EvToolResult:
			h.addDispatch(e)
		case agent.EvCompactDone:
			if skipped, _ := e.Meta["skipped"].(bool); skipped {
				continue
			}
			h.Compactions++
			h.Last = compaction{
				Before: numMeta(e.Meta, "messages_before"),
				After:  numMeta(e.Meta, "messages_after"),
				Model:  strMeta(e.Meta, "model"),
			}
		}
	}
	for _, m := range byModel {
		h.Models = append(h.Models, *m)
	}
	sort.Slice(h.Models, func(i, j int) bool {
		ti, tj := total(h.Models[i].Usage), total(h.Models[j].Usage)
		if ti != tj {
			return ti > tj
		}
		return h.Models[i].Ref < h.Models[j].Ref
	})
	return h
}

// addDispatch adds one dispatch result's subagent spend, if it reported any.
// The dispatcher writes these keys into the tool result's Meta; they come
// back as plain JSON values after a resume, so both shapes are accepted.
func (h *contextHistory) addDispatch(e agent.Event) {
	if e.Tool != "dispatch" || e.Result == nil {
		return
	}
	u, okU := usageFromMeta(e.Result.Meta["subagent_usage"])
	cost, okC := e.Result.Meta["subagent_cost_usd"].(float64)
	if !okU && !okC {
		return
	}
	h.Subagents++
	h.SubagentUsage.Add(u)
	h.SubagentCost += cost
}

// usageFromMeta reads a subagent_usage value, typed or as decoded JSON.
func usageFromMeta(v any) (llm.Usage, bool) {
	switch u := v.(type) {
	case llm.Usage:
		return u, true
	case *llm.Usage:
		if u != nil {
			return *u, true
		}
	case map[string]any:
		return llm.Usage{
			InputTokens:      numMeta(u, "input"),
			OutputTokens:     numMeta(u, "output"),
			CacheReadTokens:  numMeta(u, "cache_read"),
			CacheWriteTokens: numMeta(u, "cache_write"),
		}, true
	}
	return llm.Usage{}, false
}

// metaInt reads a number from event Meta, whether it was set in memory (int)
// or came back from the transcript file (float64).
func numMeta(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

func strMeta(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// total is every token the usage counts, cache included.
func total(u llm.Usage) int {
	return u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheWriteTokens
}

// cacheHitPercent is the share of input that was served from cache:
// cache read / (cache read + cache write + uncached input), the same ratio
// the API export reports. ok is false when there was no input at all.
func cacheHitPercent(u llm.Usage) (pct int, ok bool) {
	denom := u.CacheReadTokens + u.CacheWriteTokens + u.InputTokens
	if denom <= 0 {
		return 0, false
	}
	return (u.CacheReadTokens*100 + denom/2) / denom, true
}
