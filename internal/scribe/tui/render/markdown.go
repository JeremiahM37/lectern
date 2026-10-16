package render

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// Markdown renders CommonMark-style Markdown for a terminal of the given width.
// streaming tolerates text that is still arriving: an unclosed fence runs to
// the end, a partial <system-reminder> is hidden, and unclosed emphasis stays
// literal. <system-reminder> blocks are removed.
func Markdown(src string, width int, th theme.Theme, streaming bool) string {
	if width < 10 {
		width = 10
	}
	src = stripSystemReminders(src, streaming)
	src = strings.ReplaceAll(src, "\r\n", "\n")
	src = strings.ReplaceAll(src, "\t", "    ")
	lines := strings.Split(src, "\n")
	r := &mdRenderer{th: th}
	out := r.blocks(parseBlocks(lines), width, th.TextPrimary)
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

const reminderOpen, reminderClose = "<system-reminder>", "</system-reminder>"

var reminderBlock = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>[ \t]*\n?`)

// stripSystemReminders removes <system-reminder> blocks. When streaming, an
// open block with no close is removed to the end, and so is a partial opening
// tag at the very end.
func stripSystemReminders(src string, streaming bool) string {
	src = reminderBlock.ReplaceAllString(src, "")
	if i := strings.Index(src, reminderOpen); i >= 0 {
		src = src[:i]
	}
	if streaming {
		for k := len(reminderOpen) - 1; k >= 1; k-- {
			if strings.HasSuffix(src, reminderOpen[:k]) {
				src = src[:len(src)-k]
				break
			}
		}
	}
	return src
}

// ---- block model ----

type block interface{}

type headingBlock struct {
	level int
	text  string
}

type paraBlock struct{ text string }

type codeBlock struct {
	lang  string
	lines []string
}

type quoteBlock struct{ children []block }

type listItem struct {
	task     int // 0 none, 1 unchecked, 2 checked
	children []block
}

type listBlock struct {
	ordered bool
	start   int
	loose   bool
	items   []listItem
}

type tableBlock struct {
	aligns []string
	header []string
	rows   [][]string
}

type hrBlock struct{}

var (
	fenceRe   = regexp.MustCompile("^( {0,3})(`{3,}|~{3,})\\s*(.*)$")
	headingRe = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*))?$`)
	hrRe      = regexp.MustCompile(`^ {0,3}((\*[ \t]*){3,}|(-[ \t]*){3,}|(_[ \t]*){3,})$`)
	quoteRe   = regexp.MustCompile(`^ {0,3}>`)
	listRe    = regexp.MustCompile(`^( {0,3})([-*+]|\d{1,9}[.)])(?:([ \t]+)(.*)|$)`)
	delimRe   = regexp.MustCompile(`^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$`)
)

func indentOf(s string) int {
	n := 0
	for n < len(s) && s[n] == ' ' {
		n++
	}
	return n
}

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }

// isBlockStart reports whether a line starts a block that interrupts a paragraph.
func isBlockStart(s string) bool {
	return fenceRe.MatchString(s) || headingRe.MatchString(s) || hrRe.MatchString(s) ||
		quoteRe.MatchString(s) || listRe.MatchString(s)
}

// parseBlocks parses lines into blocks. It recurses into containers.
func parseBlocks(lines []string) []block {
	var out []block
	i := 0
	for i < len(lines) {
		line := lines[i]
		if isBlank(line) {
			i++
			continue
		}
		if m := fenceRe.FindStringSubmatch(line); m != nil && !(m[2][0] == '`' && strings.Contains(m[3], "`")) {
			indent := len(m[1])
			fence := m[2]
			lang := strings.Fields(m[3])
			cb := codeBlock{}
			if len(lang) > 0 {
				cb.lang = lang[0]
			}
			j := i + 1
			for ; j < len(lines); j++ {
				t := strings.TrimSpace(lines[j])
				if strings.HasPrefix(t, fence[:1]) && strings.Trim(t, fence[:1]) == "" && len(t) >= len(fence) && indentOf(lines[j]) <= 3 {
					break
				}
				body := lines[j]
				strip := indent
				if k := indentOf(body); k < strip {
					strip = k
				}
				cb.lines = append(cb.lines, body[strip:])
			}
			out = append(out, cb)
			i = j + 1
			continue
		}
		if m := headingRe.FindStringSubmatch(line); m != nil {
			text := strings.TrimSpace(m[2])
			text = strings.TrimSpace(strings.TrimRight(text, "#"))
			out = append(out, headingBlock{level: len(m[1]), text: text})
			i++
			continue
		}
		if hrRe.MatchString(line) {
			out = append(out, hrBlock{})
			i++
			continue
		}
		if quoteRe.MatchString(line) {
			var inner []string
			j := i
			for j < len(lines) {
				l := lines[j]
				if quoteRe.MatchString(l) {
					l = strings.TrimLeft(l, " ")[1:]
					l = strings.TrimPrefix(l, " ")
					inner = append(inner, l)
					j++
					continue
				}
				// lazy continuation of a paragraph inside the quote
				if !isBlank(l) && len(inner) > 0 && !isBlank(inner[len(inner)-1]) && !isBlockStart(l) {
					inner = append(inner, l)
					j++
					continue
				}
				break
			}
			out = append(out, quoteBlock{children: parseBlocks(inner)})
			i = j
			continue
		}
		if m := listRe.FindStringSubmatch(line); m != nil {
			lb, next := parseList(lines, i)
			out = append(out, lb)
			i = next
			continue
		}
		if i+1 < len(lines) && strings.Contains(line, "|") && delimRe.MatchString(lines[i+1]) &&
			len(splitRow(line)) == len(splitRow(lines[i+1])) {
			tb, next := parseTable(lines, i)
			out = append(out, tb)
			i = next
			continue
		}
		if indentOf(line) >= 4 {
			cb := codeBlock{}
			j := i
			for j < len(lines) && (indentOf(lines[j]) >= 4 || isBlank(lines[j])) {
				if isBlank(lines[j]) {
					// a blank line belongs to the code only if more code follows
					k := j
					for k < len(lines) && isBlank(lines[k]) {
						k++
					}
					if k >= len(lines) || indentOf(lines[k]) < 4 {
						break
					}
				}
				cb.lines = append(cb.lines, strings.TrimPrefix(lines[j], "    "))
				j++
			}
			out = append(out, cb)
			i = j
			continue
		}
		// paragraph
		var para []string
		j := i
		for j < len(lines) && !isBlank(lines[j]) && (j == i || !isBlockStart(lines[j])) {
			para = append(para, strings.TrimLeft(lines[j], " "))
			j++
		}
		out = append(out, paraBlock{text: strings.Join(para, "\n")})
		i = j
	}
	return out
}

