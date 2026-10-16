package web

import (
	"html"
	"net/url"
	"strconv"
	"strings"
)

// The HTML side is a small tolerant parser (stdlib only) that builds a tree
// of element and text nodes, then a renderer that writes Markdown-ish text.
// It is not a full HTML5 parser: it handles the malformed markup common on
// the web (unclosed p/li/td, stray end tags, unquoted attributes) and never
// fails.

type node struct {
	tag   string // lowercase element name; "" for a text node
	text  string // text nodes only
	attrs map[string]string
	kids  []*node
}

var voidTags = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true,
	"img": true, "input": true, "link": true, "meta": true, "param": true,
	"source": true, "track": true, "wbr": true,
}

// closesP lists elements that end an open <p> when they start.
var closesP = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true, "dd": true, "details": true,
	"dialog": true, "div": true, "dl": true, "dt": true, "fieldset": true, "figcaption": true,
	"figure": true, "footer": true, "form": true, "h1": true, "h2": true, "h3": true, "h4": true,
	"h5": true, "h6": true, "header": true, "hr": true, "main": true, "menu": true, "nav": true,
	"ol": true, "p": true, "pre": true, "section": true, "table": true, "ul": true,
}

// dropped elements are removed with their content.
var dropped = map[string]bool{
	"script": true, "style": true, "nav": true, "footer": true, "header": true, "aside": true,
	"noscript": true, "svg": true, "template": true, "head": true, "iframe": true,
	"object": true, "canvas": true, "button": true,
}

// blockTags separate words when text is flattened to a single line.
var blockTags = map[string]bool{
	"address": true, "article": true, "blockquote": true, "br": true, "dd": true, "div": true,
	"dl": true, "dt": true, "figcaption": true, "figure": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "hr": true, "li": true, "main": true, "ol": true, "p": true,
	"pre": true, "section": true, "table": true, "td": true, "th": true, "tr": true, "ul": true,
}

// inlineTags are walked in place: they never start a new paragraph.
var inlineTags = map[string]bool{
	"abbr": true, "b": true, "bdi": true, "bdo": true, "cite": true, "data": true, "del": true,
	"dfn": true, "em": true, "font": true, "i": true, "ins": true, "kbd": true, "label": true,
	"mark": true, "q": true, "s": true, "samp": true, "small": true, "span": true, "strong": true,
	"sub": true, "sup": true, "time": true, "u": true, "var": true,
}

func isAlpha(b byte) bool { return (b|0x20) >= 'a' && (b|0x20) <= 'z' }
func isDigit(b byte) bool { return b >= '0' && b <= '9' }
func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' }

// tagName reads an element name at the start of s.
func tagName(s string) string {
	j := 0
	for j < len(s) && (isAlpha(s[j]) || isDigit(s[j]) || s[j] == '-' || s[j] == ':') {
		j++
	}
	return strings.ToLower(s[:j])
}

