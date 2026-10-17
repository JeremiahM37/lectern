package tui

import (
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
)

// contextLines is the /context panel: how full the window is, what fills it,
// this session's tokens and cost, and today's spend against the budget.
func contextLines(t *tab) []string {
	used, win := t.be.Context()
	u, cost := t.be.Usage()
	d := t.be.ContextDetail()
	lines := []string{
		fmt.Sprintf("Model          %s", t.be.Model()),
		fmt.Sprintf("Context        %s of %s (%d%%)", render.HumanTokens(used), render.HumanTokens(win), pct(used, win)),
	}
	if total := d.System + d.Tools + d.Conversation; total > 0 {
		// Bars are measured against the whole window, so a nearly empty
		// context looks nearly empty.
		scale := win
		if scale < total {
			scale = total
		}
		lines = append(lines, "", "What fills it (estimated, bars show share of the window)")
		for _, p := range []struct {
			name string
			n    int
		}{{"System prompt", d.System}, {"Tool definitions", d.Tools}, {"Conversation", d.Conversation}} {
			lines = append(lines, fmt.Sprintf("  %-16s %6s  %s", p.name, render.HumanTokens(p.n), shareBar(p.n, scale, 20)))
		}
	}
	h := foldHistory(t.be.Transcript())
	lines = append(lines, "", "This session",
		fmt.Sprintf("  Input          %s", render.HumanTokens(u.InputTokens)),
		fmt.Sprintf("  Output         %s", render.HumanTokens(u.OutputTokens)),
		fmt.Sprintf("  Cache read     %s", render.HumanTokens(u.CacheReadTokens)),
		fmt.Sprintf("  Cache write    %s", render.HumanTokens(u.CacheWriteTokens)),
	)
	if pct, ok := cacheHitPercent(u); ok {
		lines = append(lines, fmt.Sprintf("  Cache hit      %d%% of input read from cache", pct))
	} else {
		lines = append(lines, "  Cache hit      n/a (no input yet)")
	}
	switch {
	case t.local:
		lines = append(lines, "  Cost           free (local model)")
	default:
		lines = append(lines, fmt.Sprintf("  Cost           $%.4f", cost))
	}
	lines = append(lines, subagentLine(h, t.local), compactionLine(h))
	lines = append(lines, "", "By model (main agent)")
	lines = append(lines, modelTableLines(h.Models)...)
	lines = append(lines, "", "Today")
	if d.DailyBudget > 0 {
		lines = append(lines, fmt.Sprintf("  Tokens         %s of %s daily budget (%d%%)", render.HumanTokens(d.Today), render.HumanTokens(d.DailyBudget), pct(d.Today, d.DailyBudget)))
	} else {
		lines = append(lines, fmt.Sprintf("  Tokens         %s (no daily budget; set daily_token_budget)", render.HumanTokens(d.Today)))
	}
	lines = append(lines, "", "/context clear unloads skills and memory recall",
		"/compact shrinks the conversation")
	return lines
}

// maxModelRows caps the per-model table; the rest are summed into one row so
// a session that switched models often does not push the panel off screen.
const maxModelRows = 5

// subagentLine says how much of the session's spend came from subagents.
// Their tokens and cost are already counted in the totals above it.
func subagentLine(h contextHistory, local bool) string {
	if h.Subagents == 0 {
		return "  Subagents      none this session"
	}
	noun := "dispatches"
	if h.Subagents == 1 {
		noun = "dispatch"
	}
	line := fmt.Sprintf("  Subagents      %d %s, %s tokens", h.Subagents, noun, render.HumanTokens(total(h.SubagentUsage)))
	if !local {
		line += fmt.Sprintf(", $%.4f", h.SubagentCost)
	}
	return line + " (in the totals above)"
}

// compactionLine counts the compactions that ran and sizes the latest one.
func compactionLine(h contextHistory) string {
	switch {
	case h.Compactions == 0:
		return "  Compactions    none this session"
	case h.Last.Before > 0:
		return fmt.Sprintf("  Compactions    %d (last: %d to %d messages)", h.Compactions, h.Last.Before, h.Last.After)
	}
	return fmt.Sprintf("  Compactions    %d", h.Compactions)
}

