package repomap

import (
	"context"
	"math"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
)

const (
	pageRankDamping    = 0.85
	pageRankIterations = 20
	pageRankEpsilon    = 1e-6
	// maxDefFiles drops identifiers defined in many files ("Close", "String"):
	// they would link everything to everything and carry no signal.
	maxDefFiles = 50
	// gitLogCommits and the co-change bounds follow the spec: commits touching
	// fewer than 2 or more than 20 files are ignored (mass refactors).
	gitLogCommits = 300
	maxCoFiles    = 20
	minCoCount    = 2
)

// Pair is a file that changes together with another file.
type Pair struct {
	File  string `json:"file"`
	Count int    `json:"count"`
}

// recompute rebuilds every derived structure from the file set. Callers hold
// m.mu for writing.
func (m *Map) recompute(files map[string]*fileInfo, cochange map[string][]Pair) {
	m.files = files
	m.cochange = cochange
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	m.paths = paths
	m.nodeIdx = make(map[string]int, len(paths))
	for i, p := range paths {
		m.nodeIdx[p] = i
	}

	m.syms = m.syms[:0]
	m.fileSyms = make(map[string][]Symbol, len(paths))
	m.symsByName = map[string][]Symbol{}
	m.defs = map[string][]string{}
	m.expDefs = map[string]map[string]bool{}
	m.refCount = map[string]int{}
	for _, p := range paths {
		fi := files[p]
		m.fileSyms[p] = fi.Symbols
		for _, s := range fi.Symbols {
			m.syms = append(m.syms, s)
			m.symsByName[s.Name] = append(m.symsByName[s.Name], s)
			if d := m.defs[s.Name]; len(d) == 0 || d[len(d)-1] != p {
				m.defs[s.Name] = append(d, p)
			}
			if s.Exported {
				if m.expDefs[s.Name] == nil {
					m.expDefs[s.Name] = map[string]bool{}
				}
				m.expDefs[s.Name][p] = true
			}
		}
		for name := range fi.Idents {
			m.refCount[name]++
		}
	}

	ix := newFileIndex(m.root, files)
	edges := map[string]map[string]float64{}
	add := func(a, b string, w float64) {
		if a == b || b == "" {
			return
		}
		e := edges[a]
		if e == nil {
			e = map[string]float64{}
			edges[a] = e
		}
		e[b] += w
	}
	// Import edges: proven by a statement, weight 1. They also define the
	// blast radius (who imports this file).
	importedBy := map[string][]string{}
	for _, p := range paths {
		for _, imp := range files[p].Imports {
			seen := map[string]bool{}
			for _, t := range ix.resolve(p, imp) {
				if files[t] != nil && !seen[t] {
					seen[t] = true
					add(p, t, 1)
					importedBy[t] = append(importedBy[t], p)
				}
			}
		}
	}
	// Identifier edges: a name used here and defined in another file, weighted
	// by specificity (1/sqrt(defining files)). A reference can only reach a
	// definition in the same language family, and a package-private symbol
	// (lower-case Go, or any non-exported name) only in the same directory.
	// Without those rules every local variable named "record" links to a file.
	for _, p := range paths {
		for name := range files[p].Idents {
			ds := m.defs[name]
			if len(ds) == 0 || len(ds) > maxDefFiles {
				continue
			}
			w := 1 / math.Sqrt(float64(len(ds)))
			for _, d := range ds {
				if d == p || langFamily(files[d].Lang) != langFamily(files[p].Lang) {
					continue
				}
				if !m.expDefs[name][d] && path.Dir(d) != path.Dir(p) {
					continue
				}
				add(p, d, w)
			}
		}
	}
	// Co-change edges, only where no import relation exists.
	for a, ps := range cochange {
		if files[a] == nil {
			continue
		}
		for _, pr := range ps {
			if files[pr.File] == nil || edges[a][pr.File] > 0 || edges[pr.File][a] > 0 {
				continue
			}
			add(a, pr.File, 0.5)
			add(pr.File, a, 0.5)
		}
	}

	n := len(paths)
	m.outW = make([]float64, n)
	m.eFrom, m.eTo, m.eCoef = m.eFrom[:0], m.eTo[:0], m.eCoef[:0]
	var weights []float64
	for i, p := range paths {
		dsts := make([]string, 0, len(edges[p]))
		for d := range edges[p] {
			dsts = append(dsts, d)
		}
		sort.Strings(dsts)
		for _, d := range dsts {
			w := edges[p][d]
			m.outW[i] += w
			m.eFrom = append(m.eFrom, int32(i))
			m.eTo = append(m.eTo, int32(m.nodeIdx[d]))
			weights = append(weights, w)
		}
	}
	m.in = map[string][]string{}
	for t, ps := range importedBy {
		sort.Strings(ps)
		m.in[t] = dedupeSorted(ps)
	}
	for k := range m.eFrom {
		m.eCoef = append(m.eCoef, weights[k]/m.outW[m.eFrom[k]])
	}
	m.edgeCount = len(m.eFrom)
	if n > 0 {
		u := make([]float64, n)
		for i := range u {
			u[i] = 1 / float64(n)
		}
		m.rank = m.pagerank(u)
	} else {
		m.rank = nil
	}
}

