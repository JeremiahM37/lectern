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
			"action: definition, references, hover, symbols (outline of a file), or workspace_symbols (search by name). " +
			"Give path with line and column (1-based; column counts characters), or give symbol instead and it is located for you. " +
			"For workspace_symbols, symbol is the query and path (optional) picks the language.",
		Schema: tools.Schema(map[string]any{
			"action": tools.Enum("What to look up.", "definition", "references", "hover", "symbols", "workspace_symbols"),
			"path":   tools.Str("File path, relative to the project root."),
			"line":   tools.Int("1-based line of the position."),
			"column": tools.Int("1-based column of the position, counted in characters."),
			"symbol": tools.Str("Symbol name. With no line/column it is located first; for workspace_symbols it is the search query."),
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
	case "definition", "references", "hover", "symbols":
	default:
		return tools.Errorf("unknown action %q: use definition, references, hover, symbols or workspace_symbols", a.Action)
	}
	if a.Action == "symbols" {
		if abs == "" {
			return tools.Errorf("symbols needs path: the file to outline")
		}
		syms, err := t.m.DocumentSymbols(ctx, abs)
		if err != nil {
			return missingServer(err)
		}
		return outlineResult(t.m, abs, syms)
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
	case "definition", "references":
		var locs []Location
		var err error
		if a.Action == "definition" {
			locs, err = t.m.Definition(ctx, abs, p)
		} else {
			locs, err = t.m.References(ctx, abs, p)
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
	}
	return tools.Errorf("unreachable action %q", a.Action)
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
	return tools.Result{Output: strings.TrimRight(b.String(), "\n"), Summary: fmt.Sprintf("%d symbols", n)}
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
	applied, err := ApplyWorkspaceEditWith(edit, func(path string) {
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
	var b strings.Builder
	fmt.Fprintf(&b, "Renamed '%s' to '%s' in %d file(s):\n", oldName, a.NewName, len(applied.Changes))
	for _, c := range applied.Changes {
		rel := t.m.RelPath(c.Path)
		rels = append(rels, rel)
		fmt.Fprintf(&b, "  %s\n", rel)
	}
	diff := diffSummary(applied.Changes, t.m.RelPath)
	if diff != "" {
		b.WriteString("\n")
		b.WriteString(diff)
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
		Name:        "format_file",
		Description: "Format a file with its language server's formatter and write the result.",
		Schema: tools.Schema(map[string]any{
			"path": tools.Str("File to format, relative to the project root."),
		}, "path"),
	}
}

func (t *formatTool) ReadOnly() bool { return false }

func (t *formatTool) Run(ctx context.Context, env *tools.Env, args json.RawMessage) tools.Result {
	var a struct {
		Path string `json:"path"`
	}
	if err := tools.Decode(args, &a); err != nil {
		return tools.Errorf("%v", err)
	}
	if a.Path == "" {
		return tools.Errorf("format_file needs path")
	}
	workdir := t.m.Workdir()
	if env != nil && env.Workdir != "" {
		workdir = env.Workdir
	}
	abs := resolvePath(workdir, a.Path)
	rel := t.m.RelPath(abs)
	edits, err := t.m.Format(ctx, abs)
	if err != nil {
		return missingServer(err)
	}
	if len(edits) == 0 {
		return tools.Result{Output: "Already formatted: " + rel, Summary: "no change"}
	}
	edit := WorkspaceEdit{Changes: map[string][]TextEdit{pathToURI(abs): edits}}
	applied, err := ApplyWorkspaceEditWith(edit, func(path string) {
		if env != nil && env.Files != nil {
			env.Files.BeforeWrite(path)
		}
	})
	if err != nil {
		return tools.Errorf("formatting was not applied, and no file was changed: %v", err)
	}
	if len(applied.Changes) == 0 {
		return tools.Result{Output: "Already formatted: " + rel, Summary: "no change"}
	}
	t.m.FileChanged(abs)
	diff := diffSummary(applied.Changes, t.m.RelPath)
	return tools.Result{
		Output:  fmt.Sprintf("Formatted %s (%d edit(s))", rel, len(edits)),
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
