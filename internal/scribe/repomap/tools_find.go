package repomap

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Limits for find_symbol output (spec 04 2.6): per file, up to this many
// matched symbols, then up to extraFileSyms other exported symbols of the file.
const (
	perFileMatches = 4
	extraFileSyms  = 3
)

// fileCategories are the values accepted by the type filter.
var fileCategories = map[string]bool{catTest: true, catComponent: true, catConfig: true, catTypes: true, catStyle: true}

// runFindSymbol answers find_symbol. Name matches are filtered, ranked and
// grouped by file. When no name matches, or concept is set, the concept search
// runs instead.
func runFindSymbol(m *Map, args json.RawMessage) tools.Result {
	var a struct {
		Query   string `json:"query"`
		Kind    string `json:"kind"`
		Type    string `json:"type"`
		Limit   int    `json:"limit"`
		Concept bool   `json:"concept"`
		Callees bool   `json:"callees"`
		Callers bool   `json:"callers"`
	}
	if err := tools.Decode(args, &a); err != nil {
		return tools.Errorf("%v", err)
	}
	q := strings.TrimSpace(a.Query)
	if q == "" {
		return tools.Errorf("query is required. Pass a symbol name or fragment, or a phrase with concept true.")
	}
	kind, ok := NormalizeKind(a.Kind)
	if !ok {
		return tools.Errorf("unknown kind %q. Use one of: function, method, class, interface, type, enum, trait, struct.", a.Kind)
	}
	typ := strings.ToLower(strings.TrimSpace(a.Type))
	if typ != "" && !fileCategories[typ] {
		return tools.Errorf("unknown type %q. Use one of: test, component, config, types, style.", a.Type)
	}
	if a.Limit <= 0 {
		a.Limit = 20
	}
	keep := func(s Symbol) bool {
		return kindMatches(s.Kind, kind) && (typ == "" || FileCategory(s.File) == typ)
	}

	var hits []SymbolHit
	note := ""
	if !a.Concept {
		for _, h := range m.SearchSymbols(q, len(m.syms)+1) {
			if keep(h.Symbol) {
				hits = append(hits, h)
			}
		}
		hits = m.RankSymbolMatches(hits)
	}
	if a.Concept || len(hits) == 0 {
		ch, _ := m.ConceptSearch(q, a.Limit*4)
		for _, c := range ch {
			if keep(c.Symbol) {
				hits = append(hits, c.SymbolHit)
			}
		}
		if !a.Concept && len(hits) > 0 {
			note = "No name matches. Closest by concept:\n"
		}
	}
	files := m.FindFile(q, 3)
	if len(hits) == 0 {
		msg := fmt.Sprintf("no symbols match %q. Try a shorter fragment, concept true for a phrase, or grep for the text.", q)
		if len(files) > 0 {
			msg += " Matching files: " + joinPaths(files)
		}
		return tools.Result{Output: msg, Summary: "0 matches"}
	}
	if len(hits) > a.Limit {
		hits = hits[:a.Limit]
	}

	// Group by file in order of first appearance, with a per-file cap.
	var order []string
	byFile := map[string][]SymbolHit{}
	for _, h := range hits {
		if _, seen := byFile[h.File]; !seen {
			order = append(order, h.File)
		}
		byFile[h.File] = append(byFile[h.File], h)
	}
	var sb strings.Builder
	sb.WriteString(note)
	for _, f := range order {
		cat := FileCategory(f)
		if cat != "" {
			fmt.Fprintf(&sb, "%s [%s]\n", f, cat)
		} else {
			fmt.Fprintf(&sb, "%s\n", f)
		}
		fhits := byFile[f]
		shown := map[string]bool{}
		for i, h := range fhits {
			if i == perFileMatches {
				fmt.Fprintf(&sb, "  ... %d more in this file\n", len(fhits)-perFileMatches)
				break
			}
			shown[fmt.Sprintf("%d", h.Line)] = true
			writeHit(&sb, m, h, a.Callees, a.Callers)
		}
		extras := 0
		syms, _ := m.FileSymbols(f)
		for _, s := range syms {
			if extras == extraFileSyms {
				break
			}
			if !s.Exported || !keep(s) || shown[fmt.Sprintf("%d", s.Line)] || isFuncKind(s.Kind) && s.Parent != "" {
				continue
			}
			if extras == 0 {
				sb.WriteString("  also:")
			}
			fmt.Fprintf(&sb, " %s :%d", s.Name, s.Line)
			extras++
		}
		if extras > 0 {
			sb.WriteByte('\n')
		}
	}
	if len(files) > 0 && len(order) < 3 {
		sb.WriteString("Files named like the query:\n")
		for _, f := range files {
			fmt.Fprintf(&sb, "  %s\n", f.Path)
		}
	}
	return tools.Result{Output: sb.String(), Summary: fmt.Sprintf("%d symbols in %d files", len(hits), len(order))}
}

