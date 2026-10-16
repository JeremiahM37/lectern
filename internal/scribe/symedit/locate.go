package symedit

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
)

// symbol is one definition located in a file. start and end are byte offsets
// of its first and last lines (line-aligned, end before the newline).
type symbol struct {
	name, parent, kind string
	startLine, endLine int
	start, end         int
	hasDoc             bool     // a doc comment or annotation block is included
	shared             []string // other names declared by the same spec (var a, b = ...)
	bodyOpen           int      // Go only: exact offsets of the body braces, -1 if none
	bodyClose          int
}

func (s symbol) qual() string {
	if s.parent != "" {
		return s.parent + "." + s.name
	}
	return s.name
}

// spec is a parsed symbol reference: "class Foo", "Server.Start", "ParseList".
type spec struct {
	kind, parent, name string
}

// kindWords are the keywords a symbol reference may start with, and the
// definition kinds each one accepts.
var kindWords = map[string][]string{
	"func":      {"function", "method"},
	"function":  {"function", "method"},
	"fn":        {"function", "method"},
	"def":       {"function", "method"},
	"method":    {"method"},
	"class":     {"class", "struct"},
	"struct":    {"struct", "class"},
	"interface": {"interface", "trait"},
	"trait":     {"trait", "interface"},
	"type":      {"type", "struct", "interface", "class", "enum"},
	"enum":      {"enum"},
	"const":     {"const"},
	"var":       {"var", "variable", "const"},
}

func parseSpec(s string) spec {
	var sp spec
	s = strings.TrimSpace(s)
	if f := strings.Fields(s); len(f) == 2 {
		if _, ok := kindWords[f[0]]; ok {
			sp.kind, s = f[0], f[1]
		}
	}
	s = strings.NewReplacer("(*", "", "(", "", ")", "", "*", "", "::", ".", "#", ".").Replace(s)
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "."); i >= 0 {
		sp.parent, sp.name = strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
	} else {
		sp.name = s
	}
	return sp
}

func (sp spec) qual() string {
	if sp.parent != "" {
		return sp.parent + "." + sp.name
	}
	return sp.name
}

func kindMatches(want, got string) bool {
	for _, k := range kindWords[want] {
		if k == got {
			return true
		}
	}
	return false
}

// findSymbol resolves a reference to exactly one definition. Ambiguous
// references list their candidates; unknown ones suggest the closest names.
func findSymbol(syms []symbol, sp spec, rel, raw string) (symbol, error) {
	var hits []symbol
	for _, s := range syms {
		if s.name != sp.name || (sp.parent != "" && s.parent != sp.parent) {
			continue
		}
		if sp.kind != "" && !kindMatches(sp.kind, s.kind) {
			continue
		}
		hits = append(hits, s)
	}
	switch {
	case len(hits) == 1:
		return hits[0], nil
	case len(hits) > 1:
		var parts []string
		for _, h := range hits {
			parts = append(parts, fmt.Sprintf("%s (%s, lines %d-%d)", h.qual(), h.kind, h.startLine, h.endLine))
		}
		return symbol{}, fmt.Errorf("%q matches %d definitions in %s: %s. Name the parent (Parent.%s) or add a kind (func, type, const, var) to pick one",
			raw, len(hits), rel, strings.Join(parts, "; "), sp.name)
	}
	if len(syms) == 0 {
		return symbol{}, fmt.Errorf("no definitions found in %s, so %q cannot be located. Use edit_file for this file", rel, raw)
	}
	type cand struct {
		s symbol
		d int
	}
	want := strings.ToLower(sp.qual())
	cands := make([]cand, 0, len(syms))
	for _, s := range syms {
		cands = append(cands, cand{s, levenshtein(want, strings.ToLower(s.qual()))})
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].d < cands[j].d })
	var near []string
	for i := 0; i < len(cands) && i < 3; i++ {
		c := cands[i].s
		near = append(near, fmt.Sprintf("%s (%s, line %d)", c.qual(), c.kind, c.startLine))
	}
	return symbol{}, fmt.Errorf("no definition named %q in %s. Closest: %s", raw, rel, strings.Join(near, "; "))
}

