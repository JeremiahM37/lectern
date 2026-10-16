package tui

import (
	"fmt"
	"strings"

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
		lines = append(lines, "", "What fills it (estimated)")
		for _, p := range []struct {
			name string
			n    int
		}{{"System prompt", d.System}, {"Tool definitions", d.Tools}, {"Conversation", d.Conversation}} {
			lines = append(lines, fmt.Sprintf("  %-16s %6s  %s", p.name, render.HumanTokens(p.n), shareBar(p.n, total, 20)))
		}
	}
	lines = append(lines, "", "This session",
		fmt.Sprintf("  Input          %s", render.HumanTokens(u.InputTokens)),
		fmt.Sprintf("  Output         %s", render.HumanTokens(u.OutputTokens)),
		fmt.Sprintf("  Cache read     %s", render.HumanTokens(u.CacheReadTokens)),
	)
	switch {
	case t.local:
		lines = append(lines, "  Cost           free (local model)")
	default:
		lines = append(lines, fmt.Sprintf("  Cost           $%.4f", cost))
	}
	lines = append(lines, "", "Today")
	if d.DailyBudget > 0 {
		lines = append(lines, fmt.Sprintf("  Tokens         %s of %s daily budget (%d%%)", render.HumanTokens(d.Today), render.HumanTokens(d.DailyBudget), pct(d.Today, d.DailyBudget)))
	} else {
		lines = append(lines, fmt.Sprintf("  Tokens         %s (no daily budget; set daily_token_budget)", render.HumanTokens(d.Today)))
	}
	lines = append(lines, "", "/context clear unloads skills and memory recall · /compact shrinks the conversation")
	return lines
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
