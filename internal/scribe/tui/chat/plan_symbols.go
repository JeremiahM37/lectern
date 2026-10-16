package chat

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
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
