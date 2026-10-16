package render

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// span is a run of inline text with one set of emphasis flags.
type span struct {
	text      string
	bold      bool
	italic    bool
	strike    bool
	code      bool
	link      string // destination, for links and autolinks
	urlText   bool   // the shown URL after a link label
	underline bool
}

// inlineFlags are the emphasis flags carried into nested spans.
type inlineFlags struct {
	bold, italic, strike bool
	link                 string
}

// parseInline turns inline Markdown into spans. It implements the common
// CommonMark cases: escapes, code spans, emphasis, strong, strikethrough,
// links, images (shown as links), autolinks and bare http(s) URLs. Anything that
// does not close stays literal, which is what makes streaming text safe.
func parseInline(src string, f inlineFlags) []span {
	rs := []rune(src)
	var out []span
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			out = append(out, span{text: buf.String(), bold: f.bold, italic: f.italic, strike: f.strike, link: f.link})
			buf.Reset()
		}
	}
	n := len(rs)
	for i := 0; i < n; {
		c := rs[i]
		switch {
		case c == '\\' && i+1 < n && isASCIIPunct(rs[i+1]):
			buf.WriteRune(rs[i+1])
			i += 2
		case c == '`':
			run := runLen(rs, i, '`')
			if j := findRun(rs, i+run, '`', run); j >= 0 {
				content := string(rs[i+run : j])
				if len(content) >= 2 && content[0] == ' ' && content[len(content)-1] == ' ' && strings.Trim(content, " ") != "" {
					content = content[1 : len(content)-1]
				}
				flush()
				out = append(out, span{text: content, code: true, bold: f.bold, link: f.link})
				i = j + run
			} else {
				buf.WriteString(string(rs[i : i+run]))
				i += run
			}
		case c == '<':
			if j := indexRune(rs, i+1, '>'); j > 0 {
				inner := string(rs[i+1 : j])
				if isAutolink(inner) {
					flush()
					out = append(out, span{text: inner, link: inner, underline: true})
					i = j + 1
					break
				}
			}
			buf.WriteRune(c)
			i++
		case c == '[' || (c == '!' && i+1 < n && rs[i+1] == '['):
			start := i
			if c == '!' {
				start = i + 1
			}
			if label, dest, next, ok := parseLinkAt(rs, start); ok {
				flush()
				lf := f
				lf.link = dest
				children := parseInline(label, lf)
				if len(children) == 0 {
					children = []span{{text: dest, link: dest}}
				}
				out = append(out, children...)
				if strings.TrimSpace(label) != dest && dest != "" {
					out = append(out, span{text: " (" + dest + ")", urlText: true})
				}
				i = next
				break
			}
			buf.WriteRune(c)
			i++
		case (c == '*' || c == '_') && canOpen(rs, i, c):
			run := runLen(rs, i, c)
			need := run
			if need > 3 {
				need = 3
			}
			matched := false
			for need >= 1 && !matched {
				if j := findCloser(rs, i+need, c, need); j >= 0 {
					flush()
					ef := f
					switch need {
					case 1:
						ef.italic = true
					case 2:
						ef.bold = true
					default:
						ef.bold, ef.italic = true, true
					}
					out = append(out, parseInline(string(rs[i+need:j]), ef)...)
					i = j + need
					matched = true
				} else {
					need--
				}
			}
			if !matched {
				buf.WriteString(string(rs[i : i+run]))
				i += run
			}
		case c == '~' && i+1 < n && rs[i+1] == '~' && canOpen(rs, i, '~'):
			if j := findRun(rs, i+2, '~', 2); j >= 0 {
				flush()
				ef := f
				ef.strike = true
				out = append(out, parseInline(string(rs[i+2:j]), ef)...)
				i = j + 2
				break
			}
			buf.WriteString("~~")
			i += 2
		case (c == 'h') && (hasPrefixRunes(rs, i, "https://") || hasPrefixRunes(rs, i, "http://")) && (i == 0 || !isWordRune(rs[i-1])):
			j := i
			for j < n && !unicode.IsSpace(rs[j]) && rs[j] != '<' && rs[j] != '>' && rs[j] != '"' {
				j++
			}
			for j > i && strings.ContainsRune(".,;:!?)]}'", rs[j-1]) {
				j--
			}
			flush()
			out = append(out, span{text: string(rs[i:j]), link: string(rs[i:j]), underline: true})
			i = j
		case c == '\n':
			buf.WriteByte(' ')
			i++
		default:
			buf.WriteRune(c)
			i++
		}
	}
	flush()
	return out
}

