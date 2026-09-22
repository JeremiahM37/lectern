package worktree

import (
	"bytes"
	"context"
	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/testutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFailedCheckoutHookRetainsRecoverableOwnedWorktree(t *testing.T) {
	for _, bin := range []string{"git", "python3", "tmux"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " unavailable")
		}
	}
	socketRoot, err := os.MkdirTemp("", "lec-hook-")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_TMPDIR", socketRoot)
	t.Setenv("TMUX", "")
	t.Cleanup(func() { testutil.CleanupTmux(t, socketRoot); os.RemoveAll(socketRoot) })
	repo := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(repo, "init", "-q")
	os.WriteFile(filepath.Join(repo, "source"), []byte("keep source\n"), 0600)
	git(repo, "add", ".")
	git(repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "base")
	head := git(repo, "rev-parse", "HEAD")
	hook := filepath.Join(repo, ".git", "hooks", "post-checkout")
	os.WriteFile(hook, []byte("#!/bin/sh\nprintf 'setup artifact' > setup-artifact\necho 'setup failure sentinel' >&2\nexit 1\n"), 0700)
	plan := PlanInteractive(repo, 17, InteractiveOptions{Branch: "failed-setup"})
	ex := executor.NewLocal()
	if err := RunInteractive(context.Background(), ex, "create", plan); err == nil || !strings.Contains(err.Error(), "setup failure sentinel") {
		t.Fatalf("hook failure was not reported: %v", err)
	}
	artifact := filepath.Join(plan.Path, "setup-artifact")
	if plan.State != "failed" || plan.Commit != head || !strings.Contains(plan.Error, "setup failure sentinel") {
		t.Fatal("failed allocation lost its revision or error details")
	}
	if body, _ := os.ReadFile(artifact); string(body) != "setup artifact" {
		t.Fatal("failed setup files were lost")
	}
	if err := RunInteractive(context.Background(), ex, "remove", plan); err == nil {
		t.Fatal("setup output was deleted without review")
	}
	if err := os.Remove(artifact); err != nil {
		t.Fatal(err)
	}
	if err := RunInteractive(context.Background(), ex, "remove", plan); err != nil {
		t.Fatalf("clean failed allocation is not recoverable: %v", err)
	}
	if git(repo, "rev-parse", "failed-setup") != head || git(repo, "status", "--porcelain") != "" {
		t.Fatal("cleanup changed source or deleted branch")
	}
}

func TestInteractiveIsolationOwnershipAndSafeRemoval(t *testing.T) {
	testutil.RequireIsolated(t)
	for _, bin := range []string{"git", "python3", "tmux"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " unavailable")
		}
	}
	socketRoot, err := os.MkdirTemp("", "adkw-")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_TMPDIR", socketRoot)
	t.Setenv("TMUX", "")
	tmuxSocket := filepath.Join(socketRoot, "tmux.sock")
	t.Setenv("ADK_TEST_TMUX_SOCKET", tmuxSocket)
	t.Cleanup(func() { testutil.CleanupTmuxSocket(t, tmuxSocket); os.RemoveAll(socketRoot) })
	root := t.TempDir()
	repo := filepath.Join(root, "source with spaces")
	os.Mkdir(repo, 0700)
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(repo, "init", "-q")
	os.WriteFile(filepath.Join(repo, "file"), []byte("original\n"), 0600)
	os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("cache/\n"), 0600)
	git(repo, "add", ".")
	git(repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "base")
	original := git(repo, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(repo, "file"), []byte("uncommitted source\n"), 0600)
	plan := PlanInteractive(repo, 1, InteractiveOptions{Branch: "feature/isolated"})
	ex := executor.NewLocal()
	ctx := context.Background()
	if err := RunInteractive(ctx, ex, "create", plan); err != nil {
		t.Fatal(err)
	}
	if plan.Commit != original {
		t.Fatal("incorrect base")
	}
	indexPath := git(plan.Path, "rev-parse", "--path-format=absolute", "--git-path", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunInteractive(ctx, ex, "check-remove", plan); err != nil {
		t.Fatal(err)
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil || !bytes.Equal(indexBefore, indexAfter) || plan.State != "ready" {
		t.Fatal("removal preflight mutated the index or allocation state")
	}
	if !strings.Contains(git(repo, "worktree", "list", "--porcelain"), plan.Path) {
		t.Fatal("preflight removed the worktree")
	}
	body, _ := os.ReadFile(filepath.Join(plan.Path, "file"))
	if string(body) != "original\n" {
		t.Fatal("source edits copied")
	}
	if got := git(repo, "status", "--porcelain"); got != "M file" {
		t.Fatal("source index changed", got)
	}
	collision := PlanInteractive(repo, 2, InteractiveOptions{Branch: plan.Branch})
	if err := RunInteractive(ctx, ex, "create", collision); err == nil {
		t.Fatal("branch collision accepted")
	}
	if out, err := exec.Command("tmux", "-S", tmuxSocket, "new-session", "-d", "-s", "external-proof", "-c", plan.Path, "--", "sleep", "600").CombinedOutput(); err != nil {
		t.Fatalf("tmux: %s", out)
	}
	if err := RunInteractive(ctx, ex, "remove", plan); err == nil {
		t.Fatal("worktree with external terminal removed")
	}
	exec.Command("tmux", "-S", tmuxSocket, "kill-session", "-t", "=external-proof").Run()
	changed := *plan
	changed.Token = "someone-else"
	if err := RunInteractive(ctx, ex, "remove", &changed); err == nil {
		t.Fatal("foreign ownership accepted")
	}
	os.WriteFile(filepath.Join(plan.Path, "new"), []byte("keep me"), 0600)
	if err := RunInteractive(ctx, ex, "remove", plan); err == nil {
		t.Fatal("untracked file removed")
	}
	os.Remove(filepath.Join(plan.Path, "new"))
	os.Mkdir(filepath.Join(plan.Path, "cache"), 0700)
	os.WriteFile(filepath.Join(plan.Path, "cache", "important"), []byte("ignored but valuable"), 0600)
	if err := RunInteractive(ctx, ex, "remove", plan); err == nil {
		t.Fatal("ignored files removed")
	}
	os.RemoveAll(filepath.Join(plan.Path, "cache"))
	os.WriteFile(filepath.Join(plan.Path, "file"), []byte("branch change\n"), 0600)
	git(plan.Path, "add", ".")
	git(plan.Path, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "work")
	head := git(plan.Path, "rev-parse", "HEAD")
	if err := RunInteractive(ctx, ex, "remove", plan); err != nil {
		t.Fatal(err)
	}
	if plan.State != "removed" {
		t.Fatal(plan.State)
	}
	if _, err := os.Stat(plan.Path); !os.IsNotExist(err) {
		t.Fatal("directory still exists")
	}
	if git(repo, "rev-parse", plan.Branch) != head {
		t.Fatal("committed branch lost")
	}
	body, _ = os.ReadFile(filepath.Join(repo, "file"))
	if string(body) != "uncommitted source\n" {
		t.Fatal("source modified")
	}
}
