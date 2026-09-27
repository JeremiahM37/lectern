// Package filelinks finds what a click on terminal output points at: a web
// address or a file path, including a path an agent's TUI hard-wrapped across
// rows. It is the native client's port of frontend/src/terminal/links.ts; both
// run testdata/vectors.json, so the web terminal and `lectern attach` find
// the same links.
package filelinks

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Span is one row's part of a link: [Start, End) in characters (runes).
type Span struct {
	Row   int `json:"row"`
	Start int `json:"start"`
	End   int `json:"end"`
}

// Link is a web address (Kind "url") or a file (Kind "file"). A file Path is
// relative to the workspace unless External, when it is an absolute or ~/
// path on the session's machine. Verify marks a relative path that is only a
// link if that workspace file exists.
type Link struct {
	Kind     string
	URL      string
	Path     string
	External bool
	Verify   bool
	Line     int
	Column   int
	Text     string
	Spans    []Span
}

// Row is one screen row. Wrapped marks a row that continues the one before
// it (a terminal soft wrap).
type Row struct {
	Text    string
	Wrapped bool
}

// The characters of a file name, in any script; the patterns match the
// frontend's.
const name = `\p{L}\p{N}_@.+~\-`
const segment = `[` + name + `]+`

// :12, :12:5 or (12,5), as compilers and test runners print a position.
const position = `(?::\d+(?::\d+)?|\(\d+(?:,\s?\d+)?\))`

var (
	urlRE = regexp.MustCompile("\\bhttps?://[^\\s<>\"'`]+")
	// A name with an extension, a path with a folder, or a name with a line
	// (Makefile:12). Relative ones are only links if the file exists.
	pathRE = regexp.MustCompile(`(?:/|\./|~/)?` + segment + `(?:/` + segment + `)*\.[A-Za-z0-9]{1,12}` + position + `?` +
		`|(?:/|\./|~/)` + segment + `(?:/` + segment + `)+` + position + `?` +
		`|[` + name + `]*\p{L}[` + name + `]*(?:/` + segment + `)*:\d+(?::\d+)?`)
	// A Python traceback names the line after the path: File "x.py", line 9.
	pythonLine = regexp.MustCompile(`^", line (\d+)`)
	// Box borders, tree gutters and the markers agent TUIs draw beside text.
	leftGutter  = regexp.MustCompile(`^[\s│┃║▏▕╎┆┊⎿└├╰]*`)
	rightGutter = regexp.MustCompile(`[\s│┃║▕]+$`)
	// The path fragment a row ends with; it must hold a "/".
	tailRE = regexp.MustCompile("(?:^|[\\s(\\[{<'\"`])((?:~|\\.{1,2})?/?[" + name + "]*/[" + name + "/]*)$")
	// The start of a following row that continues a path.
	continuation  = regexp.MustCompile(`^\.?[\p{L}\p{N}_@+~][` + name + `]*`)
	folderEnd     = regexp.MustCompile(`[\p{L}\p{N}_.~-]/$`)
	onlySlashes   = regexp.MustCompile(`^/+$`)
	versionRE     = regexp.MustCompile(`^v?\d+(\.\d+)+$`)
	positionRE    = regexp.MustCompile(`(?::(\d+)(?::(\d+))?|\((\d+)(?:,\s?(\d+))?\))$`)
	trailingPunct = regexp.MustCompile(`[.,;:!?'"]+$`)
)

const maxRows = 12

func endsInSpace(s string) bool {
	r, _ := utf8.DecodeLastRuneInString(s)
	return unicode.IsSpace(r)
}

func runes(s string) int { return utf8.RuneCountInString(s) }

// runeSlice returns s[from:to] in rune indices.
func runeSlice(s string, from, to int) string {
	r := []rune(s)
	if from > len(r) {
		from = len(r)
	}
	if to > len(r) {
		to = len(r)
	}
	if to < from {
		to = from
	}
	return string(r[from:to])
}

