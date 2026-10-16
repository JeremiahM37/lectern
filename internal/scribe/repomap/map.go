// Package repomap is Scribe's code map: a ranked index of a repository's
// files, symbols and references. It is built from source with no tree-sitter
// or cgo: Go uses go/parser, other languages use masked-source scanners. The
// result is persisted in <root>/.scribe/cache/map.db so a later Build parses
// only the files that changed.
package repomap

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Options tunes a Map. Zero values pick defaults.
type Options struct {
	Workers      int   // parse workers; default GOMAXPROCS
	MaxFileBytes int64 // skip larger files; default 1 MiB
	MaxFiles     int   // index at most this many files; default 10000
	InMemory     bool  // keep the cache in memory (tests, throwaway use)
}

// Map is the code map for one repository. Build and Update are serialised;
// queries take a read lock and may run concurrently with either.
type Map struct {
	root string
	opts Options
	db   *sql.DB

	buildMu sync.Mutex
	mu      sync.RWMutex

	files      map[string]*fileInfo
	paths      []string
	nodeIdx    map[string]int
	syms       []Symbol
	fileSyms   map[string][]Symbol
	symsByName map[string][]Symbol
	defs       map[string][]string
	expDefs    map[string]map[string]bool
	refCount   map[string]int
	outW       []float64
	eFrom      []int32
	eTo        []int32
	eCoef      []float64
	edgeCount  int
	in         map[string][]string // files that import each file
	imports    map[string][]string // files each file imports (resolved)
	renderMu   sync.Mutex          // guards rendered
	rendered   []string            // paths in the last Render, for memory injection
	rank       []float64
	cochange   map[string][]Pair
	recent     []string
	lastParsed int
	warning    string               // set by Build: a truncated walk, and similar notes
	callers    map[string][]callRef // callee name -> functions whose body calls it
	summarizer Summarizer
	summaries  map[string]summaryEntry // model summaries, keyed by path
}

// summaryEntry is a model-written summary and the content hash it describes.
// It is shown only while the file still has that hash.
type summaryEntry struct {
	hash, text string
}

// Open prepares a map for the repository at root. It does not scan anything;
// call Build.
func Open(root string, opts Options) (*Map, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("repomap: %s is not a directory", root)
	}
	if opts.Workers <= 0 {
		opts.Workers = runtime.GOMAXPROCS(0)
	}
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = 1 << 20
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = 10000
	}
	dsn := ":memory:"
	if !opts.InMemory {
		dir := filepath.Join(abs, ".scribe", "cache")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("repomap: %w", err)
		}
		dsn = filepath.Join(dir, "map.db")
	}
	db, err := openStore(dsn)
	if err != nil {
		return nil, err
	}
	summaries, err := loadSummaries(db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("repomap cache: %w", err)
	}
	return &Map{root: abs, opts: opts, db: db, files: map[string]*fileInfo{}, cochange: map[string][]Pair{}, summaries: summaries}, nil
}

// Warning returns a note from the last Build about partial coverage, or "".
func (m *Map) Warning() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.warning
}

// Recheck compares each indexed file's modification time with the disk and
// re-indexes the ones that moved. It is for changes the edit tools did not
// report, such as a shell command that rewrote files. It returns how many
// files it re-indexed.
func (m *Map) Recheck() (int, error) {
	m.mu.RLock()
	var stale []string
	for _, p := range m.paths {
		fi := m.files[p]
		st, err := os.Stat(filepath.Join(m.root, filepath.FromSlash(p)))
		if err != nil || fi == nil || st.ModTime().UnixNano() != fi.Mtime {
			stale = append(stale, p)
		}
	}
	m.mu.RUnlock()
	if len(stale) == 0 {
		return 0, nil
	}
	return len(stale), m.Update(stale)
}

// Close releases the cache database.
func (m *Map) Close() error { return m.db.Close() }

// Root returns the absolute repository root.
func (m *Map) Root() string { return m.root }

// LastParsed reports how many files the most recent Build or Update parsed
// (read and extracted). Files served from the cache do not count.
func (m *Map) LastParsed() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastParsed
}

type fileResult struct {
	rel    string
	fi     *fileInfo
	row    cacheRow
	write  bool
	parsed bool
}