func writeHit(sb *strings.Builder, m *Map, h SymbolHit, callees, callers bool) {
	fmt.Fprintf(sb, "  %s %s  :%d", h.Kind, h.Name, h.Line)
	if h.Parent != "" {
		fmt.Fprintf(sb, "  (in %s)", h.Parent)
	}
	if h.Signature != "" {
		fmt.Fprintf(sb, "\n      %s", h.Signature)
	}
	sb.WriteByte('\n')
	if !isFuncKind(h.Kind) {
		return
	}
	if callers {
		cs := m.Callers(h.Name)
		if len(cs) == 0 {
			sb.WriteString("      called by: none found in the code map\n")
		}
		for i, c := range cs {
			if i == 5 {
				fmt.Fprintf(sb, "      ... %d more callers\n", len(cs)-5)
				break
			}
			fmt.Fprintf(sb, "      called by %s  %s:%d\n", c.Name, c.File, c.Line)
		}
	}
	if callees {
		cs := m.Callees(h.File, h.Line)
		if len(cs) == 0 {
			sb.WriteString("      calls: none found in the code map\n")
		}
		for _, c := range cs {
			fmt.Fprintf(sb, "      calls %s  %s:%d\n", c.Name, c.File, c.Line)
		}
	}
}

// actionAliases maps the action names a model may type to the canonical
// name. Keys are lower-case with '-' and ' ' replaced by '_'.
var actionAliases = map[string]string{
	"": "map", "map": "map", "render": "map", "overview": "map", "code_map": "map", "ranked": "map",
	"identifier_frequency": "identifier_frequency", "identifiers": "identifier_frequency", "frequency": "identifier_frequency", "frequent_identifiers": "identifier_frequency",
	"unused_exports": "unused_exports", "unused": "unused_exports", "dead_exports": "unused_exports", "dead": "unused_exports", "unused_export": "unused_exports",
	"packages": "packages", "package": "packages", "external_imports": "packages", "external": "packages", "imports": "packages",
	"symbols_by_kind": "symbols_by_kind", "by_kind": "symbols_by_kind", "kinds": "symbols_by_kind", "symbols_of_kind": "symbols_by_kind",
	"class_members": "class_members", "members": "class_members", "methods": "class_members", "member": "class_members",
	"top_files": "top_files", "top": "top_files", "pagerank": "top_files", "ranking": "top_files",
	"file_profile": "file_profile", "profile": "file_profile", "file_info": "file_profile",
	"duplication": "duplication", "duplicates": "duplication", "duplicate": "duplication", "clones": "duplication", "clone": "duplication",
	"call_graph": "call_graph", "callgraph": "call_graph", "callers": "call_graph", "callees": "call_graph", "calls": "call_graph",
	"search_symbols": "search_symbols", "symbol_search": "search_symbols", "prefix": "search_symbols", "search": "search_symbols",
	"summaries": "summaries", "summary": "summaries",
}

// codeMapActions lists the canonical actions for error messages.
const codeMapActions = "map, identifier_frequency, unused_exports, packages, symbols_by_kind, class_members, top_files, file_profile, duplication, call_graph, search_symbols, summaries"

