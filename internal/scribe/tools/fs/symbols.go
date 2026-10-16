package fs

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// maxSymbolBytes caps the files symbol lookup will read whole. Larger files
// can still be read with lines=.
const maxSymbolBytes = 4 << 20

// symbolSpan is one definition found in a file.
type symbolSpan struct {
	Name, Kind string
	Start, End int // 1-based inclusive
}

// findSymbol locates the definition of name in one file, optionally only
// those of kind (a word such as "function", "class" or "type"; see
// kindGroup). The code map answers first when the session has one and it
// knows this file. Otherwise a regex over common definition forms is used.
// name may be qualified as "Type.Method"; the qualifier is matched against
// the code map's parent only. t.abs is empty for a web source, which has no
// map entry.
func findSymbol(env *tools.Env, t target, src []byte, name, kind string) (symbolSpan, error) {
	parent, base := splitQualified(name)
	if base == "" {
		return symbolSpan{}, fmt.Errorf("symbol must name a function, type or other definition")
	}
	if t.abs != "" {
		if syms, known := mapSymbols(env, t.abs); known {
			if hits := filterKind(matchMapSymbols(syms, parent, base), kind); len(hits) > 0 {
				return pickSymbol(hits, name, t.rel)
			}
		}
	}
	lines := strings.Split(string(src), "\n")
	all := regexSymbols(lines, base)
	if parent != "" {
		// Without a map, the qualifier can only be checked against the
		// definition line itself: a method's receiver names its type.
		all = keepOwned(all, lines, parent)
	}
	hits := filterKind(all, kind)
	if len(hits) == 0 && kind != "" && len(all) > 0 {
		return symbolSpan{}, kindMismatch(all, name, kind, t.rel)
	}
	return pickSymbol(hits, name, t.rel)
}

// keepOwned keeps the spans whose definition line names owner as a whole
// word, such as "func (s *Server) Run" for owner Server.
func keepOwned(hits []symbolSpan, lines []string, owner string) []symbolSpan {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(owner) + `\b`)
	var out []symbolSpan
	for _, h := range hits {
		if h.Start-1 < len(lines) && re.MatchString(lines[h.Start-1]) {
			out = append(out, h)
		}
	}
	return out
}

// kindMismatch explains that name exists in the file, but not as kind.
func kindMismatch(all []symbolSpan, name, kind, rel string) error {
	seen := map[string]bool{}
	var kinds []string
	for _, h := range all {
		if g := kindGroup(h.Kind); !seen[g] {
			seen[g] = true
			kinds = append(kinds, g)
		}
	}
	return fmt.Errorf("%q in %s is not a %s. It is a %s. Retry with target set to one of those, or drop target.",
		name, rel, kind, strings.Join(kinds, " or "))
}

// filterKind keeps the spans whose kind group is kind. An empty kind keeps all.
func filterKind(hits []symbolSpan, kind string) []symbolSpan {
	want := kindGroup(kind)
	if want == "" {
		return hits
	}
	var out []symbolSpan
	for _, h := range hits {
		if kindGroup(h.Kind) == want {
			out = append(out, h)
		}
	}
	return out
}

// kindGroup maps the words a model or the code map uses for a definition to
// one group name. Methods are functions, and "struct" is a type, because a
// Go struct is declared as "type X struct" and the regex fallback reports it
// as a type. An unknown word maps to itself, so it matches only that word.
func kindGroup(k string) string {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "":
		return ""
	case "func", "function", "fn", "def", "method", "methods", "constructor", "procedure", "callable", "arrow", "arrow_function", "arrowfunction":
		return "function"
	case "class":
		return "class"
	case "type", "struct", "typealias", "type_alias", "alias", "union":
		return "type"
	case "interface", "protocol":
		return "interface"
	case "enum":
		return "enum"
	case "trait":
		return "trait"
	case "var", "let", "const", "variable", "constant":
		return "variable"
	case "mod", "module", "namespace", "package":
		return "module"
	case "any", "symbol":
		return ""
	}
	return strings.ToLower(strings.TrimSpace(k))
}

// symbolKindWords are the words that may lead a symbol argument, as in
// "function Run", so a model that merges kind and name still resolves.
var symbolKindWords = map[string]bool{
	"func": true, "function": true, "fn": true, "def": true, "method": true,
	"class": true, "struct": true, "type": true, "interface": true, "enum": true,
	"trait": true, "var": true, "let": true, "const": true, "module": true,
	"namespace": true, "constructor": true,
}