// modelTableLines is the per-model table: calls, tokens, cache hit and cost
// for each model the main agent answered with. It is 61 columns wide so it
// fits the /context overlay on an 80-column terminal.
func modelTableLines(models []modelUsage) []string {
	if len(models) == 0 {
		return []string{"  No model calls in this session's transcript yet."}
	}
	lines := []string{fmt.Sprintf("  %-22s %5s %7s %7s %5s %8s", "Model", "Calls", "Input", "Output", "Hit", "Cost")}
	shown := models
	if len(shown) > maxModelRows {
		shown = shown[:maxModelRows]
	}
	for _, m := range shown {
		hit := "-"
		if pct, ok := cacheHitPercent(m.Usage); ok {
			hit = fmt.Sprintf("%d%%", pct)
		}
		lines = append(lines, fmt.Sprintf("  %-22s %5d %7s %7s %5s %8s",
			render.Truncate(m.Ref, 22), m.Calls,
			render.HumanTokens(m.Usage.InputTokens), render.HumanTokens(m.Usage.OutputTokens), hit, usd(m.CostUSD)))
	}
	if rest := models[len(shown):]; len(rest) > 0 {
		var calls int
		var u llm.Usage
		var cost float64
		for _, m := range rest {
			calls += m.Calls
			u.Add(m.Usage)
			cost += m.CostUSD
		}
		lines = append(lines, fmt.Sprintf("  %-22s %5d %7s %7s %5s %8s",
			fmt.Sprintf("+%d more models", len(rest)), calls,
			render.HumanTokens(u.InputTokens), render.HumanTokens(u.OutputTokens), "-", usd(cost)))
	}
	return lines
}

// usd formats a cost for the model table: "-" for nothing recorded (a free
// local model, or a transcript from before per-step costs were kept).
func usd(v float64) string {
	switch {
	case v <= 0:
		return "-"
	case v < 0.01:
		return "<$0.01"
	}
	return fmt.Sprintf("$%.2f", v)
}

// shareBar draws n's share of total as a bar of width cells.
func shareBar(n, total, width int) string {
	if total <= 0 || n <= 0 {
		return strings.Repeat("░", width)
	}
	fill := (n*width + total/2) / total
	if fill < 1 {
		fill = 1
	}
	if fill > width {
		fill = width
	}
	return strings.Repeat("█", fill) + strings.Repeat("░", width-fill)
}

// contextTabWord returns "dispatch" or "system" when the /context argument
// names one of the other panels, else "". The first word decides, so
// "/context system" and "/context dispatch now" both open their tab.
func contextTabWord(args string) string {
	f := strings.Fields(strings.ToLower(args))
	if len(f) == 0 {
		return ""
	}
	switch f[0] {
	case "dispatch", "system":
		return f[0]
	}
	return ""
}

// contextTabLines is the Dispatch or System tab of /context. Dispatch lists
// the subagents of the newest dispatch; System says what fills the system
// prompt and which instruction files are loaded.
func contextTabLines(t *tab, which string) []string {
	if which == "dispatch" {
		subs := t.be.Subagents()
		lines := []string{"Newest dispatch"}
		if len(subs) == 0 {
			return append(lines, "  No subagents have run in this session.",
				"", "Subagents appear here once the agent dispatches explore or code work.")
		}
		for _, a := range subs {
			last := a.Last
			if a.Error != "" {
				last = "error: " + a.Error
			}
			lines = append(lines, fmt.Sprintf("  %-10s %-8s %-8s step %-3d %6s tokens  %s",
				a.ID, a.Role, a.Status, a.Step, render.HumanTokens(a.Tokens.InputTokens+a.Tokens.OutputTokens), last))
		}
		return lines
	}
	d := t.be.ContextDetail()
	lines := []string{"System prompt",
		fmt.Sprintf("  System prompt    %6s  (estimated)", render.HumanTokens(d.System)),
		fmt.Sprintf("  Tool definitions %6s  (estimated)", render.HumanTokens(d.Tools)),
		"", "Instruction files loaded"}
	files := fields7(t.be).Instructions
	if files == nil {
		files = agent.DefaultInstructionFiles
	}
	if len(files) == 0 {
		return append(lines, "  none; the instruction files are all off on the Tools tab of /settings")
	}
	for _, f := range files {
		lines = append(lines, "  "+f)
	}
	return lines
}
