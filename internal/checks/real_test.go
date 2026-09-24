package checks

// Integration: the checks runner against REAL git and a real shell command
// through the local executor, not the mock — the mock's git-shaped commands
// return fixed output, so it cannot exercise a fingerprint that genuinely
// changes between two runs, and it never actually executes a check command
// long enough to time out. Modeled on internal/worktree's realgit_test.go
// (same risk profile: temp directories and local subprocesses, no tmux, no
// shared host state), so it runs directly as well as inside the isolated
// runner.

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/sinks"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func realGitRepo(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", "-b", "main", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-qm", "initial")
}

func realFixture(t *testing.T, verifyCmd string) (*Runner, *store.DB, *store.Session, string) {
	t.Helper()
	dir := t.TempDir()
	realGitRepo(t, dir)

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target, err := db.InsertTarget(&store.Target{Name: "local", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	proj, err := db.InsertProject(&store.Project{Name: "p", TargetID: target.ID, RepoPath: dir, VerifyCmd: verifyCmd})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := db.InsertSession(&store.Session{ProjectID: &proj.ID, TargetID: target.ID,
		Name: "real-session", Agent: "claude", Workdir: dir, TmuxSession: "lec-real"})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := executor.NewRegistry(false, 0)
	r := New(db, reg, bus.New(), &sinks.Notifier{DB: db, Log: log}, time.Second, log)
	return r, db, sess, dir
}

// TestRealWorktreeGoesFailedThenPassedAfterFileAppears is the "session card
// shows failed then passed after the file appears and a run-now" scenario,
// exercised against the actual runner rather than a mock. A push is expected
// on the failure and again on the recovery.
func TestRealWorktreeGoesFailedThenPassedAfterFileAppears(t *testing.T) {
	r, db, sess, dir := realFixture(t, "test -f ok.txt")
	db.SetSetting("ntfy_server", "http://ntfy.example")
	db.SetSetting("ntfy_topic", "lectern-test")
	var pushed []string
	r.Notifier.Hook = func(payloads []sinks.Payload) {
		for _, p := range payloads {
			if title, _ := p.Body["title"].(string); title != "" {
				pushed = append(pushed, title)
			}
		}
	}

	r.RunForSession(context.Background(), sess.ID, ReasonStop)
	rows, err := db.SessionChecks(sess.ID, 10)
	if err != nil || len(rows) != 1 || rows[0].Status != StatusFailed {
		t.Fatalf("expected one failed check before ok.txt exists, got %+v (err=%v)", rows, err)
	}
	if rows[0].ExitCode == nil || *rows[0].ExitCode == 0 {
		t.Fatalf("expected a non-zero exit code, got %+v", rows[0].ExitCode)
	}

	// A second Stop with nothing changed must be skipped, not re-run.
	r.RunForSession(context.Background(), sess.ID, ReasonStop)
	if rows, _ := db.SessionChecks(sess.ID, 10); len(rows) != 1 {
		t.Fatalf("expected the unchanged worktree to skip a second run, got %d rows", len(rows))
	}

	// The file appears: the worktree's `git status --porcelain` now differs,
	// so the fingerprint changes and a "run-now" (or the next real Stop) runs
	// again and passes.
	if err := os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.RunForSession(context.Background(), sess.ID, ReasonManual)
	rows, err = db.SessionChecks(sess.ID, 10)
	if err != nil || len(rows) != 2 || rows[0].Status != StatusPassed {
		t.Fatalf("expected the run-now after ok.txt appeared to pass, got %+v (err=%v)", rows, err)
	}

	if len(pushed) != 2 || pushed[0] != "lectern: Check failed" || pushed[1] != "lectern: Check passed" {
		t.Fatalf("expected a failure push then a recovery push, got %v", pushed)
	}
}

// TestRealCheckTimesOut bounds a genuinely slow command to a short Timeout —
// executor.Local turns a context deadline into rc=124 (the `timeout(1)`
// convention), which lands here as a failed check with that exit code and
// "timed out" in the output, well before the command's own 5s sleep.
func TestRealCheckTimesOut(t *testing.T) {
	r, db, sess, _ := realFixture(t, "sleep 5")
	r.Timeout = 200 * time.Millisecond

	start := time.Now()
	r.RunForSession(context.Background(), sess.ID, ReasonStop)
	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Fatalf("expected the check to be bounded by Timeout, took %s", elapsed)
	}

	rows, err := db.SessionChecks(sess.ID, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("expected one check row, got %+v (err=%v)", rows, err)
	}
	if rows[0].Status != StatusFailed || rows[0].ExitCode == nil || *rows[0].ExitCode != 124 {
		t.Fatalf("expected a timed-out check to fail with exit code 124, got %+v", rows[0])
	}
}