// pickSymbol returns the only candidate, or explains why there is none or too
// many.
func pickSymbol(hits []symbolSpan, name, rel string) (symbolSpan, error) {
	switch len(hits) {
	case 0:
		return symbolSpan{}, fmt.Errorf("No symbol named %q in %s. Search with grep for the name, or read the file with lines=.", name, rel)
	case 1:
		return hits[0], nil
	}
	parts := make([]string, len(hits))
	for i, h := range hits {
		parts[i] = fmt.Sprintf("%s %s at lines %d-%d", h.Kind, h.Name, h.Start, h.End)
	}
	return symbolSpan{}, fmt.Errorf("%d definitions match %q in %s: %s. Read the one you need with lines=START-END, or add target (the kind) to pick one.",
		len(hits), name, rel, strings.Join(parts, "; "))
}

// splitQualified splits "Type.Method" into its qualifier and base name. A
// name without a dot has no qualifier.
func splitQualified(name string) (parent, base string) {
	name = strings.TrimSpace(name)
	if i := strings.LastIndex(name, "."); i >= 0 {
		return strings.TrimSpace(name[:i]), strings.TrimSpace(name[i+1:])
	}
	return "", name
}

// matchMapSymbols takes the code map's symbols for one file. An exact name
// match wins; a case-insensitive match is used only when there is none.
func matchMapSymbols(syms []repomap.Symbol, parent, base string) []symbolSpan {
	for _, exact := range []bool{true, false} {
		var out []symbolSpan
		for _, s := range syms {
			if parent != "" && !strings.EqualFold(s.Parent, parent) {
				continue
			}
			if exact && s.Name != base || !exact && !strings.EqualFold(s.Name, base) {
				continue
			}
			end := s.EndLine
			if end < s.Line {
				end = s.Line
			}
			out = append(out, symbolSpan{Name: s.Name, Kind: s.Kind, Start: s.Line, End: end})
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// defTemplates are definition forms for common languages. NAME is replaced by
// the quoted symbol name. They are anchored to the start of a line, so a bare
// call on its own line is not a definition.
var defTemplates = []string{
	// Go: func, func (r T) Name, and type/var/const declarations.
	`^\s*func\s+(?:\([^)]*\)\s*)?NAME\s*[(\[]`,
	// JavaScript and TypeScript.
	`^\s*(?:export\s+)?(?:default\s+)?(?:declare\s+)?(?:async\s+)?function\b\s*\*?\s*NAME\b`,
	`^\s*(?:export\s+)?(?:default\s+)?(?:declare\s+)?(?:abstract\s+)?class\s+NAME\b`,
	`^\s*(?:export\s+)?(?:declare\s+)?(?:const|let|var)\s+NAME\b`,
	`^\s*(?:export\s+)?(?:declare\s+)?(?:interface|type|enum|namespace)\s+NAME\b`,
	// Rust.
	`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?(?:unsafe\s+)?(?:const\s+)?fn\s+NAME\b`,
	`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:struct|enum|trait|union|mod)\s+NAME\b`,
	// Go and others: type and var/const declarations.
	`^\s*(?:type|var|const)\s+NAME\b`,
	// Python.
	`^\s*(?:async\s+)?def\s+NAME\s*\(`,
	`^\s*class\s+NAME\b`,
	// Methods in C-like languages: a return type, then the name, then an open paren.
	`^\s*(?:(?:public|private|protected|internal|static|final|abstract|override|virtual|async|get|set)\s+)*[\w<>\[\],.?*&:]+\s+NAME\s*\([^;]*$`,
	// Methods in JavaScript classes and object literals: name(...) {.
	`^\s*(?:(?:public|private|protected|static|async|get|set)\s+)*NAME\s*\([^;]*\)\s*(?::[^{;]*)?\{\s*$`,
}

// nonTypeWords start a line that is a statement, not a definition, when they
// appear where a return type would be (for example "return f(x," ).
var nonTypeWords = map[string]bool{
	"return": true, "else": true, "new": true, "await": true, "throw": true,
	"yield": true, "case": true, "if": true, "while": true, "for": true,
	"switch": true, "do": true, "go": true, "defer": true,
}

// regexSymbols finds definitions of base in lines.
func regexSymbols(lines []string, base string) []symbolSpan {
	q := regexp.QuoteMeta(base) + `\b`
	var res []*regexp.Regexp
	for _, tpl := range defTemplates {
		res = append(res, regexp.MustCompile(strings.ReplaceAll(tpl, "NAME", q)))
	}
	var out []symbolSpan
	seen := map[int]bool{}
	for i, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		if seen[i] || !anyMatch(res, line) {
			continue
		}
		if first := strings.Fields(line); len(first) > 0 && nonTypeWords[first[0]] {
			continue
		}
		seen[i] = true
		kind := definitionKind(line)
		end := blockEnd(lines, i)
		out = append(out, symbolSpan{Name: base, Kind: kind, Start: i + 1, End: end + 1})
	}
	return out
}

func anyMatch(res []*regexp.Regexp, line string) bool {
	for _, re := range res {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

// definitionKind names what a definition line declares, for the header: the
// first declaring keyword on the line ("type" in "type Server struct {").
// A line with none is a method, as in "render(a) {".
func definitionKind(line string) string {
	kinds := map[string]bool{
		"func": true, "function": true, "def": true, "fn": true, "class": true,
		"struct": true, "interface": true, "trait": true, "enum": true, "type": true,
		"const": true, "let": true, "var": true, "mod": true, "union": true, "namespace": true,
	}
	for _, w := range strings.Fields(line) {
		if kinds[w] {
			return w
		}
	}
	return "method"
}

// blockEnd returns the 0-based index of the last line of the definition that
// starts on line i. A brace-delimited body ends where its braces balance; an
// indented body (Python) ends before the next line that is not indented past
// the definition. Anything else is a one-line definition.
func blockEnd(lines []string, i int) int {
	head := strings.TrimSpace(stripLineComment(lines[i]))
	if strings.HasSuffix(head, ":") && !strings.Contains(head, "{") {
		return indentedEnd(lines, i)
	}
	j := i
	for {
		l := stripLineComment(lines[j])
		if k := strings.IndexByte(l, '{'); k >= 0 {
			return braceEnd(lines, j, k)
		}
		if strings.HasSuffix(strings.TrimSpace(l), ";") || j-i >= 2 {
			return i
		}
		// The signature continues on the next line only after a token that
		// cannot end a declaration.
		if !continues(l) || j+1 >= len(lines) {
			return i
		}
		j++
	}
}

// continues reports whether a declaration line is unfinished, so its body or
// the rest of its signature is on the next line.
func continues(l string) bool {
	t := strings.TrimSpace(l)
	for _, suf := range []string{",", "(", "\\", "=", "->", ")"} {
		if strings.HasSuffix(t, suf) {
			return true
		}
	}
	return false
}

// braceEnd finds the line where the brace opened at lines[j][k] closes.
// String literals and // comments are skipped; this is a line-level scan, not
// a parser, so unusual literals can confuse it. Unterminated blocks run to
// the end of the file.
func braceEnd(lines []string, j, k int) int {
	depth := 0
	for li := j; li < len(lines); li++ {
		s := lines[li]
		start := 0
		if li == j {
			start = k
		}
		var quote byte
		for c := start; c < len(s); c++ {
			ch := s[c]
			if quote != 0 {
				switch {
				case ch == '\\':
					c++
				case ch == quote:
					quote = 0
				}
				continue
			}
			switch ch {
			case '"', '\'', '`':
				quote = ch
			case '/':
				if c+1 < len(s) && s[c+1] == '/' {
					c = len(s)
				}
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return li
				}
			}
		}
	}
	return len(lines) - 1
}

// indentedEnd is the last line of a Python-style block that starts on line i.
func indentedEnd(lines []string, i int) int {
	base := indentWidth(lines[i])
	end := i
	for k := i + 1; k < len(lines); k++ {
		if strings.TrimSpace(lines[k]) == "" {
			continue
		}
		if indentWidth(lines[k]) <= base {
			break
		}
		end = k
	}
	return end
}

func indentWidth(s string) int {
	return len(s) - len(strings.TrimLeft(s, " \t"))
}

// stripLineComment drops a trailing // comment. It does not look for string
// literals, so it can cut a line inside a string that contains "//".
func stripLineComment(s string) string {
	if i := strings.Index(s, "//"); i >= 0 {
		return s[:i]
	}
	return s
}