// parseList parses a list starting at lines[i]. It returns the list and the
// index of the first line after it.
func parseList(lines []string, i int) (listBlock, int) {
	first := listRe.FindStringSubmatch(lines[i])
	marker := first[2]
	ordered := marker[0] >= '0' && marker[0] <= '9'
	lb := listBlock{ordered: ordered}
	if ordered {
		lb.start = atoi(strings.TrimRight(marker, ".)"))
	} else {
		lb.start = 1
	}
	j := i
	for j < len(lines) {
		m := listRe.FindStringSubmatch(lines[j])
		if m == nil || (m[2][0] >= '0' && m[2][0] <= '9') != ordered || (!ordered && m[2] != marker) {
			break
		}
		indent := len(m[1])
		spaces := m[3]
		content := m[4]
		contentCol := indent + len(m[2]) + len(spaces)
		if len(spaces) > 4 || spaces == "" {
			contentCol = indent + len(m[2]) + 1
			content = strings.TrimLeft(content, " ")
		}
		item := []string{content}
		k := j + 1
		sawBlank := false
		for k < len(lines) {
			l := lines[k]
			if isBlank(l) {
				// blank lines continue the item only if indented content follows
				n := k
				for n < len(lines) && isBlank(lines[n]) {
					n++
				}
				if n < len(lines) && indentOf(lines[n]) >= contentCol {
					for ; k < n; k++ {
						item = append(item, "")
					}
					sawBlank = true
					continue
				}
				break
			}
			if indentOf(l) >= contentCol {
				item = append(item, l[contentCol:])
				k++
				continue
			}
			// lazy continuation of the item's paragraph
			if !isBlockStart(l) && item[len(item)-1] != "" {
				item = append(item, strings.TrimLeft(l, " "))
				k++
				continue
			}
			break
		}
		li := listItem{}
		children := parseBlocks(item)
		if len(children) > 0 {
			if p, ok := children[0].(paraBlock); ok {
				switch {
				case strings.HasPrefix(p.text, "[ ] "), p.text == "[ ]":
					li.task = 1
					p.text = strings.TrimPrefix(strings.TrimPrefix(p.text, "[ ]"), " ")
					children[0] = p
				case strings.HasPrefix(p.text, "[x] "), strings.HasPrefix(p.text, "[X] "), p.text == "[x]", p.text == "[X]":
					li.task = 2
					p.text = strings.TrimSpace(p.text[3:])
					children[0] = p
				}
			}
		}
		li.children = children
		lb.items = append(lb.items, li)
		j = k
		if sawBlank {
			lb.loose = true
		}
		// a blank line between two items makes the list loose
		if j < len(lines) && isBlank(lines[j-1]) {
			lb.loose = true
		}
	}
	return lb, j
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}

