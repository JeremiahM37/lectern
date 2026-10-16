package chat

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
)

// builder collects rendered lines and the click targets within them. hit
// line numbers are relative to the builder that owns them.
type builder struct {
	lines []string
	hits  []hitRec
}

type hitRec struct {
	line int
	call *Call
	seg  *Segment
}

func (b *builder) add(lines ...string) { b.lines = append(b.lines, lines...) }

func (b *builder) mark(c *Call, s *Segment) {
	b.hits = append(b.hits, hitRec{line: len(b.lines), call: c, seg: s})
}

// block appends sub, separated from earlier content by a blank line.
func (b *builder) block(sub builder) {
	if len(sub.lines) == 0 {
		return
	}
	if len(b.lines) > 0 {
		b.lines = append(b.lines, "")
	}
	off := len(b.lines)
	b.lines = append(b.lines, sub.lines...)
	for _, h := range sub.hits {
		h.line += off
		b.hits = append(b.hits, h)
	}
}

// tone maps a theme token name to its colour.
func (v *View) tone(name string) lipgloss.TerminalColor {
	t := v.th
	switch name {
	case "brand":
		return t.Brand
	case "brandSecondary":
		return t.BrandSecondary
	case "brandDim":
		return t.BrandDim
	case "brandAlt":
		return t.BrandAlt
	case "error":
		return t.Error
	case "success":
		return t.Success
	case "warning":
		return t.Warning
	case "info":
		return t.Info
	case "amber":
		return t.Amber
	case "textPrimary":
		return t.TextPrimary
	case "textMuted":
		return t.TextMuted
	case "textDim":
		return t.TextDim
	case "textFaint":
		return t.TextFaint
	case "textSubtle":
		return t.TextSubtle
	case "border":
		return t.Border
	case "accentUser":
		return t.AccentUser
	case "accentAssistant":
		return t.AccentAssistant
	case "bgUser":
		return t.BgUser
	case "bgElevated":
		return t.BgElevated
	}
	return t.TextSecondary
}

// fg colours s; a nil colour leaves it plain.
func fg(c lipgloss.TerminalColor, s string) string {
	if s == "" || c == nil {
		return s
	}
	return lipgloss.NewStyle().Foreground(c).Render(s)
}

func styledItalic(s string) string {
	return lipgloss.NewStyle().Italic(true).Render(s)
}

func bold(c lipgloss.TerminalColor, s string) string {
	if c == nil {
		return lipgloss.NewStyle().Bold(true).Render(s)
	}
	return lipgloss.NewStyle().Foreground(c).Bold(true).Render(s)
}

// fit makes s exactly w cells: truncated when wider, padded with spaces when
// narrower. ANSI sequences are preserved.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) > w {
		s = ansi.Truncate(s, w, "")
	}
	if n := w - ansi.StringWidth(s); n > 0 {
		s += strings.Repeat(" ", n)
	}
	return s
}

// clip shortens plain text to n cells, ending in "..." when it cuts.
func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= n {
		return s
	}
	if n <= 3 {
		return ansi.Truncate(s, n, "")
	}
	return ansi.Truncate(s, n-3, "") + "..."
}

func trimmed(s string) string { return strings.TrimSpace(s) }

