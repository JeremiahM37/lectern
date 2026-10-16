package repomap

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
)

// File-content trigram index (spec 4.9). Every indexed file keeps the sorted
// set of its lower-cased ASCII trigrams. A literal can only occur in a file
// whose set holds all of the literal's trigrams, so the index answers "which
// files can NOT contain this literal" and grep skips those. The answer is a
// superset by construction: callers still run the real match on every file
// the index does not rule out.
//
// The index lives in memory and is rebuilt by Build. It is small (a few
// bytes per distinct trigram per file) and fast to build, where one database
// row per (trigram, file) pair made the cache hundreds of megabytes and the
// build many seconds slower on a large repository.
//
// Soundness rules, each enforced here or at the call site:
//   - a file is only ruled out when its stored stamp (mtime, size) equals the
//     file's current stamp, so an edited file is always searched;
//   - a file with any byte >= 0x80 or a NUL is not indexable and is always
//     searched, because ASCII case folding of a non-ASCII rune (for example
//     the Kelvin sign for k) can make an ASCII literal match it;
//   - files larger than triMaxFileBytes are not indexable, as above.
const triMaxFileBytes = 256 << 10

// triEntry is one file's index entry. grams is nil for a file that is not
// indexable; such a file is never ruled out.
type triEntry struct {
	mtime, size int64
	grams       []uint32 // sorted, distinct
}

// triIndex is the in-memory index. The zero value is empty and usable.
type triIndex struct {
	mu    sync.RWMutex
	files map[string]triEntry
}

// dropTrigramTables removes the tables an earlier build kept in the cache
// database, which could reach hundreds of megabytes.
func dropTrigramTables(db *sql.DB) {
	for _, t := range []string{"tri_postings", "tri_files", "tri_common"} {
		_, _ = db.Exec(`DROP TABLE IF EXISTS ` + t)
	}
}

// triGramsOf returns the distinct lower-cased ASCII trigrams of b, packed as
// 24-bit integers and sorted. Trigrams made only of whitespace are skipped.
func triGramsOf(b []byte) []uint32 {
	if len(b) < 3 {
		return nil
	}
	out := make([]uint32, 0, len(b)/4+1)
	for i := 0; i+2 < len(b); i++ {
		x, y, z := lowerASCII(b[i]), lowerASCII(b[i+1]), lowerASCII(b[i+2])
		if triSpace(x) && triSpace(y) && triSpace(z) {
			continue
		}
		out = append(out, uint32(x)<<16|uint32(y)<<8|uint32(z))
	}
	slices.Sort(out)
	return slices.Clip(slices.Compact(out))
}

func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

func triSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

// indexableBytes reports whether content may be ruled out by trigrams.
func indexableBytes(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 || c == 0 {
			return false
		}
	}
	return true
}

// triRead builds the entry for one file. A read error leaves grams nil, so the
// file is simply always searched.
func triRead(e fileEntry) triEntry {
	te := triEntry{mtime: e.mtime, size: e.size}
	if e.size > triMaxFileBytes {
		return te
	}
	if b, err := os.ReadFile(e.abs); err == nil && indexableBytes(b) {
		te.grams = triGramsOf(b)
		if te.grams == nil {
			te.grams = []uint32{} // indexable but too short: holds no trigram
		}
	}
	return te
}

// indexTrigrams brings the index in line with the given files and removes the
// gone paths. Files whose stamp already matches are not read again. With prune
// every file not in entries is dropped too (only Build passes the full list).
func (m *Map) indexTrigrams(ctx context.Context, entries []fileEntry, gone []string, prune bool) {
	m.tri.mu.RLock()
	old := m.tri.files
	m.tri.mu.RUnlock()

	var todo []fileEntry
	for _, e := range entries {
		if s, ok := old[e.rel]; ok && s.mtime == e.mtime && s.size == e.size {
			continue
		}
		todo = append(todo, e)
	}
	built := make([]triEntry, len(todo))
	workers := min(runtime.GOMAXPROCS(0), 8)
	var wg sync.WaitGroup
	next := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				built[i] = triRead(todo[i])
			}
		}()
	}
feed:
	for i := range todo {
		select {
		case next <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(next)
	wg.Wait()
	if ctx.Err() != nil {
		return // a cancelled build keeps the old index
	}

	m.tri.mu.Lock()
	defer m.tri.mu.Unlock()
	files := make(map[string]triEntry, len(m.tri.files)+len(todo))
	if prune {
		listed := make(map[string]bool, len(entries))
		for _, e := range entries {
			listed[e.rel] = true
		}
		for f, te := range m.tri.files {
			if listed[f] {
				files[f] = te
			}
		}
	} else {
		for f, te := range m.tri.files {
			files[f] = te
		}
	}
	for _, f := range gone {
		delete(files, f)
	}
	for i, e := range todo {
		files[e.rel] = built[i]
	}
	m.tri.files = files
}

// LiteralFilter builds a filter for lit from the trigram index. ok is false
// when no narrowing is possible: lit is shorter than three bytes, is not pure
// ASCII, has no usable trigram, or nothing is indexed yet. Callers must then
// search without a filter. The returned function is safe to call for any path.
//
// The returned function reports skip=true only for a file proven not to
// contain the literal: indexed, fresh (stamp match), ASCII, and missing a
// trigram. It is typed with an unnamed func so tools/fs can match it through
// an interface without importing repomap.
func (m *Map) LiteralFilter(lit string) (func(rel string, mtime, size int64) bool, bool) {
	if len(lit) < 3 {
		return nil, false
	}
	for i := 0; i < len(lit); i++ {
		if lit[i] >= 0x80 {
			return nil, false
		}
	}
	grams := triGramsOf([]byte(lit))
	if len(grams) == 0 {
		return nil, false
	}
	m.tri.mu.RLock()
	files := m.tri.files // replaced, never mutated, so safe to keep
	m.tri.mu.RUnlock()
	if len(files) == 0 {
		return nil, false
	}
	return func(rel string, mtime, size int64) bool {
		te, ok := files[rel]
		if !ok || te.grams == nil || te.mtime != mtime || te.size != size {
			return false
		}
		for _, g := range grams {
			if _, found := slices.BinarySearch(te.grams, g); !found {
				return true
			}
		}
		return false
	}, true
}

// updateTrigrams refreshes the index for the given paths after Update. Paths
// outside the code map (other languages, docs) are indexed too: the index
// covers any file grep could search, not only code files.
func (m *Map) updateTrigrams(ctx context.Context, rels []string) {
	var entries []fileEntry
	var gone []string
	for _, rel := range rels {
		abs := filepath.Join(m.root, filepath.FromSlash(rel))
		st, err := os.Lstat(abs)
		if err != nil || !st.Mode().IsRegular() {
			gone = append(gone, rel)
			continue
		}
		entries = append(entries, fileEntry{rel: rel, abs: abs, size: st.Size(), mtime: st.ModTime().UnixNano()})
	}
	m.indexTrigrams(ctx, entries, gone, false)
}
