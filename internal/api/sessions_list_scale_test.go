package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/app"
	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// seedSharedRepo inserts n live sessions working in one repository, each with
// recent edits to a few files drawn from a small shared set — the case where
// parallel agents' work overlaps. Rows are written directly: this is about the
// list endpoint, not about launching.
func seedSharedRepo(tb testing.TB, db *store.DB, n int) {
	tb.Helper()
	target, err := db.InsertTarget(&store.Target{Name: "scale", Kind: "local"})
	if err != nil {
		tb.Fatal(err)
	}
	now := store.Now()
	key := fmt.Sprintf("%d:/repo/.git", target.ID)
	for i := 0; i < n; i++ {
		s, err := db.InsertSession(&store.Session{TargetID: target.ID, Name: fmt.Sprintf("agent-%03d", i),
			Agent: "claude", Workdir: fmt.Sprintf("/repo-wt/%d", i), TmuxSession: fmt.Sprintf("lec-scale-%d", i)})
		if err != nil {
			tb.Fatal(err)
		}
		if err := db.Update("sessions", s.ID, map[string]any{"repo_key": key, "repo_toplevel": "/repo"}); err != nil {
			tb.Fatal(err)
		}
		for j := 0; j < 3; j++ {
			if err := db.UpsertSessionFileEdit(s.ID, key, fmt.Sprintf("src/module_%d.go", (i+j)%5), now-float64(i+j)); err != nil {
				tb.Fatal(err)
			}
		}
	}
}

type overlapChip struct {
	SessionID int64    `json:"session_id"`
	Name      string   `json:"name"`
	Files     []string `json:"files"`
}

// The list computes every row's overlap chip in one batch; a single session's
// own view still computes it alone. Both must say the same thing.
func TestListSessionsOverlapMatchesSingleSessionView(t *testing.T) {
	h := newHarness(t)
	seedSharedRepo(t, h.App.DB, 12)
	var list []struct {
		ID      int64        `json:"id"`
		Overlap *overlapChip `json:"awareness_overlap"`
	}
	h.decode("GET", "/api/sessions", nil, 200, &list)
	chips := 0
	for _, row := range list {
		var one struct {
			Overlap *overlapChip `json:"awareness_overlap"`
		}
		h.decode("GET", fmt.Sprintf("/api/sessions/%d", row.ID), nil, 200, &one)
		if !reflect.DeepEqual(row.Overlap, one.Overlap) {
			t.Fatalf("session %d: list says %+v, its own view says %+v", row.ID, row.Overlap, one.Overlap)
		}
		if row.Overlap != nil {
			chips++
			if row.Overlap.SessionID == row.ID || len(row.Overlap.Files) == 0 {
				t.Fatalf("session %d: bad overlap %+v", row.ID, row.Overlap)
			}
		}
	}
	if chips != 12 {
		t.Fatalf("every seeded session shares files with another; got %d chips of 12", chips)
	}
}

// Listing 200 sessions that share a repository took ~0.8 s when each row
// looked its peers up separately (quadratic in the number of sessions); it is
// ~12 ms batched. The bound is loose enough for a loaded machine and still
// fails the quadratic version several times over.
func TestListSessionsSharedRepoStaysFast(t *testing.T) {
	h := newHarness(t)
	seedSharedRepo(t, h.App.DB, 200)
	var times []time.Duration
	for i := 0; i < 3; i++ {
		start := time.Now()
		if rows := h.getList("/api/sessions"); len(rows) != 200 {
			t.Fatalf("got %d rows", len(rows))
		}
		times = append(times, time.Since(start))
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	if times[1] > 400*time.Millisecond {
		t.Fatalf("listing 200 sessions in one repository took %v (median of 3)", times[1])
	}
}

// BenchmarkListSessionsSharedRepo is the session list with every agent in one
// repository. Before the overlap chips were batched it grew quadratically
// (docs/benchmarks/scale.md).
func BenchmarkListSessionsSharedRepo(b *testing.B) {
	for _, n := range []int{50, 100, 200} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			dir := b.TempDir()
			a, err := app.New(&config.Config{DBPath: filepath.Join(dir, "bench.db"), Mock: true,
				TickInterval: time.Hour, SessionPoll: time.Hour,
				HostClaudeConfig: filepath.Join(dir, "none"), ClaudeCredsPath: filepath.Join(dir, "none"),
				CodexCredsPath: filepath.Join(dir, "none")}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				b.Fatal(err)
			}
			srv := httptest.NewServer(a.Handler())
			b.Cleanup(func() { srv.Close(); a.Close() })
			seedSharedRepo(b, a.DB, n)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				resp, err := http.Get(srv.URL + "/api/sessions")
				if err != nil {
					b.Fatal(err)
				}
				var rows []json.RawMessage
				if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil || len(rows) != n {
					b.Fatalf("got %d rows: %v", len(rows), err)
				}
				resp.Body.Close()
			}
		})
	}
}
