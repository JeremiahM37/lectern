package scheduler

import (
	"testing"

	"github.com/JeremiahM37/lectern/internal/config"
	"github.com/JeremiahM37/lectern/internal/store"
)

func TestTaskWorktreeNamespacesLocalInstances(t *testing.T) {
	ctx := &runCtx{
		Task:    &store.Task{ID: 1},
		Project: &store.Project{RepoPath: "/srv/app"},
		Target:  &store.Target{Kind: "local"},
	}
	att := &store.Attempt{N: 1}
	first := &Scheduler{Cfg: &config.Config{WorktreeNamespace: "local-a"}}
	second := &Scheduler{Cfg: &config.Config{WorktreeNamespace: "local-b"}}
	path1, branch1 := first.taskWorktree(ctx, att)
	path2, branch2 := second.taskWorktree(ctx, att)
	if path1 == path2 || branch1 == branch2 {
		t.Fatalf("local instances collided: %q/%q and %q/%q", path1, branch1, path2, branch2)
	}
	if path1 != "/srv/.lectern-worktrees/local-a/task1-a1" || branch1 != "lec/local-a/task1-a1" {
		t.Fatalf("unexpected local allocation: %q %q", path1, branch1)
	}
}

func TestTaskWorktreeHostedLayoutUnchanged(t *testing.T) {
	ctx := &runCtx{
		Task:    &store.Task{ID: 1},
		Project: &store.Project{RepoPath: "/srv/app"},
		Target:  &store.Target{Kind: "ssh"},
	}
	path, branch := (&Scheduler{Cfg: &config.Config{WorktreeNamespace: "local-a"}}).taskWorktree(ctx, &store.Attempt{N: 1})
	if path != "/srv/.lectern-worktrees/task1-a1" || branch != "lec/task1-a1" {
		t.Fatalf("hosted allocation changed: %q %q", path, branch)
	}
}
