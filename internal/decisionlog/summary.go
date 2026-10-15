package decisionlog

import (
	"math"
	"sort"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Buckets are the active-session counts the latency table is split by, in
// display order. They follow how many agents one person can supervise at
// once: a single session, a pair, a small group, a busy board, and more.
var Buckets = []string{"1", "2", "3-4", "5-7", "8+"}

// Bucket names the active-session bucket a count falls into. A count below one
// has no bucket: no live session means there was no supervision load to
// measure.
func Bucket(active int) string {
	switch {
	case active < 1:
		return ""
	case active == 1:
		return "1"
	case active == 2:
		return "2"
	case active <= 4:
		return "3-4"
	case active <= 7:
		return "5-7"
	}
	return "8+"
}

// Percentile is the nearest-rank p-th percentile (0 < p <= 1) of vals, or nil
// when there are none. Nearest rank is used rather than interpolation so every
// reported value is a latency that actually happened.
func Percentile(vals []float64, p float64) *float64 {
	if len(vals) == 0 {
		return nil
	}
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	rank := int(math.Ceil(p * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	v := sorted[rank-1]
	return &v
}

// LatencyStats is a latency distribution: how many decisions had one, and its
// median and 90th percentile in seconds.
type LatencyStats struct {
	N   int      `json:"n"`
	P50 *float64 `json:"p50"`
	P90 *float64 `json:"p90"`
}

// BucketStats is the latency median for one active-session bucket. N counts
// the decisions in the bucket that had a latency.
type BucketStats struct {
	Bucket string   `json:"bucket"`
	N      int      `json:"n"`
	P50    *float64 `json:"p50"`
}

// KindSummary is one decision kind over the window.
type KindSummary struct {
	Kind            string         `json:"kind"`
	Count           int            `json:"count"`
	Decisions       map[string]int `json:"decisions"`
	Latency         LatencyStats   `json:"latency_s"`
	ByActiveSession []BucketStats  `json:"by_active_sessions"`
}

// Summary is the response of /api/decisions/summary.
type Summary struct {
	Days  int           `json:"days"`
	Kinds []KindSummary `json:"kinds"`
}

// Summarize groups decisions by kind, in kind-name order. The rows are whatever
// the caller fetched for the window; Summarize does no filtering of its own.
func Summarize(rows []*store.Decision, days int) Summary {
	type acc struct {
		count     int
		decisions map[string]int
		latencies []float64
		byBucket  map[string][]float64
	}
	byKind := map[string]*acc{}
	for _, d := range rows {
		a := byKind[d.Kind]
		if a == nil {
			a = &acc{decisions: map[string]int{}, byBucket: map[string][]float64{}}
			byKind[d.Kind] = a
		}
		a.count++
		a.decisions[d.Decision]++
		if d.LatencyS == nil {
			continue
		}
		a.latencies = append(a.latencies, *d.LatencyS)
		if b := Bucket(d.ActiveSessions); b != "" {
			a.byBucket[b] = append(a.byBucket[b], *d.LatencyS)
		}
	}
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)

	out := Summary{Days: days, Kinds: []KindSummary{}}
	for _, k := range kinds {
		a := byKind[k]
		ks := KindSummary{
			Kind:      k,
			Count:     a.count,
			Decisions: a.decisions,
			Latency: LatencyStats{
				N:   len(a.latencies),
				P50: Percentile(a.latencies, 0.5),
				P90: Percentile(a.latencies, 0.9),
			},
			ByActiveSession: []BucketStats{},
		}
		for _, b := range Buckets {
			vals := a.byBucket[b]
			ks.ByActiveSession = append(ks.ByActiveSession, BucketStats{
				Bucket: b, N: len(vals), P50: Percentile(vals, 0.5),
			})
		}
		out.Kinds = append(out.Kinds, ks)
	}
	return out
}
