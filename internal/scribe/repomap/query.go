package repomap

import (
	"fmt"
	"math"
	"os"
	"path"
	"sort"
	"strings"
	"time"
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
	Imports    []string    `json:"imports"`
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

// EnclosingSymbol returns the innermost symbol whose line range contains
// line of file, such as the function a reference sits in.
func (m *Map) EnclosingSymbol(file string, line int) (Symbol, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var best Symbol
	found := false
	for _, s := range m.fileSyms[m.relPath(file)] {
		end := s.EndLine
		if end < s.Line {
			end = s.Line
		}
		if s.Line <= line && line <= end && (!found || s.Line >= best.Line) {
			best, found = s, true
		}
	}
	return best, found
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
	hitFor := func(s Symbol, score float64) SymbolHit {
		if s.Exported {
			score += 2
		}
		rank := m.fileRank(s.File) * (1 + math.Log1p(float64(m.refCount[s.Name])))
		return SymbolHit{Symbol: s, Score: score, Rank: rank}
	}
	// Fast path: a single word of three or more bytes. Every symbol whose name
	// contains q scores at least 60; a subsequence match scores at most 32
	// (before the export bonus). So when the trigram index already yields
	// limit substring hits, the subsequence tier cannot reach the top results
	// and the full scan is skipped. The result is the same as the scan's.
	if len(strings.Fields(q)) == 1 && len(q) >= 3 {
		d := m.derivedFor(true, false)
		for _, i := range symbolCandidates(d.tri, m.syms, q) {
			s := m.syms[i]
			if score, ok := nameScore(s.Name, query, q); ok {
				hits = append(hits, hitFor(s, score))
			}
		}
		if len(hits) >= limit {
			return sortHits(hits, limit)
		}
		hits = hits[:0]
	}
	for _, s := range m.syms {
		score, ok := nameScore(s.Name, query, q)
		if !ok {
			continue
		}
		hits = append(hits, hitFor(s, score))
	}
	return sortHits(hits, limit)
}

// sortHits orders search hits by score, then rank, then location, and keeps
// the first limit.
func sortHits(hits []SymbolHit, limit int) []SymbolHit {
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
	im := Impact{File: rel, Imports: append([]string(nil), m.imports[rel]...)}
	sort.Strings(im.Imports)
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
//
// Ranking for display follows spec 4.6: rank scaled to a score, a bump for
// files next to the focus, a bump for co-change partners of the focus, and a
// halving for type modules that are not in focus. Blocks are taken in score
// order while they fit. A block that does not fit is skipped, not allowed to
// end the map, and no directory may take more than dirCap blocks before the
// budget is used up.
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
	nb := m.neighbours(fx)
	boost := map[string]float64{}
	for _, c := range fx {
		for _, pr := range m.cochange[c] {
			boost[pr.File] += math.Min(float64(pr.Count)/5, 3)
		}
	}
	type cand struct {
		i     int
		score float64
	}
	var order []cand
	for i, p := range m.paths {
		// A barrel has no symbols of its own, but its source list is the thing
		// a reader needs, so it stays eligible.
		if len(m.fileSyms[p]) == 0 && !inFocus[p] && !(m.files[p] != nil && m.files[p].Barrel) {
			continue
		}
		s := rank[i] * 1000
		if nb[p] {
			s++
		}
		s += boost[p]
		if !inFocus[p] && len(m.fileSyms[p]) >= 5 && typeHeavyFile(m.fileSyms[p]) {
			s *= 0.5
		}
		// Barrels with few definitions of their own are index files: spec 4.6
		// scales them down unless the focus is on them.
		if fi := m.files[p]; !inFocus[p] && fi != nil && fi.Barrel && len(m.fileSyms[p]) < 10 {
			s *= barrelRankFactor
		}
		order = append(order, cand{i, s})
	}
	sort.SliceStable(order, func(a, b int) bool {
		if order[a].score != order[b].score {
			return order[a].score > order[b].score
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
	header := fmt.Sprintf("Code map: %d files, %d symbols\n", len(m.paths), len(m.syms))
	used := estTokens(header)
	if used > budgetTokens {
		m.recordRendered(nil)
		return ""
	}
	// Pass 1 fills within the budget and respects dirCap. Pass 2 spends what
	// is left on the blocks pass 1 held back for directory diversity. The
	// budget stays a hard cap: the spec's 10% fill allowance is not used.
	type pick struct {
		pos   int
		block string
	}
	var picks []pick
	perDir := map[string]int{}
	var skipped []int
	take := func(pos int, limit float64, dirLimit bool) bool {
		p := m.paths[order[pos].i]
		d := path.Dir(p)
		if dirLimit && perDir[d] >= dirCap {
			return false
		}
		block := m.renderBlock(p)
		t := estTokens(block)
		if float64(used+t) > limit {
			return false
		}
		used += t
		perDir[d]++
		picks = append(picks, pick{pos, block})
		return true
	}
	for pos := range order {
		if !take(pos, float64(budgetTokens), true) {
			skipped = append(skipped, pos)
		}
	}
	for _, pos := range skipped {
		take(pos, float64(budgetTokens), false)
	}
	sort.Slice(picks, func(a, b int) bool { return picks[a].pos < picks[b].pos })
	var sb strings.Builder
	var shown []string
	sb.WriteString(header)
	for _, pk := range picks {
		sb.WriteString(pk.block)
		shown = append(shown, m.paths[order[pk.pos].i])
	}
	m.recordRendered(shown)
	return sb.String()
}

// dirCap is how many blocks one directory may take in the first render pass.
const dirCap = 4

// barrelRankFactor scales the display rank of a barrel (spec 4.6: 0.6).
const barrelRankFactor = 0.6

// typeHeavyFile reports whether a file is a type module: most of its symbols
// are structural (spec 4.6).
func typeHeavyFile(syms []Symbol) bool {
	n := 0
	for _, s := range syms {
		if structuralKinds[s.Kind] {
			n++
		}
	}
	return float64(n) > typeRatio*float64(len(syms))
}

// neighbours returns the files that share an import edge with any file in
// focus. Callers hold m.mu.
func (m *Map) neighbours(focus []string) map[string]bool {
	out := map[string]bool{}
	if len(focus) == 0 {
		return out
	}
	ctx := map[int32]bool{}
	for _, f := range focus {
		ctx[int32(m.nodeIdx[f])] = true
	}
	for k := range m.eFrom {
		a, b := m.eFrom[k], m.eTo[k]
		if ctx[a] {
			out[m.paths[b]] = true
		}
		if ctx[b] {
			out[m.paths[a]] = true
		}
	}
	for _, f := range focus {
		delete(out, f)
	}
	return out
}

// recordRendered keeps the paths shown by the last render, so memory
// injection can say which files the model was just told about.
func (m *Map) recordRendered(paths []string) {
	m.renderMu.Lock()
	m.rendered = append([]string(nil), paths...)
	m.renderMu.Unlock()
}

// RenderedPaths returns the files shown by the most recent Render.
func (m *Map) RenderedPaths() []string {
	m.renderMu.Lock()
	defer m.renderMu.Unlock()
	return append([]string(nil), m.rendered...)
}

// newWindow is how recent a modification must be for the [NEW] tag.
const newWindow = 48 * time.Hour

func (m *Map) renderBlock(p string) string {
	var sb strings.Builder
	sb.WriteString(p)
	if deps := len(m.in[p]); deps >= 2 {
		fmt.Fprintf(&sb, " (→%d)", deps)
	}
	if fi := m.files[p]; fi != nil && fi.Mtime > 0 && time.Since(time.Unix(0, fi.Mtime)) < newWindow {
		sb.WriteString(" [NEW]")
	}
	sb.WriteByte('\n')
	if h := m.headerFor(p, m.files[p]); h != "" {
		fmt.Fprintf(&sb, "  ~ %s\n", h)
	}
	if srcs := m.reexports[p]; len(srcs) > 0 {
		const maxSrcs = 6
		shown := srcs
		if len(shown) > maxSrcs {
			shown = shown[:maxSrcs]
		}
		fmt.Fprintf(&sb, "  ← %s", strings.Join(shown, ", "))
		if len(srcs) > maxSrcs {
			fmt.Fprintf(&sb, " +%d more", len(srcs)-maxSrcs)
		}
		sb.WriteByte('\n')
	}
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
		mark := ""
		if s.Exported {
			mark = "+"
		}
		badge := ""
		if n := len(m.callers[s.Name]); n > 0 && s.Exported {
			badge = fmt.Sprintf(" <-%d", n)
		}
		summary := ""
		if s.Summary != "" {
			summary = " - " + s.Summary
		}
		fmt.Fprintf(&sb, "  %s%s :%d%s%s\n", mark, sig, s.Line, badge, summary)
	}
	if more > 0 {
		fmt.Fprintf(&sb, "  ... +%d more\n", more)
	}
	return sb.String()
}

// Budget is the token budget for the map given the size of the conversation
// (spec 4.7). A short conversation gets the default; past 1,000 tokens the
// budget shrinks toward 60% of the 2,500-token span as the conversation grows
// to 100,000 tokens, so a deep session keeps most of its map.
func Budget(conversationTokens int) int {
	if conversationTokens <= 1000 {
		return 2500
	}
	scale := 1 - 0.4*float64(conversationTokens)/100000
	if scale < 0.6 {
		scale = 0.6
	}
	return int(math.Round(1500 + 2500*scale))
}

func estTokens(s string) int { return (len(s) + 3) / 4 }