// splitRow splits a table row into trimmed cells, honouring \| escapes and
// dropping one leading and one trailing pipe.
func splitRow(line string) []string {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "|")
	if strings.HasSuffix(s, "|") && !strings.HasSuffix(s, "\\|") {
		s = s[:len(s)-1]
	}
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && s[i+1] == '|' {
			cur.WriteByte('|')
			i++
			continue
		}
		if s[i] == '|' {
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(s[i])
	}
	cells = append(cells, strings.TrimSpace(cur.String()))
	return cells
}

func parseTable(lines []string, i int) (tableBlock, int) {
	tb := tableBlock{header: splitRow(lines[i])}
	for _, d := range splitRow(lines[i+1]) {
		switch {
		case strings.HasPrefix(d, ":") && strings.HasSuffix(d, ":"):
			tb.aligns = append(tb.aligns, "center")
		case strings.HasSuffix(d, ":"):
			tb.aligns = append(tb.aligns, "right")
		case strings.HasPrefix(d, ":"):
			tb.aligns = append(tb.aligns, "left")
		default:
			tb.aligns = append(tb.aligns, "")
		}
	}
	j := i + 2
	for ; j < len(lines) && !isBlank(lines[j]) && strings.Contains(lines[j], "|"); j++ {
		row := splitRow(lines[j])
		for len(row) < len(tb.header) {
			row = append(row, "")
		}
		tb.rows = append(tb.rows, row[:len(tb.header)])
	}
	return tb, j
}

// ---- rendering ----

type mdRenderer struct {
	th    theme.Theme
	depth int  // list nesting depth, selects the bullet glyph
	tight bool // tight list items: no blank line between an item's blocks
}

// blocks renders blocks with one blank line between them.
func (r *mdRenderer) blocks(bs []block, width int, base lipgloss.TerminalColor) []string {
	var out []string
	for idx, b := range bs {
		if idx > 0 && !r.tight {
			out = append(out, "")
		}
		out = append(out, r.block(b, width, base)...)
	}
	return out
}