// joinNonEmpty joins the non-empty parts with sep.
func joinNonEmpty(sep string, parts ...string) string {
	out := parts[:0:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// bgSeq is the escape prefix that sets background c.
func bgSeq(c lipgloss.TerminalColor) string {
	if c == nil {
		return ""
	}
	s := lipgloss.NewStyle().Background(c).Render("x")
	i := strings.Index(s, "x")
	if i <= 0 {
		return ""
	}
	return s[:i]
}

// bgLine fills a line to w cells with background c. Resets inside s are
// followed by the background again, so inner colours do not drop it.
func bgLine(s string, w int, c lipgloss.TerminalColor) string {
	seq := bgSeq(c)
	if seq == "" {
		return fit(s, w)
	}
	s = strings.ReplaceAll(s, "\x1b[0m", "\x1b[0m"+seq)
	s = strings.ReplaceAll(s, "\x1b[m", "\x1b[m"+seq)
	return seq + fit(s, w) + "\x1b[0m"
}

// boxed draws a rounded box of the given outer width. title sits on the top
// border. bg may be nil.
func boxed(title string, body []string, width, padX int, border, bg lipgloss.TerminalColor) []string {
	if width < 6 {
		width = 6
	}
	inner := width - 2 - 2*padX
	bc := func(s string) string { return fg(border, s) }
	var top string
	if title != "" {
		t := " " + title + " "
		if maxT := width - 4; ansi.StringWidth(t) > maxT {
			t = ansi.Truncate(t, maxT, "")
		}
		fill := width - 3 - ansi.StringWidth(t)
		if fill < 0 {
			fill = 0
		}
		top = bc("╭─") + t + bc(strings.Repeat("─", fill)+"╮")
	} else {
		top = bc("╭" + strings.Repeat("─", width-2) + "╮")
	}
	out := []string{bgLine(top, width, bg)}
	pad := strings.Repeat(" ", padX)
	for _, l := range body {
		row := bc("│") + pad + fit(l, inner) + pad + bc("│")
		out = append(out, bgLine(row, width, bg))
	}
	out = append(out, bgLine(bc("╰"+strings.Repeat("─", width-2)+"╯"), width, bg))
	return out
}

var (
	urlRe   = regexp.MustCompile(`https?://\S+`)
	linkRe  = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	spaceRe = regexp.MustCompile(`[ \t]{2,}`)
)

// cleanBody strips URLs and Markdown link targets from system text.
func cleanBody(s string) string {
	s = linkRe.ReplaceAllString(s, "$1")
	s = urlRe.ReplaceAllString(s, "")
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(spaceRe.ReplaceAllString(l, " "))
		if l == "" && len(out) > 0 && out[len(out)-1] == "" {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// wrapText word-wraps each line of s to w cells.
func wrapText(s string, w int) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if w > 0 && ansi.StringWidth(l) > w {
			out = append(out, strings.Split(render.Wrap(l, w), "\n")...)
		} else {
			out = append(out, l)
		}
	}
	return out
}

// shortModelId turns "provider/claude-haiku-4-5-20251001" into "haiku-4-5".
// Names without a known family are cut to 12 cells plus "...".
func shortModelId(ref string) string {
	name := ref
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	low := strings.ToLower(name)
	switch {
	case strings.Contains(low, "gpt-4o-mini"):
		return "4o-mini"
	case strings.Contains(low, "gpt-4o"):
		return "gpt-4o"
	}
	if m := familyRe.FindStringSubmatchIndex(low); m != nil {
		rest := low[m[4]:]
		if d := dateRe.FindStringIndex(rest); d != nil {
			rest = rest[:d[0]]
		}
		if len(rest) > 15 {
			return clip(rest, 15)
		}
		return rest
	}
	if len(name) > 15 {
		return name[:12] + "..."
	}
	return name
}

var (
	familyRe = regexp.MustCompile(`(^|[-_.])(haiku|sonnet|opus|flash|pro)`)
	dateRe   = regexp.MustCompile(`-\d{8}`)
)

// relPath shows a path under the workdir relative to it.
func (v *View) relPath(p string) string {
	if p == "" || v.opt.Workdir == "" || !filepath.IsAbs(p) {
		return p
	}
	rel, err := filepath.Rel(v.opt.Workdir, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return p
	}
	return rel
}

func clockText(t time.Time) string { return t.Format("3:04 PM") }

// secs formats an elapsed time for status lines: "0s", "12s", "1m 05s".
func secs(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	return render.HumanDuration(d.Truncate(time.Second))
}

// frameAt is the animation frame index for a 150 ms spinner.
func frameAt(now time.Time) int { return int(now.UnixMilli() / 150) }

var garblePool = []rune("░▒▓█▄▀▐▌┤├┼─│┌┐└┘╔╗╚╝")

// garble replaces every non-space rune with a block glyph, deterministically
// from seed, for the phrase transition.
func garble(s string, seed int) string {
	if seed < 0 {
		seed = -seed
	}
	var b strings.Builder
	for i, r := range []rune(s) {
		if r == ' ' {
			b.WriteRune(r)
			continue
		}
		b.WriteRune(garblePool[(seed*31+i*17)%len(garblePool)])
	}
	return b.String()
}

// argsOf decodes tool arguments into a map; a bad payload gives an empty map.
func argsOf(raw json.RawMessage) map[string]any {
	m := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

func argStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func argInt(m map[string]any, key string) int {
	if f, ok := m[key].(float64); ok {
		return int(f)
	}
	return 0
}

// genericArg is the first string argument in key order.
func genericArg(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return firstLine(s)
		}
	}
	return ""
}

// iconOf returns an icon glyph in the given theme tone.
func (v *View) iconOf(name, tone string) string {
	return fg(v.tone(tone), icons.Icon(name))
}

// lines splits a rendered block into lines without a trailing empty line.
func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
