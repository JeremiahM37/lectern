package conflicts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Ref names one piece of live work: a session or a running task attempt.
type Ref struct {
	Kind  string `json:"kind"` // "session" | "attempt"
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

// Label is how a Ref reads inside a reason sentence.
func (r Ref) Label() string {
	if r.Kind == "attempt" {
		return fmt.Sprintf("Attempt #%d", r.ID)
	}
	return fmt.Sprintf("Session #%d", r.ID)
}

// key identifies a Ref for caching.
func (r Ref) key() string { return fmt.Sprintf("%s:%d", r.Kind, r.ID) }

// Subject is one live piece of work the scanner can read. Fingerprint is
// cheap and changes whenever the worktree does; Diff is the expensive capture
// and is only called when the fingerprint is new.
type Subject struct {
	Ref
	Fingerprint func(ctx context.Context) (string, error)
	Diff        func(ctx context.Context) (string, error)
}

// Collector lists the live subjects for one project. It is a function so the
// scanner can be tested without a database or git.
type Collector func(ctx context.Context, projectID int64) ([]Subject, error)

// Pair is one predicted interaction between two subjects. A is always the
// one that sorts first by kind and id, so a pair appears once.
type Pair struct {
	A       Ref      `json:"a"`
	B       Ref      `json:"b"`
	Level   string   `json:"level"`
	Reasons []string `json:"reasons"`
}

const maxCachedPairs = 4096

type footprintEntry struct {
	fingerprint string
	fp          Footprint
}

// Scanner predicts pairwise conflicts for a project. It caches footprints by
// fingerprint and pair predictions by both footprints, so a call on an idle
// project costs one fingerprint per subject and no parsing.
type Scanner struct {
	collect Collector

	mu        sync.Mutex
	footprint map[string]footprintEntry
	pairs     map[string]Risk
	last      map[int64][]Pair // project -> most recent result, for hook-path lookups
	lastAt    map[int64]time.Time
	running   map[int64]bool
}

// NewScanner builds a Scanner over a collector.
func NewScanner(collect Collector) *Scanner {
	return &Scanner{
		collect:   collect,
		footprint: map[string]footprintEntry{},
		pairs:     map[string]Risk{},
		last:      map[int64][]Pair{},
		lastAt:    map[int64]time.Time{},
		running:   map[int64]bool{},
	}
}

// Warm starts a background Scan for a project whose last result is older than
// maxAge, and returns at once. A scan already in flight is not duplicated. It
// exists for the hook path, which must never wait on a diff: the hook reads
// Last now and gets the fresher result on a later prompt.
func (s *Scanner) Warm(projectID int64, maxAge time.Duration) {
	s.mu.Lock()
	if s.running[projectID] || (!s.lastAt[projectID].IsZero() && time.Since(s.lastAt[projectID]) < maxAge) {
		s.mu.Unlock()
		return
	}
	s.running[projectID] = true
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.running, projectID)
			s.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		_, _ = s.Scan(ctx, projectID)
	}()
}

// Scan collects the project's live subjects and returns every pair whose
// prediction is not "none", ordered by level (semantic first) and then by
// reference. Subjects whose fingerprint or diff cannot be read are left out
// rather than failing the whole scan: one unreachable worktree should not hide
// the others.
func (s *Scanner) Scan(ctx context.Context, projectID int64) ([]Pair, error) {
	subjects, err := s.collect(ctx, projectID)
	if err != nil {
		return nil, err
	}
	sort.Slice(subjects, func(i, j int) bool { return subjects[i].key() < subjects[j].key() })

	type ready struct {
		ref Ref
		fp  Footprint
		fpr string
	}
	var rs []ready
	for _, sub := range subjects {
		fpr, err := sub.Fingerprint(ctx)
		if err != nil {
			continue
		}
		fp, ok := s.footprintFor(ctx, sub, fpr)
		if !ok {
			continue
		}
		rs = append(rs, ready{ref: sub.Ref, fp: fp, fpr: fpr})
	}

	out := []Pair{}
	for i := 0; i < len(rs); i++ {
		for j := i + 1; j < len(rs); j++ {
			risk := s.predict(rs[i].ref, rs[j].ref, rs[i].fpr, rs[j].fpr, rs[i].fp, rs[j].fp)
			if risk.Level == LevelNone {
				continue
			}
			out = append(out, Pair{A: rs[i].ref, B: rs[j].ref, Level: risk.Level, Reasons: risk.Reasons})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Level != out[j].Level {
			return out[i].Level == LevelSemantic
		}
		return false
	})

	s.mu.Lock()
	s.last[projectID] = out
	s.lastAt[projectID] = time.Now()
	s.mu.Unlock()
	return out, nil
}

// Last returns the most recent scan result for a project without touching git.
// The hook path uses it, because a hook response must never wait on a diff.
func (s *Scanner) Last(projectID int64) []Pair {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last[projectID]
}

// ForRef filters a pair list to those involving one subject.
func ForRef(pairs []Pair, r Ref) []Pair {
	var out []Pair
	for _, p := range pairs {
		if (p.A.Kind == r.Kind && p.A.ID == r.ID) || (p.B.Kind == r.Kind && p.B.ID == r.ID) {
			out = append(out, p)
		}
	}
	return out
}

func (s *Scanner) footprintFor(ctx context.Context, sub Subject, fpr string) (Footprint, bool) {
	k := sub.key()
	s.mu.Lock()
	if e, ok := s.footprint[k]; ok && e.fingerprint == fpr {
		s.mu.Unlock()
		return e.fp, true
	}
	s.mu.Unlock()

	patch, err := sub.Diff(ctx)
	if err != nil {
		return Footprint{}, false
	}
	fp := ParseFootprint(patch)
	s.mu.Lock()
	s.footprint[k] = footprintEntry{fingerprint: fpr, fp: fp}
	s.mu.Unlock()
	return fp, true
}

func (s *Scanner) predict(a, b Ref, fprA, fprB string, fpA, fpB Footprint) Risk {
	sum := sha256.Sum256([]byte(a.key() + "|" + fprA + "|" + a.Label() + "|" + b.key() + "|" + fprB + "|" + b.Label()))
	k := hex.EncodeToString(sum[:])
	s.mu.Lock()
	if r, ok := s.pairs[k]; ok {
		s.mu.Unlock()
		return r
	}
	s.mu.Unlock()
	r := PredictNamed(a.Label(), b.Label(), fpA, fpB)
	s.mu.Lock()
	// Every fingerprint change makes new keys and orphans the old ones, so the
	// map is bounded by dropping it when it grows past a generous size. A miss
	// only costs one re-prediction.
	if len(s.pairs) > maxCachedPairs {
		s.pairs = map[string]Risk{}
	}
	s.pairs[k] = r
	s.mu.Unlock()
	return r
}
