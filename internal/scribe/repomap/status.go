package repomap

import (
	"os"
	"path/filepath"
)

// Status is what the /repo-map view shows beyond the graph counts. Everything
// here is read from memory or from a stat call, so the view can poll it.
type Status struct {
	Summaries     int    // model summaries that still match their file's content
	DBBytes       int64  // cache file size on disk; 0 when the map is in memory
	Warning       string // partial-coverage note from the last build
	SummaryMode   string // off, ast, synthetic, llm or full
	AutoRegen     bool   // whether a build should ask the model again
	HasSummarizer bool   // a model is installed for summaries
}

// Status reports the current state of the map.
func (m *Map) Status() Status {
	m.mu.RLock()
	st := Status{
		Warning:       m.warning,
		SummaryMode:   m.summaryMode,
		AutoRegen:     m.autoRegen,
		HasSummarizer: m.summarizer != nil,
	}
	if st.SummaryMode == "" {
		st.SummaryMode = SummaryAST
	}
	for _, p := range m.paths {
		e, ok := m.summaries[p]
		if !ok {
			continue
		}
		if fi := m.files[p]; fi != nil && fi.Hash == e.hash {
			st.Summaries++
		}
	}
	m.mu.RUnlock()
	st.DBBytes = m.dbBytes()
	return st
}

// dbBytes sums the cache database and its write-ahead log. An in-memory map
// has no file, so it reports 0.
func (m *Map) dbBytes() int64 {
	if m.opts.InMemory {
		return 0
	}
	base := filepath.Join(m.root, ".scribe", "cache", "map.db")
	var total int64
	for _, p := range []string{base, base + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			total += fi.Size()
		}
	}
	return total
}