// runCodeMap dispatches code_map. With no action it renders the map, as it
// always did; the other actions answer one question each.
func runCodeMap(m *Map, args json.RawMessage) tools.Result {
	var a struct {
		Action     string   `json:"action"`
		Focus      []string `json:"focus"`
		Query      string   `json:"query"`
		Budget     int      `json:"budget"`
		Name       string   `json:"name"`
		Symbol     string   `json:"symbol"`
		Identifier string   `json:"identifier"`
		Class      string   `json:"class"`
		File       string   `json:"file"`
		Path       string   `json:"path"`
		Kind       string   `json:"kind"`
		Limit      int      `json:"limit"`
	}
	if err := tools.Decode(args, &a); err != nil {
		return tools.Errorf("%v", err)
	}
	key := strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(a.Action)))
	action, ok := actionAliases[key]
	if !ok {
		return tools.Errorf("unknown action %q. Use one of: %s.", a.Action, codeMapActions)
	}
	name := firstNonEmpty(a.Name, a.Symbol, a.Identifier, a.Class)
	file := firstNonEmpty(a.File, a.Path)
	switch action {
	case "map":
		return runMapRender(m, a.Budget, a.Focus, a.Query)
	case "identifier_frequency":
		return actIdentFreq(m, a.Limit)
	case "unused_exports":
		return actUnusedExports(m, a.Limit)
	case "packages":
		return actPackages(m, name, a.Limit)
	case "symbols_by_kind":
		return actByKind(m, a.Kind, a.Limit)
	case "class_members":
		if name == "" {
			return tools.Errorf("name is required for class_members. Pass the type name, for example Server.")
		}
		return actClassMembers(m, name)
	case "top_files":
		return actTopFiles(m, a.Limit)
	case "file_profile":
		if file == "" {
			return tools.Errorf("file is required for file_profile. Pass a repo-relative path from code_map.")
		}
		return actFileProfile(m, file)
	case "duplication":
		return actDuplication(m, a.Limit)
	case "call_graph":
		if name == "" {
			return tools.Errorf("name is required for call_graph. Pass the function name.")
		}
		return actCallGraph(m, name, a.Limit)
	case "search_symbols":
		if name == "" {
			return tools.Errorf("name is required for search_symbols. Pass a name prefix, for example build*.")
		}
		return actSearchPrefix(m, name, a.Limit)
	case "summaries":
		return actSummaries(m, a.Limit)
	}
	return tools.Errorf("unhandled action %q", action)
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func runMapRender(m *Map, budget int, focus []string, query string) tools.Result {
	if budget <= 0 {
		budget = 2500
	}
	if budget > 4000 {
		budget = 4000
	}
	if q := strings.TrimSpace(query); q != "" {
		for _, h := range m.FindFile(q, 5) {
			focus = append(focus, h.Path)
		}
		for _, h := range m.SearchSymbols(q, 10) {
			focus = append(focus, h.File)
		}
	}
	out := m.Render(budget, focus)
	if out == "" {
		return tools.Errorf("the code map is empty. The repository may have no supported source files, or it has not been built yet.")
	}
	return tools.Result{Output: out, Summary: fmt.Sprintf("%d focus files", len(focus))}
}

func capLimit(n, def int) int {
	if n <= 0 {
		return def
	}
	return n
}

func actIdentFreq(m *Map, limit int) tools.Result {
	rows := m.IdentifierFrequency(capLimit(limit, 20))
	if len(rows) == 0 {
		return tools.Result{Output: "no defined names are used by the map.", Summary: "0 names"}
	}
	var sb strings.Builder
	sb.WriteString("Most widely used defined names (files, uses, defined in):\n")
	for _, r := range rows {
		fmt.Fprintf(&sb, "  %s  %d files, %d uses  %s\n", r.Name, r.Files, r.Uses, r.Defined)
	}
	return tools.Result{Output: sb.String(), Summary: fmt.Sprintf("%d names", len(rows))}
}

func actUnusedExports(m *Map, limit int) tools.Result {
	rep := m.UnusedExports()
	limit = capLimit(limit, 50)
	var sb strings.Builder
	fmt.Fprintf(&sb, "Unused exports (%d): no other file mentions them. Methods are not checked.\n", len(rep.Unused))
	for i, s := range rep.Unused {
		if i == limit {
			fmt.Fprintf(&sb, "  ... %d more\n", len(rep.Unused)-limit)
			break
		}
		fmt.Fprintf(&sb, "  %s %s  %s:%d\n", s.Kind, s.Name, s.File, s.Line)
	}
	fmt.Fprintf(&sb, "Test-only exports (%d): only test files mention them.\n", len(rep.TestOnly))
	for i, u := range rep.TestOnly {
		if i == limit {
			fmt.Fprintf(&sb, "  ... %d more\n", len(rep.TestOnly)-limit)
			break
		}
		fmt.Fprintf(&sb, "  %s %s  %s:%d  used by %s\n", u.Symbol.Kind, u.Symbol.Name, u.Symbol.File, u.Symbol.Line, strings.Join(u.Users, ", "))
	}
	return tools.Result{Output: sb.String(), Summary: fmt.Sprintf("%d unused, %d test-only", len(rep.Unused), len(rep.TestOnly))}
}