// parseHTML builds the node tree for src. The root has tag "#document".
func parseHTML(src string) *node {
	root := &node{tag: "#document"}
	stack := []*node{root}
	cur := func() *node { return stack[len(stack)-1] }
	appendKid := func(n *node) {
		c := cur()
		c.kids = append(c.kids, n)
	}
	addText := func(s string) {
		if s != "" {
			appendKid(&node{text: html.UnescapeString(s)})
		}
	}
	closeTag := func(name string) {
		for j := len(stack) - 1; j > 0; j-- {
			if stack[j].tag == name {
				stack = stack[:j]
				return
			}
		}
	}
	openTag := func(name string, attrs map[string]string, selfClose bool) {
		top := cur().tag
		switch {
		case closesP[name] && top == "p":
			stack = stack[:len(stack)-1]
		case name == "li" && top == "li", (name == "dt" || name == "dd") && (top == "dt" || top == "dd"):
			stack = stack[:len(stack)-1]
		case name == "td" || name == "th":
			if top == "td" || top == "th" {
				stack = stack[:len(stack)-1]
			}
		case name == "tr":
			if top == "td" || top == "th" {
				stack = stack[:len(stack)-1]
			}
			if cur().tag == "tr" {
				stack = stack[:len(stack)-1]
			}
		}
		n := &node{tag: name, attrs: attrs}
		appendKid(n)
		if voidTags[name] || selfClose {
			return
		}
		stack = append(stack, n)
	}

	i := 0
	for i < len(src) {
		lt := strings.IndexByte(src[i:], '<')
		if lt < 0 {
			addText(src[i:])
			break
		}
		addText(src[i : i+lt])
		i += lt
		rest := src[i:]
		switch {
		case strings.HasPrefix(rest, "<!--"):
			if end := strings.Index(rest[4:], "-->"); end < 0 {
				i = len(src)
			} else {
				i += 4 + end + 3
			}
		case len(rest) > 1 && (rest[1] == '!' || rest[1] == '?'):
			if end := strings.IndexByte(rest, '>'); end < 0 {
				i = len(src)
			} else {
				i += end + 1
			}
		case len(rest) > 2 && rest[1] == '/' && isAlpha(rest[2]):
			end := strings.IndexByte(rest, '>')
			if end < 0 {
				i = len(src)
				break
			}
			closeTag(tagName(rest[2:]))
			i += end + 1
		case len(rest) > 1 && isAlpha(rest[1]):
			name, attrs, selfClose, n, ok := parseStartTag(rest)
			if !ok {
				addText("<")
				i++
				break
			}
			i += n
			openTag(name, attrs, selfClose)
			if (name == "script" || name == "style") && !selfClose {
				// Raw text: everything up to the matching end tag is text.
				body := src[i:]
				end := indexFold(body, "</"+name)
				if end < 0 {
					end = len(body)
				}
				if end > 0 {
					appendKid(&node{text: body[:end]})
				}
				i += end
			}
		default:
			addText("<")
			i++
		}
	}
	return root
}

// parseStartTag parses "<name attrs...>" at the start of s. ok is false
// when the tag never closes, in which case the caller treats "<" as text.
func parseStartTag(s string) (name string, attrs map[string]string, selfClose bool, n int, ok bool) {
	name = tagName(s[1:])
	j := 1 + len(name)
	attrs = map[string]string{}
	for {
		for j < len(s) && isSpace(s[j]) {
			j++
		}
		if j >= len(s) {
			return "", nil, false, 0, false
		}
		switch {
		case s[j] == '>':
			return name, attrs, selfClose, j + 1, true
		case s[j] == '/':
			if j+1 < len(s) && s[j+1] == '>' {
				return name, attrs, true, j + 2, true
			}
			j++
			continue
		}
		k := j
		for k < len(s) && !isSpace(s[k]) && s[k] != '>' && s[k] != '/' && s[k] != '=' {
			k++
		}
		if k == j {
			k++ // a stray '=' at the start of a name
		}
		aname := strings.ToLower(s[j:k])
		j = k
		for j < len(s) && isSpace(s[j]) {
			j++
		}
		val := ""
		if j < len(s) && s[j] == '=' {
			j++
			for j < len(s) && isSpace(s[j]) {
				j++
			}
			if j < len(s) && (s[j] == '"' || s[j] == '\'') {
				q := s[j]
				end := strings.IndexByte(s[j+1:], q)
				if end < 0 {
					return "", nil, false, 0, false
				}
				val = s[j+1 : j+1+end]
				j = j + 1 + end + 1
			} else {
				k = j
				for k < len(s) && !isSpace(s[k]) && s[k] != '>' {
					k++
				}
				val = s[j:k]
				j = k
			}
		}
		if _, dup := attrs[aname]; !dup {
			attrs[aname] = html.UnescapeString(val)
		}
	}
}

