package sessions

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// fakeChecks stands in for internal/checks.Runner (which structurally
// satisfies agentevents.StopListener) without this package importing it.
type fakeChecks struct{ ids []int64 }

func (f *fakeChecks) OnAgentStop(sessionID int64) { f.ids = append(f.ids, sessionID) }

func checksTriggerRig(t *testing.T, initial string) (*Manager, *store.Session, *fakeChecks) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target, err := db.InsertTarget(&store.Target{Name: "t", Kind: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := db.InsertSession(&store.Session{TargetID: target.ID, Name: "s",
		TmuxSession: "lec-s1", Workdir: "/mock/work", Status: initial})
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeChecks{}
	m := New(db, executor.NewRegistry(true, 0), bus.New(), Launcher{}, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Checks = fake
	return m, sess, fake
}

// TestApplyPaneTriggersChecksOnBusyToIdle is the screen-derived fallback
// trigger docs/agent-events.md section 4 calls for: a session with no (or
// stale) lifecycle hooks still gets its check command run when the pane goes
// from busy to idle.
func TestApplyPaneTriggersChecksOnBusyToIdle(t *testing.T) {
	m, sess, fake := checksTriggerRig(t, StatusRunning)
	sess.PaneHash = Hash("done.")

	m.applyPane(sess, "done.", false)

	if len(fake.ids) != 1 || fake.ids[0] != sess.ID {
		t.Fatalf("expected one OnAgentStop(%d) on running->idle, got %v", sess.ID, fake.ids)
	}
	fresh, err := m.DB.Session(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != StatusIdle {
		t.Fatalf("expected the session to actually land idle, got %q", fresh.Status)
	}
}

// TestApplyPaneDoesNotTriggerChecksOnOtherTransitions guards the specific
// busy->idle rule: waiting->idle and idle->idle (no real transition) must
// not fire a check the same way a real Stop hook would not.
func TestApplyPaneDoesNotTriggerChecksOnOtherTransitions(t *testing.T) {
	m, sess, fake := checksTriggerRig(t, StatusWaiting)
	sess.PaneHash = Hash("done.")

	m.applyPane(sess, "done.", false)

	if len(fake.ids) != 0 {
		t.Fatalf("waiting->idle must not trigger a check, got %v", fake.ids)
	}
}

// TestApplyPaneNilChecksIsSafe documents that a Manager with no checks runner
// configured (the default in every other test in this package) never panics
// on a busy->idle transition.
func TestApplyPaneNilChecksIsSafe(t *testing.T) {
	m, sess, _ := checksTriggerRig(t, StatusRunning)
	m.Checks = nil
	sess.PaneHash = Hash("done.")
	m.applyPane(sess, "done.", false)
}
