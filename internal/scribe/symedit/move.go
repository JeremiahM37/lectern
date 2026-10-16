package symedit

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/fs"
)

const moveName = "move_symbol"

// moveReport is what a planned move did, for the result text.
type moveReport struct {
	symbol    string
	from, to  string
	importers []string
	added     []string // import statements written into the destination
	warnings  []string
}

type moveSymbolTool struct{}

func (t *moveSymbolTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: moveName,
		Description: "Move a top-level function, type, class or constant from one file to another in the same language, " +
			"and rewrite the imports that name it. Go moves within one package. TypeScript and JavaScript move between any " +
			"two files, and relative imports in the moved code are rebased. Nothing is type-checked: run project typecheck " +
			"or project build afterwards.",
		Schema: tools.Schema(map[string]any{
			"symbol": tools.Str("Name of the top-level definition to move, e.g. parseConfig or class Store."),
			"from":   tools.Str("File that defines the symbol, relative to the project root. Must be read first."),
			"to":     tools.Str("Destination file, relative to the project root. Created if it does not exist."),
		}, "symbol", "from", "to"),
	}
}

func (t *moveSymbolTool) ReadOnly() bool { return false }

func (t *moveSymbolTool) Run(ctx context.Context, env *tools.Env, raw json.RawMessage) (res tools.Result) {
	defer func() {
		if r := recover(); r != nil {
			res = tools.Errorf("%s failed unexpectedly: %v", moveName, r)
		}
	}()
	if env == nil {
		return tools.Errorf("%s: no environment", moveName)
	}
	var a struct {
		Symbol string `json:"symbol"`
		From   string `json:"from"`
		To     string `json:"to"`
	}
	if err := tools.Decode(raw, &a); err != nil {
		return fail(err)
	}
	if strings.TrimSpace(a.Symbol) == "" || a.From == "" || a.To == "" {
		return tools.Errorf("symbol, from and to are all required")
	}
	fromAbs, fromRel, err := gate(ctx, env, a.From)
	if err != nil {
		return fail(err)
	}
	toAbs, toRel, err := gate(ctx, env, a.To)
	if err != nil {
		return fail(err)
	}
	if fromRel == toRel {
		return tools.Errorf("from and to are the same file. %s moves code between files; use ast_edit to reorder within one file.", moveName)
	}
	if err := checkReadable(env, fromAbs, fromRel); err != nil {
		return fail(err)
	}
	if fi, err := os.Stat(toAbs); err == nil {
		if fi.IsDir() {
			return tools.Errorf("%s is a directory.", toRel)
		}
		if env.Files != nil && !env.Files.WasRead(toAbs) {
			return tools.Errorf("%s exists and has not been read this session. Read it first so the move is based on its current content.", toRel)
		}
	}
	fromLang, toLang := languages[extOf(fromRel)], languages[extOf(toRel)]
	if fromLang == nil || toLang == nil {
		return tools.Errorf("%s only moves code between source files it can parse. Check the file types of %s and %s.", moveName, fromRel, toRel)
	}

	var (
		writes []fileWrite
		rep    moveReport
	)
	switch {
	case isTSFamily(fromLang) && isTSFamily(toLang):
		writes, rep, err = planMoveTS(env.Workdir, a.Symbol, fromRel, toRel)
	case fromLang.name == "go" && toLang.name == "go":
		writes, rep, err = planMoveGo(env.Workdir, a.Symbol, fromRel, toRel)
	default:
		return tools.Errorf("%s supports Go and TypeScript/JavaScript. %s and %s are not both of those.", moveName, fromRel, toRel)
	}
	if err != nil {
		return tools.Errorf("%s", err.Error())
	}

	var diff strings.Builder
	var files []string
	for _, w := range writes {
		orig, _ := readProjectText(env.Workdir, w.rel)
		if !w.remove {
			d, _, _ := fs.UnifiedDiff(w.rel, orig, w.content)
			diff.WriteString(d)
		} else {
			d, _, _ := fs.UnifiedDiff(w.rel, orig, "")
			diff.WriteString(d)
		}
		files = append(files, w.rel)
	}
	if err := commitWrites(env, env.Workdir, writes); err != nil {
		return tools.Errorf("%s failed during write, and all changes were rolled back: %v", moveName, err)
	}
	return tools.Result{
		Output:  rep.text(),
		Summary: fmt.Sprintf("moved %s; %d file(s) changed", rep.symbol, len(files)),
		Diff:    diff.String(),
		Files:   files,
	}
}

