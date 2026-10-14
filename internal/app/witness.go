package app

import (
	"context"
	"fmt"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/checks"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/witness"
)

// approvalWitness returns the broker's Witness func. It resolves the worktree
// an approval acts on the same way the checks runner does: a session's check
// directory on its target, or a task attempt's own worktree on the target its
// project runs on. A grouped session is read at its check directory, one of
// its repositories, so drift in the others is not seen by this check.
func approvalWitness(db *store.DB, reg *executor.Registry) func(a *store.Approval) (*witness.Witness, error) {
	return func(a *store.Approval) (*witness.Witness, error) {
		ex, dir, err := approvalWorkdir(db, reg, a)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		w, err := witness.Snapshot(ctx, ex, dir)
		if err != nil {
			return nil, err
		}
		return &w, nil
	}
}

func approvalWorkdir(db *store.DB, reg *executor.Registry, a *store.Approval) (executor.Executor, string, error) {
	if a.SessionID != 0 {
		sess, err := db.Session(a.SessionID)
		if err != nil {
			return nil, "", err
		}
		target, err := db.Target(sess.TargetID)
		if err != nil {
			return nil, "", err
		}
		ex, err := reg.For(target)
		if err != nil {
			return nil, "", err
		}
		dir, err := checks.SessionWorkdir(sess)
		return ex, dir, err
	}
	if a.AttemptID != 0 {
		att, err := db.Attempt(a.AttemptID)
		if err != nil {
			return nil, "", err
		}
		if att.WorktreePath == "" {
			return nil, "", fmt.Errorf("attempt %d has no worktree", att.ID)
		}
		task, err := db.Task(att.TaskID)
		if err != nil {
			return nil, "", err
		}
		project, err := db.Project(task.ProjectID)
		if err != nil {
			return nil, "", err
		}
		target, err := db.Target(project.TargetID)
		if err != nil {
			return nil, "", err
		}
		ex, err := reg.For(target)
		if err != nil {
			return nil, "", err
		}
		return ex, att.WorktreePath, nil
	}
	return nil, "", fmt.Errorf("approval %d belongs to neither a session nor an attempt", a.ID)
}
