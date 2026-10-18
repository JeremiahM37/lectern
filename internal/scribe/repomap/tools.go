package repomap

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Register adds the read-only code map tools to r: code_map (with actions),
// find_symbol, symbol_refs, code_impact, file_outline and soul_query.
func Register(r *tools.Registry, m *Map) {
	registerQueryTool(r, m)
	r.Register(&mapTool{m: m, spec: llm.ToolSpec{
		Name: "code_map",
		Description: "Look at the repository's code map. With no action it shows the ranked map: the most important files and their key symbols. " +
			"Other actions answer specific questions: identifier_frequency, unused_exports, packages, symbols_by_kind, class_members, top_files, " +
			"file_profile, duplication, call_graph, search_symbols (name prefix), summaries. Pass only the arguments that action needs.",
		Schema: tools.Schema(map[string]any{
			"action": tools.Str("map (default), identifier_frequency, unused_exports, packages, symbols_by_kind, class_members, top_files, file_profile, duplication, call_graph, search_symbols, summaries"),
			"focus":  tools.Arr("Repo-relative files to rank around (action map)", map[string]any{"type": "string"}),
			"query":  tools.Str("Optional name or concept; matching files are added to the focus (action map)"),
			"budget": tools.Int("Approximate token budget for the map (default 2500, max 4000)"),
			"name":   tools.Str("Symbol or type name (call_graph, class_members, search_symbols with a prefix such as build*, packages filter)"),
			"file":   tools.Str("Repo-relative file path (file_profile)"),
			"kind":   tools.Str("Symbol kind: function, method, class, interface, type, enum, trait, struct (symbols_by_kind)"),
			"limit":  tools.Int("Maximum rows for the action"),
		}),
	}, run: runCodeMap})
	r.Register(&mapTool{m: m, spec: llm.ToolSpec{
		Name: "find_symbol",
		Description: "Find functions, types, classes and other symbols by name. Fuzzy: prefixes, substrings and abbreviations all match. " +
			"Exact names come first, then the most important symbols. Results are grouped by file. " +
			"Filter with kind (function, class, ...) or type (test, component, config, types, style). Set concept to search by what the code is about " +
			"(for example \"token refresh\"). Set callees or callers to list what a function calls or is called by.",
		Schema: tools.Schema(map[string]any{
			"query":   tools.Str("Symbol name or fragment, or a phrase when concept is true"),
			"kind":    tools.Str("Only symbols of this kind: function, method, class, interface, type, enum, trait, struct"),
			"type":    tools.Str("Only files of this category: test, component, config, types, style"),
			"limit":   tools.Int("Maximum symbols (default 20)"),
			"concept": tools.Bool("Search by concept over symbol names, file paths and doc summaries (default false; also used when no name matches)"),
			"callees": tools.Bool("List the function definitions each matched function calls (default false)"),
			"callers": tools.Bool("List the functions that call each matched function (default false)"),
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
	if hints := m.CloneHints(im.File, 6); len(hints) > 0 {
		sb.WriteString("Duplication:\n")
		for _, h := range hints {
			fmt.Fprintf(&sb, "  %s\n", h)
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
