package symedit

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
)

var goImportRe = regexp.MustCompile("^(?:(\\.|_|[A-Za-z][A-Za-z0-9_]*)\\s+)?(\"[^\"]+\"|`[^`]+`)$")

// addImport adds one import line unless the file already has it. code is the
// import itself: "fmt", `alias "path"`, `import "fmt"` for Go; a full
// statement for TypeScript/JavaScript and Python.
func (e *editor) addImport(text, code string) (string, string, error) {
	want := strings.TrimSpace(code)
	switch e.lang.fam {
	case famGo:
		return e.goAddImport(text, want)
	case famPy:
		return e.lineAddImport(text, want, true)
	case famBrace:
		if e.lang.name == "typescript" || e.lang.name == "javascript" {
			return e.lineAddImport(text, want, false)
		}
	}
	return "", "", fmt.Errorf("add_import supports Go, TypeScript/JavaScript and Python, not %s", e.lang.name)
}

func (e *editor) goAddImport(text, code string) (string, string, error) {
	s := strings.TrimSpace(strings.TrimPrefix(code, "import"))
	m := goImportRe.FindStringSubmatch(s)
	if m == nil {
		return "", "", fmt.Errorf("new_code must be a Go import such as \"fmt\" or alias \"example.com/pkg\"")
	}
	path, err := strconv.Unquote(m[2])
	if err != nil {
		return "", "", fmt.Errorf("invalid import path %s", m[2])
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, e.rel, text, parser.ParseComments)
	if err != nil {
		return "", "", fmt.Errorf("the file does not parse as Go (%s). Fix the syntax first, with edit_file", firstParseError(err))
	}
	for _, im := range f.Imports {
		if p, _ := strconv.Unquote(im.Path.Value); p == path {
			return text, fmt.Sprintf("already present: %s is already imported", path), nil
		}
	}
	tf := fset.File(f.Pos())
	idx := newLineIndex(text)
	for _, d := range f.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT && gd.Lparen.IsValid() {
			pos := idx.start(tf.Line(gd.Rparen))
			return text[:pos] + "\t" + s + "\n" + text[pos:], "added import " + s, nil
		}
	}
	last := 0
	for _, d := range f.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
			last = tf.Line(gd.End())
		}
	}
	if last > 0 {
		return insertLineAt(text, idx.start(last+1), "import "+s), "added import " + s, nil
	}
	pl := tf.Line(f.Name.End())
	return insertLineAt(text, idx.start(pl+1), "\nimport "+s), "added import " + s, nil
}

// lineAddImport handles TypeScript/JavaScript and Python, whose imports are
// statements that may span lines. The new line goes after the last top-level
// import.
func (e *editor) lineAddImport(text, want string, python bool) (string, string, error) {
	norm := func(s string) string { return strings.TrimSuffix(strings.TrimSpace(s), ";") }
	idx := newLineIndex(text)
	for ln := 1; ln <= idx.count(); ln++ {
		if norm(idx.text(ln)) == norm(want) {
			return text, fmt.Sprintf("already present: %s is already imported", want), nil
		}
	}
	masked := newLineIndex(mask(text, e.lang.lex))
	last := 0
	for ln := 1; ln <= masked.count(); ln++ {
		t := masked.text(ln)
		if !isTopLevelImport(t, python) {
			continue
		}
		last = stmtEnd(masked, ln)
		ln = last
	}
	if last > 0 {
		return insertLineAt(text, idx.start(last+1), want), "added import " + want, nil
	}
	if isBlank(text) {
		return want + "\n", "added import " + want, nil
	}
	return want + "\n\n" + text, "added import " + want, nil
}

func isTopLevelImport(line string, python bool) bool {
	if python {
		return strings.HasPrefix(line, "import ") || strings.HasPrefix(line, "from ")
	}
	for _, p := range []string{"import ", "import{", "import*", "import\""} {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return strings.HasPrefix(line, "import '")
}
