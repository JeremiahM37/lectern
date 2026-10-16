package repomap

import (
	"context"
	"sort"
	"strings"
)

// Summarizer writes one-line summaries of source files for the map header.
// The app implements it over the router's compact model. repomap never calls
// a model itself: with no Summarizer set, SummarizeMissing does nothing and
// the header falls back to the file's doc comment, if it has one.
type Summarizer interface {
	// Summarize returns one summary per request, in the same order. An empty
	// string means "no summary" for that file.
	Summarize(ctx context.Context, files []SummaryRequest) ([]string, error)
}

// SummaryRequest is what a Summarizer sees for one file: path, language, doc
// comment and exported signatures. Source text is never sent.
type SummaryRequest struct {
	Path       string   `json:"path"`
	Lang       string   `json:"lang"`
	Doc        string   `json:"doc,omitempty"`
	Signatures []string `json:"signatures,omitempty"`
}

// summaryBatch is how many files one Summarize call covers.
const summaryBatch = 10

// maxSummaryRequestSigs caps the signatures sent for one file.
const maxSummaryRequestSigs = 15

// maxSummaryChars caps a summary shown in the map.
const maxSummaryChars = 160

// SetSummarizer installs the model used by SummarizeMissing. nil removes it.
func (m *Map) SetSummarizer(s Summarizer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.summarizer = s
}

// SummarizeMissing asks the Summarizer for summaries of up to limit of the
// highest-ranked files that have none for their current content. It stores
// what it gets and returns how many summaries it stored. Without a
// Summarizer it does nothing. Call it from the background: it waits on the
// model, and the map stays usable while it does.
func (m *Map) SummarizeMissing(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 50
	}
	m.mu.RLock()
	sum := m.summarizer
	if sum == nil || len(m.paths) == 0 {
		m.mu.RUnlock()
		return 0, nil
	}
	type want struct {
		path, hash string
		req        SummaryRequest
	}
	idx := make([]int, len(m.paths))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return m.rank[idx[a]] > m.rank[idx[b]] })
	var todo []want
	for _, i := range idx {
		if len(todo) >= limit {
			break
		}
		p := m.paths[i]
		fi := m.files[p]
		if fi == nil || len(fi.Symbols) == 0 || fi.Hash == "" {
			continue
		}
		if e, ok := m.summaries[p]; ok && e.hash == fi.Hash {
			continue
		}
		todo = append(todo, want{path: p, hash: fi.Hash, req: summaryRequest(p, fi)})
	}
	m.mu.RUnlock()

	put := map[string]summaryEntry{}
	var callErr error
	for start := 0; start < len(todo) && callErr == nil; start += summaryBatch {
		end := min(start+summaryBatch, len(todo))
		reqs := make([]SummaryRequest, 0, end-start)
		for _, w := range todo[start:end] {
			reqs = append(reqs, w.req)
		}
		texts, err := sum.Summarize(ctx, reqs)
		if err != nil {
			callErr = err
			break
		}
		for k, w := range todo[start:end] {
			if k < len(texts) {
				if t := cleanSummary(texts[k]); t != "" {
					put[w.path] = summaryEntry{hash: w.hash, text: t}
				}
			}
		}
	}

	// A file edited while the model ran has a new hash; its summary is stale.
	m.mu.RLock()
	for p, e := range put {
		if fi := m.files[p]; fi == nil || fi.Hash != e.hash {
			delete(put, p)
		}
	}
	m.mu.RUnlock()
	if err := writeSummaries(m.db, put, nil); err != nil {
		return 0, err
	}
	m.mu.Lock()
	for p, e := range put {
		m.summaries[p] = e
	}
	m.mu.Unlock()
	return len(put), callErr
}

// summaryRequest builds what the model sees for one file.
func summaryRequest(p string, fi *fileInfo) SummaryRequest {
	req := SummaryRequest{Path: p, Lang: fi.Lang, Doc: fi.Doc}
	for _, s := range fi.Symbols {
		if s.Exported && len(req.Signatures) < maxSummaryRequestSigs {
			sig := s.Signature
			if sig == "" {
				sig = s.Kind + " " + s.Name
			}
			req.Signatures = append(req.Signatures, sig)
		}
	}
	return req
}

// cleanSummary makes a model reply one line of bounded length.
func cleanSummary(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(s, "\"'` ")
	if len(s) > maxSummaryChars {
		s = s[:maxSummaryChars-3] + "..."
	}
	return s
}

// dropSummaries forgets summaries for files that left the map. A stale row
// is also ignored by its hash, so a failed delete costs only disk.
func (m *Map) dropSummaries(paths []string) {
	if len(paths) == 0 {
		return
	}
	m.mu.Lock()
	for _, p := range paths {
		delete(m.summaries, p)
	}
	m.mu.Unlock()
	_ = writeSummaries(m.db, nil, paths)
}

// headerFor is the one-line header shown under a file in the map: a model
// summary while it matches the file's content, else the doc comment. Callers
// hold m.mu.
func (m *Map) headerFor(p string, fi *fileInfo) string {
	if fi == nil {
		return ""
	}
	if e, ok := m.summaries[p]; ok && e.hash == fi.Hash {
		return e.text
	}
	return fi.Doc
}
