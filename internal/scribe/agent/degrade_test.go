package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// bigTranscript is a history whose tool results are large enough that the
// full summary request is over the fake provider's limit.
func bigTranscript() []llm.Message {
	var msgs []llm.Message
	for i := 0; i < 12; i++ {
		msgs = append(msgs,
			llm.UserText(strings.Repeat("please change the parser ", 200)),
			llm.Message{Role: llm.RoleAssistant, Parts: []llm.Part{{Type: llm.PartText, Text: "working on it"}}},
		)
	}
	return msgs
}

// When the provider rejects the summary request as too large, the request is
// retried with a smaller transcript until one is accepted.
func TestSummaryDegradesWhenRejectedAsTooLarge(t *testing.T) {
	// About 60 KB of transcript at level 0 and 36 KB at level 1; level 2
	// (18 KB) fits under the limit.
	const limit = 30000 // bytes of request body the fake accepts
	f := newFake(t)
	f.setPick(func(n int, req recordedReq) reply {
		if len(req.raw) > limit {
			return apiError(400, "", `{"error":{"message":"prompt is too long for this model"}}`)
		}
		return turn(sseTurn{text: []string{"Summary of the earlier work: the parser was changed and its tests pass."}})
	})
	c := SummaryCompactor{Model: fakeModel(f.srv.URL, "fake/model-a", priced), Strategy: "v1"}
	out, err := c.batchSummary(context.Background(), bigTranscript())
	if err != nil {
		t.Fatalf("batchSummary: %v", err)
	}
	if !strings.Contains(out, "parser was changed") {
		t.Fatalf("summary = %q, want the model's answer", out)
	}
	if f.count() < 2 {
		t.Fatalf("requests = %d, want a degraded retry", f.count())
	}
}

// A provider that rejects every size gets the mechanical summary, not an
// error, so compaction still shrinks the history.
func TestSummaryFallsBackWhenEverySizeIsRejected(t *testing.T) {
	f := newFake(t)
	f.setPick(func(int, recordedReq) reply {
		return apiError(413, "", `{"error":{"message":"request too large"}}`)
	})
	c := SummaryCompactor{Model: fakeModel(f.srv.URL, "fake/model-a", priced), Strategy: "v1"}
	out, err := c.batchSummary(context.Background(), bigTranscript())
	if err != nil {
		t.Fatalf("batchSummary error = %v, want the degraded summary", err)
	}
	if out != degradedSummary {
		t.Fatalf("summary = %q, want the degraded summary", out)
	}
	if got := f.count(); got != summaryDegradeLevels+1 {
		t.Fatalf("requests = %d, want one per degradation level (%d)", got, summaryDegradeLevels+1)
	}
}

// A rejection that is not about size is not degraded: it is returned.
func TestSummaryDoesNotDegradeOnOtherErrors(t *testing.T) {
	f := newFake(t)
	f.setPick(func(int, recordedReq) reply {
		return apiError(401, "", `{"error":{"message":"invalid api key"}}`)
	})
	c := SummaryCompactor{Model: fakeModel(f.srv.URL, "fake/model-a", priced), Strategy: "v1"}
	if _, err := c.batchSummary(context.Background(), bigTranscript()); err == nil {
		t.Fatal("an auth error must be returned, not summarised around")
	}
	if got := f.count(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

// countingCompactor records how often compaction ran.
type countingCompactor struct {
	mu sync.Mutex
	n  int
}

func (c *countingCompactor) Compact(_ context.Context, msgs []llm.Message) ([]llm.Message, string, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return msgs[len(msgs)-2:], "summary", nil
}

// The compaction latch: one oversized step compacts once, and the fill has
// to drop under CompactReset before the next automatic compaction.
func TestCompactionLatchHysteresis(t *testing.T) {
	cc := &countingCompactor{}
	a := New(Options{
		Model:     Model{Ref: "p/m", ID: "m", Info: llm.ModelInfo{ContextWindow: 1000}},
		Tools:     tools.NewRegistry(),
		Compactor: cc,
		Sink:      func(Event) {},
	})
	a.msgs = bigTranscript()
	fill := func(tokens int) {
		a.mu.Lock()
		a.msgs = bigTranscript() // a compaction shrinks the history; refill it
		a.lastIn = tokens
		a.mu.Unlock()
		if err := a.maybeCompact(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	fill(800) // trigger
	fill(800) // still over, latched: no second compaction
	fill(750) // still over, latched
	if cc.n != 1 {
		t.Fatalf("compactions while latched = %d, want 1", cc.n)
	}
	fill(300) // below CompactReset (0.4): the latch clears
	if cc.n != 1 {
		t.Fatalf("a fill below the trigger compacted: %d", cc.n)
	}
	fill(850) // trigger again
	if cc.n != 2 {
		t.Fatalf("compactions after the latch cleared = %d, want 2", cc.n)
	}
}
