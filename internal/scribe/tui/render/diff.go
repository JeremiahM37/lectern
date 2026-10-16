package render

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// DiffOpts controls Diff.
type DiffOpts struct {
	SideBySide  bool // two columns, old on the left and new on the right
	MaxLines    int  // cap on body rows (0 = no cap)
	LineNumbers bool // old and new line-number gutters
}

// largeDiffLines is the changed-line count above which the body is replaced
// by a one-line summary.
const largeDiffLines = 50

// contextKeep is how many unchanged lines stay on each side of a collapsed run.
const contextKeep = 2

type diffRow struct {
	kind    byte // ' ' context, '+' added, '-' removed, '~' collapse marker
	oldNo   int
	newNo   int
	text    string
	changed []bool // per rune: word-level change (paired -/+ rows only)
}

// Diff renders a unified diff inside a rounded box whose top edge carries the
// title "Edit ─ path +N -M" (or "New" for a file creation). Context runs keep
// two lines on each side of a change and collapse the middle. Changed word
// spans are emphasised inside paired -/+ lines.
func Diff(unified string, width int, th theme.Theme, opts DiffOpts) string {
	if width < 12 {
		width = 12
	}
	rows, path, adds, dels, created := parseUnified(unified)
	lang := LangFromPath(path)
	verb := "Edit"
	if created {
		verb = "New"
	}
	inner := width - 2

	var body []string
	if adds+dels > largeDiffLines {
		msg := fmt.Sprintf("%d lines changed", adds+dels)
		body = []string{padCells(lipgloss.NewStyle().Foreground(th.TextMuted).Render(msg), inner)}
	} else {
		pairWordChanges(rows)
		display := collapseContext(rows, created)
		if opts.SideBySide {
			body = sideBySide(display, inner, lang, th, opts.LineNumbers)
		} else {
			body = unifiedBody(display, inner, lang, th, opts.LineNumbers)
		}
	}
	if opts.MaxLines > 0 && len(body) > opts.MaxLines {
		hidden := len(body) - (opts.MaxLines - 1)
		body = append(body[:opts.MaxLines-1], padCells(lipgloss.NewStyle().Foreground(th.TextDim).Render(fmt.Sprintf("… %d more lines", hidden)), inner))
	}
	return frameBox(verb, path, adds, dels, body, width, th)
}

// parseUnified reads a unified diff. Headers are skipped; the path comes from
// the +++ line (or ---). Lines without a leading sign count as context.
func parseUnified(s string) (rows []diffRow, path string, adds, dels int, created bool) {
	oldNo, newNo := 1, 1
	for _, raw := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		switch {
		case strings.HasPrefix(raw, "+++ "):
			if path == "" {
				path = cleanPath(raw[4:])
			}
		case strings.HasPrefix(raw, "--- "):
			if path == "" {
				path = cleanPath(raw[4:])
			}
		case strings.HasPrefix(raw, "diff "), strings.HasPrefix(raw, "index "), strings.HasPrefix(raw, "\\ "):
		case strings.HasPrefix(raw, "@@"):
			var a, b, c, d int
			if n, _ := fmt.Sscanf(raw, "@@ -%d,%d +%d,%d @@", &a, &b, &c, &d); n == 4 {
				oldNo, newNo = a, c
				if a == 0 && b == 0 {
					created = true // "-0,0": the file did not exist before
				}
			} else if n, _ := fmt.Sscanf(raw, "@@ -%d +%d", &a, &c); n == 2 {
				oldNo, newNo = a, c
			}
		case strings.HasPrefix(raw, "+"):
			rows = append(rows, diffRow{kind: '+', newNo: newNo, text: raw[1:]})
			newNo++
			adds++
		case strings.HasPrefix(raw, "-"):
			rows = append(rows, diffRow{kind: '-', oldNo: oldNo, text: raw[1:]})
			oldNo++
			dels++
		default:
			text := strings.TrimPrefix(raw, " ")
			rows = append(rows, diffRow{kind: ' ', oldNo: oldNo, newNo: newNo, text: text})
			oldNo++
			newNo++
		}
	}
	return
}

func cleanPath(p string) string {
	p = strings.TrimSpace(p)
	if i := strings.IndexByte(p, '\t'); i >= 0 {
		p = p[:i]
	}
	if p == "/dev/null" {
		return ""
	}
	p = strings.TrimPrefix(p, "b/")
	p = strings.TrimPrefix(p, "a/")
	return p
}

