package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/symedit"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Register adds the language-server tools: navigate, diagnostics,
// rename_symbol and format_file. All of them use m.
func Register(r *tools.Registry, m *Manager) {
	r.Register(&navigateTool{m: m})
	r.Register(&diagnosticsTool{m: m})
	r.Register(&renameTool{m: m})
	r.Register(&formatTool{m: m})
	r.Register(&codeActionTool{m: m})
	r.Register(&refactorTool{m: m})
}

const maxListed = 200

// resolvePath makes a tool path absolute against the workdir.
func resolvePath(workdir, p string) string {
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(workdir, p)
	}
	return filepath.Clean(p)
}

// noLanguageServer reports whether err means no language server can serve
// the file at all, as opposed to a server that is installed and failed.
func noLanguageServer(err error) bool {
	var ni *NotInstalledError
	return errors.As(err, &ni) || errors.Is(err, ErrNoServer)
}

// missingServer turns a Manager error into a tool result that says what to do.
func missingServer(err error) tools.Result {
	var ni *NotInstalledError
	if errors.As(err, &ni) {
		return tools.Result{IsError: true, Output: ni.Error() + ". Once it is installed, retry this call."}
	}
	if errors.Is(err, ErrNoServer) {
		return tools.Result{IsError: true, Output: err.Error()}
	}
	return tools.Result{IsError: true, Output: "language server error: " + err.Error()}
}

// --- navigate ---------------------------------------------------------------

type navigateTool struct{ m *Manager }

