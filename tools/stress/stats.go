//go:build unix

package main

import (
	"math"
	"sort"
	"sync"
	"time"
)

// Stats summarises one latency series in milliseconds.
type Stats struct {
	N   int     `json:"n"`
	P50 float64 `json:"p50_ms"`
	P95 float64 `json:"p95_ms"`
	P99 float64 `json:"p99_ms"`
	Max float64 `json:"max_ms"`
}

// series is a concurrency-safe list of samples.
type series struct {
	mu sync.Mutex
	v  []float64
}

func (s *series) add(d time.Duration) {
	s.mu.Lock()
	s.v = append(s.v, float64(d)/float64(time.Millisecond))
	s.mu.Unlock()
}

func (s *series) stats() Stats {
	s.mu.Lock()
	v := append([]float64(nil), s.v...)
	s.mu.Unlock()
	return summarize(v)
}

func summarize(v []float64) Stats {
	if len(v) == 0 {
		return Stats{}
	}
	sort.Float64s(v)
	pick := func(p float64) float64 {
		// nearest-rank percentile: the smallest sample with at least p of the
		// series at or below it
		i := int(math.Ceil(p*float64(len(v)))) - 1
		return round(v[max(0, min(i, len(v)-1))])
	}
	return Stats{N: len(v), P50: pick(0.50), P95: pick(0.95), P99: pick(0.99), Max: round(v[len(v)-1])}
}

func round(f float64) float64 { return math.Round(f*10) / 10 }
