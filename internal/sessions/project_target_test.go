package sessions

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/internal/bus"
	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/store"
)

func TestLaunchRejectsProjectOnDifferentTargetBeforeUsingWorkdir(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	projectTarget, err := db.InsertTarget(&store.Target{Name: "project target", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	launchTarget, err := db.InsertTarget(&store.Target{Name: "launch target", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := db.InsertProject(&store.Project{
		Name: "project", TargetID: projectTarget.ID, RepoPath: "/project/repository",
	})
	if err != nil {
		t.Fatal(err)
	}
	m := New(db, executor.NewRegistry(true, 0), bus.New(), Launcher{}, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Specs = func() []Spec { return Builtins() }
	_, err = m.Launch(context.Background(), LaunchOpts{
		ProjectID: &project.ID,
		TargetID:  launchTarget.ID,
		Agent:     "claude",
		// An internal worktree/fork caller may have a deliberate override. The
		// project/target relation is still checked before that path is used.
		Workdir: "/deliberate/worktree/override",
	})
	if err == nil || !strings.Contains(err.Error(), "belongs to target") {
		t.Fatalf("mismatched project launch error = %v", err)
	}
	if sessions, err := db.Sessions(true); err != nil {
		t.Fatal(err)
	} else if len(sessions) != 0 {
		t.Fatalf("mismatch created a session: %v", sessions)
	}
}
