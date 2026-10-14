package conflicts

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSubject is a Subject whose fingerprint and diff are set by the test and
// which counts how often the expensive diff is read.
type fakeSubject struct {
	ref    Ref
	fpr    string
	fprErr error
	patch  string
	diffs  atomic.Int32
}

func (f *fakeSubject) subject() Subject {
	return Subject{
		Ref: f.ref,
		Fingerprint: func(context.Context) (string, error) {
			return f.fpr, f.fprErr
		},
		Diff: func(context.Context) (string, error) {
			f.diffs.Add(1)
			return f.patch, nil
		},
	}
}

func collectorOf(fs ...*fakeSubject) Collector {
	return func(context.Context, int64) ([]Subject, error) {
		out := make([]Subject, 0, len(fs))
		for _, f := range fs {
			out = append(out, f.subject())
		}
		return out, nil
	}
}

func TestScanFindsSemanticPairAcrossSessions(t *testing.T) {
	a := &fakeSubject{ref: Ref{Kind: "session", ID: 12, Title: "parser"}, fpr: "a1", patch: goParseDiff}
	b := &fakeSubject{ref: Ref{Kind: "session", ID: 14, Title: "loader"}, fpr: "b1", patch: goOtherParseEdit}
	s := NewScanner(collectorOf(a, b))

	pairs, err := s.Scan(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 {
		t.Fatalf("pairs = %+v, want exactly one", pairs)
	}
	p := pairs[0]
	if p.Level != LevelSemantic {
		t.Fatalf("level = %s", p.Level)
	}
	want := "Session #12 changes Parse (internal/x/parse.go); Session #14's new code uses Parse"
	if len(p.Reasons) == 0 || p.Reasons[0] != want {
		t.Fatalf("reasons = %v, want first %q", p.Reasons, want)
	}
}

func TestScanSkipsNoneAndKeepsOnlyInterestingPairs(t *testing.T) {
	a := &fakeSubject{ref: Ref{Kind: "session", ID: 1}, fpr: "a", patch: goParseDiff}
	b := &fakeSubject{ref: Ref{Kind: "session", ID: 2}, fpr: "b", patch: goUnrelatedEdit}
	c := &fakeSubject{ref: Ref{Kind: "attempt", ID: 9}, fpr: "c", patch: goOtherParseEdit}
	s := NewScanner(collectorOf(a, b, c))

	pairs, err := s.Scan(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	// Pairs are stored once, with A the subject that sorts first by key
	// ("attempt:9" before "session:1").
	if len(pairs) != 1 || pairs[0].A.Kind != "attempt" || pairs[0].A.ID != 9 || pairs[0].B.ID != 1 {
		t.Fatalf("pairs = %+v, want only attempt 9 with session 1", pairs)
	}
	if got := pairs[0].A.Label(); got != "Attempt #9" {
		t.Fatalf("attempt label = %q", got)
	}
}

func TestScanCachesDiffsByFingerprint(t *testing.T) {
	a := &fakeSubject{ref: Ref{Kind: "session", ID: 1}, fpr: "a", patch: goParseDiff}
	b := &fakeSubject{ref: Ref{Kind: "session", ID: 2}, fpr: "b", patch: goOtherParseEdit}
	s := NewScanner(collectorOf(a, b))
	ctx := context.Background()

	if _, err := s.Scan(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if a.diffs.Load() != 1 || b.diffs.Load() != 1 {
		t.Fatalf("diff reads a=%d b=%d after two scans, want 1 each", a.diffs.Load(), b.diffs.Load())
	}

	// A worktree that moved reads its diff again; the other one stays cached.
	a.fpr, a.patch = "a2", goUnrelatedEdit
	pairs, err := s.Scan(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if a.diffs.Load() != 2 || b.diffs.Load() != 1 {
		t.Fatalf("diff reads a=%d b=%d after change, want 2 and 1", a.diffs.Load(), b.diffs.Load())
	}
	if len(pairs) != 0 {
		t.Fatalf("after a's change the pair should be none, got %+v", pairs)
	}
}

func TestScanSkipsSubjectWhoseFingerprintFails(t *testing.T) {
	a := &fakeSubject{ref: Ref{Kind: "session", ID: 1}, fpr: "a", patch: goParseDiff}
	b := &fakeSubject{ref: Ref{Kind: "session", ID: 2}, fprErr: errors.New("target unreachable"), patch: goOtherParseEdit}
	s := NewScanner(collectorOf(a, b))

	pairs, err := s.Scan(context.Background(), 1)
	if err != nil {
		t.Fatalf("one unreachable subject must not fail the scan: %v", err)
	}
	if len(pairs) != 0 {
		t.Fatalf("unreachable subject should be left out, got %+v", pairs)
	}
	if b.diffs.Load() != 0 {
		t.Fatalf("diff must not be read when the fingerprint fails")
	}
}

func TestScanCollectorErrorPropagates(t *testing.T) {
	boom := errors.New("db down")
	s := NewScanner(func(context.Context, int64) ([]Subject, error) { return nil, boom })
	if _, err := s.Scan(context.Background(), 1); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

func TestLastAndForRef(t *testing.T) {
	a := &fakeSubject{ref: Ref{Kind: "session", ID: 1}, fpr: "a", patch: goParseDiff}
	b := &fakeSubject{ref: Ref{Kind: "session", ID: 2}, fpr: "b", patch: goOtherParseEdit}
	c := &fakeSubject{ref: Ref{Kind: "session", ID: 3}, fpr: "c", patch: goUnrelatedEdit}
	s := NewScanner(collectorOf(a, b, c))

	if got := s.Last(1); got != nil {
		t.Fatalf("Last before any scan = %+v, want nil", got)
	}
	pairs, err := s.Scan(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Last(1); len(got) != len(pairs) {
		t.Fatalf("Last = %d pairs, scan had %d", len(got), len(pairs))
	}
	if got := ForRef(pairs, Ref{Kind: "session", ID: 3}); len(got) != 0 {
		t.Fatalf("unrelated session 3 should have no pairs, got %+v", got)
	}
	if got := ForRef(pairs, Ref{Kind: "session", ID: 2}); len(got) != 1 {
		t.Fatalf("session 2 should have one pair, got %+v", got)
	}
}

func TestWarmRefreshesOnlyWhenStale(t *testing.T) {
	a := &fakeSubject{ref: Ref{Kind: "session", ID: 1}, fpr: "a", patch: goParseDiff}
	s := NewScanner(collectorOf(a))
	s.Warm(1, time.Hour)
	waitFor(t, func() bool { return a.diffs.Load() > 0 || s.Last(1) != nil || lastAtSet(s, 1) })
	before := a.diffs.Load()
	s.Warm(1, time.Hour) // fresh: must not scan again
	time.Sleep(20 * time.Millisecond)
	if a.diffs.Load() != before {
		t.Fatalf("fresh project rescanned: diffs %d -> %d", before, a.diffs.Load())
	}
}

func lastAtSet(s *Scanner, id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.lastAt[id].IsZero()
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not reached in time")
}