type navigateArgs struct {
	Action string `json:"action"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Symbol string `json:"symbol"`
}

func (t *navigateTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "navigate",
		Description: "Code navigation through the project's language server. " +
			"action: definition, references, implementation, hover, symbols (outline of a file), " +
			"workspace_symbols (search by name), or read_symbol (the source of one definition in path). " +
			"Hierarchies, from a function, method or type name: incoming_calls (callers), outgoing_calls (what it calls), " +
			"supertypes and subtypes. " +
			"Give path with line and column (1-based; column counts characters), or give symbol instead and it is located for you. " +
			"For workspace_symbols, symbol is the query and path (optional) picks the language. " +
			"When no language server answers, definition, symbols and read_symbol fall back to a text-based search and say so.",
		Schema: tools.Schema(map[string]any{
			"action": tools.Enum("What to look up.", "definition", "references", "implementation", "hover", "symbols", "workspace_symbols", "read_symbol",
				"incoming_calls", "outgoing_calls", "supertypes", "subtypes"),
			"path":   tools.Str("File path, relative to the project root."),
			"line":   tools.Int("1-based line of the position."),
			"column": tools.Int("1-based column of the position, counted in characters."),
			"symbol": tools.Str("Symbol name. With no line/column it is located first; for workspace_symbols it is the search query; for read_symbol it is the definition to read."),
		}, "action"),
	}
}

func (t *navigateTool) ReadOnly() bool { return true }

func (t *navigateTool) Run(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
	var a navigateArgs
	if err := tools.Decode(args, &a); err != nil {
		return tools.Errorf("%v", err)
	}
	workdir := t.m.Workdir()
	if env != nil && env.Workdir != "" {
		workdir = env.Workdir
	}
	abs := resolvePath(workdir, a.Path)
	a.Action = normalizeNavAction(a.Action)

	if a.Action == "workspace_symbols" {
		if a.Symbol == "" {
			return tools.Errorf("workspace_symbols needs symbol: the name, or part of it, to search for")
		}
		hits, err := t.m.WorkspaceSymbols(ctx, abs, a.Symbol)
		if err != nil {
			return missingServer(err)
		}
		return symbolHitsResult(t.m, hits)
	}
	switch a.Action {
	case "definition", "references", "implementation", "hover", "symbols", "read_symbol",
		hierIncoming, hierOutgoing, hierSuper, hierSub:
	default:
		return tools.Errorf("unknown action %q: use definition, references, implementation, hover, symbols, workspace_symbols, read_symbol, incoming_calls, outgoing_calls, supertypes or subtypes", a.Action)
	}
	if a.Action == "symbols" {
		if abs == "" {
			return tools.Errorf("symbols needs path: the file to outline")
		}
		syms, err := t.m.DocumentSymbols(ctx, abs)
		if err == nil && len(syms) > 0 {
			return outlineResult(t.m, abs, syms)
		}
		return outlineFallback(t.m, abs, err)
	}
	if a.Action == "read_symbol" {
		return readSymbolResult(ctx, t.m, workdir, abs, a.Symbol)
	}

	var p Point
	switch {
	case a.Line > 0 && abs != "":
		col := a.Column
		if col < 1 {
			col = 1
		}
		p = Point{Line: a.Line, Column: col}
	case a.Symbol != "":
		var err error
		abs, p, err = locateSymbol(ctx, t.m, workdir, abs, a.Symbol)
		if err != nil {
			return tools.Errorf("%v", err)
		}
	default:
		return tools.Errorf("%s needs path with line and column, or a symbol name", a.Action)
	}
	where := fmt.Sprintf("%s:%d:%d", t.m.RelPath(abs), p.Line, p.Column)

	switch a.Action {
	case "definition":
		locs, err := t.m.Definition(ctx, abs, p)
		if err == nil && len(locs) > 0 {
			return locationsResult(t.m, a.Action, locs)
		}
		return definitionFallback(workdir, abs, p, a.Symbol, err, t.m)
	case "references", "implementation":
		var locs []Location
		var err error
		if a.Action == "references" {
			locs, err = t.m.References(ctx, abs, p)
		} else {
			locs, err = t.m.Implementations(ctx, abs, p)
		}
		if err != nil {
			return missingServer(err)
		}
		if len(locs) == 0 {
			return tools.Result{Output: fmt.Sprintf("No %s found for the symbol at %s.", a.Action, where), Summary: "none"}
		}
		return locationsResult(t.m, a.Action, locs)
	case "hover":
		text, err := t.m.Hover(ctx, abs, p)
		if err != nil {
			return missingServer(err)
		}
		if strings.TrimSpace(text) == "" {
			return tools.Result{Output: "No hover information at " + where + ".", Summary: "none"}
		}
		if len(text) > 6000 {
			text = text[:6000] + "\n[truncated]"
		}
		return tools.Result{Output: strings.TrimSpace(text), Summary: "hover at " + where}
	case hierIncoming, hierOutgoing, hierSuper, hierSub:
		target, edges, err := t.m.Hierarchy(ctx, abs, p, a.Action)
		if err != nil {
			return missingServer(err)
		}
		if target.Name == "" {
			return tools.Result{Output: "No symbol at " + where + " to show a hierarchy for. Put the position on a function, method or type name.", Summary: "none"}
		}
		return hierarchyResult(t.m, a.Action, target, edges)
	}
	return tools.Errorf("unreachable action %q", a.Action)
}

// normalizeNavAction maps the spellings a model uses for an action onto the
// action names (callers -> incoming_calls, super -> supertypes, and so on).
func normalizeNavAction(a string) string {
	a = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(a)), "-", "_")
	if mapped, ok := hierarchyAliases[a]; ok {
		return mapped
	}
	return a
}

// fallbackNote says why a lookup came from the text tier. It is empty when
// the language server simply found nothing.
func fallbackNote(err error) string {
	if err == nil {
		return "The language server found nothing, so this comes from a text search of the project."
	}
	var ni *NotInstalledError
	if errors.As(err, &ni) {
		return "No language server is installed for this language, so this comes from a text search of the project."
	}
	return "The language server did not answer (" + firstLine(err.Error()) + "), so this comes from a text search of the project."
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// definitionFallback is the text tier for definition: the declaration of the
// name, found by searching the project. It replaces an empty answer or an
// error from the language server.
func definitionFallback(workdir, abs string, p Point, symbol string, lspErr error, m *Manager) tools.Result {
	name := symbol
	if name == "" && abs != "" {
		if data, err := os.ReadFile(abs); err == nil {
			name = identAt(string(data), p)
		}
	}
	if name == "" {
		if lspErr != nil {
			return missingServer(lspErr)
		}
		return tools.Result{Output: fmt.Sprintf("No definition found for the symbol at %s:%d:%d.", m.RelPath(abs), p.Line, p.Column), Summary: "none"}
	}
	file, at, ok := findTextSymbol(workdir, "", name)
	if !ok {
		if lspErr != nil {
			return missingServer(lspErr)
		}
		return tools.Result{Output: fmt.Sprintf("No definition found for %q.", name), Summary: "none"}
	}
	where := fmt.Sprintf("%s:%d:%d", m.RelPath(file), at.Line, at.Column)
	return tools.Result{
		Output:  fmt.Sprintf("%s  %s\n%s", where, sourceLine(file, at.Line), fallbackNote(lspErr)),
		Summary: "1 definition (text search)",
	}
}

// outlineFallback outlines a file without a language server, from the
// definitions symedit finds. lspErr is what the server said, if anything.
func outlineFallback(m *Manager, abs string, lspErr error) tools.Result {
	defs, err := symedit.Outline(m.RelPath(abs), readText(abs))
	if err != nil {
		if lspErr != nil {
			return missingServer(lspErr)
		}
		return tools.Result{Output: "No symbols in " + m.RelPath(abs) + ".", Summary: "none"}
	}
	syms := nestDefinitions(defs)
	res := outlineResult(m, abs, syms)
	if res.Summary != "none" && !res.IsError {
		res.Output = res.Output + "\n" + fallbackNote(lspErr)
	}
	return res
}

// nestDefinitions turns the flat definitions of symedit into an outline,
// putting each member under its enclosing type.
func nestDefinitions(defs []symedit.Definition) []Symbol {
	var top []Symbol
	at := map[string]int{}
	for _, d := range defs {
		if d.Parent == "" {
			at[d.Name] = len(top)
			top = append(top, Symbol{Name: d.Name, Kind: d.Kind, Line: d.Line, Column: d.Column, StartLine: d.StartLine, EndLine: d.EndLine})
		}
	}
	for _, d := range defs {
		if d.Parent == "" {
			continue
		}
		child := Symbol{Name: d.Name, Kind: d.Kind, Line: d.Line, Column: d.Column, StartLine: d.StartLine, EndLine: d.EndLine}
		if i, ok := at[d.Parent]; ok {
			top[i].Children = append(top[i].Children, child)
		} else {
			top = append(top, child)
		}
	}
	return top
}

// readSymbolResult prints the source of one definition, from the language
// server's ranges when it has them, else from the text tier.
func readSymbolResult(ctx context.Context, m *Manager, workdir, abs, name string) tools.Result {
	if name == "" {
		return tools.Errorf("read_symbol needs symbol: the definition to read")
	}
	file := abs
	if file == "" {
		var err error
		file, _, err = locateSymbol(ctx, m, workdir, "", name)
		if err != nil {
			return tools.Errorf("%v", err)
		}
	}
	var start, end int
	var source string
	if syms, err := m.DocumentSymbols(ctx, file); err == nil {
		if s, ok := findSymbol(syms, name); ok && s.EndLine >= s.StartLine && s.StartLine > 0 {
			start, end, source = s.StartLine, s.EndLine, "language server"
		}
	}
	if start == 0 {
		defs, err := symedit.Outline(file, readText(file))
		if err != nil {
			return tools.Errorf("cannot read %s: %v", m.RelPath(file), err)
		}
		for _, d := range defs {
			if d.Name == name || d.Parent+"."+d.Name == name {
				start, end, source = d.StartLine, d.EndLine, "text search"
				break
			}
		}
		if start == 0 {
			return tools.Errorf("could not find the definition %q in %s. Check the spelling, or use navigate symbols to list the file.", name, m.RelPath(file))
		}
	}
	lines := strings.Split(readText(file), "\n")
	const maxLines = 400
	var b strings.Builder
	fmt.Fprintf(&b, "%s:%d-%d (%s)\n", m.RelPath(file), start, end, source)
	shown := 0
	for ln := start; ln <= end && ln <= len(lines); ln++ {
		if shown == maxLines {
			fmt.Fprintf(&b, "... %d more lines not shown\n", end-ln+1)
			break
		}
		fmt.Fprintf(&b, "%6d  %s\n", ln, strings.TrimRight(lines[ln-1], "\r"))
		shown++
	}
	return tools.Result{Output: strings.TrimRight(b.String(), "\n"), Summary: fmt.Sprintf("%s lines %d-%d", name, start, end), Files: []string{m.RelPath(file)}}
}

// readText returns the file's text, or "" when it cannot be read.
func readText(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func locationsResult(m *Manager, verb string, locs []Location) tools.Result {
	var b strings.Builder
	files := map[string]bool{}
	for i, l := range locs {
		if i == maxListed {
			fmt.Fprintf(&b, "... %d more not shown\n", len(locs)-maxListed)
			break
		}
		rel := m.RelPath(l.Path)
		files[rel] = true
		fmt.Fprintf(&b, "%s:%d:%d  %s\n", rel, l.Line, l.Column, sourceLine(l.Path, l.Line))
	}
	return tools.Result{
		Output:  strings.TrimRight(b.String(), "\n"),
		Summary: fmt.Sprintf("%d %s in %d file(s)", len(locs), verb, len(files)),
	}
}

func symbolHitsResult(m *Manager, hits []SymbolInfo) tools.Result {
	if len(hits) == 0 {
		return tools.Result{Output: "No symbols match.", Summary: "none"}
	}
	var b strings.Builder
	for i, h := range hits {
		if i == maxListed {
			fmt.Fprintf(&b, "... %d more not shown\n", len(hits)-maxListed)
			break
		}
		container := ""
		if h.Container != "" {
			container = " in " + h.Container
		}
		fmt.Fprintf(&b, "%s %s  %s:%d:%d%s\n", h.Kind, h.Name, m.RelPath(h.Path), h.Line, h.Column, container)
	}
	return tools.Result{Output: strings.TrimRight(b.String(), "\n"), Summary: fmt.Sprintf("%d symbols", len(hits))}
}

func outlineResult(m *Manager, abs string, syms []Symbol) tools.Result {
	var b strings.Builder
	n := 0
	var walk func(list []Symbol, depth int)
	walk = func(list []Symbol, depth int) {
		for _, s := range list {
			if n == 400 {
				return
			}
			n++
			fmt.Fprintf(&b, "%s%s %s  line %d\n", strings.Repeat("  ", depth), s.Kind, s.Name, s.Line)
			walk(s.Children, depth+1)
		}
	}
	walk(syms, 0)
	if n == 0 {
		return tools.Result{Output: "No symbols in " + m.RelPath(abs) + ".", Summary: "none"}
	}
	out := strings.TrimRight(b.String(), "\n")
	if hint := monikerHint(m.RelPath(abs), readText(abs)); hint != "" {
		out += "\n" + hint
	}
	return tools.Result{Output: out, Summary: fmt.Sprintf("%d symbols", n)}
}

// monikerHint is one line telling the model how to name a symbol of this file
// exactly in an edit, with a real example. It is empty when the file's
// language has no moniker support.
func monikerHint(rel, text string) string {
	defs, err := symedit.Outline(rel, text)
	if err != nil {
		return ""
	}
	for _, d := range defs {
		if d.Moniker != "" {
			return "To edit one symbol exactly, ast_edit takes a moniker: path#kind:Parent.Name, e.g. " + d.Moniker
		}
	}
	return ""
}

// sourceLine returns line n (1-based) of path, trimmed, for display.
func sourceLine(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	start, end, ok := lineBounds(string(data), n-1)
	if !ok {
		return ""
	}
	s := strings.TrimSpace(string(data)[start:end])
	if utf8.RuneCountInString(s) > 160 {
		s = string([]rune(s)[:160]) + "..."
	}
	return s
}

// locateSymbol finds name and returns the file and 1-based position of its
// definition. It tries the file's document symbols, then the workspace symbol
// search of running servers, then a text search of the project.
func locateSymbol(ctx context.Context, m *Manager, root, abs, name string) (string, Point, error) {
	if abs != "" {
		if syms, err := m.DocumentSymbols(ctx, abs); err == nil {
			if s, ok := findSymbol(syms, name); ok {
				return abs, Point{Line: s.Line, Column: s.Column}, nil
			}
		}
	}
	if hits, err := m.WorkspaceSymbols(ctx, abs, name); err == nil {
		for _, h := range hits {
			if h.Name == name || strings.HasPrefix(h.Name, name+"(") {
				return h.Path, Point{Line: h.Line, Column: h.Column}, nil
			}
		}
	}
	if file, p, ok := findTextSymbol(root, abs, name); ok {
		return file, p, nil
	}
	return "", Point{}, fmt.Errorf("could not find the symbol %q. Pass path with line and column, or check the spelling", name)
}

func findSymbol(syms []Symbol, name string) (Symbol, bool) {
	for _, s := range syms {
		if s.Name == name || strings.HasPrefix(s.Name, name+"(") {
			return s, true
		}
		if c, ok := findSymbol(s.Children, name); ok {
			return c, true
		}
	}
	return Symbol{}, false
}

// findTextSymbol searches source files for name. A line that looks like a
// declaration of name wins; otherwise the first whole-word match is used.
// only, when set, restricts the search to that file.
func findTextSymbol(root, only, name string) (string, Point, bool) {
	word := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
	decl := regexp.MustCompile(`^\s*(?:(?:export|pub|public|private|protected|static|async|default|abstract|final|const)\s+)*` +
		`(?:func|function|def|fn|class|struct|interface|type|enum|trait|const|var|let|object|module|defmodule)\b.*?` +
		`\b` + regexp.QuoteMeta(name) + `\b`)

	// scan reports the first declaration-like match in path, and the first
	// plain match when there is no declaration.
	scan := func(path string) (decls, plain *Point) {
		data, err := os.ReadFile(path)
		if err != nil || len(data) > 1<<20 {
			return nil, nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			loc := word.FindStringIndex(line)
			if loc == nil {
				continue
			}
			p := &Point{Line: i + 1, Column: utf8.RuneCountInString(line[:loc[0]]) + 1}
			if decl.MatchString(line) {
				return p, plain
			}
			if plain == nil {
				plain = p
			}
		}
		return nil, plain
	}

	var declFile, plainFile string
	var declAt, plainAt Point
	consider := func(path string) bool {
		d, pl := scan(path)
		if d != nil {
			declFile, declAt = path, *d
			return true
		}
		if pl != nil && plainFile == "" {
			plainFile, plainAt = path, *pl
		}
		return false
	}

	if only != "" {
		consider(only)
	} else {
		count := 0
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if path != root && skipDir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !SupportedExtensions(path) || count >= 5000 {
				return nil
			}
			count++
			if consider(path) {
				return errStopWalk
			}
			return nil
		})
	}
	switch {
	case declFile != "":
		return declFile, declAt, true
	case plainFile != "":
		return plainFile, plainAt, true
	}
	return "", Point{}, false
}

var errStopWalk = errors.New("stop")

func skipDir(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "node_modules", "vendor", "target", "dist", "build", "__pycache__", "venv":
		return true
	}
	return false
}

// --- diagnostics ------------------------------------------------------------

type diagnosticsTool struct{ m *Manager }

func (t *diagnosticsTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "diagnostics",
		Description: "Errors and warnings the language server reports for one or more files. " +
			"Output lines are path:line:col severity message.",
		Schema: tools.Schema(map[string]any{
			"path":  tools.Str("One file, relative to the project root."),
			"paths": tools.Arr("Several files.", map[string]any{"type": "string"}),
		}),
	}
}

func (t *diagnosticsTool) ReadOnly() bool { return true }

func (t *diagnosticsTool) Run(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
	var a struct {
		Path  string   `json:"path"`
		Paths []string `json:"paths"`
	}
	if err := tools.Decode(args, &a); err != nil {
		return tools.Errorf("%v", err)
	}
	list := append([]string(nil), a.Paths...)
	if a.Path != "" {
		list = append([]string{a.Path}, list...)
	}
	if len(list) == 0 {
		return tools.Errorf("diagnostics needs path or paths")
	}
	workdir := t.m.Workdir()
	if env != nil && env.Workdir != "" {
		workdir = env.Workdir
	}
	var lines, failures []string
	nErr, nWarn, failed := 0, 0, 0
	for _, p := range list {
		abs := resolvePath(workdir, p)
		rel := t.m.RelPath(abs)
		diags, err := t.m.Diagnostics(ctx, abs)
		if err != nil {
			failed++
			failures = append(failures, rel+": "+missingServer(err).Output)
			continue
		}
		sort.SliceStable(diags, func(i, j int) bool {
			if diags[i].Line != diags[j].Line {
				return diags[i].Line < diags[j].Line
			}
			return diags[i].Column < diags[j].Column
		})
		for _, d := range diags {
			switch d.Severity {
			case "error":
				nErr++
			case "warning":
				nWarn++
			}
			msg := strings.Join(strings.Fields(d.Message), " ")
			lines = append(lines, fmt.Sprintf("%s:%d:%d %s %s", rel, d.Line, d.Column, d.Severity, msg))
		}
	}
	out := append(lines, failures...)
	summary := fmt.Sprintf("%d errors, %d warnings in %d file(s)", nErr, nWarn, len(list)-failed)
	if len(out) == 0 {
		return tools.Result{Output: "No diagnostics.", Summary: summary}
	}
	if len(lines) > maxListed {
		out = append(out[:maxListed], fmt.Sprintf("... %d more not shown", len(lines)-maxListed))
	}
	return tools.Result{
		Output:  strings.Join(out, "\n"),
		Summary: summary,
		IsError: failed == len(list),
	}
}

// --- rename_symbol ----------------------------------------------------------

type renameTool struct{ m *Manager }

func (t *renameTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "rename_symbol",
		Description: "Rename a symbol everywhere the language server knows about, across files, and apply it. " +
			"Give path with line and column, or give symbol. Reports the files changed and a diff.",
		Schema: tools.Schema(map[string]any{
			"path":     tools.Str("File containing the symbol, relative to the project root."),
			"line":     tools.Int("1-based line of the symbol."),
			"column":   tools.Int("1-based column of the symbol, counted in characters."),
			"symbol":   tools.Str("Current name of the symbol. Used to locate it when line/column are not given."),
			"new_name": tools.Str("The new name."),
		}, "new_name"),
	}
}

func (t *renameTool) ReadOnly() bool { return false }

func (t *renameTool) Run(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
	var a struct {
		Path    string `json:"path"`
		Line    int    `json:"line"`
		Column  int    `json:"column"`
		Symbol  string `json:"symbol"`
		NewName string `json:"new_name"`
	}
	if err := tools.Decode(args, &a); err != nil {
		return tools.Errorf("%v", err)
	}
	if a.NewName == "" || strings.ContainsAny(a.NewName, " \t\n") {
		return tools.Errorf("new_name must be a single identifier")
	}
	workdir := t.m.Workdir()
	if env != nil && env.Workdir != "" {
		workdir = env.Workdir
	}
	abs := resolvePath(workdir, a.Path)
	var p Point
	switch {
	case a.Line > 0 && abs != "":
		col := a.Column
		if col < 1 {
			col = 1
		}
		p = Point{Line: a.Line, Column: col}
	case a.Symbol != "":
		var err error
		abs, p, err = locateSymbol(ctx, t.m, workdir, abs, a.Symbol)
		if err != nil {
			return tools.Errorf("%v", err)
		}
	default:
		return tools.Errorf("rename_symbol needs path with line and column, or a symbol name")
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return tools.Errorf("cannot read %s: %v", t.m.RelPath(abs), err)
	}
	oldName := a.Symbol
	if oldName == "" {
		oldName = identAt(string(data), p)
	}
	edit, err := t.m.Rename(ctx, abs, p, a.NewName)
	if err != nil {
		if noLanguageServer(err) {
			// Without a server the rename is a comment- and string-aware text sweep.
			return symedit.TextRename(env, workdir, oldName, a.NewName)
		}
		return missingServer(err)
	}
	applied, err := applyChecked(ctx, env, edit, func(path string) {
		if env != nil && env.Files != nil {
			env.Files.BeforeWrite(path)
		}
	})
	if err != nil {
		return tools.Errorf("the rename was not applied, and no file was changed: %v", err)
	}
	if len(applied.Changes) == 0 {
		return tools.Errorf("the server returned a rename with no changes; check that the position is the symbol's name")
	}
	for _, c := range applied.Changes {
		t.m.FileChanged(c.Path)
	}
	rels := make([]string, 0, len(applied.Changes))
	done := map[string]bool{}
	var b strings.Builder
	fmt.Fprintf(&b, "Renamed '%s' to '%s' in %d file(s):\n", oldName, a.NewName, len(applied.Changes))
	for _, c := range applied.Changes {
		rel := t.m.RelPath(c.Path)
		rels = append(rels, rel)
		done[filepath.ToSlash(rel)] = true
		fmt.Fprintf(&b, "  %s\n", rel)
	}
	diff := diffSummary(applied.Changes, t.m.RelPath)
	if diff != "" {
		b.WriteString("\n")
		b.WriteString(diff)
	}
	// The text sweep always runs after the server's rename, over the files the
	// server did not change, so names left in other code are not missed.
	if oldName != "" && oldName != a.NewName {
		swept, sites, err := symedit.SweepRemaining(env, workdir, oldName, a.NewName, done)
		if err != nil {
			fmt.Fprintf(&b, "\nThe text sweep for remaining references failed, and nothing else was changed by it: %v\n", err)
		} else if len(swept) > 0 {
			fmt.Fprintf(&b, "\nText sweep also renamed '%s' by name in %d more file(s) (comment-aware, not resolved by a server; a different symbol with the same name would change too):\n", oldName, len(swept))
			for _, s := range sites {
				fmt.Fprintf(&b, "  %s\n", s)
			}
			rels = append(rels, swept...)
		} else {
			fmt.Fprintf(&b, "\nNo other code references to '%s' remain (text sweep). This does not run a type check; run project test.\n", oldName)
		}
	}
	return tools.Result{
		Output:  strings.TrimRight(b.String(), "\n"),
		Summary: fmt.Sprintf("renamed in %d file(s)", len(rels)),
		Diff:    diff,
		Files:   rels,
	}
}

// --- format_file ------------------------------------------------------------

type formatTool struct{ m *Manager }

func (t *formatTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "format_file",
		Description: "Format a file with its language server's formatter and write the result. " +
			"Give start_line and end_line to format only those lines. organize_imports sorts and removes unused imports first.",
		Schema: tools.Schema(map[string]any{
			"path":             tools.Str("File to format, relative to the project root."),
			"start_line":       tools.Int("First line (1-based) to format. Give it with end_line to format a range."),
			"end_line":         tools.Int("Last line (1-based, inclusive) to format."),
			"organize_imports": tools.Bool("Also run the server's organize-imports action before formatting. Default false."),
		}, "path"),
	}
}

func (t *formatTool) ReadOnly() bool { return false }

func (t *formatTool) Run(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
	var a struct {
		Path            string `json:"path"`
		StartLine       int    `json:"start_line"`
		EndLine         int    `json:"end_line"`
		OrganizeImports bool   `json:"organize_imports"`
	}
	if err := tools.Decode(args, &a); err != nil {
		return tools.Errorf("%v", err)
	}
	if a.Path == "" {
		return tools.Errorf("format_file needs path")
	}
	ranged := a.StartLine > 0 || a.EndLine > 0
	if ranged && (a.StartLine < 1 || a.EndLine < a.StartLine) {
		return tools.Errorf("format_file range needs 1 <= start_line <= end_line; got %d to %d", a.StartLine, a.EndLine)
	}
	abs := resolvePath(workdirOf(t.m, env), a.Path)
	return formatFile(ctx, t.m, env, abs, formatOpts{
		organize:  a.OrganizeImports,
		format:    true,
		startLine: a.StartLine,
		endLine:   a.EndLine,
	})
}

// formatOpts says which steps formatFile runs on one file.
type formatOpts struct {
	organize  bool // sort and remove unused imports first
	format    bool // then format, the whole file or only startLine to endLine
	startLine int  // 1-based; 0 with endLine 0 formats the whole file
	endLine   int  // 1-based, inclusive
}

// formatFile runs the organize-imports and format steps that format_file and
// the refactor tool's format and organize_imports actions share. Each step is
// one workspace edit, checked against the write rules before any file changes.
func formatFile(ctx context.Context, m *Manager, env *tools.Env, abs string, o formatOpts) tools.Result {
	rel := m.RelPath(abs)
	ranged := o.startLine > 0 || o.endLine > 0
	before := func(path string) {
		if env != nil && env.Files != nil {
			env.Files.BeforeWrite(path)
		}
	}
	var steps []string
	var changed []FileChange
	if o.organize {
		actions, err := m.OrganizeImports(ctx, abs)
		if err != nil {
			return missingServer(err)
		}
		if len(actions) > 0 {
			applied, err := applyChecked(ctx, env, *actions[0].Edit, before)
			if err != nil {
				return tools.Errorf("organizing imports was not applied, and no file was changed: %v", err)
			}
			changed = append(changed, applied.Changes...)
			steps = append(steps, "organized imports")
		}
	}
	if o.format {
		var edits []TextEdit
		var err error
		if ranged {
			edits, err = m.FormatRange(ctx, abs, Point{Line: o.startLine, Column: 1}, Point{Line: o.endLine, Column: 1 << 20})
		} else {
			edits, err = m.Format(ctx, abs)
		}
		if err != nil {
			return missingServer(err)
		}
		if len(edits) > 0 {
			edit := WorkspaceEdit{Changes: map[string][]TextEdit{pathToURI(abs): edits}}
			applied, err := applyChecked(ctx, env, edit, before)
			if err != nil {
				return tools.Errorf("formatting was not applied, and no file was changed: %v", err)
			}
			changed = append(changed, applied.Changes...)
			steps = append(steps, fmt.Sprintf("formatted %d edit(s)", len(edits)))
		}
	}
	if len(changed) == 0 {
		if o.format {
			return tools.Result{Output: "Already formatted: " + rel, Summary: "no change"}
		}
		return tools.Result{Output: "Imports already organized: " + rel, Summary: "no change"}
	}
	m.FileChanged(abs)
	diff := diffSummary(changed, m.RelPath)
	where := rel
	if ranged {
		where = fmt.Sprintf("%s lines %d-%d", rel, o.startLine, o.endLine)
	}
	return tools.Result{
		Output:  fmt.Sprintf("%s in %s", strings.Join(steps, "; "), where),
		Summary: fmt.Sprintf("formatted %s", rel),
		Diff:    diff,
		Files:   []string{rel},
	}
}

// diffSummary renders changes as unified-style hunks. Hunks pair lines by
// index, so it is exact when line counts match and says so when they do not.
func diffSummary(changes []FileChange, rel func(string) string) string {
	const maxLines = 300
	var b strings.Builder
	emitted := 0
	more := 0
	emit := func(s string) {
		if emitted >= maxLines {
			more++
			return
		}
		b.WriteString(s)
		b.WriteString("\n")
		emitted++
	}
	for _, c := range changes {
		name := rel(c.Path)
		switch {
		case c.Deleted:
			emit("--- a/" + name)
			emit("+++ /dev/null")
			continue
		case c.Created:
			emit("--- /dev/null")
			emit("+++ b/" + name)
			for _, l := range strings.Split(c.After, "\n") {
				emit("+" + l)
			}
			continue
		}
		emit("--- a/" + name)
		emit("+++ b/" + name)
		oldL := strings.Split(c.Before, "\n")
		newL := strings.Split(c.After, "\n")
		if len(oldL) != len(newL) {
			emit(fmt.Sprintf("@@ line count changed %d -> %d @@", len(oldL), len(newL)))
			continue
		}
		for i := range oldL {
			if oldL[i] != newL[i] {
				emit(fmt.Sprintf("@@ line %d @@", i+1))
				emit("-" + oldL[i])
				emit("+" + newL[i])
			}
		}
	}
	if more > 0 {
		fmt.Fprintf(&b, "... %d more diff lines not shown\n", more)
	}
	return strings.TrimRight(b.String(), "\n")
}
