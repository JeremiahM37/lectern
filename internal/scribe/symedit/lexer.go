package symedit

import (
	"fmt"
	"strings"
)

// family says how a language delimits a definition's body.
type family int

const (
	famGo    family = iota
	famPy           // indentation-delimited body under a ':' header
	famBrace        // { } delimited body
	famEnd          // body closed by an "end" line (Ruby, Lua, Elixir)
)

// lexer describes how a language writes comments and strings, so that mask
// can blank everything that is not code.
type lexer struct {
	line       []string // line comment markers
	blockOpen  string
	blockClose string
	quotes     string   // single-line string quote characters; ` may span lines
	triples    []string // multi-line string delimiters
}

type language struct {
	name string
	fam  family
	lex  lexer
}

var (
	cLexer    = lexer{line: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: `"'`}
	jsLexer   = lexer{line: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: "\"'`"}
	pyLexer   = lexer{line: []string{"#"}, quotes: `"'`, triples: []string{`"""`, `'''`}}
	javaLexer = lexer{line: []string{"//"}, blockOpen: "/*", blockClose: "*/", quotes: `"'`, triples: []string{`"""`}}
)

// languages maps a file extension to its language.
var languages = map[string]*language{
	".go":    {"go", famGo, jsLexer},
	".ts":    {"typescript", famBrace, jsLexer},
	".tsx":   {"typescript", famBrace, jsLexer},
	".mts":   {"typescript", famBrace, jsLexer},
	".cts":   {"typescript", famBrace, jsLexer},
	".js":    {"javascript", famBrace, jsLexer},
	".jsx":   {"javascript", famBrace, jsLexer},
	".mjs":   {"javascript", famBrace, jsLexer},
	".cjs":   {"javascript", famBrace, jsLexer},
	".py":    {"python", famPy, pyLexer},
	".rs":    {"rust", famBrace, cLexer},
	".java":  {"java", famBrace, javaLexer},
	".kt":    {"kotlin", famBrace, javaLexer},
	".kts":   {"kotlin", famBrace, javaLexer},
	".cs":    {"csharp", famBrace, cLexer},
	".swift": {"swift", famBrace, javaLexer},
	".c":     {"c", famBrace, cLexer},
	".h":     {"c", famBrace, cLexer},
	".cc":    {"cpp", famBrace, cLexer},
	".cpp":   {"cpp", famBrace, cLexer},
	".cxx":   {"cpp", famBrace, cLexer},
	".hpp":   {"cpp", famBrace, cLexer},
	".hh":    {"cpp", famBrace, cLexer},
	".hxx":   {"cpp", famBrace, cLexer},
	".php":   {"php", famBrace, lexer{line: []string{"//", "#"}, blockOpen: "/*", blockClose: "*/", quotes: `"'`}},
	".zig":   {"zig", famBrace, lexer{line: []string{"//"}, quotes: `"'`}},
	".rb":    {"ruby", famEnd, lexer{line: []string{"#"}, quotes: `"'`}},
	".lua":   {"lua", famEnd, lexer{line: []string{"--"}, blockOpen: "--[[", blockClose: "]]", quotes: `"'`}},
	".ex":    {"elixir", famEnd, lexer{line: []string{"#"}, quotes: `"'`, triples: []string{`"""`}}},
	".exs":   {"elixir", famEnd, lexer{line: []string{"#"}, quotes: `"'`, triples: []string{`"""`}}},
}

// mask returns src with comments and string contents replaced by spaces.
// Newlines are kept and the byte length is unchanged, so offsets and line
// numbers in the masked text match src.
func mask(src string, lx lexer) string {
	b := []byte(src)
	n := len(b)
	blank := func(from, to int) {
		for k := from; k < to && k < n; k++ {
			if b[k] != '\n' && b[k] != '\r' {
				b[k] = ' '
			}
		}
	}
	for i := 0; i < n; {
		if lx.blockOpen != "" && strings.HasPrefix(src[i:], lx.blockOpen) {
			end := n
			if j := strings.Index(src[i+len(lx.blockOpen):], lx.blockClose); j >= 0 {
				end = i + len(lx.blockOpen) + j + len(lx.blockClose)
			}
			blank(i, end)
			i = end
			continue
		}
		if hasAnyPrefix(src[i:], lx.line) {
			end := strings.IndexByte(src[i:], '\n')
			if end < 0 {
				end = n
			} else {
				end += i
			}
			blank(i, end)
			i = end
			continue
		}
		if tq := hasAnyPrefixString(src[i:], lx.triples); tq != "" {
			end := n
			if j := strings.Index(src[i+len(tq):], tq); j >= 0 {
				end = i + len(tq) + j + len(tq)
			}
			blank(i+len(tq), end-len(tq))
			i = end
			continue
		}
		if q := src[i]; strings.IndexByte(lx.quotes, q) >= 0 {
			j := i + 1
			for j < n {
				c := src[j]
				if c == '\\' {
					j += 2
					continue
				}
				if c == q || (c == '\n' && q != '`') {
					break
				}
				j++
			}
			if j > n {
				j = n
			}
			blank(i+1, j)
			if j < n && src[j] == q {
				i = j + 1
			} else {
				i = j
			}
			continue
		}
		i++
	}
	return string(b)
}

func hasAnyPrefix(s string, prefixes []string) bool {
	return hasAnyPrefixString(s, prefixes) != ""
}

func hasAnyPrefixString(s string, prefixes []string) string {
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(s, p) {
			return p
		}
	}
	return ""
}

// balance checks that (), [] and {} nest in masked text. It returns the line
// and a message for the first problem, or "" when the text is balanced.
func balance(masked string) (int, string) {
	type open struct {
		ch   byte
		line int
	}
	pairs := map[byte]byte{')': '(', ']': '[', '}': '{'}
	var st []open
	line := 1
	for i := 0; i < len(masked); i++ {
		c := masked[i]
		switch c {
		case '\n':
			line++
		case '(', '[', '{':
			st = append(st, open{c, line})
		case ')', ']', '}':
			if len(st) == 0 || st[len(st)-1].ch != pairs[c] {
				return line, fmt.Sprintf("unexpected %q", c)
			}
			st = st[:len(st)-1]
		}
	}
	if len(st) > 0 {
		o := st[len(st)-1]
		return o.line, fmt.Sprintf("%q opened on line %d is never closed", o.ch, o.line)
	}
	return 0, ""
}
