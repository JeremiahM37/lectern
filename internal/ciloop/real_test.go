package ciloop

// These tests run the real Local executor against a fake `gh` on PATH, so
// every command string the loop builds is actually executed by a shell and
// every parser reads real process output. Like the trigger tests' fake gh,
// they need the isolated runner (tools/run-isolated-tests.sh).

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/sinks"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

const prURL = "https://github.com/a/b/pull/5"

// fakeGH is the scripted GitHub: tests write the PR's state, head commit,
// checks table and failed-job log into dir, and the script serves them.
type fakeGH struct {
	t   *testing.T
	dir string
}

func newFakeGH(t *testing.T) *fakeGH {
	t.Helper()
	testutil.RequireIsolated(t)
	dir := t.TempDir()
	q := func(name string) string { return "'" + filepath.Join(dir, name) + "'" }
	script := `#!/usr/bin/env bash
echo "$*" >> ` + q("gh.log") + `
if [ -f ` + q("noauth") + ` ]; then
  echo "To get started with GitHub CLI, please run:  gh auth login" >&2
  echo "Alternatively, populate the GH_TOKEN environment variable with a GitHub API authentication token." >&2
  exit 4
fi
case "$1 $2" in
  "pr view")
    printf '{"headRefOid":"%s","state":"%s"}\n' "$(cat ` + q("sha") + `)" "$(cat ` + q("state") + `)"
    ;;
  "pr checks")
    cat ` + q("checks") + `
    if grep -q "	fail	" ` + q("checks") + `; then exit 1; fi
    if grep -q "	pending	" ` + q("checks") + `; then exit 8; fi
    ;;
  "run view")
    cat ` + q("log") + `
    ;;
  *)
    echo "fake gh: unrecognized invocation: $*" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	f := &fakeGH{t: t, dir: dir}
	f.set("state", "OPEN")
	f.set("sha", "1111111111111111111111111111111111111111")
	f.set("checks", "")
	f.set("log", "")
	return f
}

func (f *fakeGH) set(name, value string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(value), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeGH) calls() []string {
	raw, _ := os.ReadFile(filepath.Join(f.dir, "gh.log"))
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

const failingChecks = "build\tpass\t45s\thttps://github.com/a/b/actions/runs/11/job/21\t\n" +
	"test\tfail\t2m\thttps://github.com/a/b/actions/runs/11/job/22\t\n"

const failingLog = "test\tRun go test\t2026-09-26T10:00:00.0000000Z ok  \tpkg/a\t0.1s\n" +
	"test\tRun go test\t2026-09-26T10:00:00.1000000Z GITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123456789\n" +
	"test\tRun go test\t2026-09-26T10:00:01.0000000Z --- FAIL: TestThing (0.00s)\n" +
	"test\tRun go test\t2026-09-26T10:00:01.0000000Z     thing_test.go:9: want 3, got 4\n"

type sent struct {
	id   int64
	text string
}

type captureSender struct {
	mu   sync.Mutex
	msgs []sent
}

func (c *captureSender) SendNotice(_ context.Context, id int64, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, sent{id, text})
	return nil
}

func (c *captureSender) all() []sent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]sent(nil), c.msgs...)
}

type note struct{ title, body, url string }

type captureNotifier struct {
	mu    sync.Mutex
	notes []note
}

func (c *captureNotifier) Notify(title, body, urlPath string, _ *sinks.Extra) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notes = append(c.notes, note{title, body, urlPath})
}

func (c *captureNotifier) all() []note {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]note(nil), c.notes...)
}

type fixture struct {
	w       *Watcher
	db      *store.DB
	proj    *store.Project
	target  *store.Target
	clock   float64
	sender  *captureSender
	notices *captureNotifier
}

func newFixture(t *testing.T, maxAttempts int) *fixture {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target, err := db.InsertTarget(&store.Target{Name: "local", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	proj, err := db.InsertProject(&store.Project{Name: "p", TargetID: target.ID, RepoPath: t.TempDir(),
		CILoop: 1, CIMaxAttempts: maxAttempts})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{db: db, proj: proj, target: target, clock: 1_000_000,
		sender: &captureSender{}, notices: &captureNotifier{}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	f.w = New(db, executor.NewRegistry(false, 0), bus.New(), f.notices, f.sender, log)
	f.w.Sync = true
	f.w.Now = func() float64 { return f.clock }
	return f
}

// tick advances the clock past the watch's next poll and runs one tick.
func (f *fixture) tick(t *testing.T, id int64) *store.CIWatch {
	t.Helper()
	cw, err := f.db.CIWatch(id)
	if err != nil {
		t.Fatal(err)
	}
	if cw.NextPollAt > f.clock {
		f.clock = cw.NextPollAt
	}
	f.w.Tick(context.Background())
	cw, err = f.db.CIWatch(id)
	if err != nil {
		t.Fatal(err)
	}
	return cw
}

func TestFailFixPassInLiveSession(t *testing.T) {
	gh := newFakeGH(t)
	f := newFixture(t, 3)
	sess, err := f.db.InsertSession(&store.Session{ProjectID: &f.proj.ID, TargetID: f.target.ID,
		Name: "fixer", Agent: "codex", Workdir: f.proj.RepoPath, TmuxSession: "lec-fixer"})
	if err != nil {
		t.Fatal(err)
	}
	cw, err := f.w.Arm(Owner{SessionID: &sess.ID, ProjectID: &f.proj.ID, TargetID: f.target.ID,
		Branch: "lec/fixer"}, prURL, false)
	if err != nil {
		t.Fatal(err)
	}

	// 1) CI fails: one report goes to the session, with the failing job and
	// the end of its log, and without the token the log printed.
	gh.set("checks", failingChecks)
	gh.set("log", failingLog)
	cw = f.tick(t, cw.ID)
	msgs := f.sender.all()
	if len(msgs) != 1 || msgs[0].id != sess.ID {
		t.Fatalf("want one report to session %d, got %+v", sess.ID, msgs)
	}
	report := msgs[0].text
	for _, want := range []string{prURL, "commit 1111111", "fix request 1 of 3", "- test — https://github.com/a/b/actions/runs/11/job/22",
		"--- FAIL: TestThing", "want 3, got 4", "push to lec/fixer"} {
		if !strings.Contains(report, want) {
			t.Errorf("report is missing %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "ghp_abcdefghij") || strings.Contains(report, "- build") {
		t.Errorf("report leaked a token or listed a passing check:\n%s", report)
	}
	if cw.State != StateFailing || cw.Attempts != 1 || Label(cw) != "CI failing — attempt 1/3" {
		t.Fatalf("after the first failure: %+v label=%q", cw, Label(cw))
	}
	var fetched bool
	for _, c := range gh.calls() {
		fetched = fetched || c == "run view 11 -R a/b --log-failed --job 22"
	}
	if !fetched {
		t.Fatalf("expected the failed job's log to be fetched, calls: %v", gh.calls())
	}

	// 2) Same commit still failing: no second report, and the poll backs off.
	interval := cw.IntervalS
	cw = f.tick(t, cw.ID)
	if len(f.sender.all()) != 1 {
		t.Fatalf("a failure must be reported once per commit, got %d reports", len(f.sender.all()))
	}
	if cw.IntervalS <= interval {
		t.Fatalf("expected backoff while nothing changes: %v -> %v", interval, cw.IntervalS)
	}

	// 3) The agent pushes; checks re-run.
	gh.set("sha", "2222222222222222222222222222222222222222")
	gh.set("checks", "build\tpending\t0\thttps://github.com/a/b/actions/runs/12/job/31\t\n"+
		"test\tpending\t0\thttps://github.com/a/b/actions/runs/12/job/32\t\n")
	cw = f.tick(t, cw.ID)
	if cw.State != StatePending || Label(cw) != "CI running — attempt 1/3" || cw.IntervalS != DefaultMinInterval.Seconds() {
		t.Fatalf("after the push: %+v label=%q", cw, Label(cw))
	}

	// 4) Green: the watch ends, and the phone hears about it.
	gh.set("checks", "build\tpass\t40s\thttps://github.com/a/b/actions/runs/12/job/31\t\n"+
		"test\tpass\t2m\thttps://github.com/a/b/actions/runs/12/job/32\t\n")
	cw = f.tick(t, cw.ID)
	if cw.State != StatePassed || Label(cw) != "CI passed after 1 fix" {
		t.Fatalf("after green: %+v label=%q", cw, Label(cw))
	}
	notes := f.notices.all()
	if len(notes) != 1 || notes[0].title != "CI passed" || !strings.Contains(notes[0].body, "green after 1 fix attempt") ||
		notes[0].url != "/#session/"+itoa(sess.ID) {
		t.Fatalf("want one CI passed push, got %+v", notes)
	}
	if due, _ := f.db.DueCIWatches(f.clock + 1e9); len(due) != 0 {
		t.Fatalf("a passed PR must stop polling, still due: %+v", due)
	}
}

func TestCapReachedOnTask(t *testing.T) {
	gh := newFakeGH(t)
	f := newFixture(t, 2)
	task, err := f.db.InsertTask(&store.Task{ProjectID: f.proj.ID, Title: "flaky", Prompt: "p", Status: "review"})
	if err != nil {
		t.Fatal(err)
	}
	cw, err := f.w.Arm(Owner{TaskID: &task.ID, ProjectID: &f.proj.ID, TargetID: f.target.ID,
		Branch: "lec/task-1"}, prURL, false)
	if err != nil {
		t.Fatal(err)
	}
	gh.set("checks", failingChecks)
	gh.set("log", failingLog)
	for i, sha := range []string{"aaaaaaa1", "bbbbbbb2", "ccccccc3"} {
		gh.set("sha", sha)
		cw = f.tick(t, cw.ID)
		if i < 2 && (cw.State != StateFailing || cw.Attempts != i+1) {
			t.Fatalf("round %d: %+v", i+1, cw)
		}
	}
	if cw.State != StateCapped || cw.Attempts != 2 || Label(cw) != "CI failing — gave up after 2/2" {
		t.Fatalf("after the cap: %+v label=%q", cw, Label(cw))
	}
	msgs, err := f.db.TaskMessages(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || !strings.Contains(msgs[0].Text, "fix request 1 of 2") ||
		!strings.Contains(msgs[1].Text, "fix request 2 of 2") || msgs[0].Interrupt {
		t.Fatalf("want exactly two queued, non-interrupting fix requests, got %+v", msgs)
	}
	if len(f.sender.all()) != 0 {
		t.Fatal("a task with no live session must get a task message, not a session send")
	}
	notes := f.notices.all()
	if len(notes) != 1 || notes[0].title != "CI still failing" || !strings.Contains(notes[0].body, "gave up after 2 fix attempts") ||
		notes[0].url != "/#task/"+itoa(task.ID) {
		t.Fatalf("want one cap push, got %+v", notes)
	}
	if due, _ := f.db.DueCIWatches(f.clock + 1e9); len(due) != 0 {
		t.Fatal("a capped PR must stop polling")
	}
}

func TestPRClosedStopsWatching(t *testing.T) {
	gh := newFakeGH(t)
	f := newFixture(t, 3)
	task, _ := f.db.InsertTask(&store.Task{ProjectID: f.proj.ID, Title: "t", Prompt: "p", Status: "review"})
	cw, err := f.w.Arm(Owner{TaskID: &task.ID, ProjectID: &f.proj.ID, TargetID: f.target.ID}, prURL, false)
	if err != nil {
		t.Fatal(err)
	}
	gh.set("state", "CLOSED")
	gh.set("checks", failingChecks)
	cw = f.tick(t, cw.ID)
	if cw.State != StateClosed || Label(cw) != "PR closed" {
		t.Fatalf("closed PR: %+v", cw)
	}
	for _, c := range gh.calls() {
		if strings.HasPrefix(c, "pr checks") || strings.HasPrefix(c, "run view") {
			t.Fatalf("a closed PR's checks must not be read: %v", gh.calls())
		}
	}
	if msgs, _ := f.db.TaskMessages(task.ID); len(msgs) != 0 || len(f.notices.all()) != 0 {
		t.Fatal("a closed PR must not message the agent or the phone")
	}
	calls := len(gh.calls())
	f.clock += 1e6
	f.w.Tick(context.Background())
	if len(gh.calls()) != calls {
		t.Fatal("a closed PR must not be polled again")
	}
}

func TestNoGHAuthStopsWithReason(t *testing.T) {
	gh := newFakeGH(t)
	f := newFixture(t, 3)
	task, _ := f.db.InsertTask(&store.Task{ProjectID: f.proj.ID, Title: "t", Prompt: "p", Status: "review"})
	cw, err := f.w.Arm(Owner{TaskID: &task.ID, ProjectID: &f.proj.ID, TargetID: f.target.ID}, prURL, false)
	if err != nil {
		t.Fatal(err)
	}
	gh.set("noauth", "")
	cw = f.tick(t, cw.ID)
	if cw.State != StateError || !strings.Contains(cw.Detail, "gh auth login") || Label(cw) != "CI watch stopped" {
		t.Fatalf("unauthenticated gh: %+v", cw)
	}
	if msgs, _ := f.db.TaskMessages(task.ID); len(msgs) != 0 {
		t.Fatal("no report can be built without gh auth; none must be sent")
	}
	if due, _ := f.db.DueCIWatches(f.clock + 1e9); len(due) != 0 {
		t.Fatal("retrying cannot fix missing auth; the watch must stop")
	}
}

func TestArmNeedsProjectOptIn(t *testing.T) {
	f := newFixture(t, 3)
	if err := f.db.Update("projects", f.proj.ID, map[string]any{"ci_loop": 0}); err != nil {
		t.Fatal(err)
	}
	task, _ := f.db.InsertTask(&store.Task{ProjectID: f.proj.ID, Title: "t", Prompt: "p", Status: "review"})
	o := Owner{TaskID: &task.ID, ProjectID: &f.proj.ID, TargetID: f.target.ID}
	if _, err := f.w.Arm(o, prURL, false); err != ErrNotEnabled {
		t.Fatalf("want ErrNotEnabled, got %v", err)
	}
	if cw, err := f.w.Arm(o, prURL, true); err != nil || cw.MaxAttempts != 3 {
		t.Fatalf("an explicit arm overrides the opt-in: %+v %v", cw, err)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// A task that was taken over into an interactive session has a live agent
// again: the report goes into that session, not into a queued task message
// (which the takeover would refuse anyway).
func TestTakenOverTaskGetsReportInItsSession(t *testing.T) {
	gh := newFakeGH(t)
	f := newFixture(t, 3)
	task, _ := f.db.InsertTask(&store.Task{ProjectID: f.proj.ID, Title: "t", Prompt: "p", Status: "review"})
	att, err := f.db.InsertAttempt(&store.Attempt{TaskID: task.ID, N: 1, Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := f.db.InsertSession(&store.Session{ProjectID: &f.proj.ID, TargetID: f.target.ID,
		Name: "takeover", Agent: "gemini", Workdir: f.proj.RepoPath, TmuxSession: "lec-take"})
	if _, err := f.db.Exec(`INSERT INTO task_takeovers(task_id,attempt_id,session_id,status,created_at)
		VALUES(?,?,?,'active',?)`, task.ID, att.ID, sess.ID, store.Now()); err != nil {
		t.Fatal(err)
	}
	cw, err := f.w.Arm(Owner{TaskID: &task.ID, ProjectID: &f.proj.ID, TargetID: f.target.ID}, prURL, false)
	if err != nil {
		t.Fatal(err)
	}
	gh.set("checks", failingChecks)
	gh.set("log", failingLog)
	cw = f.tick(t, cw.ID)
	if msgs := f.sender.all(); len(msgs) != 1 || msgs[0].id != sess.ID || cw.State != StateFailing {
		t.Fatalf("want the report in session %d, got %+v (watch %+v)", sess.ID, msgs, cw)
	}
}

// However large the failure, one fix request stays bounded: the check list
// is capped, at most four job logs are fetched, and the whole report fits
// its byte budget.
func TestReportIsBounded(t *testing.T) {
	gh := newFakeGH(t)
	f := newFixture(t, 3)
	var huge strings.Builder
	for i := 0; i < 20000; i++ {
		huge.WriteString("test\tRun go test\t2026-09-26T10:00:00.0000000Z noisy output line number ")
		huge.WriteString(strconv.Itoa(i))
		huge.WriteString("\n")
	}
	gh.set("log", huge.String())
	var failing []check
	for i := 0; i < 30; i++ {
		failing = append(failing, check{Name: "matrix-" + strconv.Itoa(i), Bucket: "fail",
			Link: "https://github.com/a/b/actions/runs/7/job/" + strconv.Itoa(100+i)})
	}
	cw := &store.CIWatch{ID: 1, PRURL: prURL, MaxAttempts: 3, Branch: "lec/x"}
	report := f.w.report(context.Background(), executor.NewLocal(), cw, "abcdef0123", failing, 1)
	if len(report) > reportBytes+2000 {
		t.Fatalf("report is %d bytes", len(report))
	}
	if !strings.Contains(report, "- … and 10 more") || strings.Contains(report, "matrix-25") {
		t.Fatalf("the check list must be capped:\n%s", report[:600])
	}
	fetches := 0
	for _, c := range gh.calls() {
		if strings.HasPrefix(c, "run view") {
			fetches++
		}
	}
	if fetches > maxJobsLogged {
		t.Fatalf("fetched %d job logs, over the %d cap", fetches, maxJobsLogged)
	}
	if !strings.Contains(report, "noisy output line number 19999") {
		t.Fatal("the end of the log is the part that must survive")
	}
}