func trimTail(text string) string {
	out := text
	for changed := true; changed; {
		changed = false
		if stripped := trailingPunct.ReplaceAllString(out, ""); stripped != out {
			out, changed = stripped, true
		}
		for _, pair := range [][2]string{{"(", ")"}, {"[", "]"}, {"{", "}"}} {
			if strings.HasSuffix(out, pair[1]) && strings.Count(out, pair[1]) > strings.Count(out, pair[0]) {
				out, changed = out[:len(out)-1], true
			}
		}
	}
	return out
}

// join reports how row a runs into row b: where a's text ends and b's begins
// (rune indices), or ok=false when they are separate lines.
func join(a, b Row, width int) (aEnd, bStart int, ok bool) {
	if b.Wrapped {
		return runes(a.Text), 0, true
	}
	trimmed := rightGutter.ReplaceAllString(a.Text, "")
	m := tailRE.FindStringSubmatch(trimmed)
	if m == nil || onlySlashes.MatchString(m[1]) {
		return 0, 0, false
	}
	tail := m[1]
	folder := folderEnd.MatchString(tail)
	cut := width > 0 && runes(a.Text) >= width && !endsInSpace(a.Text) && !strings.HasSuffix(tail, "/")
	if !folder && !cut {
		return 0, 0, false
	}
	gutter := leftGutter.FindString(b.Text)
	if !continuation.MatchString(b.Text[len(gutter):]) {
		return 0, 0, false
	}
	return runes(trimmed), runes(gutter), true
}

type piece struct{ row, from, to, offset int }

func chainAround(rows []Row, row, width int) (string, []piece) {
	start, end := row, row
	for start > 0 && row-start < maxRows {
		if _, _, ok := join(rows[start-1], rows[start], width); !ok {
			break
		}
		start--
	}
	for end < len(rows)-1 && end-row < maxRows {
		if _, _, ok := join(rows[end], rows[end+1], width); !ok {
			break
		}
		end++
	}
	var text strings.Builder
	var pieces []piece
	length := 0
	for i := start; i <= end; i++ {
		from, to := 0, runes(rows[i].Text)
		if i > start {
			_, from, _ = join(rows[i-1], rows[i], width)
		}
		if i < end {
			to, _, _ = join(rows[i], rows[i+1], width)
		}
		if to < from {
			to = from
		}
		pieces = append(pieces, piece{row: i, from: from, to: to, offset: length})
		part := runeSlice(rows[i].Text, from, to)
		text.WriteString(part)
		length += runes(part)
	}
	return text.String(), pieces
}

func spansOf(pieces []piece, start, end int) []Span {
	var spans []Span
	for _, p := range pieces {
		lo, hi := max(start, p.offset), min(end, p.offset+p.to-p.from)
		if lo < hi {
			spans = append(spans, Span{Row: p.row, Start: p.from + lo - p.offset, End: p.from + hi - p.offset})
		}
	}
	return spans
}

func normalize(parts []string) ([]string, bool) {
	var out []string
	for _, part := range parts {
		switch part {
		case "..":
			if len(out) == 0 {
				return nil, false
			}
			out = out[:len(out)-1]
		case "", ".":
		default:
			out = append(out, part)
		}
	}
	return out, true
}

// Classify says what a path token names, relative to the workspace where it can be.
func Classify(raw, workdir string) (Link, bool) {
	link := Link{Kind: "file", Text: raw}
	token := raw
	if m := positionRE.FindStringSubmatchIndex(raw); m != nil {
		token = raw[:m[0]]
		group := func(i int) int {
			if m[2*i] < 0 {
				return 0
			}
			n, _ := strconv.Atoi(raw[m[2*i]:m[2*i+1]])
			return n
		}
		link.Line, link.Column = max(group(1), group(3)), max(group(2), group(4))
	}
	if versionRE.MatchString(token) {
		return Link{}, false
	}
	root := strings.TrimRight(workdir, "/")
	switch {
	case strings.HasPrefix(token, "~/"):
		link.Path, link.External = token, true
	case strings.HasPrefix(token, "/"):
		parts, ok := normalize(strings.Split(token, "/"))
		if !ok || len(parts) == 0 {
			return Link{}, false
		}
		path := "/" + strings.Join(parts, "/")
		if root != "" && strings.HasPrefix(path, root+"/") {
			link.Path = path[len(root)+1:]
		} else {
			link.Path, link.External = path, true
		}
	default:
		if parts, ok := normalize(strings.Split(token, "/")); ok && len(parts) > 0 {
			link.Path, link.Verify = strings.Join(parts, "/"), true
			break
		}
		if root == "" {
			return Link{}, false
		}
		parts, ok := normalize(append(strings.Split(root, "/"), strings.Split(token, "/")...))
		if !ok || len(parts) == 0 {
			return Link{}, false
		}
		link.Path, link.External = "/"+strings.Join(parts, "/"), true
	}
	return link, true
}

