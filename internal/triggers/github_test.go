package triggers

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

// fakeGH writes a small bash script on PATH standing in for the `gh` CLI —
// the "fake gh script on PATH inside the isolated runner" the trigger spec
// asks for. It logs every invocation (one line per call, newline-joined
// args) to logPath and answers the handful of `gh` calls the GitHub trigger
// actually makes.
func fakeGH(t *testing.T, issuesJSON, commentsJSON string) (logPath string) {
	t.Helper()
	testutil.RequireIsolated(t)
	dir := t.TempDir()
	logPath = filepath.Join(dir, "gh.log")
	issuesFile := filepath.Join(dir, "issues.json")
	commentsFile := filepath.Join(dir, "comments.json")
	if err := os.WriteFile(issuesFile, []byte(issuesJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(commentsFile, []byte(commentsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `#!/usr/bin/env bash
set -e
echo "$*" >> ` + shq(logPath) + `
case "$*" in
  *"/issues/comments?"*)
    printf 'HTTP/2.0 200 OK\r\nx-ratelimit-remaining: 4999\r\n\r\n'
    cat ` + shq(commentsFile) + `
    ;;
  *"/issues?"*)
    printf 'HTTP/2.0 200 OK\r\nx-ratelimit-remaining: 4999\r\n\r\n'
    cat ` + shq(issuesFile) + `
    ;;
  "api --include repos/"*)
    printf 'HTTP/2.0 200 OK\r\nx-ratelimit-remaining: 4999\r\n\r\n{"full_name":"a/b"}'
    ;;
  "issue edit "*)
    exit 0
    ;;
  "issue comment "*)
    echo "https://github.com/a/b/issues/1#comment"
    ;;
  "pr create "*)
    echo "https://github.com/a/b/pull/2"
    ;;
  *)
    echo "fake gh: unrecognized invocation: $*" >&2
    exit 1
    ;;
esac
`
	ghPath := filepath.Join(dir, "gh")
	if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })
	return logPath
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func readLog(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

// newLocalManager builds a Manager whose one project resolves to a REAL
// executor.Local target, so pollGitHub genuinely shells out to the fake gh
// script above instead of the scripted Mock executor.
func newLocalManager(t *testing.T) (*Manager, *store.Project) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "gh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target, err := db.InsertTarget(&store.Target{Name: "local", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.InsertProject(&store.Project{Name: "p1", TargetID: target.ID, RepoPath: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	m := New(db, executor.NewRegistry(false, 0), nil)
	return m, project
}

const oneIssueJSON = `[{"number":7,"title":"fix the bug","body":"details","html_url":"https://github.com/a/b/issues/7","user":{"login":"trusted-user"},"labels":[{"name":"lectern"}]}]`
const oneCommentJSON = `[{"id":55,"body":"hey @lectern please help","html_url":"https://github.com/a/b/issues/7#c","issue_url":"https://api.github.com/repos/a/b/issues/7","user":{"login":"trusted-user"}}]`

func TestPollGitHubCreatesTaskFromLabelledIssue(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	logPath := fakeGH(t, oneIssueJSON, "[]")
	m, project := newLocalManager(t)
	var created NewTaskSpec
	m.CreateTask = func(_ *store.Project, spec NewTaskSpec) (*store.Task, error) {
		created = spec
		return &store.Task{ID: 1}, nil
	}
	src, err := m.DB.InsertTriggerSource(&store.TriggerSource{
		ProjectID: project.ID, Kind: "github", Enabled: true,
		ConfigJSON: `{"repo":"a/b","allowed_authors":["trusted-user"]}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := m.pollGitHub(context.Background(), src); err != nil {
		t.Fatal(err)
	}

	if created.Title != "fix the bug" {
		t.Fatalf("expected a task titled from the issue, got %+v", created)
	}
	events, err := m.DB.RecentTriggerEvents(project.ID, 10)
	if err != nil || len(events) != 1 || events[0].Action != "task_created" {
		t.Fatalf("expected one task_created event, got %v %v", events, err)
	}

	log := readLog(t, logPath)
	foundRelabel := false
	for _, line := range log {
		if strings.Contains(line, "issue edit 7") && strings.Contains(line, "--remove-label lectern") &&
			strings.Contains(line, "--add-label lectern:queued") {
			foundRelabel = true
		}
	}
	if !foundRelabel {
		t.Fatalf("expected the issue to be relabelled, log: %v", log)
	}

	// Polling again must not re-file the same issue: the mock gh script
	// still returns the same (now-stale) issues.json, exercising the dedup
	// ledger rather than the relabel actually happening this second time.
	fresh, _ := m.DB.TriggerSource(src.ID)
	if err := m.pollGitHub(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
	events2, _ := m.DB.RecentTriggerEvents(project.ID, 10)
	if len(events2) != 1 {
		t.Fatalf("re-polling the same issue must not create a second event, got %d", len(events2))
	}
}

func TestPollGitHubSkipsUnallowedCommentAuthor(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	fakeGH(t, "[]", oneCommentJSON)
	m, project := newLocalManager(t)
	called := false
	m.CreateTask = func(*store.Project, NewTaskSpec) (*store.Task, error) {
		called = true
		return &store.Task{ID: 1}, nil
	}
	src, err := m.DB.InsertTriggerSource(&store.TriggerSource{
		ProjectID: project.ID, Kind: "github", Enabled: true,
		ConfigJSON: `{"repo":"a/b","allowed_authors":["someone-else"]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.pollGitHub(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("a comment from an author outside the allowlist must not create a task")
	}
	events, err := m.DB.RecentTriggerEvents(project.ID, 10)
	if err != nil || len(events) != 1 || events[0].Action != "skipped" {
		t.Fatalf("expected one skipped event, got %v %v", events, err)
	}
}

func TestParseGHInclude(t *testing.T) {
	out := parseGHInclude("HTTP/2.0 200 OK\r\nX-RateLimit-Remaining: 42\r\nServer: gh\r\n\r\n{\"a\":1}")
	if out.StatusCode != 200 || out.RateRemaining != 42 || string(out.Body) != `{"a":1}` {
		t.Fatalf("unexpected parse: %+v body=%q", out, out.Body)
	}
}

// TestCommitPushPR exercises the postback path's git plumbing against a real
// local repository with a local (file-based) remote, so it never needs
// network access — the isolated runner disables it entirely.
func TestCommitPushPR(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	fakeGH(t, "[]", "[]")
	dir := t.TempDir()
	bare := filepath.Join(dir, "origin.git")
	work := filepath.Join(dir, "work")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = work
		if args[0] == "git" && len(args) > 1 && args[1] == "init" {
			cmd.Dir = dir
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	if out, err := exec.Command("git", "init", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "clone", bare, work).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	run("git", "-c", "user.email=a@b.c", "-c", "user.name=test", "commit", "--allow-empty", "-m", "init")
	run("git", "push", "origin", "HEAD:main")
	run("git", "checkout", "-b", "lec/task-1")
	if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	ex := executor.NewLocal()
	url, err := commitPushPR(context.Background(), ex, work, "lec/task-1", "fix the bug", "Closes #7.")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://github.com/a/b/pull/2" {
		t.Fatalf("expected the fake gh's PR url, got %q", url)
	}

	// The commit and push really happened against the local bare remote.
	out, err := exec.Command("git", "-C", bare, "log", "lec/task-1", "-1", "--format=%s").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "fix the bug" {
		t.Fatalf("expected the commit to have reached origin, got %q err=%v", out, err)
	}
}

func TestPostGitHubComment(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	logPath := fakeGH(t, "[]", "[]")
	ex := executor.NewLocal()
	if err := postGitHubComment(context.Background(), ex, "a/b", 7, "all done"); err != nil {
		t.Fatal(err)
	}
	log := readLog(t, logPath)
	if len(log) != 1 || !strings.Contains(log[0], "issue comment 7") || !strings.Contains(log[0], "--repo a/b") {
		t.Fatalf("unexpected gh invocation log: %v", log)
	}
}

func TestGHQueryEscape(t *testing.T) {
	if got := ghQueryEscape("in progress & more"); got != "in%20progress%20%26%20more" {
		t.Fatalf("got %q", got)
	}
}
