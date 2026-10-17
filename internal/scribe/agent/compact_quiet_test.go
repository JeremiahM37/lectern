package agent

import (
	"context"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

type shortHistoryCompactor struct{}

func (shortHistoryCompactor) Compact(context.Context, []llm.Message) ([]llm.Message, string, error) {
	return nil, "", ErrNotEnoughHistory
}

// An automatic compaction that finds too little history says nothing: the
// user asked for nothing. /compact (Compact) still explains itself.
func TestAutoCompactWithShortHistoryIsQuiet(t *testing.T) {
	notices := func(auto bool) int {
		f := newFake(t)
		log := &eventLog{}
		opts := baseOptions(f, lookRegistry(), log, t.TempDir())
		opts.Compactor = shortHistoryCompactor{}
		a := New(opts)
		if auto {
			_ = a.compact(context.Background(), "context 80% full", true)
		} else {
			_ = a.Compact(context.Background(), "requested")
		}
		n := 0
		for _, e := range log.snapshot() {
			if e.Kind == EvNotice {
				n++
			}
		}
		return n
	}
	if n := notices(true); n != 0 {
		t.Fatalf("automatic compaction showed %d notices, want none", n)
	}
	if n := notices(false); n != 1 {
		t.Fatalf("/compact showed %d notices, want 1", n)
	}
}

type cancelledCompactor struct{}

func (cancelledCompactor) Compact(ctx context.Context, _ []llm.Message) ([]llm.Message, string, error) {
	return nil, "", ctx.Err()
}

func TestCancelledCompactionIsNotAWarning(t *testing.T) {
	f := newFake(t)
	log := &eventLog{}
	opts := baseOptions(f, lookRegistry(), log, t.TempDir())
	opts.Compactor = cancelledCompactor{}
	a := New(opts)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = a.Compact(ctx, "requested")
	for _, e := range log.snapshot() {
		if e.Kind == EvWarning {
			t.Fatalf("cancelled compaction warned: %q", e.Text)
		}
	}
}
