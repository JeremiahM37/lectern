package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// Truncate cuts s to at most w cells, ANSI-aware, ending with "…" when it cuts.
func Truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= w {
		return s
	}
	if w == 1 {
		return ansi.Truncate(s, 1, "")
	}
	return ansi.Truncate(s, w, "…")
}

// Wrap breaks s at word boundaries so no line is wider than w cells. Existing
// newlines are kept. ANSI sequences are carried through.
func Wrap(s string, w int) string {
	if w <= 0 {
		return s
	}
	return ansi.Wrap(s, w, " ")
}

// PadRight pads s with spaces to w cells (ANSI-aware). Wider strings are returned
// unchanged.
func PadRight(s string, w int) string {
	n := ansi.StringWidth(s)
	if n >= w {
		return s
	}
	return s + strings.Repeat(" ", w-n)
}

// HumanTokens formats a token count: 842, 1.2k, 3.4M.
func HumanTokens(n int) string {
	switch {
	case n >= 999_950:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 9_950:
		return fmt.Sprintf("%dk", (n+500)/1000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

// HumanDuration formats a duration for tool rows: "850ms", "12s", "1m 05s",
// "1m", "2h 03m", "2h".
func HumanDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	secs := int(d / time.Second)
	switch {
	case secs < 60:
		return fmt.Sprintf("%ds", secs)
	case secs < 3600:
		m, s := secs/60, secs%60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm %02ds", m, s)
	default:
		h, m := secs/3600, (secs%3600)/60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %02dm", h, m)
	}
}

// Elapsed formats a running timer for status lines: "7s", "2m 05s", "1h 02m 03s".
func Elapsed(d time.Duration) string {
	secs := int(d / time.Second)
	switch {
	case secs < 60:
		return fmt.Sprintf("%ds", secs)
	case secs < 3600:
		return fmt.Sprintf("%dm %02ds", secs/60, secs%60)
	default:
		return fmt.Sprintf("%dh %02dm %02ds", secs/3600, (secs%3600)/60, secs%60)
	}
}

// RelTime formats a past instant relative to now: "just now", "5m ago",
// "3h ago", "2d ago", then a date.
func RelTime(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	default:
		return t.Format("Jan 2")
	}
}

// WrapPath is Wrap for text that carries paths and identifiers. A word wider
// than a line breaks after a path separator ("/" or a space) when one falls
// early enough to leave a useful piece, so "/tmp/a/b" wraps as "/tmp/a/" and
// "b". Only when there is no such point does it fall back to punctuation such
// as "-" (inside a UUID, say), and only when there is none at all is the run
// cut at the edge. Newlines are kept, and text with ANSI sequences falls back
// to Wrap.
func WrapPath(s string, w int) string {
	if w <= 0 {
		return s
	}
	if strings.Contains(s, "\x1b") {
		return Wrap(s, w)
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		out = append(out, wrapPathLine([]rune(para), w)...)
	}
	return strings.Join(out, "\n")
}

// pathSep reports a preferred break: after a path separator or a space.
func pathSep(r rune) bool { return r == ' ' || r == '/' || r == '\\' }

// pathPunct reports a fallback break: after punctuation inside a segment.
func pathPunct(r rune) bool {
	switch r {
	case '-', '_', '.', ',', ':', '=', '&', '?', ';':
		return true
	}
	return false
}

// wrapPathLine wraps one line of runes to w cells.
func wrapPathLine(in []rune, w int) []string {
	if runewidthOf(in) <= w {
		return []string{string(in)}
	}
	var lines []string
	var line []rune
	lw := 0
	minBrk := w / 4
	// sep and punct are the indexes just after the last preferred and the last
	// fallback break in line, or -1.
	sep, punct := -1, -1
	note := func(i int) {
		if pathSep(line[i-1]) {
			sep = i
		} else if pathPunct(line[i-1]) {
			punct = i
		}
	}
	for _, r := range in {
		rw := runewidthOf([]rune{r})
		if lw+rw > w && len(line) > 0 {
			cut := -1
			switch {
			case sep >= minBrk:
				cut = sep
			case punct >= minBrk:
				cut = punct
			default:
				cut = len(line)
			}
			lines = append(lines, strings.TrimRight(string(line[:cut]), " "))
			rest := append([]rune{}, line[cut:]...)
			for len(rest) > 0 && rest[0] == ' ' {
				rest = rest[1:]
			}
			line, lw, sep, punct = rest, runewidthOf(rest), -1, -1
			for i := 1; i <= len(line); i++ {
				note(i)
			}
		}
		line = append(line, r)
		lw += rw
		note(len(line))
	}
	if len(line) > 0 {
		lines = append(lines, string(line))
	}
	return lines
}

// runewidthOf is the cell width of runes, one cell for the common cases.
func runewidthOf(rs []rune) int {
	n := 0
	for _, r := range rs {
		n += runewidth.RuneWidth(r)
	}
	return n
}
