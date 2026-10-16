package symedit

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Module handling for TypeScript and JavaScript: which file a relative
// specifier names, how to spell a specifier for a new place (keeping the
// extension style the file already uses), and the import and re-export
// statements themselves.

var tsModExts = []string{".d.ts", ".tsx", ".ts", ".mts", ".cts", ".jsx", ".js", ".mjs", ".cjs"}

var (
	jsStyleExt = map[string]bool{".js": true, ".jsx": true, ".mjs": true, ".cjs": true}
	tsStyleExt = map[string]bool{".ts": true, ".tsx": true, ".mts": true, ".cts": true}
	tsToJS     = map[string]string{".ts": ".js", ".tsx": ".jsx", ".mts": ".mjs", ".cts": ".cjs"}
)

// tsSpecRe finds a module specifier in from, import, require or import(...).
// Group 1 is the quote, group 2 the specifier.
var tsSpecRe = regexp.MustCompile(`\b(?:from|import|require)\s*\(?\s*(['"])([^'"\n]+)['"]`)

// tsStmtRe finds import and export-from statements. Group 1 is the keyword,
// 2 the "type" marker, 3 the clause, 4 the quote and 5 the specifier.
var tsStmtRe = regexp.MustCompile(`\b(import|export)\s+(type\s+)?([^;'"]*?)\s*\bfrom\s*(['"])([^'"\n]+)['"]\s*;?`)

// tsModKey names the module a project path refers to: no extension and no
// trailing /index, so "./util", "./util.js" and "./util/index" agree.
func tsModKey(p string) string {
	for _, e := range tsModExts {
		if strings.HasSuffix(p, e) {
			p = strings.TrimSuffix(p, e)
			break
		}
	}
	return strings.TrimSuffix(path.Clean(p), "/index")
}

func isRelSpec(s string) bool {
	return s == "." || s == ".." || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../")
}

// tsResolve is the module key a specifier in importerRel names, or "" when
// the specifier is not relative.
func tsResolve(importerRel, spec string) string {
	if !isRelSpec(spec) {
		return ""
	}
	return tsModKey(path.Join(path.Dir(importerRel), spec))
}

func tsExtOf(p string) string {
	for _, e := range tsModExts {
		if strings.HasSuffix(p, e) {
			return e
		}
	}
	return ""
}

// tsRespec writes the specifier a file in importerDir uses to reach target,
// a project path. orig is the specifier it replaces: an extension it wrote
// is kept, in its JS or TS spelling, and one it left off stays off.
func tsRespec(importerDir, orig, target string) string {
	stem := relOf(importerDir, target)
	ext := tsExtOf(stem)
	base := strings.TrimSuffix(stem, ext)
	var out string
	switch oe := path.Ext(orig); {
	case jsStyleExt[oe]:
		out = base + jsSpelling(ext)
	case tsStyleExt[oe]:
		out = base + ext
	default:
		out = strings.TrimSuffix(base, "/index")
	}
	if !isRelSpec(out) {
		out = "./" + out
	}
	return out
}

func jsSpelling(ext string) string {
	if j, ok := tsToJS[ext]; ok {
		return j
	}
	return ext
}

// specRef is a module specifier in source text: its byte range and value.
type specRef struct {
	start, end int
	spec       string
}

// codeSpecs lists the module specifiers in src that are code, not comment
// or string text. masked is mask(src) of the same text.
func codeSpecs(src, masked string) []specRef {
	var out []specRef
	for _, m := range tsSpecRe.FindAllStringSubmatchIndex(src, -1) {
		if masked[m[0]] != src[m[0]] {
			continue
		}
		out = append(out, specRef{m[4], m[5], src[m[4]:m[5]]})
	}
	return out
}

// clause is the part of an import between the keyword and "from".
type clause struct {
	def   string
	ns    string
	star  bool // export * from
	named []binding
}

// binding is one name in braces: "Name" or "Name as Alias".
type binding struct{ name, alias string }

func (b binding) local() string {
	if b.alias != "" {
		return b.alias
	}
	return b.name
}

func (b binding) text() string {
	if b.alias != "" {
		return b.name + " as " + b.alias
	}
	return b.name
}

