package fs

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// readState remembers, per file, which line ranges this session's read tool
// has already sent to the model, and the content hash they came from. A
// re-read of lines that are still unchanged gets a short notice instead of
// the same text again. One readState belongs to one registry, so each
// session keeps its own table.
type readState struct {
	mu    sync.Mutex
	files map[string]*fileReads
}

// fileReads is what the model has been sent for one file at one content hash.
// spans are sorted, disjoint, 1-based inclusive line ranges.
type fileReads struct {
	hash  string
	spans [][2]int
}

func newReadState() *readState {
	return &readState{files: map[string]*fileReads{}}
}

// maxReadEntries bounds the table; past it the table starts over, which only
// costs a re-send of content the model may already have.
const maxReadEntries = 4096

// readKey scopes a file to the conversation that read it. Subagents share
// the parent's tools but not its history, and compaction drops what was
// read, so each (Scope, file) pair is tracked on its own.
func readKey(env *tools.Env, abs string) string {
	if env == nil || env.Scope == "" {
		return abs
	}
	return env.Scope + "\x00" + abs
}

// note records that lines lo..hi of abs, whose content hashes to hash, are
// being sent to the model. It reports whether the model already had those
// lines unchanged, in which case the caller should send the short notice
// instead. force makes it report false, so the content is sent again.
func (s *readState) note(abs, hash string, lo, hi int, force bool) (unchanged bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fr := s.files[abs]
	if fr == nil && len(s.files) >= maxReadEntries {
		s.files = map[string]*fileReads{}
	}
	if fr == nil || fr.hash != hash {
		// The file changed (or was never read): nothing sent before still
		// describes it, so start a new table.
		fr = &fileReads{hash: hash}
		s.files[abs] = fr
	}
	unchanged = !force && fr.covers(lo, hi)
	fr.add(lo, hi)
	return unchanged
}

// covers reports whether lines lo..hi all lie inside one recorded span.
// Spans are merged on add, so adjacent ranges count as one.
func (f *fileReads) covers(lo, hi int) bool {
	for _, s := range f.spans {
		if s[0] <= lo && hi <= s[1] {
			return true
		}
	}
	return false
}

// add records lo..hi, merging it with any span it overlaps or touches.
func (f *fileReads) add(lo, hi int) {
	spans := append(f.spans, [2]int{lo, hi})
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	out := spans[:1]
	for _, s := range spans[1:] {
		last := &out[len(out)-1]
		if s[0] <= last[1]+1 {
			if s[1] > last[1] {
				last[1] = s[1]
			}
			continue
		}
		out = append(out, s)
	}
	f.spans = out
}

// shortHash is the hash the tools show and accept: the first 12 hex digits of
// SHA-256 over the text.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// noteSpans is note for several spans of one file read in one call. It
// reports whether every span was already in context at the same hash, and
// records all of them either way.
func (s *readState) noteSpans(abs, hash string, spans [][2]int, force bool) (unchanged bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fr := s.files[abs]
	if fr == nil && len(s.files) >= maxReadEntries {
		s.files = map[string]*fileReads{}
	}
	if fr == nil || fr.hash != hash {
		fr = &fileReads{hash: hash}
		s.files[abs] = fr
	}
	unchanged = !force && len(spans) > 0
	for _, sp := range spans {
		if !fr.covers(sp[0], sp[1]) {
			unchanged = false
		}
	}
	for _, sp := range spans {
		fr.add(sp[0], sp[1])
	}
	return unchanged
}