// indexFold finds sub in s, ASCII case-insensitively.
func indexFold(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if strings.EqualFold(s[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}

// collapse folds all runs of whitespace into single spaces.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// inlineText flattens n to one line of text. Links keep only their text.
func inlineText(n *node) string {
	var b strings.Builder
	inlineWalk(n, &b)
	return b.String()
}

func inlineWalk(n *node, b *strings.Builder) {
	for _, c := range n.kids {
		if c.tag == "" {
			b.WriteString(c.text)
			continue
		}
		if dropped[c.tag] {
			continue
		}
		block := blockTags[c.tag]
		if block {
			b.WriteByte(' ')
		}
		inlineWalk(c, b)
		if block {
			b.WriteByte(' ')
		}
	}
}

// rawText returns text with preserved whitespace; <br> becomes a newline.
func rawText(n *node, b *strings.Builder) {
	for _, c := range n.kids {
		switch {
		case c.tag == "":
			b.WriteString(c.text)
		case c.tag == "br":
			b.WriteByte('\n')
		default:
			rawText(c, b)
		}
	}
}

// findTitle returns the first <title> in document order.
func findTitle(n *node) string {
	for _, c := range n.kids {
		if c.tag == "title" {
			return collapse(inlineText(c))
		}
		if t := findTitle(c); t != "" {
			return t
		}
	}
	return ""
}

// htmlToMarkdown returns the page title and its readable Markdown text.
// base resolves relative links; it may be nil.
func htmlToMarkdown(src string, base *url.URL) (title, md string) {
	doc := parseHTML(src)
	title = findTitle(doc)
	r := &renderer{base: base}
	r.walk(doc)
	r.flush()
	return title, tidy(r.out.String())
}

type renderer struct {
	base   *url.URL
	out    strings.Builder
	para   strings.Builder // inline text of the paragraph being built
	prefix string          // list marker for the next line written by flush
	inList int             // >0 while inside a list item
	depth  int             // number of enclosing lists
}

// flush ends the current paragraph. Inside a list item it ends a line.
func (r *renderer) flush() {
	text := collapse(r.para.String())
	r.para.Reset()
	if text == "" {
		return
	}
	sep := "\n\n"
	if r.inList > 0 {
		sep = "\n"
	}
	r.out.WriteString(r.prefix + text + sep)
	r.prefix = ""
}

func (r *renderer) walk(n *node) {
	for _, c := range n.kids {
		if c.tag == "" {
			r.para.WriteString(c.text)
			continue
		}
		if dropped[c.tag] {
			continue
		}
		if lvl := headingLevel(c.tag); lvl > 0 {
			r.flush()
			if t := collapse(inlineText(c)); t != "" {
				r.out.WriteString(strings.Repeat("#", lvl) + " " + t + "\n\n")
			}
			continue
		}
		switch c.tag {
		case "ul", "ol":
			r.flush()
			r.list(c, c.tag == "ol")
		case "li":
			r.item(c, "- ")
		case "pre":
			r.flush()
			r.pre(c)
		case "table":
			r.flush()
			r.table(c)
		case "blockquote":
			r.flush()
			r.quote(c)
		case "hr":
			r.flush()
			r.out.WriteString("---\n\n")
		case "br":
			r.para.WriteByte(' ')
		case "img":
			if alt := collapse(c.attrs["alt"]); alt != "" {
				r.para.WriteString(" [image: " + alt + "] ")
			}
		case "a":
			r.link(c)
		case "code":
			if t := collapse(inlineText(c)); t != "" {
				r.para.WriteString(codeSpan(t))
			}
		default:
			if inlineTags[c.tag] {
				r.walk(c)
				continue
			}
			r.flush()
			r.walk(c)
			r.flush()
		}
	}
}

func headingLevel(tag string) int {
	switch tag {
	case "h1":
		return 1
	case "h2":
		return 2
	case "h3":
		return 3
	case "h4":
		return 4
	case "h5":
		return 5
	case "h6":
		return 6
	}
	return 0
}

func (r *renderer) list(n *node, ordered bool) {
	if r.prefix != "" {
		// A list directly under an item with no text of its own: keep the
		// bullet as a line of its own rather than attaching it to a child.
		r.out.WriteString(strings.TrimRight(r.prefix, " ") + "\n")
		r.prefix = ""
	}
	r.depth++
	indent := strings.Repeat("  ", r.depth-1)
	num := 0
	for _, c := range n.kids {
		if c.tag != "li" {
			continue
		}
		num++
		marker := indent + "- "
		if ordered {
			marker = indent + strconv.Itoa(num) + ". "
		}
		r.item(c, marker)
	}
	r.depth--
	if r.depth == 0 {
		r.out.WriteString("\n")
	}
}

func (r *renderer) item(c *node, marker string) {
	r.flush()
	r.prefix = marker
	r.inList++
	r.walk(c)
	r.flush()
	r.prefix = "" // an empty item drops its bullet
	r.inList--
}

func (r *renderer) link(c *node) {
	text := collapse(inlineText(c))
	if text == "" {
		return
	}
	href := r.absolute(c.attrs["href"])
	if href == "" {
		r.para.WriteString(text)
		return
	}
	r.para.WriteString("[" + strings.ReplaceAll(text, "]", "\\]") + "](" + href + ")")
}

// absolute resolves href against the page URL. Only http, https and mailto
// links are kept; fragments and script URLs are dropped.
func (r *renderer) absolute(href string) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if r.base != nil {
		u = r.base.ResolveReference(u)
	}
	switch u.Scheme {
	case "http", "https", "mailto":
	default:
		return ""
	}
	return strings.NewReplacer(" ", "%20", "(", "%28", ")", "%29").Replace(u.String())
}

func codeSpan(t string) string {
	if strings.Contains(t, "`") {
		return "`` " + t + " ``"
	}
	return "`" + t + "`"
}

func (r *renderer) pre(n *node) {
	code := n
	lang := langFrom(n.attrs["class"])
	for _, k := range n.kids {
		if k.tag == "code" {
			code = k
			if l := langFrom(k.attrs["class"]); l != "" {
				lang = l
			}
		}
	}
	var b strings.Builder
	rawText(code, &b)
	text := strings.ReplaceAll(b.String(), "\r\n", "\n")
	text = strings.Trim(text, "\n")
	fence := fenceFor(text)
	r.out.WriteString(fence + lang + "\n" + text + "\n" + fence + "\n\n")
}

func langFrom(class string) string {
	for _, f := range strings.Fields(class) {
		for _, p := range []string{"language-", "lang-"} {
			if strings.HasPrefix(f, p) {
				return f[len(p):]
			}
		}
	}
	return ""
}

// fenceFor returns a backtick fence longer than any run inside text.
func fenceFor(text string) string {
	longest, run := 0, 0
	for _, ch := range text {
		if ch == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	if longest < 3 {
		return "```"
	}
	return strings.Repeat("`", longest+1)
}

func (r *renderer) quote(c *node) {
	sub := &renderer{base: r.base}
	sub.walk(c)
	sub.flush()
	body := strings.TrimRight(sub.out.String(), "\n")
	if body == "" {
		return
	}
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if l == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + l
		}
	}
	r.out.WriteString(strings.Join(lines, "\n") + "\n\n")
}

