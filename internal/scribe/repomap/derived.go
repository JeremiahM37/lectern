package repomap

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// derivedIndex holds indexes that only some queries need. They are built on
// first use rather than in recompute, so a rebuild does not pay for them. A
// change to the file set drops them (see recompute).
type derivedIndex struct {
	tri   map[uint32][]int32 // symbol-name trigram -> ascending indices into m.syms
	clone *cloneIdx
}

// derivedFor returns the derived indexes, building the ones asked for. The
// caller holds m.mu for reading, which keeps recompute (and so the file set)
// from changing underneath the build.
func (m *Map) derivedFor(wantTri, wantClone bool) *derivedIndex {
	m.derivedMu.Lock()
	defer m.derivedMu.Unlock()
	if m.derived == nil {
		m.derived = &derivedIndex{}
	}
	d := m.derived
	if wantTri && d.tri == nil {
		d.tri = buildSymTrigrams(m.syms)
	}
	if wantClone && d.clone == nil {
		d.clone = buildCloneIdx(m.paths, m.files)
	}
	return d
}

// lowerTrigrams returns the distinct trigrams of the lower-cased bytes of s,
// as packed 3-byte codes.
func lowerTrigrams(s string) []uint32 {
	s = strings.ToLower(s)
	if len(s) < 3 {
		return nil
	}
	seen := make(map[uint32]struct{}, len(s))
	var out []uint32
	for i := 0; i+3 <= len(s); i++ {
		c := uint32(s[i])<<16 | uint32(s[i+1])<<8 | uint32(s[i+2])
		if _, ok := seen[c]; !ok {
			seen[c] = struct{}{}
			out = append(out, c)
		}
	}
	return out
}

// buildSymTrigrams maps each trigram of each symbol name to the symbols that
// contain it. Indices are appended in ascending order, so postings are sorted.
func buildSymTrigrams(syms []Symbol) map[uint32][]int32 {
	idx := map[uint32][]int32{}
	for i := range syms {
		for _, c := range lowerTrigrams(syms[i].Name) {
			idx[c] = append(idx[c], int32(i))
		}
	}
	return idx
}

// symbolCandidates returns the indices of symbols whose lower-cased name
// contains q (lower-cased, at least three bytes). The trigram postings give an
// exact superset, which is then checked with strings.Contains, so the result
// is exact. It returns nil when the index has no postings for q.
func symbolCandidates(tri map[uint32][]int32, syms []Symbol, q string) []int32 {
	grams := lowerTrigrams(q)
	if len(grams) == 0 {
		return nil
	}
	lists := make([][]int32, 0, len(grams))
	for _, g := range grams {
		p := tri[g]
		if len(p) == 0 {
			return nil
		}
		lists = append(lists, p)
	}
	// Intersect from the rarest posting list, which keeps the work small.
	slices.SortFunc(lists, func(a, b []int32) int { return len(a) - len(b) })
	cur := lists[0]
	for _, l := range lists[1:] {
		cur = intersectSorted(cur, l)
		if len(cur) == 0 {
			return nil
		}
	}
	var out []int32
	for _, i := range cur {
		if strings.Contains(strings.ToLower(syms[i].Name), q) {
			out = append(out, i)
		}
	}
	return out
}

func intersectSorted(a, b []int32) []int32 {
	var out []int32
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] < b[j]:
			i++
		case a[i] > b[j]:
			j++
		default:
			out = append(out, a[i])
			i++
			j++
		}
	}
	return out
}

// ClonePair is two files whose signatures estimate at least nearDupMin
// similarity. Similarity is the estimated Jaccard similarity of their
// shingles, from 0 to 1.
type ClonePair struct {
	A          string  `json:"a"`
	B          string  `json:"b"`
	Similarity float64 `json:"similarity"`
}

// FragmentPair is two files that share sampled fragments: runs of identical
// tokens of at least cloneWindow tokens. Shared counts the sampled fragments.
type FragmentPair struct {
	A      string `json:"a"`
	B      string `json:"b"`
	Shared int    `json:"shared"`
}

