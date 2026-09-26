package console

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"
)

// TestStressDashboardRefresh times one full dashboard refresh against a live
// Lectern: the HTTP fetch the refresh command makes, the Update that filters
// and groups the rows, and the View that renders the screen. It only runs when
// the scale benchmark (tools/stress) points it at an instance, and writes its
// numbers to LECTERN_STRESS_JSON.
func TestStressDashboardRefresh(t *testing.T) {
	base := os.Getenv("LECTERN_STRESS_URL")
	if base == "" {
		t.Skip("set by tools/stress only")
	}
	iters := 20
	if n, err := strconv.Atoi(os.Getenv("LECTERN_STRESS_ITER")); err == nil && n > 0 {
		iters = n
	}
	m := newDashboard(New(base, ""), nil)
	m.width, m.height = 160, 48
	m.layout()
	var fetch, update, view, total []float64
	rows := 0
	for i := 0; i < iters; i++ {
		start := time.Now()
		cmd := m.refresh()
		if cmd == nil {
			t.Fatal("refresh returned no command")
		}
		msg := cmd()
		fetched := time.Now()
		r, ok := msg.(rowsMsg)
		if !ok || r.err != nil {
			t.Fatalf("refresh failed: %#v", msg)
		}
		rows = len(r.rows)
		m.Update(msg)
		updated := time.Now()
		if out := m.View(); out == "" {
			t.Fatal("empty view")
		}
		done := time.Now()
		fetch = append(fetch, msf(fetched.Sub(start)))
		update = append(update, msf(updated.Sub(fetched)))
		view = append(view, msf(done.Sub(updated)))
		total = append(total, msf(done.Sub(start)))
		time.Sleep(100 * time.Millisecond)
	}
	out, _ := json.Marshal(map[string]any{"rows": rows, "iterations": iters,
		"fetch": pct(fetch), "update": pct(update), "view": pct(view), "total": pct(total)})
	if path := os.Getenv("LECTERN_STRESS_JSON"); path != "" {
		if err := os.WriteFile(path, out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("%s", out)
}

func msf(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func pct(v []float64) map[string]any {
	sort.Float64s(v)
	at := func(p float64) float64 {
		i := int(math.Ceil(p*float64(len(v)))) - 1
		return math.Round(v[max(0, min(i, len(v)-1))]*10) / 10
	}
	return map[string]any{"n": len(v), "p50_ms": at(0.5), "p95_ms": at(0.95), "max_ms": at(1)}
}