// Build scans the repository and brings the map up to date. progress, if
// non-nil, is called after each file with (done, total). A home directory or
// filesystem root is refused with ErrUnsafeRoot before anything is read.
func (m *Map) Build(ctx context.Context, progress func(done, total int)) error {
	if unsafeRoot(m.root) {
		return ErrUnsafeRoot
	}
	m.buildMu.Lock()
	defer m.buildMu.Unlock()
	entries, note, err := collectFiles(ctx, m.root, m.opts)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.warning = note
	m.mu.Unlock()
	rows, err := loadRows(m.db)
	if err != nil {
		return err
	}

	jobs := make(chan fileEntry)
	results := make(chan fileResult)
	var wg sync.WaitGroup
	for w := 0; w < m.opts.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range jobs {
				row, have := rows[e.rel]
				results <- m.process(e, row, have)
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, e := range entries {
			select {
			case jobs <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	files := make(map[string]*fileInfo, len(entries))
	var ups []upsert
	parsed, done := 0, 0
	for r := range results {
		done++
		if progress != nil {
			progress(done, len(entries))
		}
		if r.fi == nil {
			continue
		}
		files[r.rel] = r.fi
		if r.write {
			ups = append(ups, upsert{rel: r.rel, row: r.row})
		}
		if r.parsed {
			parsed++
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var dels []string
	for p := range rows {
		if files[p] == nil {
			dels = append(dels, p)
		}
	}
	// Safety from the spec: a collection hiccup that sees most files as gone
	// must not wipe the cache.
	if len(rows) >= 10 && len(dels)*5 > len(rows)*4 {
		dels = nil
	}
	if err := writeRows(m.db, ups, dels); err != nil {
		return err
	}
	m.dropSummaries(dels)
	co := gitCoChanges(ctx, m.root)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastParsed = parsed
	m.recompute(files, co)
	return nil
}

// process produces the extracted info for one file, reusing the cached copy
// when mtime and size match, or when the content hash matches.
func (m *Map) process(e fileEntry, row cacheRow, have bool) fileResult {
	if have && row.mtime == e.mtime && row.size == e.size {
		if fi, err := decodeInfo(row.data); err == nil {
			fi.Mtime = e.mtime
			fi.Hash = row.hash
			return fileResult{rel: e.rel, fi: fi, row: row}
		}
	}
	src, err := os.ReadFile(e.abs)
	if err != nil || isBinary(src) {
		return fileResult{}
	}
	hash := hashBytes(src)
	if have && row.hash == hash {
		if fi, err := decodeInfo(row.data); err == nil {
			fi.Mtime = e.mtime
			fi.Hash = hash
			return fileResult{rel: e.rel, fi: fi, row: cacheRow{mtime: e.mtime, size: e.size, hash: hash, data: row.data}, write: true}
		}
	}
	fi := parseSafe(e.rel, src)
	if fi == nil {
		return fileResult{}
	}
	fi.Mtime = e.mtime
	fi.Hash = hash
	data, err := encodeInfo(fi)
	if err != nil {
		return fileResult{}
	}
	return fileResult{rel: e.rel, fi: fi, row: cacheRow{mtime: e.mtime, size: e.size, hash: hash, data: data}, write: true, parsed: true}
}

// Update re-reads the given files (absolute or repo-relative), reparses them
// and refreshes the graph. Files that no longer exist are dropped. Updated
// files are boosted in the next Render.
func (m *Map) Update(paths []string) error {
	m.buildMu.Lock()
	defer m.buildMu.Unlock()
	m.mu.RLock()
	files := make(map[string]*fileInfo, len(m.files))
	for k, v := range m.files {
		files[k] = v
	}
	m.mu.RUnlock()

	var ups []upsert
	var dels, touched []string
	parsed := 0
	for _, p := range paths {
		rel := m.relPath(p)
		if rel == "" || skipPath(rel) || langFor(rel) == nil {
			continue
		}
		abs := filepath.Join(m.root, filepath.FromSlash(rel))
		st, err := os.Lstat(abs)
		touched = append(touched, rel)
		if err != nil || !st.Mode().IsRegular() || st.Size() > m.opts.MaxFileBytes {
			if files[rel] != nil {
				delete(files, rel)
				dels = append(dels, rel)
			}
			continue
		}
		src, err := os.ReadFile(abs)
		if err != nil || isBinary(src) {
			continue
		}
		fi := parseSafe(rel, src)
		if fi == nil {
			continue
		}
		fi.Mtime = st.ModTime().UnixNano()
		hash := hashBytes(src)
		fi.Hash = hash
		data, err := encodeInfo(fi)
		if err != nil {
			continue
		}
		files[rel] = fi
		parsed++
		ups = append(ups, upsert{rel: rel, row: cacheRow{mtime: st.ModTime().UnixNano(), size: st.Size(), hash: hash, data: data}})
	}
	if err := writeRows(m.db, ups, dels); err != nil {
		return err
	}
	m.dropSummaries(dels)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastParsed = parsed
	m.recent = append(touched, m.recent...)
	if len(m.recent) > 20 {
		m.recent = m.recent[:20]
	}
	m.recompute(files, m.cochange)
	return nil
}

// relPath turns a path given by a caller into a repo-relative slash path, or
// "" when it lies outside the repo.
func (m *Map) relPath(p string) string {
	if filepath.IsAbs(p) {
		r, err := filepath.Rel(m.root, p)
		if err != nil {
			return ""
		}
		p = r
	}
	p = filepath.ToSlash(filepath.Clean(p))
	if p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return ""
	}
	return p
}

// parseSafe runs parseSource and turns a panic on malformed input into "no
// result", so one odd file cannot take down a whole build.
func parseSafe(rel string, src []byte) (fi *fileInfo) {
	defer func() {
		if recover() != nil {
			fi = nil
		}
	}()
	return parseSource(rel, src)
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
