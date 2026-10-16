package repomap

import (
	"path"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// Concept search and name search for find_symbol and code_map (spec 04 2.6,
// 2.8). Everything here is computed at query time from the symbol list and
// the path list the map already holds. Nothing is written to the database, so
// none of it adds to a build.

// splitWords breaks an identifier or phrase into lower-case words at spaces,
// punctuation, '_' and '-', and at camelCase boundaries. "HTTPServer2fix"
// becomes http, server, 2, fix.
func splitWords(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(s)
	for i, r := range rs {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if i > 0 && len(cur) > 0 && (unicode.IsDigit(r) != unicode.IsDigit(rs[i-1])) {
			flush()
		}
		if i > 0 && len(cur) > 0 && unicode.IsUpper(r) {
			prev := rs[i-1]
			nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

// conceptStopWords are words that say nothing about what a symbol does.
var conceptStopWords = words("a an and are as at be by do does for from how i in is it of on or the to what where which with")

// conceptWords returns the distinct useful words of a query.
func conceptWords(q string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range splitWords(q) {
		if len(w) < 2 || conceptStopWords[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

// wordHit scores one query word against a list of words: an exact word is
// worth 3, a word prefix (from three letters up) 2, and a substring of the
// joined name 1.5. The strongest single match counts.
func wordHit(w string, ws []string, joined string) float64 {
	best := 0.0
	for _, x := range ws {
		switch {
		case x == w:
			return 3
		case len(w) >= 3 && strings.HasPrefix(x, w):
			best = max(best, 2)
		}
	}
	if best == 0 && len(w) >= 3 && strings.Contains(joined, w) {
		best = 1.5
	}
	return best
}

// ConceptHit is a symbol matched by concept search, with the words it matched.
type ConceptHit struct {
	SymbolHit
	Matched int // how many query words matched
}

// ConceptSearch ranks symbols and files against a phrase such as "token
// refresh" or "retry backoff". A symbol scores from its name words, its file's
// path words and its doc summary. Each query word has to match something, at
// least partly, for the item to count. Ties go to the more important file.
func (m *Map) ConceptSearch(query string, limit int) ([]ConceptHit, []FileHit) {
	ws := conceptWords(query)
	if len(ws) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var syms []ConceptHit
	for _, s := range m.syms {
		nameW := splitWords(s.Name)
		fileW := splitWords(strings.TrimSuffix(s.File, path.Ext(s.File)))
		doc := strings.ToLower(s.Summary)
		nameJoined := strings.ToLower(s.Name)
		total, matched := 0.0, 0
		for _, w := range ws {
			h := wordHit(w, nameW, nameJoined)
			if fh := wordHit(w, fileW, ""); fh > 0 {
				h = max(h, fh*0.5)
			}
			if doc != "" && strings.Contains(doc, w) {
				h = max(h, 1)
			}
			if h > 0 {
				matched++
				total += h
			}
		}
		if matched == 0 {
			continue
		}
		// A symbol that matches only some of the words scores in proportion.
		total *= float64(matched) / float64(len(ws))
		if s.Exported {
			total += 0.5
		}
		rank := m.fileRank(s.File) * (1 + float64(m.refCount[s.Name])/10)
		syms = append(syms, ConceptHit{SymbolHit: SymbolHit{Symbol: s, Score: total, Rank: rank}, Matched: matched})
	}
	sort.SliceStable(syms, func(i, j int) bool {
		if syms[i].Matched != syms[j].Matched {
			return syms[i].Matched > syms[j].Matched
		}
		if syms[i].Score != syms[j].Score {
			return syms[i].Score > syms[j].Score
		}
		if syms[i].Rank != syms[j].Rank {
			return syms[i].Rank > syms[j].Rank
		}
		if syms[i].File != syms[j].File {
			return syms[i].File < syms[j].File
		}
		return syms[i].Line < syms[j].Line
	})
	if len(syms) > limit {
		syms = syms[:limit]
	}
	var files []FileHit
	for _, p := range m.paths {
		pw := splitWords(strings.TrimSuffix(p, path.Ext(p)))
		joined := strings.ToLower(p)
		total, matched := 0.0, 0
		for _, w := range ws {
			if h := wordHit(w, pw, joined); h > 0 {
				matched++
				total += h
			}
		}
		if matched == 0 {
			continue
		}
		files = append(files, FileHit{Path: p, Score: total * float64(matched) / float64(len(ws)), Rank: m.fileRank(p)})
	}
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].Score != files[j].Score {
			return files[i].Score > files[j].Score
		}
		return files[i].Rank > files[j].Rank
	})
	if len(files) > limit {
		files = files[:limit]
	}
	return syms, files
}

// importance is the ranking used once a name matches: the file's PageRank,
// boosted by how often the name is referenced elsewhere. Callers hold m.mu.
func (m *Map) importance(s Symbol) float64 {
	return m.fileRank(s.File) * (1 + float64(m.refCount[s.Name])/10)
}

// RankSymbolMatches orders name matches for find_symbol. Exact and
// case-insensitive whole-name matches come first. The rest follow importance
// (PageRank with reference counts), not raw string similarity, so an
// important file's symbol beats an obscure one with a tighter match. Input
// hits are the output of SearchSymbols with a large limit.
func (m *Map) RankSymbolMatches(hits []SymbolHit) []SymbolHit {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := slices.Clone(hits)
	sort.SliceStable(out, func(i, j int) bool {
		ei, ej := out[i].Score >= 95, out[j].Score >= 95
		if ei != ej {
			return ei
		}
		if out[i].Rank != out[j].Rank {
			return out[i].Rank > out[j].Rank
		}
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// namesSorted returns the indices of all symbols ordered by lower-case name,
// built on first use and dropped with the other derived indexes. It is what
// gives search_symbols its prefix behaviour without a database table: one
// binary search per query, and no row per symbol written at build time.
// Callers hold m.mu for reading.
func (m *Map) namesSorted() []int32 {
	m.derivedMu.Lock()
	defer m.derivedMu.Unlock()
	if m.derived == nil {
		m.derived = &derivedIndex{}
	}
	d := m.derived
	if d.sorted == nil {
		idx := make([]int32, len(m.syms))
		lower := make([]string, len(m.syms))
		for i := range m.syms {
			idx[i] = int32(i)
			lower[i] = strings.ToLower(m.syms[i].Name)
		}
		slices.SortFunc(idx, func(a, b int32) int { return strings.Compare(lower[a], lower[b]) })
		d.sorted = idx
		d.sortedKeys = make([]string, len(idx))
		for i, k := range idx {
			d.sortedKeys[i] = lower[k]
		}
	}
	return d.sorted
}

// SearchPrefix returns symbols whose name starts with prefix (case-insensitive),
// shortest names first, then by importance. It is the search_symbols action's
// "build*" form.
func (m *Map) SearchPrefix(prefix string, limit int) []SymbolHit {
	p := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(prefix), "*")))
	if p == "" {
		return nil
	}
	if limit <= 0 {
		limit = 50
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	idx := m.namesSorted()
	keys := m.derived.sortedKeys
	start := sort.SearchStrings(keys, p)
	var hits []SymbolHit
	for i := start; i < len(keys) && strings.HasPrefix(keys[i], p); i++ {
		s := m.syms[idx[i]]
		hits = append(hits, SymbolHit{Symbol: s, Score: 80, Rank: m.importance(s)})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if len(hits[i].Name) != len(hits[j].Name) {
			return len(hits[i].Name) < len(hits[j].Name)
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
