package awareness

// Integration: real git, via the local executor, not the mock — the whole
// point of repo_key/rel_path is to compare equal across two worktrees of
// ONE repository, which only a real `git worktree add` can exercise.
// Modeled on internal/checks/real_test.go (same risk profile: temp
// directories and local subprocesses, no tmux, no shared host state), so it
// runs directly as well as inside the isolated runner.

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// twoWorktrees builds a real repo with a second worktree checked out from
// it, and writes the same-named file into both — the minimal fixture that
// distinguishes "same repository, different rel_path root" from "different
// repository" or "same absolute path".
func twoWorktrees(t *testing.T) (repoDir, wtDir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	base := t.TempDir()
	repoDir = filepath.Join(base, "repo")
	wtDir = filepath.Join(base, "wt-feature")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, base, "init", "-q", "-b", "main", repoDir)
	if err := os.MkdirAll(filepath.Join(repoDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "sub", "file.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-qm", "initial")
	runGit(t, repoDir, "worktree", "add", "-q", "-b", "feature", wtDir)
	return repoDir, wtDir
}

func TestResolveRepoKeyMatchesAcrossWorktrees(t *testing.T) {
	repoDir, wtDir := twoWorktrees(t)
	ex := executor.NewLocal()
	ctx := context.Background()

	key1, top1, ok1 := ResolveRepoKey(ctx, ex, 7, repoDir)
	if !ok1 {
		t.Fatalf("ResolveRepoKey(repoDir) not ok")
	}
	key2, top2, ok2 := ResolveRepoKey(ctx, ex, 7, wtDir)
	if !ok2 {
		t.Fatalf("ResolveRepoKey(wtDir) not ok")
	}
	if key1 != key2 {
		t.Fatalf("repo_key differs across worktrees of one repo: %q vs %q", key1, key2)
	}
	if top1 == top2 {
		t.Fatalf("toplevel should differ per worktree, both were %q", top1)
	}
	// A different target id must never collide, even at the same path.
	key3, _, ok3 := ResolveRepoKey(ctx, ex, 8, repoDir)
	if !ok3 || key3 == key1 {
		t.Fatalf("repo_key must embed target id: %q vs %q", key3, key1)
	}
	// Non-git directory resolves to ok=false, not a false repo_key.
	if _, _, ok := ResolveRepoKey(ctx, ex, 7, t.TempDir()); ok {
		t.Fatalf("expected ok=false for a non-git directory")
	}
}

func TestRelPathMatchesAcrossWorktrees(t *testing.T) {
	repoDir, wtDir := twoWorktrees(t)
	ex := executor.NewLocal()
	ctx := context.Background()
	_, top1, _ := ResolveRepoKey(ctx, ex, 1, repoDir)
	_, top2, _ := ResolveRepoKey(ctx, ex, 1, wtDir)

	rel1, ok1 := RelPath(top1, filepath.Join(repoDir, "sub", "file.txt"))
	rel2, ok2 := RelPath(top2, filepath.Join(wtDir, "sub", "file.txt"))
	if !ok1 || !ok2 {
		t.Fatalf("RelPath failed: ok1=%v ok2=%v", ok1, ok2)
	}
	if rel1 != rel2 || rel1 != "sub/file.txt" {
		t.Fatalf("rel paths should match across worktrees: %q vs %q", rel1, rel2)
	}
}

func realTracker(t *testing.T) (*Tracker, *store.DB, *store.Target) {
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
	reg := executor.NewRegistry(false, 0)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(db, reg, log), db, target
}

// TestEndToEndPeersAcrossWorktrees is the full real-git path: two sessions,
// each in its own worktree of the same repository, resolve to the same
// repo_key, and an edit recorded against one shows up as a peer to the
// other with a rel_path that matches even though the absolute paths differ.
func TestEndToEndPeersAcrossWorktrees(t *testing.T) {
	repoDir, wtDir := twoWorktrees(t)
	tr, db, target := realTracker(t)
	ctx := context.Background()

	sessA, err := db.InsertSession(&store.Session{TargetID: target.ID, Name: "session-a", Agent: "claude",
		Workdir: repoDir, TmuxSession: "lec-a"})
	if err != nil {
		t.Fatal(err)
	}
	sessB, err := db.InsertSession(&store.Session{TargetID: target.ID, Name: "session-b", Agent: "codex",
		Workdir: wtDir, TmuxSession: "lec-b"})
	if err != nil {
		t.Fatal(err)
	}
	// Resolve synchronously in-package rather than racing the async
	// goroutine EnsureRepoKeyAsync would start.
	tr.resolveSession(ctx, sessA.ID, target.ID, sessA.Workdir, sessA.ProjectID)
	tr.resolveSession(ctx, sessB.ID, target.ID, sessB.Workdir, sessB.ProjectID)
	sessA, _ = db.Session(sessA.ID)
	sessB, _ = db.Session(sessB.ID)
	if sessA.RepoKey == "" || sessA.RepoKey == RepoKeyNone {
		t.Fatalf("session A repo_key not resolved: %q", sessA.RepoKey)
	}
	if sessA.RepoKey != sessB.RepoKey {
		t.Fatalf("sessions in two worktrees of one repo should share repo_key: %q vs %q", sessA.RepoKey, sessB.RepoKey)
	}

	// Session A edits sub/file.txt in its own worktree.
	tr.RecordEdit(sessA, filepath.Join(repoDir, "sub", "file.txt"))

	peers, err := tr.Peers(sessB)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0].SessionID != sessA.ID {
		t.Fatalf("expected session A as B's one peer, got %+v", peers)
	}
	if len(peers[0].Files) != 1 || peers[0].Files[0].RelPath != "sub/file.txt" {
		t.Fatalf("expected sub/file.txt in peer's files, got %+v", peers[0].Files)
	}

	// PreToolUse-shaped warning: B is about to edit the SAME rel_path in ITS
	// OWN worktree — a merge-conflict-risk wording, not an overwrite one,
	// since the two worktrees are different directories.
	text, ok := tr.EditWarning(sessB, "Edit", map[string]any{"file_path": filepath.Join(wtDir, "sub", "file.txt")})
	if !ok {
		t.Fatalf("expected an edit warning")
	}
	if !strings.Contains(text, "separate worktree") {
		t.Fatalf("expected separate-worktree wording, got: %s", text)
	}

	// A third session sharing A's exact workdir gets the stronger wording.
	sessC, err := db.InsertSession(&store.Session{TargetID: target.ID, Name: "session-c", Agent: "claude",
		Workdir: repoDir, TmuxSession: "lec-c"})
	if err != nil {
		t.Fatal(err)
	}
	tr.resolveSession(ctx, sessC.ID, target.ID, sessC.Workdir, sessC.ProjectID)
	sessC, _ = db.Session(sessC.ID)
	text, ok = tr.EditWarning(sessC, "Edit", map[string]any{"file_path": filepath.Join(repoDir, "sub", "file.txt")})
	if !ok {
		t.Fatalf("expected an edit warning for same-workdir session")
	}
	if !strings.Contains(text, "SAME working directory") {
		t.Fatalf("expected same-workdir wording, got: %s", text)
	}
}