// collapseContext keeps contextKeep lines on each side of a change and replaces
// the middle of longer unchanged runs with a marker. Runs at the start or end
// of the diff keep only the lines next to the change. A creation diff keeps its
// first three and last two lines.
func collapseContext(rows []diffRow, created bool) []diffRow {
	if created && len(rows) > 10 {
		var out []diffRow
		out = append(out, rows[:3]...)
		out = append(out, diffRow{kind: '~', text: fmt.Sprintf("⋯ %d lines", len(rows)-5)})
		return append(out, rows[len(rows)-2:]...)
	}
	var out []diffRow
	for i := 0; i < len(rows); {
		if rows[i].kind != ' ' {
			out = append(out, rows[i])
			i++
			continue
		}
		j := i
		for j < len(rows) && rows[j].kind == ' ' {
			j++
		}
		run := rows[i:j]
		head, tail := contextKeep, contextKeep
		if i == 0 {
			head = 0
		}
		if j == len(rows) {
			tail = 0
		}
		if len(run) > head+tail+1 {
			out = append(out, run[:head]...)
			hidden := len(run) - head - tail
			noun := "lines"
			if hidden == 1 {
				noun = "line"
			}
			out = append(out, diffRow{kind: '~', text: fmt.Sprintf("⋯ %d unchanged %s", hidden, noun)})
			out = append(out, run[len(run)-tail:]...)
		} else {
			out = append(out, run...)
		}
		i = j
	}
	return out
}

// pairWordChanges marks the changed runes of each paired removed/added line
// inside a change block. Pairs are matched by position.
func pairWordChanges(rows []diffRow) {
	for i := 0; i < len(rows); {
		if rows[i].kind == ' ' {
			i++
			continue
		}
		j := i
		for j < len(rows) && rows[j].kind != ' ' {
			j++
		}
		var dels, adds []int
		for k := i; k < j; k++ {
			if rows[k].kind == '-' {
				dels = append(dels, k)
			} else {
				adds = append(adds, k)
			}
		}
		for p := 0; p < len(dels) && p < len(adds); p++ {
			a, b := &rows[dels[p]], &rows[adds[p]]
			a.changed, b.changed = wordChanges(a.text, b.text)
		}
		i = j
	}
}

// wordChanges returns per-rune change flags for two lines. Tokens are words,
// runs of spaces, or single punctuation runes. Tokens outside the longest common
// subsequence are changed.
func wordChanges(a, b string) ([]bool, []bool) {
	ta, tb := splitTokens(a), splitTokens(b)
	if len(ta)*len(tb) > 40000 {
		return allTrue(a), allTrue(b)
	}
	keepA, keepB := lcsMarks(ta, tb)
	return marksToRunes(ta, keepA), marksToRunes(tb, keepB)
}

func allTrue(s string) []bool {
	out := make([]bool, len([]rune(s)))
	for i := range out {
		out[i] = true
	}
	return out
}

func splitTokens(s string) []string {
	var out []string
	rs := []rune(s)
	for i := 0; i < len(rs); {
		j := i + 1
		switch {
		case isWordChar(rs[i]):
			for j < len(rs) && isWordChar(rs[j]) {
				j++
			}
		case unicode.IsSpace(rs[i]):
			for j < len(rs) && unicode.IsSpace(rs[j]) {
				j++
			}
		}
		out = append(out, string(rs[i:j]))
		i = j
	}
	return out
}

func isWordChar(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

// lcsMarks returns, for each token of a and b, whether it is part of a longest
// common subsequence.
func lcsMarks(a, b []string) ([]bool, []bool) {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	keepA, keepB := make([]bool, n), make([]bool, m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			keepA[i], keepB[j] = true, true
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			i++
		default:
			j++
		}
	}
	return keepA, keepB
}

// marksToRunes expands token keep-flags into per-rune changed flags.
func marksToRunes(tokens []string, keep []bool) []bool {
	var out []bool
	for k, t := range tokens {
		for range []rune(t) {
			out = append(out, !keep[k])
		}
	}
	return out
}

// diffColors returns the background and emphasised background for a row kind.
func diffColors(kind byte, th theme.Theme) (bg, strong lipgloss.TerminalColor) {
	switch kind {
	case '+':
		return th.DiffAddedBg, blendColor(th.DiffAddedBg, th.DiffAddedSign, 0.45)
	case '-':
		return th.DiffRemovedBg, blendColor(th.DiffRemovedBg, th.DiffRemovedSign, 0.45)
	}
	return nil, nil
}

