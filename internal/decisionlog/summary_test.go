package decisionlog

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func TestPercentileIsNearestRank(t *testing.T) {
	vals := []float64{5, 1, 4, 2, 3}
	if got := *Percentile(vals, 0.5); got != 3 {
		t.Fatalf("p50 of 1..5 = %v, want 3", got)
	}
	if got := *Percentile(vals, 0.9); got != 5 {
		t.Fatalf("p90 of 1..5 = %v, want 5", got)
	}
	if got := *Percentile([]float64{10, 20}, 0.5); got != 10 {
		t.Fatalf("p50 of two values = %v, want the first (nearest rank)", got)
	}
	if got := *Percentile([]float64{42}, 0.9); got != 42 {
		t.Fatalf("p90 of one value = %v", got)
	}
	if Percentile(nil, 0.5) != nil {
		t.Fatal("no values should give nil, not zero")
	}
	// The input must not be reordered by the call.
	if vals[0] != 5 {
		t.Fatal("Percentile sorted the caller's slice")
	}
}

func TestBucketBoundaries(t *testing.T) {
	cases := map[int]string{0: "", 1: "1", 2: "2", 3: "3-4", 4: "3-4", 5: "5-7", 7: "5-7", 8: "8+", 40: "8+"}
	for active, want := range cases {
		if got := Bucket(active); got != want {
			t.Errorf("Bucket(%d) = %q, want %q", active, got, want)
		}
	}
}

func f(v float64) *float64 { return &v }

func TestSummarizeGroupsByKindAndBucket(t *testing.T) {
	rows := []*store.Decision{
		{Kind: "turn_response", Decision: "reply", LatencyS: f(10), ActiveSessions: 1},
		{Kind: "turn_response", Decision: "reply", LatencyS: f(30), ActiveSessions: 1},
		{Kind: "turn_response", Decision: "reply", LatencyS: f(50), ActiveSessions: 6},
		{Kind: "interrupt", Decision: "interrupt", ActiveSessions: 2},
	}
	s := Summarize(rows, 30)
	if s.Days != 30 || len(s.Kinds) != 2 {
		t.Fatalf("summary: %+v", s)
	}
	if s.Kinds[0].Kind != "interrupt" || s.Kinds[1].Kind != "turn_response" {
		t.Fatalf("kinds should be sorted by name: %+v", s.Kinds)
	}
	tr := s.Kinds[1]
	if tr.Count != 3 || tr.Decisions["reply"] != 3 {
		t.Fatalf("counts: %+v", tr)
	}
	if tr.Latency.N != 3 || *tr.Latency.P50 != 30 || *tr.Latency.P90 != 50 {
		t.Fatalf("latency: %+v", tr.Latency)
	}
	if len(tr.ByActiveSession) != len(Buckets) {
		t.Fatalf("every bucket is listed: %+v", tr.ByActiveSession)
	}
	one := tr.ByActiveSession[0]
	if one.Bucket != "1" || one.N != 2 || *one.P50 != 10 {
		t.Fatalf("bucket 1: %+v", one)
	}
	if tr.ByActiveSession[3].Bucket != "5-7" || tr.ByActiveSession[3].N != 1 {
		t.Fatalf("bucket 5-7: %+v", tr.ByActiveSession[3])
	}
	// An interrupt has no latency, so its buckets are empty with null p50.
	in := s.Kinds[0]
	if in.Latency.N != 0 || in.Latency.P50 != nil || in.ByActiveSession[1].P50 != nil {
		t.Fatalf("interrupt latency should be empty: %+v", in)
	}
}

func TestSummarizeEmptyWindow(t *testing.T) {
	s := Summarize(nil, 7)
	if s.Kinds == nil || len(s.Kinds) != 0 || s.Days != 7 {
		t.Fatalf("empty summary: %+v", s)
	}
}