func actPackages(m *Map, filter string, limit int) tools.Result {
	rows := m.ExternalPackages(filter, capLimit(limit, 30))
	if len(rows) == 0 {
		return tools.Result{Output: "no external imports found for that filter.", Summary: "0 packages"}
	}
	var sb strings.Builder
	sb.WriteString("External imports (not resolved to a repo file), most used first:\n")
	for _, r := range rows {
		fmt.Fprintf(&sb, "  %s  %d files", r.Spec, len(r.Files))
		if len(r.Files) > 0 {
			n := min(3, len(r.Files))
			fmt.Fprintf(&sb, "  e.g. %s", strings.Join(r.Files[:n], ", "))
		}
		sb.WriteByte('\n')
	}
	return tools.Result{Output: sb.String(), Summary: fmt.Sprintf("%d packages", len(rows))}
}

func actByKind(m *Map, kind string, limit int) tools.Result {
	if strings.TrimSpace(kind) == "" {
		var sb strings.Builder
		sb.WriteString("Symbols by kind. Pass kind to list one:\n")
		for _, k := range m.KindCounts() {
			fmt.Fprintf(&sb, "  %s  %d\n", k.Kind, k.Count)
		}
		return tools.Result{Output: sb.String(), Summary: "kind counts"}
	}
	norm, ok := NormalizeKind(kind)
	if !ok {
		return tools.Errorf("unknown kind %q. Use one of: function, method, class, interface, type, enum, trait, struct.", kind)
	}
	syms := m.SymbolsOfKind(norm, capLimit(limit, 100))
	if len(syms) == 0 {
		return tools.Result{Output: fmt.Sprintf("no %s symbols in the map.", norm), Summary: "0 symbols"}
	}
	var sb strings.Builder
	for _, s := range syms {
		fmt.Fprintf(&sb, "  %s %s  %s:%d\n", s.Kind, s.Name, s.File, s.Line)
	}
	return tools.Result{Output: sb.String(), Summary: fmt.Sprintf("%d %s symbols", len(syms), norm)}
}

func actClassMembers(m *Map, name string) tools.Result {
	owners, members := m.ClassMembers(name)
	if len(owners) == 0 && len(members) == 0 {
		return tools.Result{Output: fmt.Sprintf("no type named %q in the map. Use find_symbol to search for it.", name), Summary: "0 members"}
	}
	var sb strings.Builder
	for _, o := range owners {
		fmt.Fprintf(&sb, "%s %s  %s:%d\n", o.Kind, o.Name, o.File, o.Line)
	}
	fmt.Fprintf(&sb, "Members (%d):\n", len(members))
	for _, s := range members {
		sig := s.Signature
		if sig == "" {
			sig = s.Kind + " " + s.Name
		}
		fmt.Fprintf(&sb, "  %s  %s:%d\n", sig, s.File, s.Line)
	}
	return tools.Result{Output: sb.String(), Summary: fmt.Sprintf("%d members", len(members))}
}

func actTopFiles(m *Map, limit int) tools.Result {
	rows := m.TopFiles(capLimit(limit, 15))
	var sb strings.Builder
	sb.WriteString("Files by importance (PageRank), with symbols and importers:\n")
	for _, r := range rows {
		fmt.Fprintf(&sb, "  %s  %d symbols, %d importers\n", r.Path, r.Symbols, r.Importers)
	}
	return tools.Result{Output: sb.String(), Summary: fmt.Sprintf("top %d files", len(rows))}
}

func actFileProfile(m *Map, file string) tools.Result {
	fp, err := m.Profile(file)
	if err != nil {
		return tools.Errorf("%v. Use code_map or find_symbol to get the exact repo-relative path.", err)
	}
	im := fp.Impact
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s", fp.File)
	if fp.Category != "" {
		fmt.Fprintf(&sb, " [%s]", fp.Category)
	}
	sb.WriteByte('\n')
	fmt.Fprintf(&sb, "Blast radius: %d direct importers, %d further. %s\n", im.Direct, im.Transitive, im.Risk)
	fmt.Fprintf(&sb, "Imports (%d): %s\n", len(fp.Imports), strings.Join(fp.Imports, ", "))
	fmt.Fprintf(&sb, "Imported by (%d):", im.Direct)
	for _, d := range im.Dependents {
		if d.Level == 1 {
			fmt.Fprintf(&sb, " %s", d.Path)
		}
	}
	sb.WriteByte('\n')
	if len(fp.CoChanges) > 0 {
		sb.WriteString("Changes together with it:")
		for _, c := range fp.CoChanges {
			fmt.Fprintf(&sb, " %s (%d)", c.File, c.Count)
		}
		sb.WriteByte('\n')
	}
	if len(fp.Tests) > 0 {
		fmt.Fprintf(&sb, "Tests: %s\n", strings.Join(fp.Tests, ", "))
	}
	if fp.Summary != "" {
		fmt.Fprintf(&sb, "Summary: %s\n", fp.Summary)
	}
	fmt.Fprintf(&sb, "Symbols (%d):\n", len(fp.Symbols))
	for i, s := range fp.Symbols {
		if i == 40 {
			fmt.Fprintf(&sb, "  ... %d more. Use file_outline for all of them.\n", len(fp.Symbols)-40)
			break
		}
		sig := s.Signature
		if sig == "" {
			sig = s.Kind + " " + s.Name
		}
		fmt.Fprintf(&sb, "  L%d-%d  %s\n", s.Line, s.EndLine, sig)
	}
	for _, c := range fp.Clones {
		fmt.Fprintf(&sb, "Duplication: %s\n", c)
	}
	return tools.Result{Output: sb.String(), Summary: fmt.Sprintf("%d symbols, %d direct importers", len(fp.Symbols), im.Direct)}
}

