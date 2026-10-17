package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestKeepToolResultsCapsFromTheNewest(t *testing.T) {
	var xs []toolSummary
	for i := 0; i < 40; i++ {
		xs = append(xs, toolSummary{Tool: "read", Summary: fmt.Sprintf("result %d", i)})
	}
	// Zero or negative means the default of 30.
	for _, n := range []int{0, -1} {
		if got := keepToolResults(xs, n); len(got) != 30 || got[0] != xs[10] {
			t.Errorf("n=%d kept %d entries, want the newest 30", n, len(got))
		}
	}
	got := keepToolResults(xs, 3)
	if len(got) != 3 || got[0] != xs[37] || got[2] != xs[39] {
		t.Fatalf("keepToolResults(3) = %+v, want the newest three", got)
	}
	// A count above what is held keeps everything held.
	if got := keepToolResults(xs[:5], 100); len(got) != 5 {
		t.Fatalf("keepToolResults(100) on 5 entries kept %d", len(got))
	}
}

// The compactor keeps only the configured number of tool results in the
// summary, and an unset value keeps the default 30 (here all of them).
func TestCompactMaxToolResultsLimitsTheSummary(t *testing.T) {
	msgs := keepHistory(10)
	count := func(c SummaryCompactor) int {
		_, summary, err := c.Compact(context.Background(), msgs)
		if err != nil {
			t.Fatalf("Compact: %v", err)
		}
		return strings.Count(summary, "package p")
	}
	unset := count(SummaryCompactor{KeepRecent: 4})
	if unset < 3 {
		t.Fatalf("default keeps %d tool results, want the full set of older ones", unset)
	}
	if got := count(SummaryCompactor{KeepRecent: 4, MaxToolResults: 2}); got != 2 {
		t.Fatalf("MaxToolResults 2 kept %d tool results, want 2", got)
	}
	if got := count(SummaryCompactor{KeepRecent: 4, MaxToolResults: 0}); got != unset {
		t.Fatalf("MaxToolResults 0 kept %d, want the same as unset (%d)", got, unset)
	}
}

func TestSetMaxToolResultsAppliesToSummaryCompactor(t *testing.T) {
	a := &Agent{opts: Options{Compactor: SummaryCompactor{KeepRecent: 4}}}
	a.SetMaxToolResults(5)
	sc, ok := a.opts.Compactor.(SummaryCompactor)
	if !ok || sc.MaxToolResults != 5 || sc.KeepRecent != 4 {
		t.Fatalf("compactor = %+v, want MaxToolResults 5 and KeepRecent kept", a.opts.Compactor)
	}
	a.SetMaxToolResults(-2)
	if sc := a.opts.Compactor.(SummaryCompactor); sc.MaxToolResults != 0 {
		t.Fatalf("negative should reset to the default, got %d", sc.MaxToolResults)
	}
}