// CloneReport is the whole-repository duplication picture.
type CloneReport struct {
	NearDup   []ClonePair
	Fragments []FragmentPair
	// Shapes are functions with the same structure whose bodies differ in
	// identifiers or literals. Exact copies are in Duplicates.
	Shapes []DupGroup
}

// cloneIdx is the lazily built candidate set for clone queries.
type cloneIdx struct {
	near    []ClonePair
	nearBy  map[string][]ClonePair
	frags   []FragmentPair
	fragBy  map[string][]FragmentPair
	shapes  []DupGroup
	bodies  map[string][]Symbol // identical bodies, by BodyHash
	byShape map[string][]Symbol // functions sharing a shape, by ShapeHash
}

// maxBandBucket bounds the files one LSH bucket may hold. A bucket that big is
// boilerplate shared by many files, and pairing it would be quadratic.
const maxBandBucket = 50

func buildCloneIdx(paths []string, files map[string]*fileInfo) *cloneIdx {
	ci := &cloneIdx{nearBy: map[string][]ClonePair{}, fragBy: map[string][]FragmentPair{}}

	// Near duplicates: band the signatures, pair files that share a band
	// bucket, then confirm each pair with the full estimate.
	var sigIdx []int
	for i, p := range paths {
		if fi := files[p]; fi != nil && fi.Clone != nil {
			sigIdx = append(sigIdx, i)
		}
	}
	buckets := map[uint64][]int{}
	for _, i := range sigIdx {
		sig := files[paths[i]].Clone
		for b := 0; b < cloneBins/bandRows; b++ {
			k := bandKey(sig, b) ^ uint64(b)*0x9e3779b97f4a7c15
			buckets[k] = append(buckets[k], i)
		}
	}
	type pr struct{ a, b int }
	cand := map[pr]bool{}
	for _, ids := range buckets {
		if len(ids) < 2 || len(ids) > maxBandBucket {
			continue
		}
		for x := 0; x < len(ids); x++ {
			for y := x + 1; y < len(ids); y++ {
				a, b := ids[x], ids[y]
				if a > b {
					a, b = b, a
				}
				cand[pr{a, b}] = true
			}
		}
	}
	for c := range cand {
		pa, pb := paths[c.a], paths[c.b]
		sim := similarity(files[pa].Clone, files[pb].Clone)
		if sim < nearDupMin {
			continue
		}
		p := ClonePair{A: pa, B: pb, Similarity: sim}
		ci.near = append(ci.near, p)
		ci.nearBy[pa] = append(ci.nearBy[pa], p)
		ci.nearBy[pb] = append(ci.nearBy[pb], ClonePair{A: pb, B: pa, Similarity: sim})
	}
	sort.Slice(ci.near, func(i, j int) bool {
		if ci.near[i].Similarity != ci.near[j].Similarity {
			return ci.near[i].Similarity > ci.near[j].Similarity
		}
		if ci.near[i].A != ci.near[j].A {
			return ci.near[i].A < ci.near[j].A
		}
		return ci.near[i].B < ci.near[j].B
	})

	// Repeated fragments: a sampled fragment hash that several files share
	// votes for each pair of those files. Hashes that many files share are
	// boilerplate and are skipped.
	frag := map[uint32][]int{}
	for i, p := range paths {
		if fi := files[p]; fi != nil {
			for _, h := range fi.Frags {
				frag[h] = append(frag[h], i)
			}
		}
	}
	votes := map[pr]int{}
	for _, ids := range frag {
		if len(ids) < 2 || len(ids) > maxFragFiles {
			continue
		}
		for x := 0; x < len(ids); x++ {
			for y := x + 1; y < len(ids); y++ {
				votes[pr{ids[x], ids[y]}]++
			}
		}
	}
	for k, n := range votes {
		if n < minFragShared {
			continue
		}
		pa, pb := paths[k.a], paths[k.b]
		p := FragmentPair{A: pa, B: pb, Shared: n}
		ci.frags = append(ci.frags, p)
		ci.fragBy[pa] = append(ci.fragBy[pa], p)
		ci.fragBy[pb] = append(ci.fragBy[pb], FragmentPair{A: pb, B: pa, Shared: n})
	}
	sort.Slice(ci.frags, func(i, j int) bool {
		if ci.frags[i].Shared != ci.frags[j].Shared {
			return ci.frags[i].Shared > ci.frags[j].Shared
		}
		if ci.frags[i].A != ci.frags[j].A {
			return ci.frags[i].A < ci.frags[j].A
		}
		return ci.frags[i].B < ci.frags[j].B
	})

	// Exact and structural function duplicates, from the per-symbol hashes.
	ci.bodies = map[string][]Symbol{}
	ci.byShape = map[string][]Symbol{}
	for _, p := range paths {
		fi := files[p]
		if fi == nil {
			continue
		}
		for _, s := range fi.Symbols {
			if s.BodyHash != "" {
				ci.bodies[s.BodyHash] = append(ci.bodies[s.BodyHash], s)
			}
			if isFuncKind(s.Kind) && s.ShapeHash != "" {
				ci.byShape[s.ShapeHash] = append(ci.byShape[s.ShapeHash], s)
			}
		}
	}
	for h, ss := range ci.byShape {
		if len(ss) < 2 {
			delete(ci.byShape, h)
			continue
		}
		// A shape group is interesting only when its bodies are not all the
		// same, since identical bodies are reported by Duplicates.
		distinct := map[string]bool{}
		for _, s := range ss {
			distinct[s.BodyHash] = true
		}
		if len(distinct) < 2 {
			continue
		}
		ci.shapes = append(ci.shapes, DupGroup{Hash: h, Symbols: ss})
	}
	sort.Slice(ci.shapes, func(i, j int) bool {
		if len(ci.shapes[i].Symbols) != len(ci.shapes[j].Symbols) {
			return len(ci.shapes[i].Symbols) > len(ci.shapes[j].Symbols)
		}
		return ci.shapes[i].Symbols[0].File < ci.shapes[j].Symbols[0].File
	})
	return ci
}