// pagerank runs damped PageRank with teleport vector p. Dangling mass and
// teleport both follow p, so the result sums to 1.
func (m *Map) pagerank(p []float64) []float64 {
	n := len(p)
	r := make([]float64, n)
	copy(r, p)
	next := make([]float64, n)
	for it := 0; it < pageRankIterations; it++ {
		dangling := 0.0
		for i := 0; i < n; i++ {
			if m.outW[i] == 0 {
				dangling += r[i]
			}
		}
		for i := range next {
			next[i] = (1-pageRankDamping)*p[i] + pageRankDamping*dangling*p[i]
		}
		for k := range m.eFrom {
			next[m.eTo[k]] += pageRankDamping * r[m.eFrom[k]] * m.eCoef[k]
		}
		diff := 0.0
		for i := range next {
			diff += math.Abs(next[i] - r[i])
		}
		r, next = next, r
		if diff < pageRankEpsilon {
			break
		}
	}
	return r
}

// focusVector personalizes the teleport distribution toward focus files. It is
// 70% uniform and 30% boost, with boosts for the focus files and their
// co-change partners.
func (m *Map) focusVector(focus []string) []float64 {
	n := len(m.paths)
	base := 1 / float64(n)
	boost := make([]float64, n)
	for _, f := range focus {
		i, ok := m.nodeIdx[f]
		if !ok {
			continue
		}
		boost[i] += 5 * base
		for _, c := range m.cochange[f] {
			if j, ok := m.nodeIdx[c.File]; ok {
				boost[j] += base * (1 + math.Min(float64(c.Count)/3, 2))
			}
		}
	}
	sum := 0.0
	for _, b := range boost {
		sum += b
	}
	p := make([]float64, n)
	for i := range p {
		p[i] = base
		if sum > 0 {
			p[i] = 0.7*base + 0.3*boost[i]/sum
		}
	}
	return p
}

// gitCoChanges reads recent history and counts file pairs committed together.
func gitCoChanges(ctx context.Context, root string) map[string][]Pair {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "log", "-n", strconv.Itoa(gitLogCommits),
		"--name-only", "--no-renames", "--pretty=format:@@commit").Output()
	if err != nil {
		return nil
	}
	counts := map[[2]string]int{}
	var commit []string
	flush := func() {
		if len(commit) >= 2 && len(commit) <= maxCoFiles {
			for i := 0; i < len(commit); i++ {
				for j := i + 1; j < len(commit); j++ {
					a, b := commit[i], commit[j]
					if a > b {
						a, b = b, a
					}
					counts[[2]string{a, b}]++
				}
			}
		}
		commit = commit[:0]
	}
	seen := map[string]bool{}
	for _, ln := range strings.Split(string(out), "\n") {
		ln = strings.TrimSpace(ln)
		switch {
		case ln == "@@commit":
			flush()
			seen = map[string]bool{}
		case ln == "" || seen[ln]:
		default:
			seen[ln] = true
			commit = append(commit, ln)
		}
	}
	flush()
	res := map[string][]Pair{}
	for k, c := range counts {
		if c < minCoCount {
			continue
		}
		res[k[0]] = append(res[k[0]], Pair{File: k[1], Count: c})
		res[k[1]] = append(res[k[1]], Pair{File: k[0], Count: c})
	}
	for _, ps := range res {
		sort.Slice(ps, func(i, j int) bool {
			if ps[i].Count != ps[j].Count {
				return ps[i].Count > ps[j].Count
			}
			return ps[i].File < ps[j].File
		})
	}
	return res
}

// langFamily groups languages whose identifiers can refer to each other.
func langFamily(lang string) string {
	switch lang {
	case "typescript", "javascript":
		return "js"
	case "c", "cpp":
		return "c"
	case "java", "kotlin":
		return "jvm"
	}
	return lang
}

func dedupeSorted(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}