// goSymbols locates Go declarations with go/parser. A declaration's range
// starts at its doc comment.
func goSymbols(src string) ([]symbol, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "file.go", src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("the file does not parse as Go (%s). Fix the syntax first, with edit_file", firstParseError(err))
	}
	tf := fset.File(f.Pos())
	idx := newLineIndex(src)
	off := func(p token.Pos) int { return tf.Offset(p) }
	mk := func(name, parent, kind string, doc *ast.CommentGroup, pos, end token.Pos, shared []string, bo, bc int) symbol {
		first := pos
		if doc != nil {
			first = doc.Pos()
		}
		so := idx.start(idx.lineOf(off(first)))
		eo := idx.end(idx.lineOf(off(end) - 1))
		return symbol{
			name: name, parent: parent, kind: kind,
			startLine: idx.lineOf(so), endLine: idx.lineOf(eo),
			start: so, end: eo, hasDoc: doc != nil, shared: shared,
			bodyOpen: bo, bodyClose: bc,
		}
	}
	var out []symbol
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			kind, parent := "function", ""
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind, parent = "method", recvTypeName(d.Recv.List[0].Type)
			}
			bo, bc := -1, -1
			if d.Body != nil {
				bo, bc = off(d.Body.Lbrace), off(d.Body.Rbrace)
			}
			out = append(out, mk(d.Name.Name, parent, kind, d.Doc, d.Pos(), d.End(), nil, bo, bc))
		case *ast.GenDecl:
			if d.Tok != token.TYPE && d.Tok != token.CONST && d.Tok != token.VAR {
				continue
			}
			grouped := d.Lparen.IsValid()
			for _, sp := range d.Specs {
				doc, pos, end := d.Doc, d.Pos(), d.End()
				var names []*ast.Ident
				var kind string
				switch sp := sp.(type) {
				case *ast.TypeSpec:
					kind = "type"
					switch sp.Type.(type) {
					case *ast.StructType:
						kind = "struct"
					case *ast.InterfaceType:
						kind = "interface"
					}
					names = []*ast.Ident{sp.Name}
					if grouped {
						doc, pos, end = sp.Doc, sp.Pos(), sp.End()
					}
				case *ast.ValueSpec:
					kind = "var"
					if d.Tok == token.CONST {
						kind = "const"
					}
					names = sp.Names
					if grouped {
						doc, pos, end = sp.Doc, sp.Pos(), sp.End()
					}
				default:
					continue
				}
				for _, id := range names {
					if id.Name == "_" {
						continue
					}
					var others []string
					for _, o := range names {
						if o != id && o.Name != "_" {
							others = append(others, o.Name)
						}
					}
					out = append(out, mk(id.Name, "", kind, doc, pos, end, others, -1, -1))
				}
			}
		}
	}
	return out, nil
}

func firstParseError(err error) string {
	if el, ok := err.(scanner.ErrorList); ok && len(el) > 0 {
		return fmt.Sprintf("line %d: %s", el[0].Pos.Line, el[0].Msg)
	}
	return err.Error()
}

func recvTypeName(e ast.Expr) string {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}

// extractor runs repomap's extractors on in-memory text. repomap parses files
// from disk under a root, so each call writes the text to a scratch file and
// reads the symbols back. One scratch map serves a whole tool call.
type extractor struct {
	dir string
	m   *repomap.Map
}

