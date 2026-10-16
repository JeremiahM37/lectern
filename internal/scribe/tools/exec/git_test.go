package exec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func TestGitStatusAddCommitFlow(t *testing.T) {
	dir := initRepo(t)
	r, _ := newTestRegistry(t, Options{})
	env := newTestEnv(dir, nil)

	writeFile(t, filepath.Join(dir, "a.txt"), "hello\n")
	st := runTool(t, r, "git", env, map[string]any{"action": "status"})
	if st.IsError || !strings.Contains(st.Output, "?? a.txt") {
		t.Fatalf("status should list the untracked file: %+v", st)
	}

	noFiles := runTool(t, r, "git", env, map[string]any{"action": "add"})
	if !noFiles.IsError {
		t.Fatalf("add with no files must be refused")
	}
	add := runTool(t, r, "git", env, map[string]any{"action": "add", "files": []string{"a.txt"}})
	if add.IsError {
		t.Fatalf("add: %+v", add)
	}

	commit := runTool(t, r, "git", env, map[string]any{"action": "commit", "message": "add a"})
	if commit.IsError || !strings.Contains(commit.Output, "Committed.") {
		t.Fatalf("commit: %+v", commit)
	}
	// The message is written exactly as given: no trailer, no decoration.
	if msg := strings.TrimSpace(gitIn(t, dir, "log", "-1", "--format=%B")); msg != "add a" {
		t.Fatalf("commit message = %q, want exactly %q", msg, "add a")
	}

	clean := runTool(t, r, "git", env, map[string]any{"action": "log", "count": 5})
	if !strings.Contains(clean.Output, "add a") {
		t.Fatalf("log should show the commit: %q", clean.Output)
	}

	branch, dirty, ahead, behind, err := Status(dir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if branch != "main" || dirty || ahead != 0 || behind != 0 {
		t.Fatalf("Status = (%q, dirty=%v, %d, %d), want (main, clean, 0, 0)", branch, dirty, ahead, behind)
	}
}

func TestGitCommitRefusals(t *testing.T) {
	dir := initRepo(t)
	r, _ := newTestRegistry(t, Options{})
	env := newTestEnv(dir, nil)

	if res := runTool(t, r, "git", env, map[string]any{"action": "commit"}); !res.IsError {
		t.Error("commit without a message must be refused")
	}
	res := runTool(t, r, "git", env, map[string]any{"action": "commit", "message": "x", "args": []string{"--no-verify"}})
	if !res.IsError || !strings.Contains(res.Output, "refused") {
		t.Errorf("--no-verify in args must be refused without no_verify: %+v", res)
	}
}

func TestGitCommitNoVerifyIsExplicit(t *testing.T) {
	skipOnWindows(t)
	dir := initRepo(t)
	// A pre-commit hook that always fails: the commit must fail without
	// no_verify and succeed with it.
	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	writeFile(t, hook, "#!/bin/sh\necho hook says no >&2\nexit 1\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "b.txt"), "b\n")
	gitIn(t, dir, "add", "b.txt")

	r, _ := newTestRegistry(t, Options{})
	env := newTestEnv(dir, nil)

	blocked := runTool(t, r, "git", env, map[string]any{"action": "commit", "message": "blocked"})
	if !blocked.IsError || !strings.Contains(blocked.Output, "hook says no") {
		t.Fatalf("hook should block the commit: %+v", blocked)
	}

	bypass := runTool(t, r, "git", env, map[string]any{"action": "commit", "message": "bypass", "no_verify": true})
	if bypass.IsError {
		t.Fatalf("no_verify commit failed: %+v", bypass)
	}
}

func TestGitRefusesSecretFiles(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, ".env"), "TOKEN=x\n")
	r, _ := newTestRegistry(t, Options{})
	res := runTool(t, r, "git", newTestEnv(dir, nil), map[string]any{"action": "add", "files": []string{".env"}})
	if !res.IsError || !strings.Contains(res.Output, "secret") {
		t.Fatalf("staging .env must be refused: %+v", res)
	}
	if staged := gitIn(t, dir, "diff", "--cached", "--name-only"); strings.TrimSpace(staged) != "" {
		t.Fatalf("nothing should be staged, got %q", staged)
	}
}