func (r *mdRenderer) block(b block, width int, base lipgloss.TerminalColor) []string {
	th := r.th
	switch v := b.(type) {
	case headingBlock:
		var col lipgloss.TerminalColor
		switch v.level {
		case 1:
			col = th.Brand
		case 2:
			col = th.BrandAlt
		case 3:
			col = th.TextPrimary
		default:
			col = th.TextSecondary
		}
		spans := parseInline(v.text, inlineFlags{bold: true})
		lines := wrapInline(spans, width, col, th)
		if v.level == 1 {
			w := plainWidth(spans)
			if w > width {
				w = width
			}
			if w < 1 {
				w = 1
			}
			lines = append(lines, lipgloss.NewStyle().Foreground(th.TextFaint).Render(strings.Repeat("─", w)))
		}
		return lines
	case paraBlock:
		return wrapInline(parseInline(v.text, inlineFlags{}), width, base, th)
	case hrBlock:
		return []string{lipgloss.NewStyle().Foreground(th.TextFaint).Render(strings.Repeat("─", width))}
	case codeBlock:
		return r.code(v, width)
	case quoteBlock:
		inner := r.blocks(v.children, width-2, th.TextSecondary)
		rail := lipgloss.NewStyle().Foreground(th.TextDim).Render("│") + " "
		out := make([]string, 0, len(inner))
		for _, l := range inner {
			out = append(out, rail+l)
		}
		return out
	case listBlock:
		return r.list(v, width, base)
	case tableBlock:
		return r.table(v, width)
	}
	return nil
}

func (r *mdRenderer) code(cb codeBlock, width int) []string {
	th := r.th
	bg := th.BgElevated
	pad := lipgloss.NewStyle().Background(bg)
	var out []string
	if cb.lang != "" {
		out = append(out, lipgloss.NewStyle().Foreground(th.TextDim).Render("  "+cb.lang))
	}
	inner := width - 4
	if inner < 1 {
		inner = 1
	}
	lang := resolveLang(cb.lang)
	toks := tokenizeLines(cb.lines, lang)
	if len(cb.lines) == 0 {
		cb.lines = []string{""}
		toks = [][]token{nil}
	}
	for i, line := range cb.lines {
		cells := cellsFor(line, toks[i], bg)
		for _, chunk := range chunkCells(cells, inner) {
			body := paint(chunk, th)
			row := pad.Render("  ") + body
			out = append(out, row+pad.Render(strings.Repeat(" ", max0(width-ansi.StringWidth(row)))))
		}
	}
	return out
}