func (r moveReport) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Moved '%s' from %s to %s.\n", r.symbol, r.from, r.to)
	if len(r.importers) == 0 {
		b.WriteString("No other file imported it.\n")
	} else {
		fmt.Fprintf(&b, "Imports rewritten in %d file(s): %s\n", len(r.importers), strings.Join(r.importers, ", "))
	}
	for _, s := range r.added {
		fmt.Fprintf(&b, "Added to %s: %s\n", r.to, s)
	}
	for _, w := range r.warnings {
		fmt.Fprintf(&b, "Check: %s\n", w)
	}
	b.WriteString("Not type-checked: run project typecheck or project build.")
	return b.String()
}

// checkReadable applies the read rules an edit needs: the file must exist
// and must have been read, unchanged, this session.
func checkReadable(env *tools.Env, abs, rel string) error {
	fi, err := os.Stat(abs)
	if os.IsNotExist(err) {
		return fmt.Errorf("File not found: %s. Check the path.", rel)
	}
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("%s is a directory.", rel)
	}
	if env.Files != nil {
		if !env.Files.WasRead(abs) {
			return fmt.Errorf("%s has not been read this session. Read it first so the move is based on its current content.", rel)
		}
		if env.Files.StaleSinceRead(abs) {
			return fmt.Errorf("%s changed on disk since you read it. Read it again, then retry.", rel)
		}
	}
	return nil
}

// --- TypeScript and JavaScript ---------------------------------------------

var (
	tsExportListRe = regexp.MustCompile(`\bexport\s*\{([^}]*)\}`)
	tsExportDefRe  = regexp.MustCompile(`\bexport\s+default\b`)
)

// isExportedDecl reports whether a masked declaration is exported inline.
// Decorator lines above the declaration are skipped.
func isExportedDecl(masked string) bool {
	for _, ln := range strings.Split(masked, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "@") {
			continue
		}
		return len(wordHits(t, "export")) > 0
	}
	return false
}

// tsTopDeclRe matches a top-level declaration; group 1 is "export " when it
// is exported and group 2 is the name.
var tsTopDeclRe = regexp.MustCompile(`(?m)^(export\s+)?(?:declare\s+)?(?:async\s+)?(?:const|let|var|function\*?|class|type|interface|enum|abstract\s+class)\s+([A-Za-z_$][\w$]*)`)

// topLevelNames maps each name declared at the top level of masked code to
// whether it is exported inline. Unexported constants are included, which
// the symbol extractor does not report.
func topLevelNames(masked string) map[string]bool {
	out := map[string]bool{}
	for _, m := range tsTopDeclRe.FindAllStringSubmatch(masked, -1) {
		out[m[2]] = out[m[2]] || m[1] != ""
	}
	return out
}

// exportedByList reports whether an export { ... } list in masked names name.
func exportedByList(masked, name string) bool {
	for _, m := range tsExportListRe.FindAllStringSubmatch(masked, -1) {
		if len(wordHits(m[1], name)) > 0 {
			return true
		}
	}
	return false
}

// firstCode is the offset of the first character that is not blank.
func firstCode(masked string) int {
	return len(masked) - len(strings.TrimLeft(masked, " \t\r\n"))
}

// rebaseSpecs rewrites every relative specifier in src through fn.
func rebaseSpecs(src string, lx lexer, fn func(spec string) string) string {
	masked := mask(src, lx)
	var edits []textEdit
	for _, r := range codeSpecs(src, masked) {
		if n := fn(r.spec); n != r.spec {
			edits = append(edits, textEdit{r.start, r.end, n})
		}
	}
	return applyEdits(src, edits)
}

// quoteFor is the quote character the file writes its specifiers with.
func quoteFor(src string) string {
	masked := mask(src, languages[".ts"].lex)
	for _, r := range codeSpecs(src, masked) {
		if r.start > 0 {
			return src[r.start-1 : r.start]
		}
	}
	return `"`
}

