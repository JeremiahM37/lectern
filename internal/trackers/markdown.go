package trackers

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Rich text for editing: the editor speaks Markdown everywhere, and these
// convert it to what each tracker stores — Atlassian Document Format on Jira
// Cloud, wiki markup on Jira Server/Data Center (Linear stores Markdown). The
// reverse conversions feed the editor. Round trips keep paragraphs, headings,
// lists (nested), quotes, code, rules, bold/italic/code/strike and links;
// anything else in an ADF document is reported by ADFLossy so the page can
// warn before an edit would drop it.

// ---- Markdown blocks -------------------------------------------------------

type mdBlock struct {
	kind     string // p | h | code | quote | hr | ul | ol
	level    int
	lang     string
	text     string
	lines    []string
	items    []mdItem
	children []mdBlock
}

type mdItem struct {
	text   string
	nested []mdBlock
}

var (
	mdHeadingRe = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	mdBulletRe  = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	mdOrderedRe = regexp.MustCompile(`^(\s*)\d+[.)]\s+(.*)$`)
	mdRuleRe    = regexp.MustCompile(`^\s*(?:(?:-\s*){3,}|(?:\*\s*){3,}|(?:_\s*){3,})$`)
)

func indentOf(s string) int { return len(s) - len(strings.TrimLeft(s, " \t")) }

func parseMarkdown(md string) []mdBlock {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var out []mdBlock
	var para []string
	flush := func() {
		if len(para) > 0 {
			out = append(out, mdBlock{kind: "p", lines: para})
			para = nil
		}
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trim := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trim, "```"):
			flush()
			b := mdBlock{kind: "code", lang: strings.TrimSpace(strings.TrimPrefix(trim, "```"))}
			var body []string
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
				body = append(body, lines[i])
			}
			b.text = strings.Join(body, "\n")
			out = append(out, b)
		case trim == "":
			flush()
		case mdHeadingRe.MatchString(trim) && indentOf(line) < 4:
			flush()
			m := mdHeadingRe.FindStringSubmatch(trim)
			out = append(out, mdBlock{kind: "h", level: len(m[1]), text: strings.TrimRight(m[2], " #")})
		case mdRuleRe.MatchString(line):
			flush()
			out = append(out, mdBlock{kind: "hr"})
		case strings.HasPrefix(trim, ">"):
			flush()
			var q []string
			for ; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), ">"); i++ {
				q = append(q, strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(lines[i]), ">"), " "))
			}
			i--
			out = append(out, mdBlock{kind: "quote", children: parseMarkdown(strings.Join(q, "\n"))})
		case mdBulletRe.MatchString(line) || mdOrderedRe.MatchString(line):
			flush()
			var list []string
			base := indentOf(line)
			ordered := !mdBulletRe.MatchString(line)
			sameKind := func(l string) bool {
				if indentOf(l) != base {
					return false
				}
				if ordered {
					return mdOrderedRe.MatchString(l) && !mdBulletRe.MatchString(l)
				}
				return mdBulletRe.MatchString(l)
			}
			for ; i < len(lines); i++ {
				l := lines[i]
				if strings.TrimSpace(l) == "" {
					// a blank line ends the list unless the next line continues it
					if i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != "" && (indentOf(lines[i+1]) > base || sameKind(lines[i+1])) {
						continue
					}
					break
				}
				if indentOf(l) < base || (indentOf(l) == base && !sameKind(l)) {
					break
				}
				list = append(list, l)
			}
			i--
			out = append(out, parseList(list))
		default:
			para = append(para, trim)
		}
	}
	flush()
	return out
}

// parseList reads one list whose first line sets the indentation; deeper
// lines belong to the item above them and are parsed recursively.
func parseList(lines []string) mdBlock {
	base := indentOf(lines[0])
	b := mdBlock{kind: "ul"}
	if mdOrderedRe.MatchString(lines[0]) && !mdBulletRe.MatchString(lines[0]) {
		b.kind = "ol"
	}
	var cur *mdItem
	var sub []string
	finish := func() {
		if cur == nil {
			return
		}
		if len(sub) > 0 {
			cur.nested = parseMarkdown(dedent(sub))
		}
		b.items = append(b.items, *cur)
		cur, sub = nil, nil
	}
	for _, l := range lines {
		if indentOf(l) == base {
			if m := mdBulletRe.FindStringSubmatch(l); m != nil {
				finish()
				cur = &mdItem{text: m[2]}
				continue
			}
			if m := mdOrderedRe.FindStringSubmatch(l); m != nil {
				finish()
				cur = &mdItem{text: m[2]}
				continue
			}
		}
		sub = append(sub, l)
	}
	finish()
	return b
}

