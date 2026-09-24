package api_test

// Real-process coverage for session review/merge: real git, real worktrees,
// the real local executor — the mock executor doesn't speak the worktree
// control script (see session_review_test.go) or run real git, so this is
// where the diff/commit/PR-description behavior that actually depends on git
// gets proven. Requires the isolated runner: tools/run-isolated-tests.sh.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %s: %s: %v", dir, strings.Join(args, " "), out, err)
	}
	return string(out)
}

// newRealSessionHarness gives a real (non-mock) harness, a real local target
// and a real one-commit git repository registered as its project — the base
// every real session-review test builds on.
func newRealSessionHarness(t *testing.T) (*harness, *store.Project, string) {
	t.Helper()
	requireRealTools(t)
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	target, err := h.App.DB.InsertTarget(&store.Target{Name: "review-real", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "app.py"), []byte("def main():\n    pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "-c", "user.name=Test", "-c", "user.email=t@example.invalid", "commit", "-qm", "base")
	h.decode("PUT", "/api/agents", []obj{{"name": "review-real-agent", "command": "sleep 600"}}, 200, nil)
	proj, err := h.App.DB.InsertProject(&store.Project{
		Name: "review-real", TargetID: target.ID, RepoPath: repo, DefaultBaseBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	return h, proj, repo
}

// A live diff must reflect the merge-base with the base branch, not the base
// branch's current tip — otherwise a long-running session's diff fills up
// with everyone else's work on main. It must also see uncommitted and
// untracked files, since that's the whole point of a LIVE diff.
func TestRealSessionDiffAgainstMergeBase(t *testing.T) {
	h, proj, repo := newRealSessionHarness(t)
	var row obj
	h.decode("POST", "/api/sessions", obj{
		"project_id": proj.ID, "agent": "review-real-agent", "worktree": obj{}, "yolo": false,
	}, 201, &row)
	wt := row.sub("workspace").str("path")
	if wt == "" {
		t.Fatalf("session has no worktree: %v", row)
	}

	// main moves forward AFTER the worktree branched off it — this must not
	// leak into the session's diff.
	if err := os.WriteFile(filepath.Join(repo, "unrelated.txt"), []byte("noise\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "-c", "user.name=Test", "-c", "user.email=t@example.invalid", "commit", "-qm", "unrelated main work")

	// the session's own change: a modified tracked file and a new untracked one
	if err := os.WriteFile(filepath.Join(wt, "app.py"), []byte("def main():\n    print('hi')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "NEW.md"), []byte("new file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var diff obj
	h.decode("GET", fmt.Sprintf("/api/sessions/%d/diff", int64(row.num("id"))), nil, 200, &diff)
	repos := diff.list("repos")
	if len(repos) != 1 {
		t.Fatalf("expected exactly one repository: %v", diff)
	}
	seen := map[string]bool{}
	for _, f := range repos[0].list("files") {
		seen[f.str("path")] = true
	}
	if !seen["app.py"] || !seen["NEW.md"] {
		t.Fatalf("diff missing the changed/untracked files: %v", repos[0])
	}
	if seen["unrelated.txt"] {
		t.Fatalf("diff leaked an unrelated main-branch commit past the merge-base: %v", repos[0])
	}
}

// A session with no isolated worktree runs straight in the project's own
// checkout — committing there would commit directly onto main.
func TestRealSessionCommitRefusesWithoutIsolatedWorktree(t *testing.T) {
	h, proj, _ := newRealSessionHarness(t)
	var row obj
	h.decode("POST", "/api/sessions", obj{
		"project_id": proj.ID, "agent": "review-real-agent", "yolo": false, // no "worktree"
	}, 201, &row)
	code := h.status("POST", fmt.Sprintf("/api/sessions/%d/commit", int64(row.num("id"))), obj{"message": "x"})
	if code != 409 {
		t.Fatalf("commit directly on main: %d", code)
	}
}

// The whole point of the isolated-runner e2e/Go split: commit, push and land
// on a real remote, using a local bare repository instead of GitHub.
func TestRealSessionCommitPushToLocalBareRemote(t *testing.T) {
	h, proj, repo := newRealSessionHarness(t)
	bare := t.TempDir()
	gitIn(t, bare, "init", "-q", "--bare")
	gitIn(t, repo, "remote", "add", "origin", bare)
	gitIn(t, repo, "push", "-q", "origin", "main")

	var row obj
	h.decode("POST", "/api/sessions", obj{
		"project_id": proj.ID, "agent": "review-real-agent", "worktree": obj{}, "yolo": false,
	}, 201, &row)
	wt := row.sub("workspace").str("path")
	branch := row.sub("workspace").str("branch")
	if err := os.WriteFile(filepath.Join(wt, "app.py"), []byte("def main():\n    print('hi')\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := h.post(fmt.Sprintf("/api/sessions/%d/commit", int64(row.num("id"))),
		obj{"message": "feat: greet", "push": true}, 200)
	steps := map[string]obj{}
	for _, s := range got.list("steps") {
		steps[s.str("step")] = s
	}
	if steps["commit"].num("rc") != 0 {
		t.Fatalf("commit step: %v", steps)
	}
	if steps["push"].num("rc") != 0 {
		t.Fatalf("push step: %v", steps)
	}

	if out := gitIn(t, bare, "branch", "--list", branch); !strings.Contains(out, branch) {
		t.Fatalf("branch never reached the bare remote: %q", out)
	}
	if log := gitIn(t, bare, "log", branch, "-1", "--format=%s"); !strings.Contains(log, "feat: greet") {
		t.Fatalf("commit message never reached the remote: %q", log)
	}
}

// A second commit on an unrelated push=false call must not silently reuse a
// stale PR flag, and a plain commit-only call must not touch the remote.
func TestRealSessionCommitWithoutPushLeavesRemoteUntouched(t *testing.T) {
	h, proj, repo := newRealSessionHarness(t)
	bare := t.TempDir()
	gitIn(t, bare, "init", "-q", "--bare")
	gitIn(t, repo, "remote", "add", "origin", bare)
	gitIn(t, repo, "push", "-q", "origin", "main")

	var row obj
	h.decode("POST", "/api/sessions", obj{
		"project_id": proj.ID, "agent": "review-real-agent", "worktree": obj{}, "yolo": false,
	}, 201, &row)
	wt := row.sub("workspace").str("path")
	branch := row.sub("workspace").str("branch")
	if err := os.WriteFile(filepath.Join(wt, "app.py"), []byte("def main():\n    print('hi')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.post(fmt.Sprintf("/api/sessions/%d/commit", int64(row.num("id"))), obj{"message": "local only"}, 200)
	if out := gitIn(t, bare, "branch", "--list", branch); strings.Contains(out, branch) {
		t.Fatalf("commit with push:false must not reach the remote: %q", out)
	}
}

// PR description generation runs on the live diff through the session's own
// target executor; SummaryGen stands in for the real headless model call so
// this proves the wiring without spending a real token.
func TestRealSessionPRDescriptionUsesTheLiveDiff(t *testing.T) {
	h, proj, _ := newRealSessionHarness(t)
	var row obj
	h.decode("POST", "/api/sessions", obj{
		"project_id": proj.ID, "agent": "review-real-agent", "worktree": obj{}, "yolo": false,
	}, 201, &row)
	wt := row.sub("workspace").str("path")
	if err := os.WriteFile(filepath.Join(wt, "app.py"), []byte("def main():\n    print('hi')\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var seen string
	h.App.Server.SummaryGen = func(ctx context.Context, ex executor.Executor, agent, model, prompt string) (string, error) {
		seen = prompt
		return `{"title":"Greet","body":"prints hi"}`, nil
	}
	t.Cleanup(func() { h.App.Server.SummaryGen = nil })

	out := h.post(fmt.Sprintf("/api/sessions/%d/pr-description", int64(row.num("id"))), nil, 200)
	if out.str("title") != "Greet" || out.str("body") != "prints hi" {
		t.Fatalf("pr-description: %v", out)
	}
	if !strings.Contains(seen, "print('hi')") {
		t.Errorf("the live diff never reached the summary call:\n%s", seen)
	}
}

// A grouped workspace's diff covers every repository, keyed by name, and a
// change in one must not appear under another.
func TestRealMultiRepoSessionDiff(t *testing.T) {
	requireRealTools(t)
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	target, err := h.App.DB.InsertTarget(&store.Target{Name: "group-real", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	h.decode("PUT", "/api/agents", []obj{{"name": "group-real-agent", "command": "sleep 600"}}, 200, nil)
	var projects []*store.Project
	for i := 0; i < 2; i++ {
		repo := t.TempDir()
		gitIn(t, repo, "init", "-q", "-b", "main")
		if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("base\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, repo, "add", ".")
		gitIn(t, repo, "-c", "user.name=Test", "-c", "user.email=t@example.invalid", "commit", "-qm", "base")
		p, err := h.App.DB.InsertProject(&store.Project{
			Name: fmt.Sprintf("multi%d", i), TargetID: target.ID, RepoPath: repo, DefaultBaseBranch: "main"})
		if err != nil {
			t.Fatal(err)
		}
		projects = append(projects, p)
	}

	var row obj
	h.decode("POST", "/api/sessions", obj{
		"name": "grouped-review", "agent": "group-real-agent", "project_id": projects[0].ID,
		"worktree": obj{"extra_repositories": []obj{{"project_id": projects[1].ID}}}, "yolo": false,
	}, 201, &row)
	repositories := row.sub("workspace").list("repositories")
	if len(repositories) != 2 {
		t.Fatalf("expected 2 repositories in the workspace: %v", row)
	}
	paths := make([]string, 2)
	for i, r := range repositories {
		paths[i] = r.sub("worktree").str("path")
	}
	if err := os.WriteFile(filepath.Join(paths[0], "extra0.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths[1], "extra1.txt"), []byte("y\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var diff obj
	h.decode("GET", fmt.Sprintf("/api/sessions/%d/diff", int64(row.num("id"))), nil, 200, &diff)
	diffRepos := diff.list("repos")
	if len(diffRepos) != 2 {
		t.Fatalf("expected a diff per repository: %v", diff)
	}
	byName := map[string]map[string]bool{}
	for _, d := range diffRepos {
		files := map[string]bool{}
		for _, f := range d.list("files") {
			files[f.str("path")] = true
		}
		byName[d.str("name")] = files
	}
	for i, name := range []string{"multi0", "multi1"} {
		files, ok := byName[name]
		if !ok {
			t.Fatalf("missing repository %q in diff: %v", name, byName)
		}
		want := fmt.Sprintf("extra%d.txt", i)
		if !files[want] {
			t.Errorf("repository %q missing its own change %q: %v", name, want, files)
		}
		other := fmt.Sprintf("extra%d.txt", 1-i)
		if files[other] {
			t.Errorf("repository %q leaked the other repository's change: %v", name, files)
		}
	}
}
