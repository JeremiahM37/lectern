package repomap

import (
	"sort"
	"strings"
)

// Analysis queries behind the code_map actions (spec 04 section 2.8). They
// read the in-memory map, and the derived indexes they need are built on
// first use, so none of them adds to a build. Each one takes the read lock.

// IdentCount is how widely a defined name is used.
type IdentCount struct {
	Name    string
	Files   int // files that mention the name
	Uses    int // total mentions, outside strings and comments
	Defined string
}

// IdentifierFrequency returns the most widely used names that the map
// defines, most files first. Names the map does not define (locals, library
// calls) are left out, because they would drown the list.
func (m *Map) IdentifierFrequency(limit int) []IdentCount {
	if limit <= 0 {
		limit = 20
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	acc := map[string]*IdentCount{}
	for _, p := range m.paths {
		fi := m.files[p]
		if fi == nil {
			continue
		}
		for name, n := range fi.Idents {
			defs, ok := m.symsByName[name]
			if !ok {
				continue
			}
			c := acc[name]
			if c == nil {
				c = &IdentCount{Name: name, Defined: defs[0].File}
				acc[name] = c
			}
			c.Files++
			c.Uses += n
		}
	}
	out := make([]IdentCount, 0, len(acc))
	for _, c := range acc {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Files != out[j].Files {
			return out[i].Files > out[j].Files
		}
		if out[i].Uses != out[j].Uses {
			return out[i].Uses > out[j].Uses
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ExportReport is the result of the dead-export check.
type ExportReport struct {
	Unused   []Symbol    // exported, and no other file mentions the name
	TestOnly []ExportUse // exported, and only test files mention the name
}

// ExportUse is an export whose only users are tests.
type ExportUse struct {
	Symbol Symbol
	Users  []string
}

// UnusedExports reports exported functions, types, variables and constants
// that no other file mentions. Methods are not checked, because they are
// reached through interfaces and by name the map cannot see. Entry points
// (main, init) and underscore names are skipped, as are symbols in test files.
// Like everything name-based here, it can miss a use that is spelled
// differently, so it is a list to review, not a deletion list.
func (m *Map) UnusedExports() ExportReport {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cands := map[string][]Symbol{}
	for _, s := range m.syms {
		if !s.Exported || s.Parent != "" || s.Kind == "module" {
			continue
		}
		if s.Name == "main" || s.Name == "init" || strings.HasPrefix(s.Name, "_") || isTestPath(s.File) {
			continue
		}
		cands[s.Name] = append(cands[s.Name], s)
	}
	users := map[string]map[string]bool{}
	for _, p := range m.paths {
		fi := m.files[p]
		if fi == nil {
			continue
		}
		for name := range fi.Idents {
			if _, ok := cands[name]; !ok {
				continue
			}
			if users[name] == nil {
				users[name] = map[string]bool{}
			}
			users[name][p] = true
		}
	}
	var rep ExportReport
	for name, ss := range cands {
		for _, s := range ss {
			var other []string
			for u := range users[name] {
				if u != s.File {
					other = append(other, u)
				}
			}
			sort.Strings(other)
			switch {
			case len(other) == 0:
				rep.Unused = append(rep.Unused, s)
			case allTests(other):
				rep.TestOnly = append(rep.TestOnly, ExportUse{Symbol: s, Users: other})
			}
		}
	}
	sortSymbols(rep.Unused)
	sort.Slice(rep.TestOnly, func(i, j int) bool {
		a, b := rep.TestOnly[i].Symbol, rep.TestOnly[j].Symbol
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	return rep
}

func allTests(files []string) bool {
	for _, f := range files {
		if !isTestPath(f) {
			return false
		}
	}
	return len(files) > 0
}

// PackageUse is one external import and the files that use it.
type PackageUse struct {
	Spec  string
	Files []string
}

// ExternalPackages lists imports that do not resolve to a file in the map,
// most used first. For Go that includes the standard library, which is still
// useful for "what depends on X". When filter is set, only specs containing it
// are returned.
func (m *Map) ExternalPackages(filter string, limit int) []PackageUse {
	if limit <= 0 {
		limit = 30
	}
	filter = strings.ToLower(strings.TrimSpace(filter))
	m.mu.RLock()
	defer m.mu.RUnlock()
	ix := newFileIndex(m.root, m.files)
	acc := map[string]map[string]bool{}
	for _, p := range m.paths {
		fi := m.files[p]
		if fi == nil {
			continue
		}
		for _, imp := range fi.Imports {
			if imp.Spec == "" || strings.HasPrefix(imp.Spec, ".") {
				continue
			}
			if len(ix.resolve(p, imp)) > 0 {
				continue
			}
			if filter != "" && !strings.Contains(strings.ToLower(imp.Spec), filter) {
				continue
			}
			if acc[imp.Spec] == nil {
				acc[imp.Spec] = map[string]bool{}
			}
			acc[imp.Spec][p] = true
		}
	}
	out := make([]PackageUse, 0, len(acc))
	for spec, files := range acc {
		pu := PackageUse{Spec: spec}
		for f := range files {
			pu.Files = append(pu.Files, f)
		}
		sort.Strings(pu.Files)
		out = append(out, pu)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Files) != len(out[j].Files) {
			return len(out[i].Files) > len(out[j].Files)
		}
		return out[i].Spec < out[j].Spec
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// KindCount is how many symbols have a kind.
type KindCount struct {
	Kind  string
	Count int
}

// SymbolsOfKind returns symbols of one normalized kind (function includes
// methods), in file and line order, up to limit.
func (m *Map) SymbolsOfKind(kind string, limit int) []Symbol {
	if limit <= 0 {
		limit = 100
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Symbol
	for _, s := range m.syms {
		if kindMatches(s.Kind, kind) {
			out = append(out, s)
		}
	}
	sortSymbols(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// KindCounts counts symbols by kind, most common first.
func (m *Map) KindCounts() []KindCount {
	m.mu.RLock()
	defer m.mu.RUnlock()
	by := map[string]int{}
	for _, s := range m.syms {
		by[s.Kind]++
	}
	out := make([]KindCount, 0, len(by))
	for k, n := range by {
		out = append(out, KindCount{Kind: k, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// ClassMembers returns the members of every type called name, across the
// repository, in file and line order. The owners are the type definitions
// themselves. A case-insensitive match is used when nothing matches exactly.
func (m *Map) ClassMembers(name string) (owners []Symbol, members []Symbol) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	want := func(s Symbol) bool { return s.Name == name }
	if len(m.symsByName[name]) == 0 {
		want = func(s Symbol) bool { return strings.EqualFold(s.Name, name) }
	}
	ownerNames := map[string]bool{}
	for _, s := range m.syms {
		if want(s) && !isFuncKind(s.Kind) && s.Kind != "var" && s.Kind != "const" {
			owners = append(owners, s)
			ownerNames[s.Name] = true
		}
	}
	for _, s := range m.syms {
		if s.Parent != "" && ownerNames[s.Parent] {
			members = append(members, s)
		}
	}
	sortSymbols(owners)
	sortSymbols(members)
	return owners, members
}

// FileRank is one file in the PageRank listing.
type FileRank struct {
	Path      string
	Rank      float64
	Symbols   int
	Importers int
}

// TopFiles returns the files with the highest PageRank.
func (m *Map) TopFiles(limit int) []FileRank {
	if limit <= 0 {
		limit = 15
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]FileRank, 0, len(m.paths))
	for _, p := range m.paths {
		out = append(out, FileRank{Path: p, Rank: m.fileRank(p), Symbols: len(m.fileSyms[p]), Importers: len(m.in[p])})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Rank != out[j].Rank {
			return out[i].Rank > out[j].Rank
		}
		return out[i].Path < out[j].Path
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// FileProfile is everything the map knows about one file, in one place. It
// replaces the separate code_impact and file_outline calls for a quick look.
type FileProfile struct {
	File      string
	Category  string
	Symbols   []Symbol
	Imports   []string
	Impact    Impact
	CoChanges []Pair
	Tests     []string
	Clones    []string
	Summary   string
}

// Profile builds the FileProfile for file. The error is the same "not in the
// map" message Impact gives.
func (m *Map) Profile(file string) (FileProfile, error) {
	im, err := m.Impact(file)
	if err != nil {
		return FileProfile{}, err
	}
	syms, _ := m.FileSymbols(im.File)
	fp := FileProfile{
		File:      im.File,
		Category:  FileCategory(im.File),
		Symbols:   syms,
		Imports:   im.Imports,
		Impact:    im,
		CoChanges: m.CoChange(im.File),
		Tests:     m.TestsOf(im.File),
		Clones:    m.CloneHints(im.File, 6),
	}
	if len(fp.CoChanges) > 5 {
		fp.CoChanges = fp.CoChanges[:5]
	}
	m.mu.RLock()
	fp.Summary = m.summaries[im.File].text
	// Every import as written, including the external ones Impact leaves out.
	fp.Imports = nil
	for _, imp := range m.files[im.File].Imports {
		fp.Imports = append(fp.Imports, imp.Spec)
	}
	m.mu.RUnlock()
	return fp, nil
}

// CallGraph returns the callers and callees of every function called name.
// Callers are name-based, so a same-named function elsewhere can be credited;
// the maxCallerDefs rule keeps that to unambiguous names.
func (m *Map) CallGraph(name string, limit int) (callers, callees []Symbol) {
	if limit <= 0 {
		limit = 50
	}
	callers = m.Callers(name)
	callees = m.CalleesNamed(name)
	if len(callers) > limit {
		callers = callers[:limit]
	}
	if len(callees) > limit {
		callees = callees[:limit]
	}
	return callers, callees
}

// SummaryLine is one model summary, for the summaries action.
type SummaryLine struct {
	Path, Text string
}

// Summaries lists the model summaries that still match their file's content.
func (m *Map) Summaries(limit int) []SummaryLine {
	if limit <= 0 {
		limit = 50
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []SummaryLine
	for _, p := range m.paths {
		e, ok := m.summaries[p]
		if !ok {
			continue
		}
		if fi := m.files[p]; fi == nil || fi.Hash != e.hash {
			continue
		}
		out = append(out, SummaryLine{Path: p, Text: e.text})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