// blendColor mixes two hex colours. It returns the first colour unchanged when
// either is not a hex value (ANSI indexes, NO_COLOR).
func blendColor(base, accent lipgloss.TerminalColor, t float64) lipgloss.TerminalColor {
	b, ok1 := hexRGB(base)
	a, ok2 := hexRGB(accent)
	if !ok1 || !ok2 {
		return base
	}
	var out [3]int
	for k := 0; k < 3; k++ {
		out[k] = int(float64(b[k]) + (float64(a[k])-float64(b[k]))*t + 0.5)
	}
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", out[0], out[1], out[2]))
}

func hexRGB(c lipgloss.TerminalColor) ([3]int, bool) {
	var out [3]int
	lc, ok := c.(lipgloss.Color)
	if !ok {
		return out, false
	}
	h := strings.TrimPrefix(string(lc), "#")
	switch len(h) {
	case 3:
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	case 6:
	default:
		return out, false
	}
	for k := 0; k < 3; k++ {
		v, err := strconv.ParseUint(h[2*k:2*k+2], 16, 8)
		if err != nil {
			return out, false
		}
		out[k] = int(v)
	}
	return out, true
}

// rowCells builds the content cells for one diff row: syntax colours, row
// background, and emphasis on changed runes.
func rowCells(r diffRow, lang string, th theme.Theme) []cell {
	bg, strong := diffColors(r.kind, th)
	toks := tokenizeLines([]string{strings.ReplaceAll(r.text, "\t", "    ")}, lang)[0]
	cells := cellsFor(strings.ReplaceAll(r.text, "\t", "    "), toks, bg)
	for k := range cells {
		if k < len(r.changed) && r.changed[k] && strong != nil {
			cells[k].bg = strong
			cells[k].bold = true
		}
	}
	return cells
}

func gutterWidth(rows []diffRow) int {
	maxNo := 1
	for _, r := range rows {
		if r.oldNo > maxNo {
			maxNo = r.oldNo
		}
		if r.newNo > maxNo {
			maxNo = r.newNo
		}
	}
	return len(strconv.Itoa(maxNo))
}

// rowPrefix renders the line-number and sign columns for a row. first is false
// for wrapped continuation lines, which get a blank gutter.
func rowPrefix(r diffRow, gw int, first, numbers bool, th theme.Theme) (string, int) {
	bg, _ := diffColors(r.kind, th)
	style := func(c lipgloss.TerminalColor) lipgloss.Style {
		st := lipgloss.NewStyle().Foreground(c)
		if bg != nil {
			st = st.Background(bg)
		}
		return st
	}
	var sb strings.Builder
	w := 0
	if numbers {
		oldS, newS := strings.Repeat(" ", gw), strings.Repeat(" ", gw)
		if first && r.oldNo > 0 && r.kind != '+' {
			oldS = fmt.Sprintf("%*d", gw, r.oldNo)
		}
		if first && r.newNo > 0 && r.kind != '-' {
			newS = fmt.Sprintf("%*d", gw, r.newNo)
		}
		sb.WriteString(style(th.TextDim).Render(oldS + " " + newS + " "))
		w += 2*gw + 2
	}
	sign := " "
	signColor := th.TextDim
	if first {
		switch r.kind {
		case '+':
			sign, signColor = "+", th.DiffAddedSign
		case '-':
			sign, signColor = "-", th.DiffRemovedSign
		}
	}
	sb.WriteString(style(signColor).Render(sign + " "))
	w += 2
	return sb.String(), w
}

func unifiedBody(rows []diffRow, inner int, lang string, th theme.Theme, numbers bool) []string {
	gw := gutterWidth(rows)
	var out []string
	for _, r := range rows {
		if r.kind == '~' {
			out = append(out, padCells(lipgloss.NewStyle().Foreground(th.TextDim).Render("  "+r.text), inner))
			continue
		}
		bg, _ := diffColors(r.kind, th)
		cells := rowCells(r, lang, th)
		prefixW := 2
		if numbers {
			prefixW += 2*gw + 2
		}
		contentW := inner - prefixW
		if contentW < 1 {
			contentW = 1
		}
		for k, chunk := range chunkCells(cells, contentW) {
			prefix, _ := rowPrefix(r, gw, k == 0, numbers, th)
			line := prefix + paint(chunk, th)
			out = append(out, fillRow(line, bg, inner))
		}
	}
	return out
}

