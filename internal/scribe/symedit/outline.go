package symedit

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Definition is one definition found without a language server. It is the
// fallback tier for definition lookups and outlines.
type Definition struct {
	Name      string
	Parent    string // enclosing type or class, empty at top level
	Kind      string
	Line      int // 1-based line of the name
	Column    int // 1-based column of the name, in characters
	StartLine int // first line, including a doc comment when there is one
	EndLine   int // last line of the definition

	found bool
}

// Outline lists the definitions in text. rel is the file's path, used to pick
// the language, and must have an extension Scribe can parse. Files over 1 MB
// are refused, as the symbol tools refuse them.
func Outline(rel, text string) ([]Definition, error) {
	lang, ok := languages[strings.ToLower(filepath.Ext(rel))]
	if !ok {
		return nil, fmt.Errorf("no outline for %s", rel)
	}
	ed := &editor{lang: lang, rel: rel, base: filepath.Base(rel)}
	defer ed.close()
	syms, err := ed.symbols(text)
	if err != nil {
		return nil, err
	}
	idx := newLineIndex(text)
	out := make([]Definition, 0, len(syms))
	for _, s := range syms {
		d := Definition{Name: s.name, Parent: s.parent, Kind: s.kind, Line: s.startLine, Column: 1,
			StartLine: s.startLine, EndLine: s.endLine}
		// The name is on the first code line that holds it, not on a doc
		// comment above the definition that happens to mention it.
		// Code lines first; a line that starts with a comment but holds the
		// declaration (/* x */ function f) is the fallback.
		for pass := 0; pass < 2 && !d.found; pass++ {
			for ln := s.startLine; ln <= s.endLine && !d.found; ln++ {
				text := idx.text(ln)
				if pass == 0 && isCommentLine(text) {
					continue
				}
				if i := wordIndex(text, s.name); i >= 0 {
					d.Line, d.Column, d.found = ln, utf8.RuneCountInString(text[:i])+1, true
				}
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// isCommentLine reports whether a line is only a comment.
func isCommentLine(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "*")
}

// wordIndex is the byte offset of name as a whole identifier in line, or -1.
func wordIndex(line, name string) int {
	for i := 0; ; {
		j := strings.Index(line[i:], name)
		if j < 0 {
			return -1
		}
		p := i + j
		end := p + len(name)
		if (p == 0 || !isIdentByte(line[p-1])) && (end == len(line) || !isIdentByte(line[end])) {
			return p
		}
		i = end
	}
}
