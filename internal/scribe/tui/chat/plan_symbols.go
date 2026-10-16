package chat

import (
	"fmt"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// planSymbolRows draws the code symbols the active plan step touches, dimmed
// under its row: a mark for the action, then the name and kind. Three at most,
// then "+N more". rail is the plan's left rule, repeated on each row.
func planSymbolRows(rail string, syms []agent.PlanSymbol, cw int, th theme.Theme) []string {
	var out []string
	for i, s := range syms {
		if i == 3 {
			out = append(out, rail+"  "+fg(th.TextDim, fmt.Sprintf("+%d more", len(syms)-i)))
			break
		}
		mark, col := symbolMark(s.Action, th)
		label := s.Name
		if s.Kind != "" {
			label += " (" + s.Kind + ")"
		}
		out = append(out, rail+"  "+fg(col, mark+" ")+fg(th.TextMuted, clip(label, cw-8)))
	}
	return out
}

// symbolMark is the glyph and colour for a symbol action: + add, ~ modify,
// - remove, → rename.
func symbolMark(action string, th theme.Theme) (string, lipgloss.TerminalColor) {
	switch action {
	case "add":
		return "+", th.Success
	case "modify":
		return "~", th.Amber
	case "remove":
		return "-", th.Error
	}
	return "→", th.Info
}

// planFileMark is the glyph and colour for a file's action in a plan: + create,
// ~ modify, - delete. A file with no action given is a plain dot.
func planFileMark(action string, th theme.Theme) (string, lipgloss.TerminalColor) {
	switch action {
	case "create":
		return "+", th.Success
	case "modify":
		return "~", th.Amber
	case "delete":
		return "-", th.Error
	}
	return "·", th.TextMuted
}

// planTaskRows draws the tasks nested under an active plan step: "+N done",
// then up to six open tasks, then "+N more".
func planTaskRows(tasks []agent.Task, cw int, th theme.Theme, now time.Time) []string {
	if len(tasks) == 0 {
		return nil
	}
	done := 0
	var open []agent.Task
	for _, t := range tasks {
		if t.Status == "done" {
			done++
		} else {
			open = append(open, t)
		}
	}
	var out []string
	if done > 0 {
		out = append(out, fg(th.Success, fmt.Sprintf("+%d done", done)))
	}
	for i, t := range open {
		if i == 6 {
			out = append(out, fg(th.TextMuted, fmt.Sprintf("+%d more", len(open)-6)))
			break
		}
		out = append(out, taskRow(t, cw, th, now))
	}
	return out
}

// taskRow is one task-list entry: its status glyph and label.
func taskRow(t agent.Task, cw int, th theme.Theme, now time.Time) string {
	var glyph, text string
	switch t.Status {
	case "in_progress":
		glyph, text = fg(th.Brand, render.Frame(render.Braille, frameAt(now))), bold(th.TextPrimary, clip(t.Text, cw-8))
	case "blocked":
		glyph, text = fg(th.Error, icons.Icon("fail")), fg(th.Error, clip(t.Text, cw-8))
	case "cancelled":
		glyph, text = fg(th.TextDim, icons.Icon("skip")), fg(th.TextDim, clip(t.Text, cw-8))
	default:
		glyph, text = fg(th.TextMuted, "○"), fg(th.TextMuted, clip(t.Text, cw-8))
	}
	return glyph + " " + text
}

// prefixRows puts pre in front of every row.
func prefixRows(rows []string, pre string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = pre + r
	}
	return out
}