// runeIndex converts a byte offset in s to a rune index.
func runeIndex(s string, byteOffset int) int { return runes(s[:byteOffset]) }

// LinksOnRow is every link with a part on this row, each with its spans on
// all rows. width is the terminal width, or 0 when unknown.
func LinksOnRow(rows []Row, row int, workdir string, width int) []Link {
	if row < 0 || row >= len(rows) {
		return nil
	}
	text, pieces := chainAround(rows, row, width)
	var found []Link
	var taken [][2]int
	for _, m := range urlRE.FindAllStringIndex(text, -1) {
		start := runeIndex(text, m[0])
		value := trimTail(text[m[0]:m[1]])
		taken = append(taken, [2]int{start, runeIndex(text, m[1])})
		found = append(found, Link{Kind: "url", URL: value, Text: value, Spans: spansOf(pieces, start, start+runes(value))})
	}
	for _, m := range pathRE.FindAllStringIndex(text, -1) {
		start := runeIndex(text, m[0])
		raw := trimTail(text[m[0]:m[1]])
		// A path cut short with an ellipsis (a status line, a truncated title) names nothing.
		if strings.HasPrefix(text[m[1]:], "…") {
			continue
		}
		end := start + runes(raw)
		overlaps := false
		for _, t := range taken {
			if start < t[1] && end > t[0] {
				overlaps = true
			}
		}
		if overlaps {
			continue
		}
		if link, ok := Classify(raw, workdir); ok {
			if python := pythonLine.FindStringSubmatch(text[m[1]:]); python != nil && link.Line == 0 {
				link.Line, _ = strconv.Atoi(python[1])
			}
			link.Spans = spansOf(pieces, start, end)
			found = append(found, link)
		}
	}
	var out []Link
	for _, link := range found {
		for _, span := range link.Spans {
			if span.Row == row {
				out = append(out, link)
				break
			}
		}
	}
	return out
}

// LinkAt is the link under character col of row, if any.
func LinkAt(rows []Row, row, col int, workdir string, width int) (Link, bool) {
	for _, link := range LinksOnRow(rows, row, workdir, width) {
		for _, span := range link.Spans {
			if span.Row == row && col >= span.Start && col < span.End {
				return link, true
			}
		}
	}
	return Link{}, false
}

// HyperlinkTarget is an OSC 8 hyperlink's target: http(s) as a web link, a
// file:// URL or a bare absolute or ~/ path (as agent TUIs print for Markdown
// links to files) as a file. Percent-encoding is decoded.
func HyperlinkTarget(uri, workdir string) (Link, bool) {
	if strings.HasPrefix(uri, "/") || strings.HasPrefix(uri, "~/") {
		path := uri
		if decoded, err := url.PathUnescape(uri); err == nil {
			path = decoded
		}
		link, ok := Classify(path, workdir)
		link.Text = uri
		return link, ok
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		return Link{}, false
	}
	switch parsed.Scheme {
	case "http", "https":
		return Link{Kind: "url", URL: parsed.String(), Text: uri}, true
	case "file":
		link, ok := Classify(parsed.Path, workdir)
		link.Text = uri
		return link, ok
	}
	return Link{}, false
}