// semiFor reports whether the file ends its import statements with a semicolon.
func semiFor(src string) bool {
	if m := tsStmtRe.FindStringIndex(src); m != nil {
		return strings.HasSuffix(src[m[0]:m[1]], ";")
	}
	return true
}

// retargetImports rewrites the statements in src (the text of file rel) that
// import sym from fromRel. The name leaves its old statement. In every file
// except the destination toRel it is imported from toRel instead.
func retargetImports(rel, src, fromRel, toRel, sym string, lx lexer, warns *[]string) string {
	key := tsModKey(fromRel)
	return editStatements(src, lx, func(kw string, typ bool, c clause, quote, spec string, semi bool) (string, bool) {
		if !isRelSpec(spec) || tsResolve(rel, spec) != key {
			return "", false
		}
		if kw == "export" && c.star {
			*warns = append(*warns, fmt.Sprintf("%s re-exports everything from %s. Check that %s is still re-exported.", rel, fromRel, sym))
			return "", false
		}
		if kw == "import" && c.ns != "" {
			*warns = append(*warns, fmt.Sprintf("%s imports %s as %s. Uses of %s.%s must change to %s by hand.", rel, fromRel, c.ns, c.ns, sym, toRel))
		}
		var moved, keep []binding
		for _, b := range c.named {
			if b.name == sym {
				moved = append(moved, b)
			} else {
				keep = append(keep, b)
			}
		}
		if len(moved) == 0 {
			return "", false
		}
		rest := c
		rest.named = keep
		var out []string
		if !rest.empty() {
			out = append(out, stmtText(kw, typ, rest, quote, spec, semi))
		}
		if rel != toRel {
			out = append(out, stmtText(kw, typ, clause{named: moved}, quote, tsRespec(path.Dir(rel), spec, toRel), semi))
		}
		return strings.Join(out, "\n"), true
	})
}

