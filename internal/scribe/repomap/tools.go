package repomap

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Register adds the read-only code map tools to r: code_map, find_symbol,
// symbol_refs, code_impact and file_outline.
func Register(r *tools.Registry, m *Map) {
	r.Register(&mapTool{m: m, spec: llm.ToolSpec{
		Name: "code_map",
		Description: "Show the ranked code map of the repository: the most important files and their key symbols as signatures with line numbers. " +
			"Use it first to orient yourself. Pass focus files, or a query to personalize the ranking toward matching files.",
		Schema: tools.Schema(map[string]any{
			"focus":  tools.Arr("Repo-relative files to rank around", map[string]any{"type": "string"}),
			"query":  tools.Str("Optional name or concept; matching files are added to the focus"),
			"budget": tools.Int("Approximate token budget for the map (default 2500, max 4000)"),
		}),
	}, run: runCodeMap})
	r.Register(&mapTool{m: m, spec: llm.ToolSpec{
		Name:        "find_symbol",
		Description: "Find functions, types, classes and other symbols by name. Fuzzy: prefixes, substrings and abbreviations all match. Returns file, line and signature.",
		Schema: tools.Schema(map[string]any{
			"query": tools.Str("Symbol name or fragment"),
			"limit": tools.Int("Maximum results (default 20)"),
		}, "query"),
	}, run: runFindSymbol})
	r.Register(&mapTool{m: m, spec: llm.ToolSpec{
		Name:        "symbol_refs",
		Description: "List every use of an identifier across the repository (file:line, the line text, and the enclosing function when there is one). Strings and comments are excluded.",
		Schema: tools.Schema(map[string]any{
			"name":  tools.Str("Exact identifier name"),
			"limit": tools.Int("Maximum results (default 100)"),
		}, "name"),
	}, run: runSymbolRefs})
	r.Register(&mapTool{m: m, spec: llm.ToolSpec{
		Name:        "code_impact",
		Description: "Estimate what a change to one file affects: what it imports, files that import it (direct and one level further), files that change together with it in git history, and a risk summary.",
		Schema: tools.Schema(map[string]any{
			"file": tools.Str("Repo-relative file path"),
		}, "file"),
	}, run: runCodeImpact})
	r.Register(&mapTool{m: m, spec: llm.ToolSpec{
		Name:        "file_outline",
		Description: "Outline one file: its symbols in line order with line ranges, methods nested under their type. Use it to read a file selectively.",
		Schema: tools.Schema(map[string]any{
			"path": tools.Str("Repo-relative file path"),
		}, "path"),
	}, run: runFileOutline})
}

type mapTool struct {
	m    *Map
	spec llm.ToolSpec
	run  func(m *Map, args json.RawMessage) tools.Result
}

func (t *mapTool) Spec() llm.ToolSpec { return t.spec }

// ReadOnly is always true: the code map never changes files.
func (t *mapTool) ReadOnly() bool { return true }

func (t *mapTool) Run(_ context.Context, _ *tools.Env, args json.RawMessage) (res tools.Result) {
	defer func() {
		if r := recover(); r != nil {
			res = tools.Errorf("code map failed: %v. Try again, or use grep if this persists.", r)
		}
	}()
	if t.m == nil {
		return tools.Errorf("the code map is not available in this session. Use grep and glob instead.")
	}
	return t.run(t.m, args)
}

func runCodeMap(m *Map, args json.RawMessage) tools.Result {
	var a struct {
		Focus  []string `json:"focus"`
		Query  string   `json:"query"`
		Budget int      `json:"budget"`
	}
	if err := tools.Decode(args, &a); err != nil {
		return tools.Errorf("%v", err)
	}
	if a.Budget <= 0 {
		a.Budget = 2500
	}
	if a.Budget > 4000 {
		a.Budget = 4000
	}
	focus := a.Focus
	if q := strings.TrimSpace(a.Query); q != "" {
		for _, h := range m.FindFile(q, 5) {
			focus = append(focus, h.Path)
		}
		for _, h := range m.SearchSymbols(q, 10) {
			focus = append(focus, h.File)
		}
	}
	out := m.Render(a.Budget, focus)
	if out == "" {
		return tools.Errorf("the code map is empty. The repository may have no supported source files, or it has not been built yet.")
	}
	return tools.Result{Output: out, Summary: fmt.Sprintf("%d focus files", len(focus))}
}

