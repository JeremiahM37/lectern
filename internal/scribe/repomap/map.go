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
	"sync/atomic"
	"time"
)

// Options tunes a Map. Zero values pick defaults.
type Options struct {
	Workers      int   // parse workers; default GOMAXPROCS
	MaxFileBytes int64 // skip larger files; default 1 MiB
	MaxFiles     int   // index at most this many files; default 10000
	InMemory     bool  // keep the cache in memory (tests, throwaway use)
	// IdleTimeout abandons a Build when no file finishes for this long, so a
	// stuck parse cannot hold the map forever (spec 4.2: 10 minutes). Zero
	// picks the default; a negative value turns the check off.
	IdleTimeout time.Duration
	// stall, when set, runs in a worker before each file is parsed. Tests use
	// it to hold a parse open and exercise the idle watchdog.
	stall func(rel string)
}

// DefaultIdleTimeout is the scan idle limit when Options.IdleTimeout is zero.
const DefaultIdleTimeout = 10 * time.Minute

// Map is the code map for one repository. Build and Update are serialised;
// queries take a read lock and may run concurrently with either.
type Map struct {
	root string
	opts Options
	db   *sql.DB

	buildMu sync.Mutex
	mu      sync.RWMutex
	tri     triIndex // file-content trigrams for grep (trigram.go)

	files        map[string]*fileInfo
	paths        []string
	nodeIdx      map[string]int
	syms         []Symbol
	fileSyms     map[string][]Symbol
	symsByName   map[string][]Symbol
	defs         map[string][]string
	expDefs      map[string]map[string]bool
	refCount     map[string]int
	outW         []float64
	eFrom        []int32
	eTo          []int32
	eCoef        []float64
	edgeCount    int
	in           map[string][]string // files that import each file
	imports      map[string][]string // files each file imports (resolved)
	renderMu     sync.Mutex          // guards rendered
	rendered     []string            // paths in the last Render, for memory injection
	rank         []float64
	cochange     map[string][]Pair
	recent       []string
	lastParsed   int
	warning      string               // set by Build: a truncated walk, and similar notes
	buildErr     string               // the error the last Build returned; "" when it finished
	renderTokens int                  // estimated size of the last Render, in tokens
	callers      map[string][]callRef // callee name -> functions whose body calls it
	summarizer   Summarizer
	summaries    map[string]summaryEntry // model summaries, keyed by path
	// summaryMode picks what the map shows as a symbol's one-line summary; see
	// SetSummaryMode.
	summaryMode string
	autoRegen   bool // see SetAutoRegen
	// reexports lists, for each barrel, the files it re-exports from.
	reexports map[string][]string
	// derived holds indexes built on first use from the file set (symbol
	// trigrams, clone candidates). It is dropped by recompute and rebuilt
	// under derivedMu by whichever query needs it first.
	derivedMu sync.Mutex
	derived   *derivedIndex
	// generation counts every recompute of the graph.
	// Readers compare it to tell whether a rendered map is still current.
	generation atomic.Uint64
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
	stale := m.staleFiles()
	if len(stale) == 0 {
		return 0, nil
	}
	return len(stale), m.Update(stale)
}

// staleFiles returns the indexed files whose modification time on disk no
// longer matches the map, including files that were deleted.
func (m *Map) staleFiles() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var stale []string
	for _, p := range m.paths {
		fi := m.files[p]
		st, err := os.Stat(filepath.Join(m.root, filepath.FromSlash(p)))
		if err != nil || fi == nil || st.ModTime().UnixNano() != fi.Mtime {
			stale = append(stale, p)
		}
	}
	return stale
}

// Close releases the cache database.
// It waits for a running Build or Update (the file watcher's included) so the
// database is never closed under one.
func (m *Map) Close() error {
	m.buildMu.Lock()
	defer m.buildMu.Unlock()
	return m.db.Close()
}

// Clear empties the map: its cache rows, its summaries, its trigram index and
// its in-memory graph. The next Build starts from nothing. It waits for a
// running Build or Update, as Close does.
func (m *Map) Clear() error {
	m.buildMu.Lock()
	defer m.buildMu.Unlock()
	m.mu.RLock()
	paths := append([]string(nil), m.paths...)
	m.mu.RUnlock()
	if err := writeRows(m.db, nil, paths); err != nil {
		return err
	}
	m.mu.RLock()
	sums := make([]string, 0, len(m.summaries))
	for p := range m.summaries {
		sums = append(sums, p)
	}
	m.mu.RUnlock()
	if err := writeSummaries(m.db, nil, sums); err != nil {
		return err
	}
	m.tri.mu.Lock()
	m.tri.files = map[string]triEntry{}
	m.tri.mu.Unlock()
	m.mu.Lock()
	m.summaries = map[string]summaryEntry{}
	m.cochange = map[string][]Pair{}
	m.recent = nil
	m.warning = ""
	m.lastParsed = 0
	m.recompute(map[string]*fileInfo{}, map[string][]Pair{})
	m.mu.Unlock()
	m.recordRendered(nil)
	return nil
}

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
// filesystem root is refused with ErrUnsafeRoot before anything is read. The
// error is kept for Status, so the /repo-map view can show why a build failed
// even when the caller dropped it.
func (m *Map) Build(ctx context.Context, progress func(done, total int)) error {
	err := m.build(ctx, progress)
	m.mu.Lock()
	m.buildErr = ""
	if err != nil {
		m.buildErr = err.Error()
	}
	m.mu.Unlock()
	return err
}

func (m *Map) build(ctx context.Context, progress func(done, total int)) error {
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

	// The scan runs under its own context so the idle watchdog can abandon it.
	// A worker stuck in one parse cannot be stopped, so on a stall Build stops
	// waiting for it, and a goroutine drains the stragglers later.
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var lastProgress atomic.Int64
	lastProgress.Store(time.Now().UnixNano())
	var stalled atomic.Bool
	idle := m.opts.IdleTimeout
	if idle == 0 {
		idle = DefaultIdleTimeout
	}
	if idle > 0 {
		go func() {
			tick := time.NewTicker(max(idle/8, time.Millisecond))
			defer tick.Stop()
			for {
				select {
				case <-scanCtx.Done():
					return
				case <-tick.C:
					if time.Since(time.Unix(0, lastProgress.Load())) > idle {
						stalled.Store(true)
						cancel()
						return
					}
				}
			}
		}()
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
				if m.opts.stall != nil {
					m.opts.stall(e.rel)
				}
				results <- m.process(e, row, have)
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, e := range entries {
			select {
			case jobs <- e:
			case <-scanCtx.Done():
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
	for {
		var r fileResult
		var open bool
		select {
		case r, open = <-results:
		case <-scanCtx.Done():
			go func() {
				for range results {
				}
			}()
			if stalled.Load() {
				return fmt.Errorf("repomap: scan stalled: no file finished for %s, so the scan was abandoned. Check for a file that hangs the parser, or set a longer IdleTimeout", idle)
			}
			return ctx.Err()
		}
		if !open {
			break
		}
		lastProgress.Store(time.Now().UnixNano())
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
	m.indexTrigrams(ctx, collectTextFiles(ctx, m.root), nil, true)
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
	m.updateTrigrams(context.Background(), touched)
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