func planMoveTS(workdir, sym, fromRel, toRel string) ([]fileWrite, moveReport, error) {
	rep := moveReport{from: fromRel, to: toRel}
	fromSrc, ok := readProjectText(workdir, fromRel)
	if !ok {
		return nil, rep, fmt.Errorf("cannot read %s as text", fromRel)
	}
	lx := languages[extOf(fromRel)].lex
	fromMasked := mask(fromSrc, lx)
	ed := &editor{lang: languages[extOf(fromRel)], rel: fromRel, base: path.Base(fromRel)}
	defer ed.close()
	syms, err := ed.symbols(fromSrc)
	if err != nil {
		return nil, rep, err
	}
	s, err := findSymbol(syms, parseSpec(sym), fromRel, sym)
	if err != nil {
		return nil, rep, err
	}
	rep.symbol = s.name
	if s.parent != "" {
		return nil, rep, fmt.Errorf("%s moves top-level definitions. %s is a member of %s; move the whole %s instead.", moveName, sym, s.parent, s.parent)
	}
	if len(s.shared) > 0 {
		return nil, rep, fmt.Errorf("%s is declared in the same statement as %s, and %s moves one declaration. Split the statement first.", s.name, strings.Join(s.shared, ", "), moveName)
	}
	declSrc := fromSrc[s.start:s.end]
	declMasked := fromMasked[s.start:s.end]
	if tsExportDefRe.MatchString(declMasked) {
		return nil, rep, fmt.Errorf("%s is a default export. %s moves named declarations only.", s.name, moveName)
	}
	exported := isExportedDecl(declMasked) || exportedByList(fromMasked, s.name)

	fromDir, toDir := path.Dir(fromRel), path.Dir(toRel)
	styleSpec := ""
	for _, r := range codeSpecs(fromSrc, fromMasked) {
		if isRelSpec(r.spec) {
			styleSpec = r.spec
			break
		}
	}
	quote, semi := quoteFor(fromSrc), semiFor(fromSrc)

	// The moved code needs whatever its names came from: top-level names
	// of this file, and the imports this file makes.
	var addStmts []string
	tops := topLevelNames(fromMasked)
	var topNames []string
	for n := range tops {
		topNames = append(topNames, n)
	}
	sort.Strings(topNames)
	for _, n := range topNames {
		if n == s.name || len(wordHits(declMasked, n)) == 0 {
			continue
		}
		if !tops[n] && !exportedByList(fromMasked, n) {
			return nil, rep, fmt.Errorf("the moved %s uses %s, which %s does not export. Export %s first, then move %s.", s.name, n, fromRel, n, s.name)
		}
		addStmts = append(addStmts, stmtText("import", false, clause{named: []binding{{name: n}}}, quote, tsRespec(toDir, styleSpec, fromRel), semi))
	}
	editStatements(fromSrc, lx, func(kw string, typ bool, c clause, q, spec string, st bool) (string, bool) {
		if kw != "import" {
			return "", false
		}
		target := spec
		if isRelSpec(spec) {
			target = tsRespec(toDir, spec, path.Join(fromDir, spec))
		}
		used := func(n string) bool { return n != "" && len(wordHits(declMasked, n)) > 0 }
		if used(c.def) {
			addStmts = append(addStmts, stmtText(kw, typ, clause{def: c.def}, q, target, st))
		}
		if used(c.ns) {
			addStmts = append(addStmts, stmtText(kw, typ, clause{ns: c.ns}, q, target, st))
		}
		for _, b := range c.named {
			if used(b.local()) {
				addStmts = append(addStmts, stmtText(kw, typ, clause{named: []binding{b}}, q, target, st))
			}
		}
		return "", false
	})

	// The moved declaration. Unexported code becomes exported, because the
	// source file imports it back from the destination.
	decl := declSrc
	if !exported {
		i := firstCode(declMasked)
		decl = declSrc[:i] + "export " + declSrc[i:]
	}
	decl = rebaseSpecs(decl, lx, func(spec string) string {
		if isRelSpec(spec) {
			return tsRespec(toDir, spec, path.Join(fromDir, spec))
		}
		return spec
	})

	// Destination: existing text with the name's old import dropped.
	toAbs := filepath.Join(workdir, filepath.FromSlash(toRel))
	toText := ""
	if _, err := os.Stat(toAbs); err == nil {
		toSrc, ok := readProjectText(workdir, toRel)
		if !ok {
			return nil, rep, fmt.Errorf("cannot read %s as text", toRel)
		}
		toEd := &editor{lang: languages[extOf(toRel)], rel: toRel, base: path.Base(toRel)}
		tsyms, err := toEd.symbols(toSrc)
		toEd.close()
		if err != nil {
			return nil, rep, err
		}
		for _, t := range tsyms {
			if t.parent == "" && t.name == s.name {
				return nil, rep, fmt.Errorf("%s already defines %s. Rename one of them, or move %s to a new file.", toRel, s.name, s.name)
			}
		}
		toText = retargetImports(toRel, toSrc, fromRel, toRel, s.name, languages[extOf(toRel)].lex, &rep.warnings)
	}
	if strings.TrimSpace(toText) != "" {
		toText = strings.TrimRight(toText, "\n") + "\n\n" + decl + "\n"
	} else {
		toText = decl + "\n"
	}
	toEd := &editor{lang: languages[extOf(toRel)], rel: toRel, base: path.Base(toRel)}
	for _, st := range addStmts {
		next, msg, err := toEd.addImport(toText, st)
		if err != nil {
			return nil, rep, err
		}
		if strings.HasPrefix(msg, "added") {
			rep.added = append(rep.added, strings.TrimPrefix(msg, "added import "))
		}
		toText = next
	}

	// Source: the declaration goes, and the name comes back as an import if
	// the rest of the file still uses it.
	fromOut, _, _, err := ed.apply(fromSrc, op{Action: "delete", Symbol: sym})
	if err != nil {
		return nil, rep, err
	}
	fromOut = dropUnusedBindings(fromOut, lx)
	if len(wordHits(withoutStatements(fromOut, lx), s.name)) > 0 {
		back := stmtText("import", false, clause{named: []binding{{name: s.name}}}, quote, tsRespec(fromDir, styleSpec, toRel), semi)
		if fromOut, _, err = ed.addImport(fromOut, back); err != nil {
			return nil, rep, err
		}
	}

	writes := []fileWrite{{rel: toRel, content: toText}}
	// Importers of the source file, in every other TypeScript or JavaScript file.
	key := tsModKey(fromRel)
	for _, rel := range projectFiles(workdir) {
		if rel == fromRel || rel == toRel || !isTSFamily(languages[extOf(rel)]) {
			continue
		}
		src, ok := readProjectText(workdir, rel)
		if !ok || !strings.Contains(src, s.name) {
			continue
		}
		lxr := languages[extOf(rel)].lex
		out := retargetImports(rel, src, fromRel, toRel, s.name, lxr, &rep.warnings)
		for _, r := range codeSpecs(src, mask(src, lxr)) {
			if !isRelSpec(r.spec) && (tsModKey(r.spec) == key || strings.HasSuffix(tsModKey(r.spec), "/"+key)) {
				rep.warnings = append(rep.warnings, fmt.Sprintf("%s imports %q by a non-relative path, which was not checked. Update it by hand if it means %s.", rel, r.spec, fromRel))
			}
		}
		if out != src {
			writes = append(writes, fileWrite{rel: rel, content: out})
			rep.importers = append(rep.importers, rel)
		}
	}
	writes = append(writes, fileWrite{rel: fromRel, content: fromOut})
	return writes, rep, nil
}

