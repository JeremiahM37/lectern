package repomap

import (
	"context"
	"math"
	"os/exec"
	"path"
	"slices"
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

// structuralKinds describe the shape of code rather than behaviour. A file
// made mostly of them is a type module, and edges into it carry less rank.
var structuralKinds = words("interface type enum trait")

const (
	// typeRatio is the structural share above which a file counts as a type
	// module. The display penalty uses the same threshold.
	typeRatio = 0.66
	// typeEdgeFactor scales rank flowing into type modules (spec: 0.3).
	typeEdgeFactor = 0.3
)

// recompute rebuilds every derived structure from the file set. Callers hold
// m.mu for writing. Edges are built over node indices rather than path
// strings: on a warm start this is most of the work, and string-keyed maps
// dominated it.
func (m *Map) recompute(files map[string]*fileInfo, cochange map[string][]Pair) {
	m.files = files
	m.cochange = cochange
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	m.paths = paths
	n := len(paths)
	m.nodeIdx = make(map[string]int, n)
	for i, p := range paths {
		m.nodeIdx[p] = i
	}

	m.syms = m.syms[:0]
	m.fileSyms = make(map[string][]Symbol, n)
	m.symsByName = map[string][]Symbol{}
	m.refCount = map[string]int{}
	defs := map[string][]int32{}           // name -> defining nodes, ascending
	expDefs := map[string]map[int32]bool{} // name -> exporting nodes
	typeHeavy := make([]bool, n)
	for i, p := range paths {
		fi := files[p]
		m.fileSyms[p] = fi.Symbols
		structural := 0
		for _, s := range fi.Symbols {
			m.syms = append(m.syms, s)
			m.symsByName[s.Name] = append(m.symsByName[s.Name], s)
			if d := defs[s.Name]; len(d) == 0 || d[len(d)-1] != int32(i) {
				defs[s.Name] = append(d, int32(i))
			}
			if s.Exported {
				if expDefs[s.Name] == nil {
					expDefs[s.Name] = map[int32]bool{}
				}
				expDefs[s.Name][int32(i)] = true
			}
			if structuralKinds[s.Kind] {
				structural++
			}
		}
		typeHeavy[i] = len(fi.Symbols) > 0 && float64(structural) > typeRatio*float64(len(fi.Symbols))
		for name := range fi.Idents {
			m.refCount[name]++
		}
	}

	m.callers = buildCallers(paths, files)
	ix := newFileIndex(m.root, files)
	out := make([]map[int32]float64, n)
	add := func(a, b int32, w float64) {
		if a == b {
			return
		}
		e := out[a]
		if e == nil {
			e = map[int32]float64{}
			out[a] = e
		}
		e[b] += w
	}
	// Import edges: proven by a statement, weight 1. They also define the
	// blast radius (who imports this file).
	importedBy := make([][]int32, n)
	m.imports = make(map[string][]string, n)
	var seen []int32
	for i, p := range paths {
		for _, imp := range files[p].Imports {
			seen = seen[:0]
			for _, t := range ix.resolve(p, imp) {
				ti, ok := m.nodeIdx[t]
				if !ok {
					continue
				}
				dup := false
				for _, s := range seen {
					if s == int32(ti) {
						dup = true
						break
					}
				}
				if dup {
					continue
				}
				seen = append(seen, int32(ti))
				add(int32(i), int32(ti), 1)
				importedBy[ti] = append(importedBy[ti], int32(i))
				m.imports[p] = append(m.imports[p], t)
			}
		}
	}
	// Identifier edges: a name used here and defined in another file, weighted
	// by specificity (1/sqrt(defining files)). A reference can only reach a
	// definition in the same language family, and a package-private symbol
	// (lower-case Go, or any non-exported name) only in the same directory.
	// Without those rules every local variable named "record" links to a file.
	fam := make([]string, n)
	dir := make([]string, n)
	for i, p := range paths {
		fam[i] = langFamily(files[p].Lang)
		dir[i] = path.Dir(p)
	}
	for i := range paths {
		for name := range files[paths[i]].Idents {
			ds := defs[name]
			if len(ds) == 0 || len(ds) > maxDefFiles {
				continue
			}
			w := 1 / math.Sqrt(float64(len(ds)))
			for _, d := range ds {
				if int(d) == i || fam[d] != fam[i] {
					continue
				}
				if !expDefs[name][d] && dir[d] != dir[i] {
					continue
				}
				add(int32(i), d, w)
			}
		}
	}
	// Co-change edges, only where no import relation exists.
	for a, ps := range cochange {
		ai, ok := m.nodeIdx[a]
		if !ok {
			continue
		}
		for _, pr := range ps {
			bi, ok := m.nodeIdx[pr.File]
			if !ok || out[ai][int32(bi)] > 0 || out[bi][int32(ai)] > 0 {
				continue
			}
			add(int32(ai), int32(bi), 0.5)
			add(int32(bi), int32(ai), 0.5)
		}
	}

	m.outW = make([]float64, n)
	m.eFrom, m.eTo, m.eCoef = m.eFrom[:0], m.eTo[:0], m.eCoef[:0]
	var weights []float64
	var dsts []int32
	for i := 0; i < n; i++ {
		dsts = dsts[:0]
		for d := range out[i] {
			dsts = append(dsts, d)
		}
		slices.Sort(dsts)
		for _, d := range dsts {
			w := out[i][d]
			if typeHeavy[d] {
				w *= typeEdgeFactor
			}
			m.outW[i] += w
			m.eFrom = append(m.eFrom, int32(i))
			m.eTo = append(m.eTo, d)
			weights = append(weights, w)
		}
	}
	m.in = make(map[string][]string, n)
	for t, ps := range importedBy {
		if len(ps) == 0 {
			continue
		}
		names := make([]string, 0, len(ps))
		for _, p := range ps {
			names = append(names, paths[p])
		}
		sort.Strings(names)
		m.in[paths[t]] = dedupeSorted(names)
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
