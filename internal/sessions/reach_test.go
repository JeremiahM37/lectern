package sessions

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// One machine that stops answering must not hold up status for the others.
// Before per-target polling, Poll visited targets one after another, so every
// session on every machine waited out the dead one's timeout each round.
func TestHangingTargetDoesNotDelayOthers(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m := New(db, executor.NewRegistry(true, 0), bus.New(), Launcher{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.TargetPollTimeout = 300 * time.Millisecond
	m.PollWait = 200 * time.Millisecond

	var healthy, dead *store.Session
	var deadTarget *store.Target
	for _, name := range []string{"healthy", "dead"} {
		target, err := db.InsertTarget(&store.Target{Name: name, Kind: "mock"})
		if err != nil {
			t.Fatal(err)
		}
		// every mock target has a pane called legacy-claude
		s, err := db.InsertSession(&store.Session{TargetID: target.ID, Name: name, TmuxSession: "legacy-claude",
			Workdir: "/mock/demo-app", Status: StatusIdle, Origin: "discovered"})
		if err != nil {
			t.Fatal(err)
		}
		if name == "healthy" {
			healthy = s
		} else {
			dead, deadTarget = s, target
		}
	}
	ex, err := m.Reg.For(deadTarget)
	if err != nil {
		t.Fatal(err)
	}
	// The dead machine accepts a command and then says nothing for far longer
	// than its poll timeout. The mock ignores the context, which is the worst
	// case: the worker cannot even be cancelled.
	var hang atomic.Bool
	hang.Store(true)
	ex.(*executor.Mock).Intercept = func(string) {
		if hang.Load() {
			time.Sleep(1500 * time.Millisecond)
		}
	}
	updated := func(id int64) float64 {
		s, err := db.Session(id)
		if err != nil {
			t.Fatal(err)
		}
		return s.UpdatedAt
	}

	// Several rounds while the dead target is stuck: each returns promptly and
	// each refreshes the healthy session.
	before := updated(healthy.ID)
	refreshed := 0
	for i := 0; i < 6; i++ {
		start := time.Now()
		m.Poll(context.Background())
		if took := time.Since(start); took > 450*time.Millisecond {
			t.Fatalf("round %d took %v while another target hung", i, took)
		}
		if now := updated(healthy.ID); now > before {
			refreshed++
			before = now
		}
		time.Sleep(20 * time.Millisecond)
	}
	if refreshed < 5 {
		t.Fatalf("healthy session refreshed in only %d of 6 rounds", refreshed)
	}
	if m.Reach(healthy.TargetID).Unreachable {
		t.Fatal("the healthy target was reported unreachable")
	}

	// Once the stuck poll comes back past its timeout, only the dead target is
	// unreachable, and its session keeps its last status rather than dying.
	deadline := time.Now().Add(3 * time.Second)
	for !m.Reach(deadTarget.ID).Unreachable {
		if time.Now().After(deadline) {
			t.Fatal("the hanging target was never reported unreachable")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got, _ := db.Session(dead.ID); got.EndedAt != nil || got.Status != StatusIdle {
		t.Fatalf("an unreachable target changed its session: %+v", got)
	}

	// It recovers on its own after the backoff once it answers again.
	hang.Store(false)
	deadline = time.Now().Add(6 * time.Second)
	for m.Reach(deadTarget.ID).Unreachable {
		if time.Now().After(deadline) {
			t.Fatal("the target never recovered")
		}
		m.Poll(context.Background())
		time.Sleep(100 * time.Millisecond)
	}
}

func TestReachBackoffAndSlowPoll(t *testing.T) {
	r := &reachTracker{targets: map[int64]*targetPollState{}, sem: make(chan struct{}, 1)}
	now := time.Now()
	if !r.begin(1, now) || r.begin(1, now) {
		t.Fatal("a target in flight must not be claimed twice")
	}
	if !r.finish(1, now, now, "boom", true) {
		t.Fatal("first failure should change reachability")
	}
	if r.begin(1, now.Add(time.Second)) {
		t.Fatal("a failing target was retried inside its backoff")
	}
	if !r.begin(1, now.Add(3*time.Second)) {
		t.Fatal("a failing target was not retried after its backoff")
	}
	if r.finish(1, now, now.Add(3*time.Second), "boom", true) {
		t.Fatal("a second failure is not a change")
	}
	if !r.finish(1, now, now.Add(4*time.Second), "", false) {
		t.Fatal("recovery should change reachability")
	}
}
