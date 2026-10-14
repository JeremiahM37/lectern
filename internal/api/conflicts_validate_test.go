package api

// Real-git coverage for the validation core: two worktrees of one repository,
// the real local executor, and the real check-command path. It calls
// runValidation directly, so it needs git and nothing else. The API harness is
// deliberately not used here because it demands the reviewed isolated runner
// for any non-mock target, and this path needs no tmux or sandbox.

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/checks"
	"github.com/JeremiahM37/lectern/v2/internal/conflicts"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %s: %s: %v", dir, strings.Join(args, " "), out, err)
	}
	return string(out)
}

// conflictRepo is a repository with one base commit and two worktrees off it.
// Side A changes compute's body in tracked code. Side B adds a new file whose
// code calls compute. Each passes alone; together they are the semantic case
// the conflicts package predicts.
func conflictRepo(t *testing.T) (repo, wtA, wtB, base string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	repo = filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "app.py"), []byte("def compute():\n    return 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "-c", "user.name=Test", "-c", "user.email=t@example.invalid", "commit", "-qm", "base")
	base = strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))

	wtA = filepath.Join(root, "a")
	wtB = filepath.Join(root, "b")
	gitRun(t, repo, "worktree", "add", "-q", "-b", "feat-a", wtA, "main")
	gitRun(t, repo, "worktree", "add", "-q", "-b", "feat-b", wtB, "main")
	if err := os.WriteFile(filepath.Join(wtA, "app.py"), []byte("def compute():\n    return 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtB, "b.py"), []byte("from app import compute\n\nvalue = compute()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return repo, wtA, wtB, base
}

// sideFor reads a worktree's live diff the way resolveConflictSide does, with
// the same local executor and base the scanner would use.
func sideFor(t *testing.T, ex executor.Executor, name, dir, base string) *conflictSide {
	t.Helper()
	d, err := liveDiffRepo(t.Context(), ex, dir, base)
	if err != nil {
		t.Fatal(err)
	}
	return &conflictSide{
		ref:      conflicts.Ref{Kind: "session", ID: 1, Title: name},
		ex:       ex,
		targetID: 1,
		dir:      d.Dir,
		base:     d.BaseRef,
		patch:    concatPatches([]repoDiff{d}),
	}
}

func newValidationServer() *Server {
	return &Server{Checks: checks.New(nil, nil, nil, nil, 0, slog.Default())}
}

// The two sides must be found on the same merge-base. That is also what makes
// the scratch worktree a faithful replay of both branches.
func TestRunValidationPassesAndCleansUp(t *testing.T) {
	_, wtA, wtB, base := conflictRepo(t)
	ex := executor.NewLocal()
	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)

	a := sideFor(t, ex, "a", wtA, base)
	b := sideFor(t, ex, "b", wtB, base)
	a.proj = &store.Project{Name: "p", VerifyCmd: `test -f b.py && grep -q 'return 1' app.py`}

	res, err := newValidationServer().runValidation(t.Context(), a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied || res.TextualConflict {
		t.Fatalf("result = %+v, want applied with no textual conflict", res)
	}
	if res.CheckStatus != "passed" {
		t.Fatalf("check_status = %s, output %q", res.CheckStatus, res.OutputTail)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("scratch directory left behind: %v", entries)
	}
	if list := gitRun(t, wtA, "worktree", "list", "--porcelain"); strings.Contains(list, "lectern-conflict") {
		t.Fatalf("scratch worktree still registered:\n%s", list)
	}
	// The real worktrees must be untouched by the validation.
	if got, _ := os.ReadFile(filepath.Join(wtA, "app.py")); !strings.Contains(string(got), "return 1") {
		t.Fatalf("side A's worktree was modified: %q", got)
	}
}

// A failing check is a result, not an error: the response carries the failure
// and the output tail, and the scratch state is still removed.
func TestRunValidationReportsFailedCheck(t *testing.T) {
	_, wtA, wtB, base := conflictRepo(t)
	ex := executor.NewLocal()
	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)

	a := sideFor(t, ex, "a", wtA, base)
	b := sideFor(t, ex, "b", wtB, base)
	a.proj = &store.Project{Name: "p", VerifyCmd: `echo nothing-to-see; exit 3`}

	res, err := newValidationServer().runValidation(t.Context(), a, b)
	if err != nil {
		t.Fatal(err)
	}
	if res.CheckStatus != "failed" || !strings.Contains(res.OutputTail, "nothing-to-see") {
		t.Fatalf("result = %+v", res)
	}
	if entries, _ := os.ReadDir(scratch); len(entries) != 0 {
		t.Fatalf("scratch directory left behind after a failed check: %v", entries)
	}
}

// Two edits to the same lines cannot both be replayed. That must come back as
// a textual conflict and must not run the check at all.
func TestRunValidationTextualConflictSkipsCheck(t *testing.T) {
	_, wtA, wtB, base := conflictRepo(t)
	ex := executor.NewLocal()
	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)
	if err := os.WriteFile(filepath.Join(wtB, "app.py"), []byte("def compute():\n    return 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	a := sideFor(t, ex, "a", wtA, base)
	b := sideFor(t, ex, "b", wtB, base)
	a.proj = &store.Project{Name: "p", VerifyCmd: `touch ran-check`}

	res, err := newValidationServer().runValidation(t.Context(), a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !res.TextualConflict || res.Applied {
		t.Fatalf("result = %+v, want a textual conflict", res)
	}
	if res.CheckStatus != "skipped" {
		t.Fatalf("check must be skipped after a textual conflict, got %s", res.CheckStatus)
	}
	if entries, _ := os.ReadDir(scratch); len(entries) != 0 {
		t.Fatalf("scratch directory left behind after a conflict: %v", entries)
	}
}

func TestClipTailKeepsLastLines(t *testing.T) {
	var lines []string
	for i := 0; i < validateOutputLines+50; i++ {
		lines = append(lines, "line")
	}
	lines[len(lines)-1] = "LAST"
	got := clipTail(strings.Join(lines, "\n"))
	if !strings.HasSuffix(got, "LAST") {
		t.Fatalf("tail lost the last line")
	}
	if n := strings.Count(got, "\n") + 1; n > validateOutputLines {
		t.Fatalf("kept %d lines, want at most %d", n, validateOutputLines)
	}
	big := strings.Repeat("x", validateOutputBytes*2) + "END"
	if got := clipTail(big); len(got) > validateOutputBytes || !strings.HasSuffix(got, "END") {
		t.Fatalf("byte bound broken: len %d", len(got))
	}
}
