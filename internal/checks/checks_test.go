package checks

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/sinks"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// fixture builds an in-memory DB, a mock target/project/session and a Runner
// wired to it. All of these tests use the Mock executor, whose git-shaped
// commands (rev-parse, status --porcelain, diff) return fixed output, so a
// worktree fingerprint is stable across calls unless the test asserts
// otherwise via a marker file in the mock filesystem.
func fixture(t *testing.T) (*Runner, *store.DB, *store.Session, *executor.Mock) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target, err := db.InsertTarget(&store.Target{Name: "t", Kind: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	reg := executor.NewRegistry(true, 0)
	proj, err := db.InsertProject(&store.Project{Name: "p", TargetID: target.ID, RepoPath: "/mock/repo"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := db.InsertSession(&store.Session{ProjectID: &proj.ID, TargetID: target.ID,
		Name: "s", Agent: "claude", Workdir: "/mock/work", TmuxSession: "lec-s1"})
	if err != nil {
		t.Fatal(err)
	}
	b := bus.New()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	notifier := &sinks.Notifier{DB: db, Log: log}
	r := New(db, reg, b, notifier, time.Second, log)
	ex, err := reg.For(target)
	if err != nil {
		t.Fatal(err)
	}
	return r, db, sess, ex.(*executor.Mock)
}

func TestResolveConfiguredCommandWins(t *testing.T) {
	r, db, sess, _ := fixture(t)
	proj, _ := db.Project(*sess.ProjectID)
	proj.VerifyCmd = "mockverify-pass"
	cmd, source := r.DetectCommand(context.Background(), proj)
	if cmd != "mockverify-pass" || source != "configured" {
		t.Fatalf("resolve: cmd=%q source=%q", cmd, source)
	}
}

func TestDetectCommandAutoDetectsVerifyYaml(t *testing.T) {
	r, db, sess, ex := fixture(t)
	proj, _ := db.Project(*sess.ProjectID)

	cmd, source := r.DetectCommand(context.Background(), proj)
	if source != "none" || cmd != "" {
		t.Fatalf("expected no auto-detect without .verify.yaml, got cmd=%q source=%q", cmd, source)
	}

	if err := ex.WriteFile(context.Background(), proj.RepoPath+"/.verify.yaml", []byte("checks: []\n")); err != nil {
		t.Fatal(err)
	}
	cmd, source = r.DetectCommand(context.Background(), proj)
	if source != "auto" || cmd != AutoVerifyCommand {
		t.Fatalf("expected auto-detect once .verify.yaml exists, got cmd=%q source=%q", cmd, source)
	}
}

// TestFingerprintSkipsUnchangedWorktree is the "fingerprint skip" test the
// workstream calls for: a second Stop trigger with nothing new in the
// worktree must not run the command again.
func TestFingerprintSkipsUnchangedWorktree(t *testing.T) {
	r, db, sess, _ := fixture(t)
	proj, _ := db.Project(*sess.ProjectID)
	proj.VerifyCmd = "mockverify-pass"
	db.Update("projects", proj.ID, map[string]any{"verify_cmd": proj.VerifyCmd})

	r.RunForSession(context.Background(), sess.ID, ReasonStop)
	first, err := db.SessionChecks(sess.ID, 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("expected exactly one check after the first Stop, got %d (err=%v)", len(first), err)
	}
	if first[0].Status != StatusPassed {
		t.Fatalf("expected the mock's passing command to record passed, got %q", first[0].Status)
	}

	// Same worktree, same fingerprint: a second Stop must be a no-op.
	r.RunForSession(context.Background(), sess.ID, ReasonStop)
	second, err := db.SessionChecks(sess.ID, 10)
	if err != nil || len(second) != 1 {
		t.Fatalf("expected the unchanged-worktree Stop to be skipped, got %d rows", len(second))
	}

	// A human pressing "Run check" always runs, regardless of fingerprint.
	r.RunForSession(context.Background(), sess.ID, ReasonManual)
	third, err := db.SessionChecks(sess.ID, 10)
	if err != nil || len(third) != 2 {
		t.Fatalf("expected a manual run to run even with an unchanged fingerprint, got %d rows", len(third))
	}
}

func TestNoCommandConfiguredIsQuietOnAutoTriggersButVisibleOnManual(t *testing.T) {
	r, db, sess, _ := fixture(t)

	r.RunForSession(context.Background(), sess.ID, ReasonStop)
	rows, err := db.SessionChecks(sess.ID, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("an automatic trigger with no check command must not write a row, got %d", len(rows))
	}

	r.RunForSession(context.Background(), sess.ID, ReasonManual)
	rows, err = db.SessionChecks(sess.ID, 10)
	if err != nil || len(rows) != 1 || rows[0].Status != StatusSkipped {
		t.Fatalf("expected one skipped row from the manual run, got %+v (err=%v)", rows, err)
	}
}

// TestCoalescesOverlappingRequests exercises the "one at a time per session,
// a request that arrives mid-run gets folded into one extra run afterward"
// rule. The mock executor's Intercept hook blocks the FIRST execution of the
// check command (not the fingerprint probes before it), which gives a
// deterministic window to fire overlapping requests in — no sleeps, no
// timing assumptions.
func TestCoalescesOverlappingRequests(t *testing.T) {
	r, db, sess, ex := fixture(t)
	proj, _ := db.Project(*sess.ProjectID)
	db.Update("projects", proj.ID, map[string]any{"verify_cmd": "mockverify-pass"})

	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	ex.Intercept = func(cmd string) {
		if strings.Contains(cmd, "mockverify-pass") {
			startOnce.Do(func() { close(started) })
			<-release // closed once; a later coalesced run passes straight through
		}
	}

	done := make(chan struct{})
	go func() {
		r.RunForSession(context.Background(), sess.ID, ReasonStop)
		close(done)
	}()
	<-started // the first run is now blocked inside the check command itself

	// Two more triggers arrive while the first is still in flight; both must
	// coalesce into a single follow-up, not two.
	r.RunForSession(context.Background(), sess.ID, ReasonStop)
	r.RunForSession(context.Background(), sess.ID, ReasonManual)

	close(release) // let the first run finish
	<-done

	rows, err := db.SessionChecks(sess.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected exactly one coalesced follow-up run (2 rows total), got %d", len(rows))
	}
}

// TestNotifyOnFailureAndRecovery checks the push behavior: notify once when a
// check turns failing, once when it recovers, never on a repeated outcome.
func TestNotifyOnFailureAndRecovery(t *testing.T) {
	r, db, sess, _ := fixture(t)
	proj, _ := db.Project(*sess.ProjectID)
	db.Update("projects", proj.ID, map[string]any{"verify_cmd": "mockverify-fail"})
	db.SetSetting("ntfy_server", "http://ntfy.example")
	db.SetSetting("ntfy_topic", "lectern-test")

	var pushes []string
	r.Notifier.Hook = func(payloads []sinks.Payload) {
		for _, p := range payloads {
			if title, _ := p.Body["title"].(string); title != "" {
				pushes = append(pushes, title)
			}
		}
	}

	r.RunForSession(context.Background(), sess.ID, ReasonManual)
	if len(pushes) != 1 || pushes[0] != "lectern: Check failed" {
		t.Fatalf("expected one failure push, got %v", pushes)
	}

	// Still failing: no repeat push.
	r.RunForSession(context.Background(), sess.ID, ReasonManual)
	if len(pushes) != 1 {
		t.Fatalf("expected no repeat push for a still-failing check, got %v", pushes)
	}

	// Now passing: a recovery push.
	db.Update("projects", proj.ID, map[string]any{"verify_cmd": "mockverify-pass"})
	r.RunForSession(context.Background(), sess.ID, ReasonManual)
	if len(pushes) != 2 || pushes[1] != "lectern: Check passed" {
		t.Fatalf("expected a recovery push, got %v", pushes)
	}
}
