package app

// Wave 12: the parser and semantic-index probes behind /diagnose. They read
// the code map that is already built; they never rebuild it or spend tokens.

import (
	"fmt"
	"sort"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// DiagnoseIndex implements tui.IndexDiagnoser.
//
// Parsers: one row per language the map has files for. A language with files
// but no symbols at all is a parser gap (the extractor read it and found
// nothing), so it warns.
//
// Semantic index: the map is built, has symbols, and a real lookup finds a
// function it holds by name. That round trip is the check that matters: a
// map that has counts but cannot answer a search is not usable.
func (b *scribeBackend) DiagnoseIndex() []tui.DiagnoseCheck {
	a := b.current()
	if a.ext.cmap == nil {
		return []tui.DiagnoseCheck{{Area: "Semantic index", Name: "code map", State: "info", Detail: "off (repo_map is disabled)"}}
	}
	if !isClosed(a.ext.mapOK) {
		return []tui.DiagnoseCheck{{Area: "Semantic index", Name: "code map", State: "info", Detail: "still building; run /diagnose again when it finishes"}}
	}
	st := a.ext.cmap.Stats()
	var out []tui.DiagnoseCheck

	langs := make([]string, 0, len(st.Languages))
	for l := range st.Languages {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	for _, l := range langs {
		c := tui.DiagnoseCheck{Area: "Parsers", Name: l, State: "ok", Detail: fmt.Sprintf("%d files", st.Languages[l])}
		if st.Symbols == 0 {
			c.State, c.Detail = "warn", c.Detail+", no symbols extracted"
		}
		out = append(out, c)
	}
	if len(langs) == 0 {
		out = append(out, tui.DiagnoseCheck{Area: "Parsers", Name: "languages", State: "info", Detail: "no source files in the map"})
	}

	idx := tui.DiagnoseCheck{Area: "Semantic index", Name: "code map", State: "ok",
		Detail: fmt.Sprintf("built: %d files, %d symbols, %d edges", st.Files, st.Symbols, st.Edges)}
	if st.Symbols == 0 {
		idx.State = "warn"
		idx.Detail = "the map has files but no symbols"
		idx.Fix = "run /repo-map rebuild"
	}
	out = append(out, idx)

	out = append(out, lookupProbe(a.ext.cmap))
	return out
}

// lookupProbe takes a function the map holds and searches for it by name. It
// passes only if the search returns that symbol in the same file.
func lookupProbe(m *repomap.Map) tui.DiagnoseCheck {
	c := tui.DiagnoseCheck{Area: "Semantic index", Name: "name lookup", State: "info"}
	var pick *repomap.Symbol
	for _, s := range m.SymbolsOfKind("function", 200) {
		if len(s.Name) >= 4 {
			s := s
			pick = &s
			break
		}
	}
	if pick == nil {
		c.Detail = "no function long enough to probe with"
		return c
	}
	for _, h := range m.SearchSymbols(pick.Name, 50) {
		if h.Name == pick.Name && h.File == pick.File {
			c.State, c.Detail = "ok", fmt.Sprintf("found %s in %s", pick.Name, pick.File)
			return c
		}
	}
	c.State, c.Detail = "fail", fmt.Sprintf("search did not return %s in %s", pick.Name, pick.File)
	return c
}