// Clones reports near-duplicate files, repeated fragments and structurally
// identical functions across the repository. It is computed from the cached
// signatures, so it costs nothing at build time.
func (m *Map) Clones() CloneReport {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ci := m.derivedFor(false, true).clone
	return CloneReport{
		NearDup:   append([]ClonePair(nil), ci.near...),
		Fragments: append([]FragmentPair(nil), ci.frags...),
		Shapes:    append([]DupGroup(nil), ci.shapes...),
	}
}

// CloneHints returns short lines describing the duplication a file takes part
// in: near-duplicate files, repeated fragments, and functions identical to or
// structurally the same as one of its functions. It reads only in-memory
// indexes, so it is cheap enough to run after every edit. max caps the lines.
func (m *Map) CloneHints(file string, max int) []string {
	if max <= 0 {
		max = 4
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	rel := m.relPath(file)
	if _, ok := m.files[rel]; !ok {
		return nil
	}
	ci := m.derivedFor(false, true).clone
	var out []string
	add := func(s string) {
		if len(out) < max {
			out = append(out, s)
		}
	}
	for _, p := range ci.nearBy[rel] {
		add(fmt.Sprintf("near-duplicate of %s (about %d%% of its shingles match)", p.B, int(p.Similarity*100+0.5)))
	}
	for _, p := range ci.fragBy[rel] {
		add(fmt.Sprintf("shares %d repeated fragments with %s", p.Shared, p.B))
	}
	for _, s := range m.fileSyms[rel] {
		if !isFuncKind(s.Kind) {
			continue
		}
		for _, o := range ci.bodies[s.BodyHash] {
			if s.BodyHash != "" && !(o.File == s.File && o.Line == s.Line) {
				add(fmt.Sprintf("%s is identical to %s in %s:%d", s.Name, o.Name, o.File, o.Line))
			}
		}
		if s.ShapeHash == "" {
			continue
		}
		for _, o := range ci.byShape[s.ShapeHash] {
			if o.BodyHash != s.BodyHash && !(o.File == s.File && o.Line == s.Line) {
				add(fmt.Sprintf("%s has the same structure as %s in %s:%d", s.Name, o.Name, o.File, o.Line))
				break
			}
		}
	}
	return out
}