func (x *extractor) symbols(base, text string) ([]repomap.Symbol, error) {
	if x.m == nil {
		dir, err := os.MkdirTemp("", "scribe-symedit-*")
		if err != nil {
			return nil, err
		}
		m, err := repomap.Open(dir, repomap.Options{InMemory: true})
		if err != nil {
			_ = os.RemoveAll(dir)
			return nil, err
		}
		x.dir, x.m = dir, m
	}
	if err := os.WriteFile(filepath.Join(x.dir, base), []byte(text), 0o644); err != nil {
		return nil, err
	}
	if err := x.m.Update([]string{base}); err != nil {
		return nil, err
	}
	syms, ok := x.m.FileSymbols(base)
	if !ok {
		return nil, fmt.Errorf("no symbol extraction for %s. Use edit_file for this file", base)
	}
	return syms, nil
}

func (x *extractor) close() {
	if x.m != nil {
		_ = x.m.Close()
		_ = os.RemoveAll(x.dir)
		x.m = nil
	}
}

// genericSymbols converts repomap symbols to byte ranges. Non-Python
// definitions take the comment and annotation lines directly above them.
func genericSymbols(x *extractor, base string, fam family, src string) ([]symbol, error) {
	rs, err := x.symbols(base, src)
	if err != nil {
		return nil, err
	}
	idx := newLineIndex(src)
	out := make([]symbol, 0, len(rs))
	for _, r := range rs {
		name, parent := r.Name, r.Parent
		if parent == "" {
			if i := strings.LastIndex(name, "::"); i >= 0 {
				parent, name = name[:i], name[i+2:]
			} else if i := strings.LastIndex(name, "."); i >= 0 {
				parent, name = name[:i], name[i+1:]
			}
		}
		start, hasDoc := r.Line, false
		if fam != famPy {
			for start > 1 && isLeadingComment(idx.text(start-1)) {
				start--
				hasDoc = true
			}
		}
		end := max(r.EndLine, r.Line)
		out = append(out, symbol{
			name: name, parent: parent, kind: r.Kind,
			startLine: start, endLine: end,
			start: idx.start(start), end: idx.end(end),
			hasDoc: hasDoc, bodyOpen: -1, bodyClose: -1,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].startLine < out[j].startLine })
	return out, nil
}

// braceBody finds the { and matching } that open a body in [start, end) of
// masked text. A brace inside parentheses does not open the body.
func braceBody(masked string, start, end int) (int, int, bool) {
	open, paren := -1, 0
	for i := start; i < end && open < 0; i++ {
		switch masked[i] {
		case '(', '[':
			paren++
		case ')', ']':
			paren--
		case '{':
			if paren <= 0 {
				open = i
			}
		}
	}
	if open < 0 {
		return -1, -1, false
	}
	depth := 0
	for i := open; i < end; i++ {
		switch masked[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return open, i, true
			}
		}
	}
	return -1, -1, false
}

// pyHeaderEnd returns the offset of the newline that ends a Python def or
// class header, the logical line whose last code character is ':'. Decorator
// lines may come first. Any other statement ends the search.
func pyHeaderEnd(masked string, start, end int) (int, bool) {
	depth := 0
	var last, first byte
	for i := start; i < end; i++ {
		c := masked[i]
		if c == '\n' {
			if depth == 0 {
				if last == ':' {
					return i, true
				}
				if first != 0 && first != '@' {
					return -1, false
				}
				first, last = 0, 0
			}
			continue
		}
		if c == ' ' || c == '\t' || c == '\r' {
			continue
		}
		if depth == 0 && first == 0 {
			first = c
		}
		switch c {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		}
		last = c
	}
	if depth == 0 && last == ':' {
		return end, true
	}
	return -1, false
}

// stmtEnd returns the last line of the statement that starts on line ln,
// following brackets across lines.
func stmtEnd(idx lineIndex, ln int) int {
	depth := 0
	for k := ln; k <= idx.count(); k++ {
		for _, c := range idx.text(k) {
			switch c {
			case '(', '[', '{':
				depth++
			case ')', ']', '}':
				depth--
			}
		}
		if depth <= 0 {
			return k
		}
	}
	return idx.count()
}