func isASCIIPunct(r rune) bool {
	return r < 128 && unicode.IsPunct(r) || strings.ContainsRune("$+<=>^`|~", r)
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func hasPrefixRunes(rs []rune, i int, p string) bool {
	return hasPrefixAt(rs, i, p)
}

func runLen(rs []rune, i int, c rune) int {
	j := i
	for j < len(rs) && rs[j] == c {
		j++
	}
	return j - i
}

// findRun finds a run of exactly want copies of c starting at or after from.
// It returns the index of the run's first rune, or -1.
func findRun(rs []rune, from int, c rune, want int) int {
	for j := from; j < len(rs); {
		if rs[j] != c {
			j++
			continue
		}
		l := runLen(rs, j, c)
		if l == want {
			return j
		}
		j += l
	}
	return -1
}

// canOpen reports whether a delimiter at i can open emphasis: the next rune is
// not whitespace, and for underscores the previous rune is not a word rune.
func canOpen(rs []rune, i int, c rune) bool {
	run := runLen(rs, i, c)
	if i+run >= len(rs) || unicode.IsSpace(rs[i+run]) {
		return false
	}
	if c == '_' && i > 0 && isWordRune(rs[i-1]) {
		return false
	}
	return true
}

// findCloser finds a delimiter run of length exactly want whose previous rune is
// not whitespace, starting at from.
func findCloser(rs []rune, from int, c rune, want int) int {
	for j := from; j < len(rs); {
		if rs[j] != c {
			j++
			continue
		}
		l := runLen(rs, j, c)
		prevOK := j > 0 && !unicode.IsSpace(rs[j-1])
		if l == want && prevOK && j > from {
			if c == '_' && j+l < len(rs) && isWordRune(rs[j+l]) {
				j += l
				continue
			}
			return j
		}
		j += l
	}
	return -1
}

func indexRune(rs []rune, from int, c rune) int {
	for j := from; j < len(rs); j++ {
		if rs[j] == c {
			return j
		}
	}
	return -1
}

// parseLinkAt parses [label](dest) starting at the '[' at i. It returns the
// label, destination and the index after the closing ')'.
func parseLinkAt(rs []rune, i int) (string, string, int, bool) {
	depth := 0
	j := i
	for ; j < len(rs); j++ {
		switch rs[j] {
		case '[':
			depth++
		case ']':
			depth--
		}
		if depth == 0 {
			break
		}
	}
	if j >= len(rs) || j+1 >= len(rs) || rs[j+1] != '(' {
		return "", "", 0, false
	}
	label := string(rs[i+1 : j])
	k := j + 2
	depth = 1
	for ; k < len(rs); k++ {
		if rs[k] == '(' {
			depth++
		} else if rs[k] == ')' {
			depth--
			if depth == 0 {
				break
			}
		}
	}
	if k >= len(rs) {
		return "", "", 0, false
	}
	dest := strings.TrimSpace(string(rs[j+2 : k]))
	if sp := strings.IndexAny(dest, " \t"); sp >= 0 {
		dest = dest[:sp] // drop an optional title
	}
	dest = strings.Trim(dest, "<>")
	return label, dest, k + 1, true
}

func isAutolink(s string) bool {
	if i := strings.IndexByte(s, ':'); i > 1 {
		scheme := s[:i]
		if !unicode.IsLetter(rune(scheme[0])) {
			return false
		}
		for _, r := range scheme {
			if !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("+.-", r)) {
				return false
			}
		}
		return !strings.ContainsAny(s, " \t")
	}
	return false
}