func dedent(lines []string) string {
	min := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if n := indentOf(l); min < 0 || n < min {
			min = n
		}
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		if len(l) >= min && min > 0 {
			out[i] = l[min:]
		} else {
			out[i] = strings.TrimLeft(l, " \t")
		}
	}
	return strings.Join(out, "\n")
}

// ---- Markdown inline -------------------------------------------------------

type mdSpan struct {
	text  string
	marks []string // strong | em | code | strike | link:<url>
}

var mdInlineRe = regexp.MustCompile("`([^`]+)`|\\*\\*(.+?)\\*\\*|__(.+?)__|~~(.+?)~~|\\[([^\\]]+)\\]\\(([^)\\s]+)\\)|\\*([^*\\s][^*]*?)\\*|(?:^|\\b)_([^_\\s][^_]*?)_(?:\\b|$)")

func parseInline(s string, marks []string) []mdSpan {
	var out []mdSpan
	add := func(text string, m []string) {
		if text != "" {
			out = append(out, mdSpan{text: text, marks: m})
		}
	}
	with := func(m string) []string { return append(append([]string{}, marks...), m) }
	for s != "" {
		loc := mdInlineRe.FindStringSubmatchIndex(s)
		if loc == nil {
			add(s, marks)
			break
		}
		add(s[:loc[0]], marks)
		g := func(i int) (string, bool) {
			if loc[2*i] < 0 {
				return "", false
			}
			return s[loc[2*i]:loc[2*i+1]], true
		}
		if t, ok := g(1); ok {
			add(t, with("code"))
		} else if t, ok := g(2); ok {
			out = append(out, parseInline(t, with("strong"))...)
		} else if t, ok := g(3); ok {
			out = append(out, parseInline(t, with("strong"))...)
		} else if t, ok := g(4); ok {
			out = append(out, parseInline(t, with("strike"))...)
		} else if t, ok := g(5); ok {
			u, _ := g(6)
			out = append(out, parseInline(t, with("link:"+u))...)
		} else if t, ok := g(7); ok {
			out = append(out, parseInline(t, with("em"))...)
		} else if t, ok := g(8); ok {
			out = append(out, parseInline(t, with("em"))...)
		}
		s = s[loc[1]:]
	}
	return out
}

// ---- Markdown → ADF --------------------------------------------------------

// MarkdownToADF converts Markdown to an Atlassian Document Format document.
func MarkdownToADF(md string) map[string]any {
	content := adfBlocks(parseMarkdown(md))
	if len(content) == 0 {
		content = []any{map[string]any{"type": "paragraph", "content": []any{}}}
	}
	return map[string]any{"type": "doc", "version": 1, "content": content}
}

func adfBlocks(blocks []mdBlock) []any {
	out := []any{}
	for _, b := range blocks {
		switch b.kind {
		case "p":
			var inl []any
			for i, l := range b.lines {
				if i > 0 {
					inl = append(inl, map[string]any{"type": "hardBreak"})
				}
				inl = append(inl, adfInline(l)...)
			}
			out = append(out, map[string]any{"type": "paragraph", "content": inl})
		case "h":
			out = append(out, map[string]any{"type": "heading", "attrs": map[string]any{"level": b.level}, "content": adfInline(b.text)})
		case "code":
			n := map[string]any{"type": "codeBlock", "content": []any{}}
			if b.text != "" {
				n["content"] = []any{map[string]any{"type": "text", "text": b.text}}
			}
			if b.lang != "" {
				n["attrs"] = map[string]any{"language": b.lang}
			}
			out = append(out, n)
		case "quote":
			out = append(out, map[string]any{"type": "blockquote", "content": adfBlocks(b.children)})
		case "hr":
			out = append(out, map[string]any{"type": "rule"})
		case "ul", "ol":
			t := "bulletList"
			if b.kind == "ol" {
				t = "orderedList"
			}
			var items []any
			for _, it := range b.items {
				c := []any{map[string]any{"type": "paragraph", "content": adfInline(it.text)}}
				c = append(c, adfBlocks(it.nested)...)
				items = append(items, map[string]any{"type": "listItem", "content": c})
			}
			out = append(out, map[string]any{"type": t, "content": items})
		}
	}
	return out
}

