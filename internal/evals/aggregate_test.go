package evals

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func f(v float64) *float64 { return &v }
func n(v int) *int         { return &v }

func TestLeaderboardComputesPassRateDurationCostAndTokensPerVariant(t *testing.T) {
	results := []*store.EvalResult{
		{VariantIdx: 0, Status: "passed", DurationS: f(10), CostUSD: f(0.1), InputTokens: n(100), OutputTokens: n(50)},
		{VariantIdx: 0, Status: "failed", DurationS: f(20), CostUSD: f(0.2), InputTokens: n(200), OutputTokens: n(100)},
		{VariantIdx: 1, Status: "passed", DurationS: f(5), CostUSD: f(0.05)},
		{VariantIdx: 1, Status: "passed", DurationS: f(15), CostUSD: f(0.15)},
		{VariantIdx: 1, Status: "queued"}, // not terminal — must not count toward pass rate
	}
	board := Leaderboard(results)
	if len(board) != 2 {
		t.Fatalf("expected 2 rows, got %d: %+v", len(board), board)
	}
	v0, v1 := board[0], board[1]
	if v0.VariantIdx != 0 || v0.Passed != 1 || v0.Failed != 1 || v0.PassRate != 0.5 {
		t.Errorf("variant 0: %+v", v0)
	}
	if v0.MeanDurationS != 15 {
		t.Errorf("variant 0 mean duration: %v", v0.MeanDurationS)
	}
	if v0.TotalCostUSD < 0.29999 || v0.TotalCostUSD > 0.30001 {
		t.Errorf("variant 0 total cost: %v", v0.TotalCostUSD)
	}
	if v0.MeanInputTokens != 150 || v0.MeanOutputTokens != 75 {
		t.Errorf("variant 0 mean tokens: %+v", v0)
	}
	if v0.CostPerPass < 0.29999 || v0.CostPerPass > 0.30001 { // 0.3 total / 1 pass
		t.Errorf("variant 0 cost per pass: %v", v0.CostPerPass)
	}
	// variant 1 has 3 total rows but only 2 terminal — pass rate is over the
	// terminal ones, and Total still reports every row seen
	if v1.Total != 3 || v1.Passed != 2 || v1.PassRate != 1 {
		t.Errorf("variant 1: %+v", v1)
	}
	if v1.CostPerPass < 0.09999 || v1.CostPerPass > 0.10001 { // 0.2 total / 2 passes
		t.Errorf("variant 1 cost per pass: %v", v1.CostPerPass)
	}
}

func TestLeaderboardCostPerPassOmittedWhenNothingPassed(t *testing.T) {
	board := Leaderboard([]*store.EvalResult{
		{VariantIdx: 0, Status: "failed", CostUSD: f(1.0)},
	})
	if len(board) != 1 || board[0].CostPerPass != 0 {
		t.Errorf("a variant with zero passes must report cost_per_pass 0, not a divide-by-zero artifact: %+v", board)
	}
}

func TestLeaderboardEmptyInputIsEmptyNotNilPanic(t *testing.T) {
	if board := Leaderboard(nil); len(board) != 0 {
		t.Fatalf("expected no rows, got %+v", board)
	}
}

func TestCompareRunsFlagsRegressionsAndImprovements(t *testing.T) {
	runA := []*store.EvalResult{
		{CaseID: 1, Status: "passed"}, {CaseID: 1, Status: "passed"}, // case 1: 100% in A
		{CaseID: 2, Status: "failed"}, {CaseID: 2, Status: "failed"}, // case 2: 0% in A
		{CaseID: 3, Status: "passed"}, // case 3: 100% in A — unchanged in B
	}
	runB := []*store.EvalResult{
		{CaseID: 1, Status: "failed"}, {CaseID: 1, Status: "passed"}, // case 1: 50% in B — regression
		{CaseID: 2, Status: "passed"}, {CaseID: 2, Status: "passed"}, // case 2: 100% in B — improvement
		{CaseID: 3, Status: "passed"}, // case 3: 100% in B — unchanged
		{CaseID: 4, Status: "passed"}, // case 4 only exists in B — skipped, nothing to diff
	}
	cmp := CompareRuns(runA, runB)
	if len(cmp) != 3 {
		t.Fatalf("expected 3 comparable cases (4 has no match in A), got %d: %+v", len(cmp), cmp)
	}
	byCase := map[int64]CaseComparison{}
	for _, c := range cmp {
		byCase[c.CaseID] = c
	}
	if got := byCase[1]; got.Status != "regression" || got.PassRateA != 1 || got.PassRateB != 0.5 {
		t.Errorf("case 1: %+v", got)
	}
	if got := byCase[2]; got.Status != "improvement" || got.PassRateA != 0 || got.PassRateB != 1 {
		t.Errorf("case 2: %+v", got)
	}
	if got := byCase[3]; got.Status != "unchanged" || got.Delta != 0 {
		t.Errorf("case 3: %+v", got)
	}
}