// --- Go ----------------------------------------------------------------------

func goImportName(im *ast.ImportSpec) string {
	if im.Name != nil {
		if im.Name.Name == "_" || im.Name.Name == "." {
			return ""
		}
		return im.Name.Name
	}
	p, err := strconv.Unquote(im.Path.Value)
	if err != nil {
		return ""
	}
	parts := strings.Split(p, "/")
	name := parts[len(parts)-1]
	if regexp.MustCompile(`^v[0-9]+$`).MatchString(name) && len(parts) > 1 {
		name = parts[len(parts)-2]
	}
	return strings.TrimPrefix(name, "go-")
}

// goNameUsed reports whether masked code uses name as a package qualifier.
// An empty name counts as used, so blank and dot imports are kept.
func goNameUsed(masked, name string) bool {
	if name == "" {
		return true
	}
	for _, off := range wordHits(masked, name) {
		if end := off + len(name); end < len(masked) && masked[end] == '.' {
			return true
		}
	}
	return false
}

// removeUnusedGoImports deletes import specs whose package no code uses.
func removeUnusedGoImports(src string) (string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return "", err
	}
	tf := fset.File(f.Pos())
	masked := mask(src, languages[".go"].lex)
	drop := map[int]bool{}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.IMPORT {
			continue
		}
		kept := 0
		for _, sp := range g.Specs {
			im := sp.(*ast.ImportSpec)
			if goNameUsed(masked, goImportName(im)) {
				kept++
				continue
			}
			for ln := tf.Line(im.Pos()); ln <= tf.Line(im.End()); ln++ {
				drop[ln] = true
			}
		}
		if kept == 0 {
			for ln := tf.Line(g.Pos()); ln <= tf.Line(g.End()); ln++ {
				drop[ln] = true
			}
		}
	}
	if len(drop) == 0 {
		return src, nil
	}
	lines := strings.Split(src, "\n")
	var out []string
	for i, l := range lines {
		if !drop[i+1] {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n"), nil
}

// goTopNames lists the package-level names a Go source declares.
func goTopNames(src string) (map[string]bool, error) {
	syms, err := goSymbols(src)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, s := range syms {
		if s.parent == "" {
			names[s.name] = true
		}
	}
	return names, nil
}

func planMoveGo(workdir, sym, fromRel, toRel string) ([]fileWrite, moveReport, error) {
	rep := moveReport{from: fromRel, to: toRel}
	if path.Dir(fromRel) != path.Dir(toRel) {
		return nil, rep, fmt.Errorf("%s cannot move a Go symbol to another package (%s is in %s, %s is in %s). Callers in other packages would need package-qualified rewrites, which this tool does not do. Move it within the package, or edit both files by hand.",
			moveName, fromRel, path.Dir(fromRel), toRel, path.Dir(toRel))
	}
	fromSrc, ok := readProjectText(workdir, fromRel)
	if !ok {
		return nil, rep, fmt.Errorf("cannot read %s as text", fromRel)
	}
	goLang := languages[".go"]
	fromMasked := mask(fromSrc, goLang.lex)
	ed := &editor{lang: goLang, rel: fromRel, base: path.Base(fromRel)}
	defer ed.close()
	syms, err := ed.symbols(fromSrc)
	if err != nil {
		return nil, rep, err
	}
	s, err := findSymbol(syms, parseSpec(sym), fromRel, sym)
	if err != nil {
		return nil, rep, err
	}
	rep.symbol = s.name
	if s.parent != "" {
		return nil, rep, fmt.Errorf("%s moves top-level declarations. %s is a method of %s; move the type instead.", moveName, sym, s.parent)
	}
	if len(s.shared) > 0 {
		return nil, rep, fmt.Errorf("%s is declared in the same statement as %s, and %s moves one declaration. Split the statement first.", s.name, strings.Join(s.shared, ", "), moveName)
	}
	declSrc := fromSrc[s.start:s.end]
	declMasked := fromMasked[s.start:s.end]

	fromFile, err := parser.ParseFile(token.NewFileSet(), fromRel, fromSrc, parser.ParseComments)
	if err != nil {
		return nil, rep, err
	}
	pkg := fromFile.Name.Name

	toAbs := filepath.Join(workdir, filepath.FromSlash(toRel))
	toSrc := ""
	toExists := false
	if _, err := os.Stat(toAbs); err == nil {
		toExists = true
		if toSrc, ok = readProjectText(workdir, toRel); !ok {
			return nil, rep, fmt.Errorf("cannot read %s as text", toRel)
		}
		tf, err := parser.ParseFile(token.NewFileSet(), toRel, toSrc, parser.ParseComments)
		if err != nil {
			return nil, rep, fmt.Errorf("%s does not parse as Go: %v", toRel, err)
		}
		if tf.Name.Name != pkg {
			return nil, rep, fmt.Errorf("%s is in package %s but %s is in package %s", toRel, tf.Name.Name, fromRel, pkg)
		}
		names, err := goTopNames(toSrc)
		if err != nil {
			return nil, rep, err
		}
		if names[s.name] {
			return nil, rep, fmt.Errorf("%s already declares %s. Rename one of them, or move %s to a new file.", toRel, s.name, s.name)
		}
	}

	// Imports the declaration uses, to be added to the destination.
	var needed []string
	for _, sp := range fromFile.Imports {
		name := goImportName(sp)
		if !goNameUsed(declMasked, name) {
			continue
		}
		code := sp.Path.Value
		if sp.Name != nil {
			code = sp.Name.Name + " " + code
		}
		needed = append(needed, code)
	}

	// The source keeps its imports only while code uses them.
	fromOut, _, _, err := ed.apply(fromSrc, op{Action: "delete", Symbol: sym})
	if err != nil {
		return nil, rep, err
	}
	if fromOut, err = removeUnusedGoImports(fromOut); err != nil {
		return nil, rep, err
	}
	if fromOut, err = formatGo(fromRel, fromOut); err != nil {
		return nil, rep, err
	}

	var toText string
	if toExists {
		toText = strings.TrimRight(toSrc, "\n") + "\n\n" + declSrc + "\n"
	} else {
		toText = "package " + pkg + "\n\n" + declSrc + "\n"
	}
	toEd := &editor{lang: goLang, rel: toRel, base: path.Base(toRel)}
	for _, code := range needed {
		next, msg, err := toEd.addImport(toText, code)
		if err != nil {
			return nil, rep, err
		}
		if strings.HasPrefix(msg, "added") {
			rep.added = append(rep.added, strings.TrimPrefix(msg, "added import "))
		}
		toText = next
	}
	if toText, err = formatGo(toRel, toText); err != nil {
		return nil, rep, err
	}
	return []fileWrite{{rel: toRel, content: toText}, {rel: fromRel, content: fromOut}}, rep, nil
}

// formatGo gofmts src and checks that it parses.
func formatGo(rel, src string) (string, error) {
	out, err := format.Source([]byte(src))
	if err != nil {
		return "", fmt.Errorf("the result for %s would not parse as Go: %v", rel, err)
	}
	return string(out), nil
}
