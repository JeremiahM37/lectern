package worktree

import (
	"context"
	"encoding/json"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/testutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func extensionFixture(t *testing.T) (*Interactive, string) {
	t.Helper()
	repo, _ := recoveryRepo(t)
	extra, _ := recoveryRepo(t)
	plan, err := PlanMultiWorkspace([]RepositorySource{{Name: "Original", Repo: repo, SetupCommand: "echo once >> prepared"}}, 90, InteractiveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := RunInteractive(context.Background(), executor.NewLocal(), "create", plan); err != nil {
		t.Fatal(err)
	}
	return plan, extra
}
func copyExtensionPlan(t *testing.T, p *Interactive) *Interactive {
	t.Helper()
	data, _ := json.Marshal(p)
	var clone Interactive
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return &clone
}

func TestExtendWorkspacePreservesDirtyFilesAndRejectsStaleCleanup(t *testing.T) {
	plan, extra := extensionFixture(t)
	old := copyExtensionPlan(t, plan)
	original := plan.Repositories[0].Worktree.Path
	if err := os.WriteFile(filepath.Join(original, "base"), []byte("user changes"), 0600); err != nil {
		t.Fatal(err)
	}
	next, err := PlanWorkspaceExtension(plan, RepositorySource{Name: "Original", Repo: extra, SetupCommand: "echo extra > prepared"}, 90)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Repositories) != 1 || next.Repositories[1].Worktree.Path != filepath.Join(plan.Path, "original-2") {
		t.Fatal("planning mutated original or collided paths")
	}
	ex := executor.NewLocal()
	if err := RunInteractive(context.Background(), ex, "extend", next); err != nil {
		t.Fatal(err)
	}
	if next.State != "ready" || next.Repositories[1].Worktree.SetupState != "complete" {
		t.Fatal("extension not ready")
	}
	if body, _ := os.ReadFile(filepath.Join(original, "base")); string(body) != "user changes" {
		t.Fatal("existing edits changed")
	}
	if body, _ := os.ReadFile(filepath.Join(original, "prepared")); string(body) != "once\n" {
		t.Fatal("existing setup reran")
	}
	if err := RunInteractive(context.Background(), ex, "remove", copyExtensionPlan(t, old)); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("stale cleanup accepted: %v", err)
	}
	if err := RunInteractive(context.Background(), ex, "status", old); err != nil {
		t.Fatal(err)
	}
	if len(old.Repositories) != 2 || old.ControlToken != next.ControlToken {
		t.Fatal("old record could not discover extension")
	}
	old.RedactOwnership()
	if old.ControlToken != "" {
		t.Fatal("operation cancellation token exposed")
	}
}

func TestExtensionPreflightRejectsAliasWithoutMutatingRoot(t *testing.T) {
	plan, _ := extensionFixture(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(plan.Repositories[0].Worktree.Repo, alias); err != nil {
		t.Fatal(err)
	}
	next, err := PlanWorkspaceExtension(plan, RepositorySource{Name: "Alias", Repo: alias}, 90)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(plan.Path, ".lectern-state.json"))
	if err := RunInteractive(context.Background(), executor.NewLocal(), "extend", next); err == nil || !strings.Contains(err.Error(), "same Git repository") {
		t.Fatalf("alias accepted: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(plan.Path, ".lectern-state.json"))
	if string(before) != string(after) {
		t.Fatal("failed preflight changed root receipt")
	}
	if _, err := os.Stat(next.Repositories[1].Worktree.Path); !os.IsNotExist(err) {
		t.Fatal("failed preflight allocated files")
	}
}

func TestExtensionFailureRemainsDiscoverableFromOldRecord(t *testing.T) {
	plan, extra := extensionFixture(t)
	old := copyExtensionPlan(t, plan)
	next, err := PlanWorkspaceExtension(plan, RepositorySource{Name: "Fail", Repo: extra, SetupCommand: "echo keep > prepared; exit 19"}, 90)
	if err != nil {
		t.Fatal(err)
	}
	ex := executor.NewLocal()
	if err := RunInteractive(context.Background(), ex, "extend", next); err == nil || !strings.Contains(err.Error(), "status 19") {
		t.Fatalf("failure lost: %v", err)
	}
	if err := RunInteractive(context.Background(), ex, "status", old); err != nil {
		t.Fatal(err)
	}
	if len(old.Repositories) != 2 || old.State != "failed" {
		t.Fatal("partial extension stranded behind old identity")
	}
	if body, _ := os.ReadFile(filepath.Join(old.Repositories[1].Worktree.Path, "prepared")); string(body) != "keep\n" {
		t.Fatal("failed files lost")
	}
	if old.Repositories[0].Worktree.State != "ready" {
		t.Fatal("original checkout state changed")
	}
}

func TestExtensionCancellationDoesNotStopExistingTerminal(t *testing.T) {
	testutil.RequireIsolated(t)
	socketRoot, err := os.MkdirTemp("", "lec-ext-")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_TMPDIR", socketRoot)
	t.Setenv("TMUX", "")
	tmuxSocket := filepath.Join(socketRoot, "tmux.sock")
	t.Setenv("ADK_TEST_TMUX_SOCKET", tmuxSocket)
	t.Cleanup(func() { testutil.CleanupTmuxSocket(t, tmuxSocket); os.RemoveAll(socketRoot) })
	plan, extra := extensionFixture(t)
	ex := executor.NewLocal()
	if output, err := exec.Command("tmux", "-S", tmuxSocket, "new-session", "-d", "-s", "extension-existing", "-c", plan.Path, "--", "sleep", "600").CombinedOutput(); err != nil {
		t.Fatalf("tmux: %v %s", err, output)
	}
	// Cancellation from an earlier operation must not poison a new extension.
	if err := RunInteractive(context.Background(), ex, "cancel", copyExtensionPlan(t, plan)); err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(t.TempDir(), "started")
	escaped := filepath.Join(t.TempDir(), "escaped")
	next, err := PlanWorkspaceExtension(plan, RepositorySource{Name: "Slow", Repo: extra, SetupCommand: "echo keep > prepared; touch " + executor.ShellQuote(started) + "; sleep 3; touch " + executor.ShellQuote(escaped)}, 90)
	if err != nil {
		t.Fatal(err)
	}
	cancellation := copyExtensionPlan(t, next)
	done := make(chan error, 1)
	go func() { done <- RunInteractive(context.Background(), ex, "extend", next) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new operation inherited old cancellation")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := RunInteractive(context.Background(), ex, "cancel", cancellation); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Fatalf("cancel failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("extension did not stop")
	}
	time.Sleep(3200 * time.Millisecond)
	if _, err := os.Stat(escaped); !os.IsNotExist(err) {
		t.Fatal("cancelled setup continued")
	}
	if err := exec.Command("tmux", "-S", tmuxSocket, "has-session", "-t", "=extension-existing").Run(); err != nil {
		t.Fatal("cancellation stopped existing terminal")
	}
	if body, _ := os.ReadFile(filepath.Join(plan.Repositories[0].Worktree.Path, "prepared")); string(body) != "once\n" {
		t.Fatal("existing repository affected")
	}
}
