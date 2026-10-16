package tui

// Helpers shared by the full modals (memory, skills, tools, servers, code map).

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// modalTabs draws a row of tabs; the active one is filled with the brand colour.
func modalTabs(th theme.Theme, labels []string, active int) string {
	parts := make([]string, 0, len(labels))
	for i, l := range labels {
		if i == active {
			parts = append(parts, lipgloss.NewStyle().Bold(true).Foreground(th.BgApp).Background(th.Brand).Render(" "+l+" "))
		} else {
			parts = append(parts, fg(th.TextMuted).Render(" "+l+" "))
		}
	}
	return strings.Join(parts, " ")
}

// relAge formats how long ago t was in short form: 5s, 3m, 2h, 4d, 3mo, 1y.
// Zero or future times read as an em dash.
func relAge(t, now time.Time) string {
	if t.IsZero() || t.After(now) {
		return "—"
	}
	d := now.Sub(t)
	days := int(d.Hours() / 24)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case days < 30:
		return fmt.Sprintf("%dd", days)
	case days < 365:
		return fmt.Sprintf("%dmo", days/30)
	default:
		return fmt.Sprintf("%dy", days/365)
	}
}

// thousands writes n with comma separators.
func thousands(n int) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		return "-" + s
	}
	return s
}

// wrapLines wraps s to width w and returns one entry per output line.
func wrapLines(s string, w int) []string {
	if w < 1 {
		w = 1
	}
	return strings.Split(render.Wrap(s, w), "\n")
}

// keepInView returns an offset that keeps row inside a window of h rows
// starting at off.
func keepInView(row, off, h int) int {
	if row < off {
		return row
	}
	if h > 0 && row >= off+h {
		return row - h + 1
	}
	return off
}

// dropLastRune removes the final character of s.
func dropLastRune(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return string(r[:len(r)-1])
}

// padRight pads s with spaces to w cells (s is truncated when longer).
func padRight(s string, w int) string {
	s = render.Truncate(s, w)
	if gap := w - ansi.StringWidth(s); gap > 0 {
		s += strings.Repeat(" ", gap)
	}
	return s
}

// bar draws n cells of a horizontal bar filled to frac (0..1).
func bar(frac float64, cells int) string {
	if cells < 1 {
		return ""
	}
	filled := int(frac*float64(cells) + 0.5)
	if frac > 0 && filled < 1 {
		filled = 1
	}
	filled = clamp(filled, 0, cells)
	return strings.Repeat("█", filled) + strings.Repeat("░", cells-filled)
}