func actDuplication(m *Map, limit int) tools.Result {
	rep := m.Clones()
	exact := m.Duplicates()
	limit = capLimit(limit, 10)
	var sb strings.Builder
	fmt.Fprintf(&sb, "Near-duplicate files (%d):\n", len(rep.NearDup))
	for i, p := range rep.NearDup {
		if i == limit {
			break
		}
		fmt.Fprintf(&sb, "  %s ~ %s  (%d%% similar)\n", p.A, p.B, int(p.Similarity*100+0.5))
	}
	fmt.Fprintf(&sb, "Repeated fragments (%d pairs):\n", len(rep.Fragments))
	for i, p := range rep.Fragments {
		if i == limit {
			break
		}
		fmt.Fprintf(&sb, "  %s ~ %s  (%d shared)\n", p.A, p.B, p.Shared)
	}
	fmt.Fprintf(&sb, "Identical functions (%d groups):\n", len(exact))
	for i, g := range exact {
		if i == limit {
			break
		}
		var names []string
		for _, s := range g.Symbols {
			names = append(names, fmt.Sprintf("%s %s:%d", s.Name, s.File, s.Line))
		}
		fmt.Fprintf(&sb, "  %s\n", strings.Join(names, " = "))
	}
	fmt.Fprintf(&sb, "Same structure, different names (%d groups):\n", len(rep.Shapes))
	for i, g := range rep.Shapes {
		if i == limit {
			break
		}
		var names []string
		for _, s := range g.Symbols {
			names = append(names, fmt.Sprintf("%s %s:%d", s.Name, s.File, s.Line))
		}
		fmt.Fprintf(&sb, "  %s\n", strings.Join(names, " ~ "))
	}
	return tools.Result{Output: sb.String(), Summary: fmt.Sprintf("%d near-duplicate files", len(rep.NearDup))}
}

func actCallGraph(m *Map, name string, limit int) tools.Result {
	callers, callees := m.CallGraph(name, capLimit(limit, 25))
	if len(callers) == 0 && len(callees) == 0 {
		return tools.Result{Output: fmt.Sprintf("no calls to or from %q in the map. Check the name with find_symbol.", name), Summary: "0 calls"}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Called by (%d):\n", len(callers))
	for _, c := range callers {
		fmt.Fprintf(&sb, "  %s  %s:%d\n", c.Name, c.File, c.Line)
	}
	fmt.Fprintf(&sb, "Calls (%d):\n", len(callees))
	for _, c := range callees {
		fmt.Fprintf(&sb, "  %s  %s:%d\n", c.Name, c.File, c.Line)
	}
	return tools.Result{Output: sb.String(), Summary: fmt.Sprintf("%d callers, %d callees", len(callers), len(callees))}
}

func actSearchPrefix(m *Map, prefix string, limit int) tools.Result {
	hits := m.SearchPrefix(prefix, capLimit(limit, 30))
	if len(hits) == 0 {
		return tools.Result{Output: fmt.Sprintf("no symbol names start with %q.", prefix), Summary: "0 matches"}
	}
	var sb strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&sb, "%s %s  %s:%d\n", h.Kind, h.Name, h.File, h.Line)
	}
	return tools.Result{Output: sb.String(), Summary: fmt.Sprintf("%d symbols", len(hits))}
}

func actSummaries(m *Map, limit int) tools.Result {
	rows := m.Summaries(capLimit(limit, 50))
	if len(rows) == 0 {
		return tools.Result{Output: "no model summaries are current. They are written in the background when a summarizer is set.", Summary: "0 summaries"}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Path < rows[j].Path })
	var sb strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&sb, "%s - %s\n", r.Path, r.Text)
	}
	return tools.Result{Output: sb.String(), Summary: fmt.Sprintf("%d summaries", len(rows))}
}