func runFindSymbol(m *Map, args json.RawMessage) tools.Result {
	var a struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := tools.Decode(args, &a); err != nil {
		return tools.Errorf("%v", err)
	}
	if strings.TrimSpace(a.Query) == "" {
		return tools.Errorf("query is required. Pass a symbol name or fragment.")
	}
	hits := m.SearchSymbols(a.Query, a.Limit)
	if len(hits) == 0 {
		fl := m.FindFile(a.Query, 5)
		msg := fmt.Sprintf("no symbols match %q. Try a shorter fragment, or grep for the text.", a.Query)
		if len(fl) > 0 {
			msg += " Matching files: " + joinPaths(fl)
		}
		return tools.Result{Output: msg, Summary: "0 matches"}
	}
	var sb strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&sb, "%s %s  %s:%d", h.Kind, h.Name, h.File, h.Line)
		if h.Parent != "" {
			fmt.Fprintf(&sb, "  (in %s)", h.Parent)
		}
		if h.Signature != "" {
			fmt.Fprintf(&sb, "\n    %s", h.Signature)
		}
		sb.WriteByte('\n')
	}
	return tools.Result{Output: sb.String(), Summary: fmt.Sprintf("%d symbols", len(hits))}
}

func runSymbolRefs(m *Map, args json.RawMessage) tools.Result {
	var a struct {
		Name  string `json:"name"`
		Limit int    `json:"limit"`
	}
	if err := tools.Decode(args, &a); err != nil {
		return tools.Errorf("%v", err)
	}
	if a.Name == "" {
		return tools.Errorf("name is required.")
	}
	if a.Limit <= 0 {
		a.Limit = 100
	}
	refs, err := m.References(a.Name)
	if err != nil {
		return tools.Errorf("%v", err)
	}
	if len(refs) == 0 {
		return tools.Result{Output: fmt.Sprintf("no references to %q outside strings and comments.", a.Name), Summary: "0 refs"}
	}
	var sb strings.Builder
	for i, r := range refs {
		if i == a.Limit {
			fmt.Fprintf(&sb, "... %d more. Raise limit to see them.\n", len(refs)-a.Limit)
			break
		}
		fmt.Fprintf(&sb, "%s:%d  %s", r.File, r.Line, r.Text)
		if s, ok := m.EnclosingSymbol(r.File, r.Line); ok {
			fmt.Fprintf(&sb, "  (in %s)", s.Name)
		}
		sb.WriteByte('\n')
	}
	return tools.Result{Output: sb.String(), Summary: fmt.Sprintf("%d refs", len(refs))}
}

func runCodeImpact(m *Map, args json.RawMessage) tools.Result {
	var a struct {
		File string `json:"file"`
	}
	if err := tools.Decode(args, &a); err != nil {
		return tools.Errorf("%v", err)
	}
	im, err := m.Impact(a.File)
	if err != nil {
		return tools.Errorf("%v. Use code_map or find_symbol to get the exact repo-relative path.", err)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Impact of %s\n", im.File)
	fmt.Fprintf(&sb, "Risk: %s\n", im.Risk)
	fmt.Fprintf(&sb, "Imports (%d):\n", len(im.Imports))
	for _, p := range im.Imports {
		fmt.Fprintf(&sb, "  %s\n", p)
	}
	fmt.Fprintf(&sb, "Direct importers (%d):\n", im.Direct)
	for _, d := range im.Dependents {
		if d.Level == 1 {
			fmt.Fprintf(&sb, "  %s\n", d.Path)
		}
	}
	fmt.Fprintf(&sb, "Second-level importers (%d):\n", im.Transitive)
	for _, d := range im.Dependents {
		if d.Level == 2 {
			fmt.Fprintf(&sb, "  %s\n", d.Path)
		}
	}
	if len(im.CoChanged) > 0 {
		sb.WriteString("Changes together with this file in git history:\n")
		for _, c := range im.CoChanged {
			fmt.Fprintf(&sb, "  %s (%d commits)\n", c.File, c.Count)
		}
	}
	return tools.Result{Output: sb.String(), Summary: fmt.Sprintf("%d direct importers", im.Direct)}
}

func runFileOutline(m *Map, args json.RawMessage) tools.Result {
	var a struct {
		Path string `json:"path"`
	}
	if err := tools.Decode(args, &a); err != nil {
		return tools.Errorf("%v", err)
	}
	syms, ok := m.FileSymbols(a.Path)
	if !ok {
		return tools.Errorf("%q is not in the code map. Use code_map or find_symbol to get the exact repo-relative path.", a.Path)
	}
	if len(syms) == 0 {
		return tools.Result{Output: fmt.Sprintf("%s has no symbols the map can extract. Read it directly.", a.Path), Summary: "0 symbols"}
	}
	var sb strings.Builder
	for _, s := range syms {
		indent := ""
		if s.Parent != "" {
			indent = "    "
		}
		sig := s.Signature
		if sig == "" {
			sig = s.Kind + " " + s.Name
		}
		fmt.Fprintf(&sb, "%sL%d-%d  %s\n", indent, s.Line, s.EndLine, sig)
	}
	return tools.Result{Output: sb.String(), Summary: fmt.Sprintf("%d symbols", len(syms))}
}

func joinPaths(hits []FileHit) string {
	var ps []string
	for _, h := range hits {
		ps = append(ps, h.Path)
	}
	return strings.Join(ps, ", ")
}