// inlineStyle returns the lipgloss style for one span.
func inlineStyle(sp span, base lipgloss.TerminalColor, th theme.Theme) lipgloss.Style {
	st := lipgloss.NewStyle().Foreground(base)
	switch {
	case sp.code:
		st = st.Foreground(th.Amber).Background(th.BgElevated)
	case sp.urlText:
		st = st.Foreground(th.TextDim)
	case sp.link != "":
		st = st.Foreground(th.Info).Underline(true)
	}
	if sp.underline {
		st = st.Underline(true)
	}
	if sp.bold {
		st = st.Bold(true)
	}
	if sp.italic {
		st = st.Italic(true)
	}
	if sp.strike {
		st = st.Strikethrough(true)
	}
	return st
}

// hyperlinks reports whether the output can carry OSC 8 hyperlinks. Any colour
// profile that strips escapes also strips these.
func hyperlinks() bool {
	return lipgloss.DefaultRenderer().ColorProfile() != termenv.Ascii
}

// renderSpan renders one span, wrapping it in an OSC 8 hyperlink when it has a
// destination and the terminal supports them.
func renderSpan(sp span, base lipgloss.TerminalColor, th theme.Theme) string {
	s := inlineStyle(sp, base, th).Render(sp.text)
	if sp.link != "" && !sp.urlText && hyperlinks() {
		s = "\x1b]8;;" + sp.link + "\x1b\\" + s + "\x1b]8;;\x1b\\"
	}
	return s
}

// wrapInline lays spans out in lines no wider than width. Words break at
// spaces; a word wider than a whole line is split by cell width.
func wrapInline(spans []span, width int, base lipgloss.TerminalColor, th theme.Theme) []string {
	if width < 1 {
		width = 1
	}
	// Group spans into words. A space ends a word; adjacent spans with no space
	// between them stay one word, so "**bold**text" wraps as a unit.
	var words [][]span
	var cur []span
	flushWord := func() {
		if len(cur) > 0 {
			words = append(words, cur)
			cur = nil
		}
	}
	for _, sp := range spans {
		for idx, p := range strings.Split(sp.text, " ") {
			if idx > 0 {
				flushWord()
			}
			if p != "" {
				piece := sp
				piece.text = p
				cur = append(cur, piece)
			}
		}
	}
	flushWord()

	var lines []string
	var line []span
	lineW := 0
	emit := func() {
		var b strings.Builder
		for _, sp := range line {
			b.WriteString(renderSpan(sp, base, th))
		}
		lines = append(lines, b.String())
		line, lineW = nil, 0
	}
	for _, w := range words {
		wW := 0
		for _, p := range w {
			wW += runewidth.StringWidth(p.text)
		}
		switch {
		case lineW > 0 && lineW+1+wW <= width:
			line = append(line, span{text: " "})
			lineW++
		case lineW > 0:
			emit()
		}
		if wW <= width {
			for _, p := range w {
				line = append(line, p)
				lineW += runewidth.StringWidth(p.text)
			}
			continue
		}
		// A word wider than the line: split it by cell width.
		for _, p := range w {
			for _, r := range p.text {
				rw := runewidth.RuneWidth(r)
				if lineW+rw > width && lineW > 0 {
					emit()
				}
				q := p
				q.text = string(r)
				line = append(line, q)
				lineW += rw
			}
		}
	}
	if len(line) > 0 || len(lines) == 0 {
		emit()
	}
	return lines
}

// plainWidth is the display width of spans without styling.
func plainWidth(spans []span) int {
	n := 0
	for _, s := range spans {
		n += runewidth.StringWidth(s.text)
	}
	return n
}
