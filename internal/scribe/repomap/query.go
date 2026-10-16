package repomap

import (
	"fmt"
	"math"
	"os"
	"path"
	"sort"
	"strings"
)

// Ref is one place an identifier is used.
type Ref struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// SymbolHit is a search result for a symbol.
type SymbolHit struct {
	Symbol
	Score float64 `json:"score"`
	Rank  float64 `json:"rank"`
}

// FileHit is a search result for a file.
type FileHit struct {
	Path  string  `json:"path"`
	Score float64 `json:"score"`
	Rank  float64 `json:"rank"`
}

// Dependent is a file that depends on the impact target, directly (Level 1)
// or through one intermediate file (Level 2).
type Dependent struct {
	Path  string  `json:"path"`
	Level int     `json:"level"`
	Rank  float64 `json:"rank"`
}

// Impact describes what a change to one file touches.
type Impact struct {
	File       string      `json:"file"`
	Dependents []Dependent `json:"dependents"`
	Direct     int         `json:"direct"`
	Transitive int         `json:"transitive"`
	CoChanged  []Pair      `json:"co_changed"`
	Risk       string      `json:"risk"`
}

// DupGroup is a set of functions whose bodies are identical once whitespace
// is removed.
type DupGroup struct {
	Hash    string   `json:"hash"`
	Symbols []Symbol `json:"symbols"`
}

// Stats summarises the map.
type Stats struct {
	Files     int            `json:"files"`
	Symbols   int            `json:"symbols"`
	Edges     int            `json:"edges"`
	Languages map[string]int `json:"languages"`
}

// Stats returns counts for the current map.
func (m *Map) Stats() Stats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st := Stats{Files: len(m.paths), Symbols: len(m.syms), Edges: m.edgeCount, Languages: map[string]int{}}
	for _, fi := range m.files {
		st.Languages[fi.Lang]++
	}
	return st
}

// CoChange returns files that changed in the same commits as file, most
// frequent first.
func (m *Map) CoChange(file string) []Pair {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rel := m.relPath(file)
	return append([]Pair(nil), m.cochange[rel]...)
}

