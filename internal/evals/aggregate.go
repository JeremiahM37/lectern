package evals

import "github.com/JeremiahM37/lectern/v2/internal/store"

// terminal reports whether a cell has finished one way or another — the
// point at which it counts toward a pass rate instead of just toward "total".
func terminal(status string) bool {
	switch status {
	case "passed", "failed", "error", "timeout":
		return true
	default:
		return false
	}
}

// VariantStats is one row of a run's per-variant leaderboard.
type VariantStats struct {
	VariantIdx       int     `json:"variant_idx"`
	Total            int     `json:"total"`
	Passed           int     `json:"passed"`
	Failed           int     `json:"failed"`
	Errored          int     `json:"errored"`
	PassRate         float64 `json:"pass_rate"`
	MeanDurationS    float64 `json:"mean_duration_s"`
	TotalCostUSD     float64 `json:"total_cost_usd"`
	MeanInputTokens  float64 `json:"mean_input_tokens"`
	MeanOutputTokens float64 `json:"mean_output_tokens"`
	// CostPerPass (docs/outcomes.md) is TotalCostUSD / Passed — this
	// variant's total spend divided by how many cells actually passed, so a
	// cheap variant that mostly fails does not look efficient. Omitted (zero
	// value) when nothing passed, so the UI can render "—" instead of a
	// divide-by-zero artifact.
	CostPerPass float64 `json:"cost_per_pass,omitempty"`
}

// Leaderboard aggregates a run's results per variant, ordered by variant
// index — the same order the dispatched variants array used, so row N of the
// leaderboard is row N of whatever variant list the run was created with.
func Leaderboard(results []*store.EvalResult) []VariantStats {
	byVariant := map[int]*VariantStats{}
	var order []int
	for _, r := range results {
		st, ok := byVariant[r.VariantIdx]
		if !ok {
			st = &VariantStats{VariantIdx: r.VariantIdx}
			byVariant[r.VariantIdx] = st
			order = append(order, r.VariantIdx)
		}
		st.Total++
		if r.CostUSD != nil {
			st.TotalCostUSD += *r.CostUSD
		}
		if terminal(r.Status) {
			switch r.Status {
			case "passed":
				st.Passed++
			case "failed":
				st.Failed++
			default:
				st.Errored++
			}
		}
	}
	// second pass for means, which need the terminal count already known
	sumDuration := map[int]float64{}
	sumIn := map[int]float64{}
	sumOut := map[int]float64{}
	nDuration := map[int]int{}
	nTokens := map[int]int{}
	for _, r := range results {
		if r.DurationS != nil {
			sumDuration[r.VariantIdx] += *r.DurationS
			nDuration[r.VariantIdx]++
		}
		if r.InputTokens != nil || r.OutputTokens != nil {
			if r.InputTokens != nil {
				sumIn[r.VariantIdx] += float64(*r.InputTokens)
			}
			if r.OutputTokens != nil {
				sumOut[r.VariantIdx] += float64(*r.OutputTokens)
			}
			nTokens[r.VariantIdx]++
		}
	}
	out := make([]VariantStats, 0, len(order))
	for _, idx := range sortedInts(order) {
		st := *byVariant[idx]
		terminalN := st.Passed + st.Failed + st.Errored
		if terminalN > 0 {
			st.PassRate = float64(st.Passed) / float64(terminalN)
		}
		if n := nDuration[idx]; n > 0 {
			st.MeanDurationS = sumDuration[idx] / float64(n)
		}
		if n := nTokens[idx]; n > 0 {
			st.MeanInputTokens = sumIn[idx] / float64(n)
			st.MeanOutputTokens = sumOut[idx] / float64(n)
		}
		if st.Passed > 0 {
			st.CostPerPass = st.TotalCostUSD / float64(st.Passed)
		}
		out = append(out, st)
	}
	return out
}

func sortedInts(in []int) []int {
	out := append([]int(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// CaseComparison is one row of a two-run diff: how a case's pass rate moved.
type CaseComparison struct {
	CaseID    int64   `json:"case_id"`
	PassRateA float64 `json:"pass_rate_a"`
	PassRateB float64 `json:"pass_rate_b"`
	Delta     float64 `json:"delta"` // B - A
	Status    string  `json:"status"`
}

func casePassRates(results []*store.EvalResult) map[int64]float64 {
	total := map[int64]int{}
	passed := map[int64]int{}
	for _, r := range results {
		if !terminal(r.Status) {
			continue
		}
		total[r.CaseID]++
		if r.Status == "passed" {
			passed[r.CaseID]++
		}
	}
	out := map[int64]float64{}
	for id, t := range total {
		if t > 0 {
			out[id] = float64(passed[id]) / float64(t)
		}
	}
	return out
}

// CompareRuns diffs two runs' pass rates case by case (matched by case_id,
// which is stable across runs of the same suite). A case present in only one
// run is skipped — there is nothing to diff against.
func CompareRuns(a, b []*store.EvalResult) []CaseComparison {
	ra, rb := casePassRates(a), casePassRates(b)
	var ids []int64
	for id := range ra {
		if _, ok := rb[id]; ok {
			ids = append(ids, id)
		}
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j-1] > ids[j]; j-- {
			ids[j-1], ids[j] = ids[j], ids[j-1]
		}
	}
	out := make([]CaseComparison, 0, len(ids))
	for _, id := range ids {
		delta := rb[id] - ra[id]
		status := "unchanged"
		switch {
		case delta < 0:
			status = "regression"
		case delta > 0:
			status = "improvement"
		}
		out = append(out, CaseComparison{CaseID: id, PassRateA: ra[id], PassRateB: rb[id],
			Delta: delta, Status: status})
	}
	return out
}
