package ciloop

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// gitRepo is a task worktree with a real origin: a bare repository the
// worktree's branch tracks, so a push actually lands somewhere to inspect.
type gitRepo struct {
	t              *testing.T
	origin, dir    string
	branch, headSH string
}

func newGitRepo(t *testing.T, branch string) *gitRepo {
	t.Helper()
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
		"GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com", "GIT_CONFIG_NOSYSTEM": "1"} {
		t.Setenv(k, v)
	}
	root := t.TempDir()
	g := &gitRepo{t: t, origin: filepath.Join(root, "origin.git"), dir: filepath.Join(root, "wt"), branch: branch}
	g.git(root, "init", "-q", "--bare", g.origin)
	g.git(root, "init", "-q", "-b", branch, g.dir)
	g.git(g.dir, "remote", "add", "origin", g.origin)
	g.write("main.go", "package main\n")
	g.git(g.dir, "add", "-A")
	g.git(g.dir, "commit", "-q", "-m", "initial")
	g.git(g.dir, "push", "-q", "-u", "origin", branch)
	g.headSH = g.git(g.dir, "rev-parse", "HEAD")
	return g
}

func (g *gitRepo) git(dir string, args ...string) string {
	g.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		g.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (g *gitRepo) write(name, body string) {
	g.t.Helper()
	if err := os.WriteFile(filepath.Join(g.dir, name), []byte(body), 0o644); err != nil {
		g.t.Fatal(err)
	}
}

// originHead is the branch's commit on origin, and its subject.
func (g *gitRepo) originHead() (sha, subject string) {
	return g.git(g.origin, "rev-parse", g.branch), g.git(g.origin, "log", "-1", "--format=%s", g.branch)
}

// taskFixture is a task whose PR is watched, with attempt 1 done in repo's
// worktree — the state the commit button leaves a task in.
func taskFixture(t *testing.T) (*fixture, *fakeGH, *gitRepo, *store.Task, *store.CIWatch) {
	gh := newFakeGH(t)
	f := newFixture(t, 3)
	repo := newGitRepo(t, "lec/task1-a1")
	gh.set("sha", repo.headSH)
	task, err := f.db.InsertTask(&store.Task{ProjectID: f.proj.ID, Title: "t",
		Prompt: "Do the thing. Do not commit or push.", Status: "review"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.InsertAttempt(&store.Attempt{TaskID: task.ID, N: 1, Status: "done",
		WorktreePath: repo.dir, Branch: repo.branch}); err != nil {
		t.Fatal(err)
	}
	cw, err := f.w.Arm(Owner{TaskID: &task.ID, ProjectID: &f.proj.ID, TargetID: f.target.ID,
		Branch: repo.branch}, prURL, false)
	if err != nil {
		t.Fatal(err)
	}
	return f, gh, repo, task, cw
}

// deliverFix does what the scheduler does with a queued fix request: starts
// follow-up attempt n in the same worktree and marks the message delivered.
func deliverFix(t *testing.T, f *fixture, repo *gitRepo, task *store.Task, n int) *store.Attempt {
	t.Helper()
	att, err := f.db.InsertAttempt(&store.Attempt{TaskID: task.ID, N: n, Status: "running",
		WorktreePath: repo.dir, Branch: repo.branch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE task_messages SET status='delivered', attempt_id=? WHERE task_id=? AND status='pending'`,
		att.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	return att
}

// The whole task round, as the real-GitHub run exercised it: the follow-up
// attempt cannot run git, so it leaves its fix uncommitted, and Lectern
// commits and pushes it and picks up the new commit on the next poll.
func TestTaskFixIsCommittedAndPushedByLectern(t *testing.T) {
	f, gh, repo, task, cw := taskFixture(t)
	gh.set("checks", failingChecks)
	gh.set("log", failingLog)
	cw = f.tick(t, cw.ID)
	msgs, _ := f.db.TaskMessages(task.ID)
	if cw.State != StateFailing || len(msgs) != 1 {
		t.Fatalf("want one queued fix request, got %+v (watch %+v)", msgs, cw)
	}
	text := msgs[0].Text
	for _, want := range []string{"You do not need to commit or push", "pushes them to lec/task1-a1",
		"even if the original task said not to commit or push"} {
		if !strings.Contains(text, want) {
			t.Errorf("a task's fix request must say Lectern does the git part; missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "commit, and push") {
		t.Errorf("a task's fix request must not ask the agent to push:\n%s", text)
	}

	// While the fix attempt runs, Lectern leaves the worktree alone.
	att := deliverFix(t, f, repo, task, 2)
	repo.write("main.go", "package main\n\nfunc fixed() {}\n")
	cw = f.tick(t, cw.ID)
	if sha, _ := repo.originHead(); sha != repo.headSH || cw.PushedAttemptID != 0 {
		t.Fatalf("nothing may be pushed while the attempt runs (origin %s, watch %+v)", sha, cw)
	}

	// The attempt ends with its fix uncommitted. The scheduler's hook makes
	// the watch due now, however far its backoff had grown.
	if err := f.db.Update("attempts", att.ID, map[string]any{"status": "done"}); err != nil {
		t.Fatal(err)
	}
	f.db.Update("ci_watches", cw.ID, map[string]any{"interval_s": 480, "next_poll_at": f.clock + 480})
	att.Status = "done"
	f.w.AttemptFinished(att)
	if cw, _ = f.db.CIWatch(cw.ID); cw.NextPollAt > f.clock || cw.IntervalS != 0 {
		t.Fatalf("a finished fix attempt must make its watch due now: %+v", cw)
	}
	f.w.Tick(context.Background())
	cw, _ = f.db.CIWatch(cw.ID)
	sha, subject := repo.originHead()
	if sha == repo.headSH || subject != "Fix CI: test" {
		t.Fatalf("want the fix pushed as \"Fix CI: test\", origin has %s %q", sha, subject)
	}
	if st := repo.git(repo.dir, "status", "--porcelain"); st != "" {
		t.Fatalf("the worktree should be clean after the commit: %q", st)
	}
	if cw.PushedAttemptID != att.ID || cw.State != StateFailing || cw.IntervalS != DefaultMinInterval.Seconds() {
		t.Fatalf("after the push: %+v", cw)
	}

	// The next poll sees the new commit on the PR, and the round finishes.
	gh.set("sha", sha)
	gh.set("checks", "build\tpass\t40s\thttps://github.com/a/b/actions/runs/12/job/31\t\n"+
		"test\tpass\t2m\thttps://github.com/a/b/actions/runs/12/job/32\t\n")
	if cw = f.tick(t, cw.ID); cw.State != StatePassed || Label(cw) != "CI passed after 1 fix" {
		t.Fatalf("after green: %+v label=%q", cw, Label(cw))
	}
}

// An agent that could run git and committed without pushing still gets its
// commit onto the PR; one that changed nothing stops the watch with a reason
// instead of leaving it to sit in "failing".
func TestTaskFixCommittedOrEmpty(t *testing.T) {
	t.Run("committed but not pushed", func(t *testing.T) {
		f, gh, repo, task, cw := taskFixture(t)
		gh.set("checks", failingChecks)
		cw = f.tick(t, cw.ID)
		att := deliverFix(t, f, repo, task, 2)
		repo.write("fix.go", "package main\n")
		repo.git(repo.dir, "add", "-A")
		repo.git(repo.dir, "commit", "-q", "-m", "agent's own fix")
		f.db.Update("attempts", att.ID, map[string]any{"status": "done"})
		cw = f.tick(t, cw.ID)
		if _, subject := repo.originHead(); subject != "agent's own fix" || cw.PushedAttemptID != att.ID {
			t.Fatalf("the agent's commit must be pushed as is: origin %q, watch %+v", subject, cw)
		}
	})
	t.Run("no changes", func(t *testing.T) {
		f, gh, repo, task, cw := taskFixture(t)
		gh.set("checks", failingChecks)
		cw = f.tick(t, cw.ID)
		att := deliverFix(t, f, repo, task, 2)
		f.db.Update("attempts", att.ID, map[string]any{"status": "done"})
		cw = f.tick(t, cw.ID)
		if cw.State != StateError || !strings.Contains(cw.Detail, "without changing anything") {
			t.Fatalf("an empty fix must stop the watch with a reason: %+v", cw)
		}
		if sha, _ := repo.originHead(); sha != repo.headSH {
			t.Fatal("nothing may be pushed for an empty fix")
		}
	})
}

// A live session's agent (or its user) runs git itself: the report still
// asks it to push, and Lectern never commits in its worktree.
func TestTakenOverTaskIsNotCommittedByLectern(t *testing.T) {
	f, gh, repo, task, cw := taskFixture(t)
	att, _ := f.db.LatestAttempt(task.ID)
	sess, _ := f.db.InsertSession(&store.Session{ProjectID: &f.proj.ID, TargetID: f.target.ID,
		Name: "takeover", Agent: "claude", Workdir: repo.dir, TmuxSession: "lec-take"})
	if _, err := f.db.Exec(`INSERT INTO task_takeovers(task_id,attempt_id,session_id,status,created_at)
		VALUES(?,?,?,'active',?)`, task.ID, att.ID, sess.ID, store.Now()); err != nil {
		t.Fatal(err)
	}
	gh.set("checks", failingChecks)
	cw = f.tick(t, cw.ID)
	msgs := f.sender.all()
	if len(msgs) != 1 || !strings.Contains(msgs[0].text, "commit, and push to lec/task1-a1") {
		t.Fatalf("a live session is asked to push itself, got %+v", msgs)
	}
	repo.write("main.go", "package main // edited\n")
	cw = f.tick(t, cw.ID)
	if sha, _ := repo.originHead(); sha != repo.headSH || cw.PushedAttemptID != 0 {
		t.Fatal("Lectern must not commit in a live session's worktree")
	}
}

// Arming a PR that is already watched comes right after a push (the commit
// button's "a pull request already exists"): the watch looks again now, with
// its backoff reset, instead of up to ten minutes later.
func TestRearmingAnActiveWatchResetsBackoff(t *testing.T) {
	f := newFixture(t, 3)
	task, _ := f.db.InsertTask(&store.Task{ProjectID: f.proj.ID, Title: "t", Prompt: "p", Status: "review"})
	o := Owner{TaskID: &task.ID, ProjectID: &f.proj.ID, TargetID: f.target.ID}
	cw, err := f.w.Arm(o, prURL, false)
	if err != nil {
		t.Fatal(err)
	}
	far := store.Now() + 480
	f.db.Update("ci_watches", cw.ID, map[string]any{"state": StateFailing, "attempts": 1,
		"interval_s": 480, "next_poll_at": far})
	cw, err = f.w.Arm(o, prURL, false)
	if err != nil {
		t.Fatal(err)
	}
	if cw.NextPollAt >= far || cw.IntervalS != 0 || cw.State != StateFailing || cw.Attempts != 1 {
		t.Fatalf("re-arming an active watch must reset its schedule and keep its counters: %+v", cw)
	}

	f.db.Update("ci_watches", cw.ID, map[string]any{"interval_s": 480, "next_poll_at": f.clock + 480})
	f.w.Pushed(o)
	if cw, _ = f.db.CIWatch(cw.ID); cw.NextPollAt > f.clock || cw.IntervalS != 0 {
		t.Fatalf("a push by Lectern must make the watch due now: %+v", cw)
	}
}