func (r *renderer) table(n *node) {
	rows := tableRows(n)
	if len(rows) == 0 {
		return
	}
	cols := 0
	for _, row := range rows {
		cols = max(cols, len(row))
	}
	for i := range rows {
		for len(rows[i]) < cols {
			rows[i] = append(rows[i], "")
		}
	}
	writeRow := func(row []string) {
		r.out.WriteString("| " + strings.Join(row, " | ") + " |\n")
	}
	writeRow(rows[0])
	r.out.WriteString("|" + strings.Repeat(" --- |", cols) + "\n")
	for _, row := range rows[1:] {
		writeRow(row)
	}
	r.out.WriteString("\n")
}

// tableRows collects the cells of each row. Nested tables are skipped.
func tableRows(n *node) [][]string {
	var rows [][]string
	var visit func(x *node)
	visit = func(x *node) {
		for _, c := range x.kids {
			if c.tag == "" || dropped[c.tag] || c.tag == "table" {
				continue
			}
			if c.tag == "tr" {
				var row []string
				for _, cell := range c.kids {
					if cell.tag == "td" || cell.tag == "th" {
						row = append(row, strings.ReplaceAll(collapse(inlineText(cell)), "|", "\\|"))
					}
				}
				if len(row) > 0 {
					rows = append(rows, row)
				}
				continue
			}
			visit(c)
		}
	}
	visit(n)
	return rows
}

// tidy trims trailing spaces, keeps at most one blank line between blocks,
// and leaves fenced code untouched.
func tidy(s string) string {
	var out []string
	inFence := false
	blank := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			out = append(out, strings.TrimRight(line, " \t\r"))
			blank = 0
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}
		line = strings.TrimRight(line, " \t\r")
		if line == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