// parseClause splits an import clause. It returns false for shapes this
// file does not rewrite, such as inline "type" modifiers.
func parseClause(s string) (clause, bool) {
	var c clause
	s = strings.TrimSpace(s)
	if s == "*" {
		c.star = true
		return c, true
	}
	if i := strings.IndexByte(s, '{'); i >= 0 {
		j := strings.LastIndexByte(s, '}')
		if j < i {
			return c, false
		}
		for _, part := range strings.Split(s[i+1:j], ",") {
			f := strings.Fields(part)
			switch {
			case len(f) == 0:
			case len(f) == 1:
				c.named = append(c.named, binding{name: f[0]})
			case len(f) == 3 && f[1] == "as":
				c.named = append(c.named, binding{name: f[0], alias: f[2]})
			default:
				return c, false
			}
		}
		s = s[:i] + "," + s[j+1:]
	}
	for _, part := range strings.Split(s, ",") {
		p := strings.TrimSpace(part)
		switch {
		case p == "":
		case strings.HasPrefix(p, "*"):
			f := strings.Fields(p)
			if len(f) != 3 || f[1] != "as" {
				return c, false
			}
			c.ns = f[2]
		case strings.ContainsAny(p, " \t\n"):
			return c, false
		default:
			c.def = p
		}
	}
	return c, true
}

// empty reports whether the clause names nothing.
func (c clause) empty() bool {
	return c.def == "" && c.ns == "" && !c.star && len(c.named) == 0
}

func (c clause) render() string {
	if c.star {
		return "*"
	}
	var parts []string
	if c.def != "" {
		parts = append(parts, c.def)
	}
	if c.ns != "" {
		parts = append(parts, "* as "+c.ns)
	}
	if len(c.named) > 0 {
		var n []string
		for _, b := range c.named {
			n = append(n, b.text())
		}
		parts = append(parts, "{ "+strings.Join(n, ", ")+" }")
	}
	return strings.Join(parts, ", ")
}

// stmtText writes an import or export-from statement.
func stmtText(kw string, typ bool, c clause, quote, spec string, semi bool) string {
	t := ""
	if typ {
		t = "type "
	}
	s := fmt.Sprintf("%s %s%s from %s%s%s", kw, t, c.render(), quote, spec, quote)
	if semi {
		s += ";"
	}
	return s
}

// stmtFn decides how to rewrite one statement. It returns the replacement
// (empty to delete the statement) and whether to replace it at all.
type stmtFn func(kw string, typ bool, c clause, quote, spec string, semi bool) (string, bool)

// editStatements rewrites the import and export-from statements of src that
// fn changes. Statements inside comments or strings are left alone.
func editStatements(src string, lx lexer, fn stmtFn) string {
	masked := mask(src, lx)
	var edits []textEdit
	for _, m := range tsStmtRe.FindAllStringSubmatchIndex(src, -1) {
		if masked[m[0]] != src[m[0]] {
			continue
		}
		c, ok := parseClause(src[m[6]:m[7]])
		if !ok {
			continue
		}
		typ := m[4] >= 0
		repl, changed := fn(src[m[2]:m[3]], typ, c, src[m[8]:m[9]], src[m[10]:m[11]], strings.HasSuffix(src[m[0]:m[1]], ";"))
		if !changed {
			continue
		}
		end := m[1]
		if repl == "" && end < len(src) && src[end] == '\n' {
			end++
		}
		edits = append(edits, textEdit{m[0], end, repl})
	}
	return applyEdits(src, edits)
}

// withoutStatements returns masked with the import and export-from
// statements blanked, so that counting remaining uses ignores the imports.
func withoutStatements(src string, lx lexer) string {
	masked := []byte(mask(src, lx))
	for _, m := range tsStmtRe.FindAllStringSubmatchIndex(src, -1) {
		for i := m[0]; i < m[1]; i++ {
			if masked[i] != '\n' {
				masked[i] = ' '
			}
		}
	}
	return string(masked)
}

// dropUnusedBindings removes import bindings that no code in src still uses.
func dropUnusedBindings(src string, lx lexer) string {
	rest := withoutStatements(src, lx)
	used := func(name string) bool { return len(wordHits(rest, name)) > 0 }
	return editStatements(src, lx, func(kw string, typ bool, c clause, quote, spec string, semi bool) (string, bool) {
		if kw != "import" {
			return "", false
		}
		keep := c
		changed := false
		if c.def != "" && !used(c.def) {
			keep.def, changed = "", true
		}
		if c.ns != "" && !used(c.ns) {
			keep.ns, changed = "", true
		}
		keep.named = nil
		for _, b := range c.named {
			if used(b.local()) {
				keep.named = append(keep.named, b)
			} else {
				changed = true
			}
		}
		if !changed {
			return "", false
		}
		if keep.empty() {
			return "", true
		}
		return stmtText(kw, typ, keep, quote, spec, semi), true
	})
}