func TestGitChangedFilesAndStatusInSubdir(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, "root.txt"), "r\n")
	writeFile(t, filepath.Join(dir, "sub", "inner.txt"), "i\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "base")

	writeFile(t, filepath.Join(dir, "root.txt"), "changed\n")
	writeFile(t, filepath.Join(dir, "sub", "new.txt"), "new\n")

	all, err := ChangedFiles(dir)
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	sort.Strings(all)
	if strings.Join(all, ",") != "root.txt,sub/new.txt" {
		t.Fatalf("ChangedFiles(repo) = %v", all)
	}

	sub, err := ChangedFiles(filepath.Join(dir, "sub"))
	if err != nil {
		t.Fatalf("ChangedFiles(sub): %v", err)
	}
	if strings.Join(sub, ",") != "new.txt" {
		t.Fatalf("ChangedFiles(sub) = %v, want paths relative to sub only", sub)
	}

	_, dirty, _, _, err := Status(dir)
	if err != nil || !dirty {
		t.Fatalf("Status should report dirty: dirty=%v err=%v", dirty, err)
	}
}

func TestStatusOutsideRepository(t *testing.T) {
	if _, _, _, _, err := Status(t.TempDir()); err == nil {
		t.Fatal("Status on a non-repository should error")
	}
	if _, err := ChangedFiles(t.TempDir()); err == nil {
		t.Fatal("ChangedFiles on a non-repository should error")
	}
}

func TestGitStashAndBranch(t *testing.T) {
	dir := initRepo(t)
	writeFile(t, filepath.Join(dir, "s.txt"), "one\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "base")
	r, _ := newTestRegistry(t, Options{})
	env := newTestEnv(dir, nil)

	writeFile(t, filepath.Join(dir, "s.txt"), "two\n")
	push := runTool(t, r, "git", env, map[string]any{"action": "stash", "sub_action": "push", "message": "wip"})
	if push.IsError {
		t.Fatalf("stash push: %+v", push)
	}
	list := runTool(t, r, "git", env, map[string]any{"action": "stash", "sub_action": "list"})
	if !strings.Contains(list.Output, "wip") {
		t.Fatalf("stash list: %q", list.Output)
	}

	create := runTool(t, r, "git", env, map[string]any{"action": "branch", "sub_action": "create", "name": "feature"})
	if create.IsError {
		t.Fatalf("branch create: %+v", create)
	}
	if out := gitIn(t, dir, "branch", "--list", "feature"); !strings.Contains(out, "feature") {
		t.Fatalf("branch not created: %q", out)
	}
	if bad := runTool(t, r, "git", env, map[string]any{"action": "branch", "sub_action": "switch"}); !bad.IsError {
		t.Fatal("branch switch without a name must be refused")
	}
}

func TestGitReadOnlyClassification(t *testing.T) {
	tool := &gitTool{}
	cases := map[string]bool{
		`{"action":"status"}`:                                true,
		`{"action":"log"}`:                                   true,
		`{"action":"diff","staged":true}`:                    true,
		`{"action":"branch"}`:                                true,
		`{"action":"branch","sub_action":"create"}`:          false,
		`{"action":"commit","message":"x"}`:                  false,
		`{"action":"add","files":["a"]}`:                     false,
		`{"action":"stash","sub_action":"list"}`:             false,
		`{"action":"tag"}`:                                   true,
		`{"action":"tag","sub_action":"list"}`:               true,
		`{"action":"tag","sub_action":"create","name":"v1"}`: false,
		`{"action":"push"}`:                                  false,
		`{"action":"pull"}`:                                  false,
		`{"action":"reset","mode":"soft"}`:                   false,
		`{"action":"reset","mode":"hard"}`:                   false,
		`{"action":"restore","files":["a"]}`:                 false,
		`{"action":"unstage","files":["a"]}`:                 false,
		`{"action":"cherry_pick","ref":"abc"}`:               false,
		`{"action":"rebase","ref":"main"}`:                   false,
	}
	for raw, want := range cases {
		if got := tool.ReadOnlyCall(json.RawMessage(raw)); got != want {
			t.Errorf("ReadOnlyCall(%s) = %v, want %v", raw, got, want)
		}
	}
	if tool.ReadOnly() {
		t.Error("the git tool as a whole is not read-only")
	}
}

func TestGitToolRejectsUnknownAction(t *testing.T) {
	r, _ := newTestRegistry(t, Options{})
	res := runTool(t, r, "git", newTestEnv(t.TempDir(), nil), map[string]any{"action": "frobnicate"})
	if !res.IsError || !strings.Contains(res.Output, "unknown git action") {
		t.Fatalf("frobnicate is not an accepted action: %+v", res)
	}
}

// compile-time check that the git tool satisfies the Tool contract.
var _ tools.Tool = (*gitTool)(nil)
