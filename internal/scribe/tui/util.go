package tui

import (
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// clamp limits v to [lo, hi].
func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// fg returns a style with only a foreground colour.
func fg(c lipgloss.TerminalColor) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

// fgb is fg plus bold.
func fgb(c lipgloss.TerminalColor) lipgloss.Style { return fg(c).Bold(true) }

// fitWidth truncates or pads s so it occupies exactly w cells.
func fitWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = render.Truncate(s, w)
	return render.PadRight(s, w)
}

// fitLines returns exactly h lines (truncating or padding with blanks), each exactly w cells wide.
func fitLines(lines []string, w, h int) []string {
	out := make([]string, h)
	for i := 0; i < h; i++ {
		if i < len(lines) {
			out[i] = fitWidth(lines[i], w)
		} else {
			out[i] = strings.Repeat(" ", w)
		}
	}
	return out
}

// fuzzyScore scores target against query (subsequence match). Scores follow
// the spec: +5 for a first-char hit, +3 after a separator, +2 for consecutive
// hits, -1 per skipped char after the first hit; a prefix match adds 100 and an
// exact match another 50. ok is false when query is not a subsequence.
func fuzzyScore(target, query string) (int, bool) {
	if query == "" {
		return 0, true
	}
	t := []rune(strings.ToLower(strings.TrimPrefix(target, "/")))
	q := []rune(strings.ToLower(query))
	score := 0
	ti := 0
	prevHit := -2
	first := -1
	for _, qc := range q {
		found := false
		for ti < len(t) {
			if t[ti] == qc {
				found = true
				break
			}
			ti++
		}
		if !found {
			return 0, false
		}
		if first < 0 {
			first = ti
			if ti == 0 {
				score += 5
			} else if isSep(t[ti-1]) {
				score += 3
			}
		} else {
			skipped := ti - prevHit - 1
			if skipped == 0 {
				score += 2
			} else {
				score -= skipped
			}
		}
		if ti > 0 && isSep(t[ti-1]) {
			score += 1
		}
		prevHit = ti
		ti++
	}
	if strings.HasPrefix(string(t), string(q)) {
		score += 100
		if len(t) == len(q) {
			score += 50
		}
	}
	score -= len(t) / 4
	return score, true
}

func isSep(r rune) bool { return r == ' ' || r == '/' || r == '-' || r == '_' || unicode.IsSpace(r) }

// printable returns the text a key message types, or "" when the key is not
// printable text (control keys, arrows, Alt combinations).
func printable(msg tea.KeyMsg) string {
	if msg.Type == tea.KeyRunes && !msg.Alt {
		return string(msg.Runes)
	}
	if msg.Type == tea.KeySpace {
		return " "
	}
	return ""
}

// overlay draws box (already framed) centred on base. base is padded or
// cut to w x h first, so the result is exactly h lines of w cells.
func overlay(base []string, box []string, w, h int) []string {
	out := fitLines(base, w, h)
	if len(box) == 0 {
		return out
	}
	bw := 0
	for _, l := range box {
		if n := ansi.StringWidth(l); n > bw {
			bw = n
		}
	}
	bh := len(box)
	x := (w - bw) / 2
	y := (h - bh) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	// The rows beside the box keep the screen behind, except a one-column
	// gutter next to each border. A side strip of overlayMinMargin columns or
	// less is blanked entirely: a sliver of the frame behind reads as a broken
	// border, not as background.
	lb, rb := 1, 1 // blank columns left and right of the box
	if x <= overlayMinMargin {
		lb = x
	}
	if rm := w - x - bw; rm <= overlayMinMargin {
		rb = max(rm, 0)
	}
	for i, bl := range box {
		row := y + i
		if row >= h {
			break
		}
		lx := max(x-lb, 0)
		left := ansi.Truncate(out[row], lx, "")
		if ansi.StringWidth(left) < lx {
			left += strings.Repeat(" ", lx-ansi.StringWidth(left))
		}
		left += strings.Repeat(" ", x-lx)
		if bwl := ansi.StringWidth(bl); bwl < bw {
			bl += strings.Repeat(" ", bw-bwl)
		}
		right := strings.Repeat(" ", rb) + ansi.TruncateLeft(out[row], x+bw+rb, "")
		out[row] = fitWidth(left+bl+right, w)
	}
	return out
}

// overlayMinMargin is the widest side strip that is blanked rather than shown.
const overlayMinMargin = 3

// frame draws a rounded box with the title in the top border and footer hints
// in the bottom border. body must already be w-4 wide per line.
func frame(title, icon string, body []string, footer string, w int, borderCol lipgloss.TerminalColor, th theme.Theme) []string {
	if w < 8 {
		w = 8
	}
	inner := w - 2
	bs := lipgloss.NewStyle().Foreground(borderCol)
	head := ""
	if title != "" {
		t := title
		if icon != "" {
			t = icon + " " + title
		}
		head = " " + fgb(th.TextPrimary).Render(render.Truncate(t, inner-4)) + " "
	}
	topFill := inner - ansi.StringWidth(head) - 1
	if topFill < 0 {
		topFill = 0
	}
	lines := []string{bs.Render("╭─") + head + bs.Render(strings.Repeat("─", topFill)+"╮")}
	for _, l := range body {
		lines = append(lines, bs.Render("│")+fitWidth(l, inner)+bs.Render("│"))
	}
	fw := inner - 2
	foot := ""
	if footer != "" {
		foot = " " + fg(th.TextDim).Render(render.Truncate(footer, fw-2)) + " "
	}
	fill := inner - ansi.StringWidth(foot) - 1
	if fill < 0 {
		fill = 0
	}
	lines = append(lines, bs.Render("╰"+strings.Repeat("─", 1))+foot+bs.Render(strings.Repeat("─", fill)+"╯"))
	return lines
}

// padBlock returns s as a block of exactly w cells wide and h lines tall.
func padBlock(s string, w, h int) []string {
	return fitLines(strings.Split(s, "\n"), w, h)
}