func adfInline(s string) []any {
	out := []any{}
	for _, sp := range parseInline(s, nil) {
		n := map[string]any{"type": "text", "text": sp.text}
		var marks []any
		for _, m := range sp.marks {
			if u, ok := strings.CutPrefix(m, "link:"); ok {
				marks = append(marks, map[string]any{"type": "link", "attrs": map[string]any{"href": u}})
			} else {
				marks = append(marks, map[string]any{"type": m})
			}
		}
		if len(marks) > 0 {
			n["marks"] = marks
		}
		out = append(out, n)
	}
	return out
}

// ---- ADF → Markdown --------------------------------------------------------

var adfKnownNodes = map[string]bool{"doc": true, "paragraph": true, "text": true, "hardBreak": true, "heading": true,
	"bulletList": true, "orderedList": true, "listItem": true, "codeBlock": true, "blockquote": true, "rule": true,
	"mention": true, "emoji": true, "inlineCard": true}
var adfKnownMarks = map[string]bool{"strong": true, "em": true, "code": true, "strike": true, "link": true}

type adfMark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs"`
}

// ADFLossy reports whether editing a document as Markdown and saving it
// would drop something (a table, panel, media, colour, …).
func ADFLossy(raw json.RawMessage) bool {
	var doc adfNode
	if json.Unmarshal(raw, &doc) != nil || doc.Type == "" {
		return false
	}
	var walk func(n adfNode) bool
	walk = func(n adfNode) bool {
		if !adfKnownNodes[n.Type] {
			return true
		}
		for _, m := range n.Marks {
			if !adfKnownMarks[m.Type] {
				return true
			}
		}
		for _, c := range n.Content {
			if walk(c) {
				return true
			}
		}
		return false
	}
	return walk(doc)
}

func adfMarked(n adfNode) string {
	t := n.Text
	for _, m := range n.Marks {
		switch m.Type {
		case "strong":
			t = "**" + t + "**"
		case "em":
			t = "*" + t + "*"
		case "code":
			t = "`" + t + "`"
		case "strike":
			t = "~~" + t + "~~"
		}
	}
	for _, m := range n.Marks {
		if m.Type == "link" {
			if href, _ := m.Attrs["href"].(string); href != "" {
				t = "[" + t + "](" + href + ")"
			}
		}
	}
	return t
}

// ---- Markdown ⇄ Jira wiki markup ------------------------------------------

// MarkdownToWiki converts Markdown to Jira Server/Data Center wiki markup.
func MarkdownToWiki(md string) string {
	var b strings.Builder
	wikiBlocks(&b, parseMarkdown(md), "")
	return strings.TrimRight(b.String(), "\n")
}

func wikiBlocks(b *strings.Builder, blocks []mdBlock, listPrefix string) {
	for _, bl := range blocks {
		switch bl.kind {
		case "p":
			for i, l := range bl.lines {
				if i > 0 {
					b.WriteString("\n")
				}
				b.WriteString(wikiInline(l))
			}
			b.WriteString("\n\n")
		case "h":
			fmt.Fprintf(b, "h%d. %s\n\n", bl.level, wikiInline(bl.text))
		case "code":
			if bl.lang != "" {
				fmt.Fprintf(b, "{code:%s}\n%s\n{code}\n\n", bl.lang, bl.text)
			} else {
				fmt.Fprintf(b, "{code}\n%s\n{code}\n\n", bl.text)
			}
		case "quote":
			var inner strings.Builder
			wikiBlocks(&inner, bl.children, "")
			fmt.Fprintf(b, "{quote}\n%s\n{quote}\n\n", strings.TrimSpace(inner.String()))
		case "hr":
			b.WriteString("----\n\n")
		case "ul", "ol":
			mark := "*"
			if bl.kind == "ol" {
				mark = "#"
			}
			prefix := listPrefix + mark
			for _, it := range bl.items {
				fmt.Fprintf(b, "%s %s\n", prefix, wikiInline(it.text))
				for _, n := range it.nested {
					if n.kind == "ul" || n.kind == "ol" {
						wikiBlocks(b, []mdBlock{n}, prefix)
					}
				}
			}
			if listPrefix == "" {
				b.WriteString("\n")
			}
		}
	}
}

