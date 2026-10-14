package worktree_test

// Integration: the worktree module against REAL git through the local executor.
// The mock target cannot catch quoting, exclude-file or diff-shape bugs — those
// only ever showed up on a live dispatch.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/worktree"
)

func gitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
}

func mkRepo(t *testing.T, path string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
		cmd.Env = gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if out, err := exec.Command("git", "init", "-q", "-b", "main", path).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(path, "app.py"), []byte("print(\"hello\")\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "app.py")
	run("commit", "-qm", "initial")
	return path
}

func TestWorktreeLifecycleRealGit(t *testing.T) {
	dir := t.TempDir()
	repo := mkRepo(t, filepath.Join(dir, "repo"))
	ex := executor.NewLocal()
	ctx := context.Background()
	wt := worktree.Path(filepath.Join(dir, "wts"), 1, 1)
	branch := worktree.BranchName(1, 1)

	if err := worktree.Ensure(ctx, ex, repo, "main", branch, wt); err != nil {
		t.Fatal(err)
	}
	// the runtime dir is excluded from git status
	if err := os.MkdirAll(filepath.Join(wt, ".lectern"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wt, ".lectern", "junk.txt"), []byte("x"), 0o644)
	// verify-run artifacts must never leak into diffs or commits (found live)
	os.MkdirAll(filepath.Join(wt, "__pycache__"), 0o755)
	os.WriteFile(filepath.Join(wt, "__pycache__", "app.cpython-311.pyc"), []byte{0}, 0o644)
	os.WriteFile(filepath.Join(wt, "app.py"), []byte("print(\"changed\")\nNEW = 1\n"), 0o644)
	os.WriteFile(filepath.Join(wt, "extra.py"), []byte("added = True\n"), 0o644)

	patch, files, err := worktree.CaptureDiff(ctx, ex, wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	sort.Strings(paths)
	if strings.Join(paths, ",") != "app.py,extra.py" {
		t.Fatalf("diff must exclude runtime and build artifacts, got %v", paths)
	}
	var splitPaths []string
	var sawNew bool
	for _, s := range worktree.SplitPatch(patch) {
		splitPaths = append(splitPaths, s.Path)
		if strings.Contains(s.Patch, "+NEW = 1") {
			sawNew = true
		}
	}
	sort.Strings(splitPaths)
	if strings.Join(splitPaths, ",") != "app.py,extra.py" {
		t.Errorf("split patch: %v", splitPaths)
	}
	if !sawNew {
		t.Error("the added line is missing from the split patch")
	}

	// idempotent re-ensure: a follow-up attempt reuses the same worktree
	if err := worktree.Ensure(ctx, ex, repo, "main", branch, wt); err != nil {
		t.Fatalf("re-ensure must be a no-op: %v", err)
	}

	if err := worktree.Remove(ctx, ex, repo, wt); err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command("git", "-C", repo, "worktree", "list").CombinedOutput()
	if strings.Contains(string(out), "task1-a1") {
		t.Errorf("the worktree was not reclaimed:\n%s", out)
	}
}

func TestWorktreeBadBaseBranchFails(t *testing.T) {
	dir := t.TempDir()
	repo := mkRepo(t, filepath.Join(dir, "repo2"))
	err := worktree.Ensure(context.Background(), executor.NewLocal(), repo,
		"no-such-branch", "lec/task9-a1", filepath.Join(dir, "wt9"))
	if err == nil {
		t.Fatal("a nonexistent base branch must fail loudly, not hang")
	}
	if !strings.Contains(err.Error(), "worktree add failed") {
		t.Errorf("the error should say what happened: %v", err)
	}
}

// ReadDiff must show untracked work like CaptureDiff does, without writing the
// index: background readers run it while an agent is using git in the same
// worktree, and an index write there can fail the agent's own command.
func TestReadDiffShowsUntrackedWithoutWritingTheIndex(t *testing.T) {
	repo := mkRepo(t, filepath.Join(t.TempDir(), "repo"))
	if err := os.WriteFile(filepath.Join(repo, "app.py"), []byte("print(\"changed\")\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new_file.py"), []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(repo, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := worktree.ReadDiff(context.Background(), executor.NewLocal(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"print(\"changed\")", "b/new_file.py", "+x = 1"} {
		if !strings.Contains(patch, want) {
			t.Fatalf("patch is missing %q:\n%s", want, patch)
		}
	}
	after, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("ReadDiff wrote the index")
	}
	if _, err := os.Stat(index + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("index.lock left behind: %v", err)
	}
}