// FileSymbols returns the symbols of one file in line order.
func (m *Map) FileSymbols(file string) ([]Symbol, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	syms, ok := m.fileSyms[m.relPath(file)]
	if !ok {
		return nil, false
	}
	out := append([]Symbol(nil), syms...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out, true
}

// Definition returns the symbols named name. Exact matches win; otherwise a
// case-insensitive match is tried.
func (m *Map) Definition(name string) []Symbol {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if s := m.symsByName[name]; len(s) > 0 {
		return append([]Symbol(nil), s...)
	}
	var out []Symbol
	for _, s := range m.syms {
		if strings.EqualFold(s.Name, name) {
			out = append(out, s)
		}
	}
	return out
}

// SearchSymbols ranks symbols against query. Exact and prefix matches score
// highest, then substring, then subsequence ("bsrv" matches "BuildServer").
// Multi-word queries require every word to match. Ties go to the more
// important file.
func (m *Map) SearchSymbols(query string, limit int) []SymbolHit {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	if limit <= 0 {
		limit = 20
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var hits []SymbolHit
	for _, s := range m.syms {
		score, ok := nameScore(s.Name, query, q)
		if !ok {
			continue
		}
		if s.Exported {
			score += 2
		}
		rank := m.fileRank(s.File) * (1 + math.Log1p(float64(m.refCount[s.Name])))
		hits = append(hits, SymbolHit{Symbol: s, Score: score, Rank: rank})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].Rank != hits[j].Rank {
			return hits[i].Rank > hits[j].Rank
		}
		if hits[i].File != hits[j].File {
			return hits[i].File < hits[j].File
		}
		return hits[i].Line < hits[j].Line
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

func nameScore(name, raw, q string) (float64, bool) {
	ln := strings.ToLower(name)
	switch {
	case name == raw:
		return 100, true
	case ln == q:
		return 95, true
	case strings.HasPrefix(ln, q):
		return 80, true
	case strings.Contains(ln, q):
		return 60, true
	case isSubsequence(q, ln):
		return 30, true
	}
	words := strings.Fields(q)
	if len(words) < 2 {
		return 0, false
	}
	total := 0.0
	for _, w := range words {
		switch {
		case strings.Contains(ln, w):
			total += 50
		case isSubsequence(w, ln):
			total += 25
		default:
			return 0, false
		}
	}
	return total / float64(len(words)), true
}

func isSubsequence(q, s string) bool {
	if q == "" {
		return true
	}
	i := 0
	for j := 0; j < len(s) && i < len(q); j++ {
		if s[j] == q[i] {
			i++
		}
	}
	return i == len(q)
}

// FindFile ranks repository files against query by base name first, then by
// full path.
func (m *Map) FindFile(query string, limit int) []FileHit {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	if limit <= 0 {
		limit = 20
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var hits []FileHit
	for _, p := range m.paths {
		lp := strings.ToLower(p)
		base := strings.ToLower(path.Base(p))
		var score float64
		switch {
		case base == q:
			score = 100
		case strings.HasPrefix(base, q):
			score = 80
		case strings.Contains(base, q):
			score = 60
		case strings.Contains(lp, q):
			score = 40
		case isSubsequence(q, base):
			score = 25
		case isSubsequence(q, lp):
			score = 15
		default:
			continue
		}
		hits = append(hits, FileHit{Path: p, Score: score, Rank: m.fileRank(p)})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Rank > hits[j].Rank
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// References returns every use of the identifier name in indexed files, in
// file and line order. Strings and comments are skipped.
func (m *Map) References(name string) ([]Ref, error) {
	m.mu.RLock()
	var cands []string
	for _, p := range m.paths {
		if m.files[p].Idents[name] > 0 {
			cands = append(cands, p)
		}
	}
	m.mu.RUnlock()
	var out []Ref
	for _, p := range cands {
		lg := langFor(p)
		src, err := os.ReadFile(m.root + "/" + p)
		if err != nil || lg == nil {
			continue
		}
		masked := mask(src, &lg.lex, false)
		li := newLineIndex(masked)
		lines := strings.Split(string(src), "\n")
		for i := 0; i < len(masked); {
			if !isIdentStart(masked[i]) {
				i++
				continue
			}
			j := i
			for j < len(masked) && isIdentPart(masked[j]) {
				j++
			}
			if string(masked[i:j]) == name {
				ln := li.lineOf(i)
				text := ""
				if ln-1 < len(lines) {
					text = strings.TrimSpace(lines[ln-1])
				}
				if len(text) > 160 {
					text = text[:160] + "..."
				}
				out = append(out, Ref{File: p, Line: ln, Text: text})
			}
			i = j
		}
	}
	return out, nil
}

// Impact reports what depends on file: direct importers, importers of those
// (two levels), and files that change with it.
func (m *Map) Impact(file string) (Impact, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rel := m.relPath(file)
	if _, ok := m.nodeIdx[rel]; !ok {
		return Impact{}, fmt.Errorf("%q is not in the code map", file)
	}
	im := Impact{File: rel}
	direct := sortByRank(m, m.in[rel])
	seen := map[string]bool{rel: true}
	for _, d := range direct {
		seen[d] = true
		im.Dependents = append(im.Dependents, Dependent{Path: d, Level: 1, Rank: m.fileRank(d)})
	}
	im.Direct = len(direct)
	var second []string
	for _, d := range direct {
		for _, x := range m.in[d] {
			if !seen[x] {
				seen[x] = true
				second = append(second, x)
			}
		}
	}
	second = sortByRank(m, second)
	im.Transitive = len(second)
	for _, x := range second {
		im.Dependents = append(im.Dependents, Dependent{Path: x, Level: 2, Rank: m.fileRank(x)})
	}
	im.CoChanged = append([]Pair(nil), m.cochange[rel]...)
	if len(im.CoChanged) > 10 {
		im.CoChanged = im.CoChanged[:10]
	}
	switch {
	case im.Direct > 10:
		im.Risk = "high: more than 10 files import this one; check callers before changing exported symbols"
	case im.Direct >= 3:
		im.Risk = "medium: several files import this one"
	default:
		im.Risk = "low: few or no importers"
	}
	return im, nil
}

func sortByRank(m *Map, files []string) []string {
	out := append([]string(nil), files...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := m.fileRank(out[i]), m.fileRank(out[j])
		if ri != rj {
			return ri > rj
		}
		return out[i] < out[j]
	})
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}

// Duplicates returns groups of functions with identical normalized bodies.
func (m *Map) Duplicates() []DupGroup {
	m.mu.RLock()
	defer m.mu.RUnlock()
	by := map[string][]Symbol{}
	for _, s := range m.syms {
		if s.BodyHash != "" {
			by[s.BodyHash] = append(by[s.BodyHash], s)
		}
	}
	var out []DupGroup
	for h, ss := range by {
		if len(ss) >= 2 {
			out = append(out, DupGroup{Hash: h, Symbols: ss})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Symbols) != len(out[j].Symbols) {
			return len(out[i].Symbols) > len(out[j].Symbols)
		}
		return out[i].Symbols[0].File < out[j].Symbols[0].File
	})
	return out
}

// fileRank returns the PageRank of a file in the base graph (0 if unknown).
// Callers hold m.mu.
func (m *Map) fileRank(p string) float64 {
	i, ok := m.nodeIdx[p]
	if !ok || i >= len(m.rank) {
		return 0
	}
	return m.rank[i]
}

// Render produces the compact map the model reads: top files by rank with
// their key symbols as one-line signatures, cut to budgetTokens (estimated as
// characters/4). focus files, and files updated with Update, are personalized
// into the ranking.
func (m *Map) Render(budgetTokens int, focus []string) string {
	if budgetTokens <= 0 {
		budgetTokens = 2500
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.paths) == 0 {
		return ""
	}
	rank := m.rank
	var fx []string
	inFocus := map[string]bool{}
	for _, f := range append(append([]string(nil), focus...), m.recent...) {
		if _, ok := m.nodeIdx[m.relPath(f)]; ok && !inFocus[m.relPath(f)] {
			inFocus[m.relPath(f)] = true
			fx = append(fx, m.relPath(f))
		}
	}
	if len(fx) > 0 {
		rank = m.pagerank(m.focusVector(fx))
	}
	type cand struct {
		i int
		r float64
	}
	var order []cand
	for i, p := range m.paths {
		if len(m.fileSyms[p]) == 0 && !inFocus[p] {
			continue
		}
		order = append(order, cand{i, rank[i]})
	}
	sort.SliceStable(order, func(a, b int) bool {
		if order[a].r != order[b].r {
			return order[a].r > order[b].r
		}
		return m.paths[order[a].i] < m.paths[order[b].i]
	})
	limit := budgetTokens / 40
	if limit < 100 {
		limit = 100
	}
	if len(order) > limit {
		order = order[:limit]
	}
	var sb strings.Builder
	header := fmt.Sprintf("Code map: %d files, %d symbols\n", len(m.paths), len(m.syms))
	used := estTokens(header)
	if used > budgetTokens {
		return ""
	}
	sb.WriteString(header)
	for _, c := range order {
		block := m.renderBlock(m.paths[c.i])
		t := estTokens(block)
		if used+t > budgetTokens {
			break // keep rank order: stop at the first block that does not fit
		}
		used += t
		sb.WriteString(block)
	}
	return sb.String()
}

func (m *Map) renderBlock(p string) string {
	var sb strings.Builder
	sb.WriteString(p)
	if deps := len(m.in[p]); deps >= 2 {
		fmt.Fprintf(&sb, " (→%d)", deps)
	}
	sb.WriteByte('\n')
	syms := append([]Symbol(nil), m.fileSyms[p]...)
	sort.SliceStable(syms, func(i, j int) bool { return syms[i].Line < syms[j].Line })
	var shown []Symbol
	for _, s := range syms {
		if s.Exported {
			shown = append(shown, s)
		}
	}
	more := 0
	if len(shown) == 0 {
		for _, s := range syms {
			if len(shown) == 3 {
				break
			}
			shown = append(shown, s)
		}
	}
	const maxLines = 12
	if len(shown) > maxLines {
		more = len(shown) - maxLines
		shown = shown[:maxLines]
	}
	for _, s := range shown {
		sig := s.Signature
		if sig == "" {
			sig = s.Kind + " " + s.Name
		}
		if len(sig) > 120 {
			sig = sig[:120] + "..."
		}
		fmt.Fprintf(&sb, "  %s :%d\n", sig, s.Line)
	}
	if more > 0 {
		fmt.Fprintf(&sb, "  ... +%d more\n", more)
	}
	return sb.String()
}

func estTokens(s string) int { return (len(s) + 3) / 4 }
