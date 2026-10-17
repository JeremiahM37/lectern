package tui

import (
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
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