func sideBySide(rows []diffRow, inner int, lang string, th theme.Theme, numbers bool) []string {
	gw := gutterWidth(rows)
	half := (inner - 3) / 2
	sideW := half
	var out []string
	sep := lipgloss.NewStyle().Foreground(th.TextFaint).Render("│")
	side := func(r *diffRow) string {
		if r == nil {
			return strings.Repeat(" ", sideW)
		}
		bg, _ := diffColors(r.kind, th)
		prefix, pw := rowPrefix(*r, gw, true, numbers, th)
		if !numbers {
			pw = 2
		}
		contentW := sideW - pw
		if contentW < 1 {
			contentW = 1
		}
		chunks := chunkCells(rowCells(*r, lang, th), contentW)
		line := prefix + paint(chunks[0], th)
		return fillRow(line, bg, sideW)
	}
	emitPair := func(l, r *diffRow) {
		out = append(out, side(l)+" "+sep+" "+side(r))
	}
	for i := 0; i < len(rows); {
		r := rows[i]
		if r.kind == '~' {
			out = append(out, padCells(lipgloss.NewStyle().Foreground(th.TextDim).Render("  "+r.text), inner))
			i++
			continue
		}
		if r.kind == ' ' {
			rr := r
			emitPair(&rr, &rr)
			i++
			continue
		}
		j := i
		var dels, adds []diffRow
		for j < len(rows) && rows[j].kind != ' ' && rows[j].kind != '~' {
			if rows[j].kind == '-' {
				dels = append(dels, rows[j])
			} else {
				adds = append(adds, rows[j])
			}
			j++
		}
		for k := 0; k < len(dels) || k < len(adds); k++ {
			var l, rt *diffRow
			if k < len(dels) {
				l = &dels[k]
			}
			if k < len(adds) {
				rt = &adds[k]
			}
			emitPair(l, rt)
		}
		i = j
	}
	return out
}

// fillRow pads a rendered line to width cells using the row background.
func fillRow(line string, bg lipgloss.TerminalColor, width int) string {
	w := ansi.StringWidth(line)
	if w >= width {
		return ansi.Truncate(line, width, "")
	}
	pad := strings.Repeat(" ", width-w)
	if bg != nil {
		pad = lipgloss.NewStyle().Background(bg).Render(pad)
	}
	return line + pad
}

// padCells pads a line with plain spaces to width.
func padCells(line string, width int) string { return fillRow(line, nil, width) }

// frameBox draws the rounded box with the title on its top edge.
func frameBox(verb, path string, adds, dels int, body []string, width int, th theme.Theme) string {
	border := lipgloss.NewStyle().Foreground(th.Border)
	inner := width - 2
	icon := lipgloss.NewStyle().Foreground(th.Brand).Render(icons.Icon("pencil"))
	verbS := lipgloss.NewStyle().Foreground(th.Brand).Bold(true).Render(verb)
	sep := border.Render(" ─ ")
	counts := ""
	if adds > 0 {
		counts += " " + lipgloss.NewStyle().Foreground(th.Success).Render(fmt.Sprintf("+%d", adds))
	}
	if dels > 0 {
		counts += " " + lipgloss.NewStyle().Foreground(th.Error).Render(fmt.Sprintf("-%d", dels))
	}
	fixed := 3 + 1 + runewidth.StringWidth(verb) + 3 + ansi.StringWidth(counts) + 1
	maxPath := width - fixed - 4
	if maxPath < 1 {
		maxPath = 1
	}
	p := path
	if p == "" {
		p = "(unknown file)"
	}
	pathS := lipgloss.NewStyle().Foreground(th.TextPrimary).Render(Truncate(p, maxPath))
	title := border.Render("─ ") + icon + " " + verbS + sep + pathS + counts + " "
	if ansi.StringWidth(title) > width-2 {
		title = ansi.Truncate(title, width-2, "")
	}
	fill := width - 2 - ansi.StringWidth(title)
	if fill < 0 {
		fill = 0
	}
	var b strings.Builder
	b.WriteString(border.Render("╭") + title + border.Render(strings.Repeat("─", fill)) + border.Render("╮"))
	vbar := border.Render("│")
	for _, line := range body {
		b.WriteString("\n" + vbar + fillRow(line, nil, inner) + vbar)
	}
	b.WriteString("\n" + border.Render("╰"+strings.Repeat("─", inner)+"╯"))
	return b.String()
}