func wikiInline(s string) string {
	var b strings.Builder
	for _, sp := range parseInline(s, nil) {
		t := sp.text
		link := ""
		for _, m := range sp.marks {
			switch m {
			case "strong":
				t = "*" + t + "*"
			case "em":
				t = "_" + t + "_"
			case "code":
				t = "{{" + t + "}}"
			case "strike":
				t = "-" + t + "-"
			default:
				link, _ = strings.CutPrefix(m, "link:")
			}
		}
		if link != "" {
			t = "[" + t + "|" + link + "]"
		}
		b.WriteString(t)
	}
	return b.String()
}

var (
	wikiHeadingRe = regexp.MustCompile(`^h([1-6])\.\s+(.*)$`)
	wikiListRe    = regexp.MustCompile(`^([*#-]+)\s+(.*)$`)
	wikiCodeRe    = regexp.MustCompile(`^\{(code|noformat)(?::([^}]*))?\}$`)
)

// WikiToMarkdown converts Jira wiki markup to Markdown for display and
// editing. It covers what MarkdownToWiki writes and the common rest.
func WikiToMarkdown(w string) string {
	lines := strings.Split(strings.ReplaceAll(w, "\r\n", "\n"), "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if m := wikiCodeRe.FindStringSubmatch(t); m != nil {
			lang := ""
			if m[1] == "code" {
				lang = strings.Split(m[2], "|")[0]
			}
			out = append(out, "```"+lang)
			for i++; i < len(lines) && !wikiCodeRe.MatchString(strings.TrimSpace(lines[i])); i++ {
				out = append(out, lines[i])
			}
			out = append(out, "```")
			continue
		}
		switch {
		case t == "{quote}":
			var q []string
			for i++; i < len(lines) && strings.TrimSpace(lines[i]) != "{quote}"; i++ {
				q = append(q, "> "+wikiInlineToMarkdown(lines[i]))
			}
			out = append(out, q...)
		case strings.HasPrefix(t, "bq. "):
			out = append(out, "> "+wikiInlineToMarkdown(strings.TrimPrefix(t, "bq. ")))
		case t == "----":
			out = append(out, "---")
		case wikiHeadingRe.MatchString(t):
			m := wikiHeadingRe.FindStringSubmatch(t)
			n, _ := strconv.Atoi(m[1])
			out = append(out, strings.Repeat("#", n)+" "+wikiInlineToMarkdown(m[2]))
		case wikiListRe.MatchString(t) && !strings.HasPrefix(t, "----"):
			m := wikiListRe.FindStringSubmatch(t)
			depth := len(m[1]) - 1
			mark := "-"
			if strings.HasSuffix(m[1], "#") {
				mark = "1."
			}
			out = append(out, strings.Repeat("  ", depth)+mark+" "+wikiInlineToMarkdown(m[2]))
		default:
			out = append(out, wikiInlineToMarkdown(lines[i]))
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

var (
	wikiLinkRe   = regexp.MustCompile(`\[([^|\]]+)\|([^\]]+)\]`)
	wikiCodeIRe  = regexp.MustCompile(`\{\{([^}]+)\}\}`)
	wikiBoldRe   = regexp.MustCompile(`(^|[\s(])\*([^*\s][^*]*?)\*([\s).,;:!?]|$)`)
	wikiItalicRe = regexp.MustCompile(`(^|[\s(])_([^_\s][^_]*?)_([\s).,;:!?]|$)`)
	wikiStrikeRe = regexp.MustCompile(`(^|[\s(])-([^-\s][^-]*?)-([\s).,;:!?]|$)`)
)

func wikiInlineToMarkdown(s string) string {
	s = wikiCodeIRe.ReplaceAllString(s, "`$1`")
	s = wikiLinkRe.ReplaceAllString(s, "[$1]($2)")
	s = wikiBoldRe.ReplaceAllString(s, "$1**$2**$3")
	s = wikiItalicRe.ReplaceAllString(s, "$1*$2*$3")
	s = wikiStrikeRe.ReplaceAllString(s, "$1~~$2~~$3")
	return s
}