// chunkCells splits cells into rows no wider than w cells. An empty input gives
// one empty row.
func chunkCells(cells []cell, w int) [][]cell {
	if len(cells) == 0 {
		return [][]cell{{}}
	}
	var rows [][]cell
	var cur []cell
	cw := 0
	for _, c := range cells {
		rw := runewidth.RuneWidth(c.r)
		if cw+rw > w && len(cur) > 0 {
			rows = append(rows, cur)
			cur, cw = nil, 0
		}
		cur = append(cur, c)
		cw += rw
	}
	return append(rows, cur)
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func (r *mdRenderer) list(lb listBlock, width int, base lipgloss.TerminalColor) []string {
	th := r.th
	var out []string
	for idx, item := range lb.items {
		if idx > 0 && lb.loose {
			out = append(out, "")
		}
		var marker string
		var markerStyle lipgloss.Style
		switch {
		case item.task == 1:
			marker, markerStyle = "☐ ", lipgloss.NewStyle().Foreground(th.TextMuted)
		case item.task == 2:
			marker, markerStyle = "☑ ", lipgloss.NewStyle().Foreground(th.Success)
		case lb.ordered:
			marker = itoa(lb.start+idx) + ". "
			markerStyle = lipgloss.NewStyle().Foreground(th.TextSecondary)
		default:
			marker = [...]string{"•", "◦", "▪"}[min(r.depth, 2)] + " "
			markerStyle = lipgloss.NewStyle().Foreground(th.TextMuted)
		}
		mw := runewidth.StringWidth(marker)
		childW := width - mw
		if childW < 4 {
			childW = 4
		}
		sub := &mdRenderer{th: th, depth: r.depth + 1, tight: !lb.loose}
		body := sub.blocks(item.children, childW, base)
		if len(body) == 0 {
			body = []string{""}
		}
		pad := strings.Repeat(" ", mw)
		for k, l := range body {
			if k == 0 {
				out = append(out, markerStyle.Render(marker)+l)
				continue
			}
			out = append(out, pad+l)
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func (r *mdRenderer) table(tb tableBlock, width int) []string {
	th := r.th
	n := len(tb.header)
	if n == 0 {
		return nil
	}
	// natural column widths
	headerSpans := make([][]span, n)
	bodySpans := make([][][]span, len(tb.rows))
	natural := make([]int, n)
	for c, h := range tb.header {
		headerSpans[c] = parseInline(h, inlineFlags{bold: true})
		natural[c] = max(plainWidth(headerSpans[c]), 3)
	}
	for ri, row := range tb.rows {
		bodySpans[ri] = make([][]span, n)
		for c, cell := range row {
			bodySpans[ri][c] = parseInline(cell, inlineFlags{})
			if w := plainWidth(bodySpans[ri][c]); w > natural[c] {
				natural[c] = w
			}
		}
	}
	// fit to width: border chars are 1 + n*3 (│ x │ x │) without padding; we pad 1 each side
	avail := width - (3*n + 1)
	if avail < n {
		// too narrow for a table: render rows as plain text
		var out []string
		for _, row := range tb.rows {
			out = append(out, wrapInline(parseInline(strings.Join(row, " | "), inlineFlags{}), width, th.TextPrimary, th)...)
		}
		return out
	}
	widths := append([]int(nil), natural...)
	for sum(widths) > avail {
		big := 0
		for c := range widths {
			if widths[c] > widths[big] {
				big = c
			}
		}
		widths[big]--
	}
	border := lipgloss.NewStyle().Foreground(th.TextFaint)
	hline := func(l, m, rgt string) string {
		var b strings.Builder
		b.WriteString(border.Render(l))
		for c, w := range widths {
			b.WriteString(border.Render(strings.Repeat("─", w+2)))
			if c < n-1 {
				b.WriteString(border.Render(m))
			}
		}
		b.WriteString(border.Render(rgt))
		return b.String()
	}
	vbar := border.Render("│")
	rowLines := func(cells [][]span) []string {
		wrapped := make([][]string, n)
		height := 1
		for c := 0; c < n; c++ {
			var spans []span
			if c < len(cells) {
				spans = cells[c]
			}
			wrapped[c] = wrapInline(spans, widths[c], th.TextPrimary, th)
			if len(wrapped[c]) > height {
				height = len(wrapped[c])
			}
		}
		var out []string
		for h := 0; h < height; h++ {
			var b strings.Builder
			b.WriteString(vbar)
			for c := 0; c < n; c++ {
				text := ""
				if h < len(wrapped[c]) {
					text = wrapped[c][h]
				}
				b.WriteString(" " + alignTo(text, widths[c], tb.alignOf(c)) + " ")
				b.WriteString(vbar)
			}
			out = append(out, b.String())
		}
		return out
	}
	var out []string
	out = append(out, hline("╭", "┬", "╮"))
	out = append(out, rowLines(headerSpans)...)
	out = append(out, hline("├", "┼", "┤"))
	for ri := range bodySpans {
		out = append(out, rowLines(bodySpans[ri])...)
	}
	out = append(out, hline("╰", "┴", "╯"))
	return out
}

// alignOf returns the column alignment: "left", "center", "right" or "".
func (tb tableBlock) alignOf(c int) string {
	if c < len(tb.aligns) {
		return tb.aligns[c]
	}
	return ""
}

// alignTo pads a rendered cell to w cells according to the column alignment.
func alignTo(s string, w int, align string) string {
	gap := w - ansi.StringWidth(s)
	if gap <= 0 {
		return s
	}
	switch align {
	case "right":
		return strings.Repeat(" ", gap) + s
	case "center":
		left := gap / 2
		return strings.Repeat(" ", left) + s + strings.Repeat(" ", gap-left)
	}
	return s + strings.Repeat(" ", gap)
}

func sum(xs []int) int {
	t := 0
	for _, x := range xs {
		t += x
	}
	return t
}
