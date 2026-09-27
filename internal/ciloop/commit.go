package ciloop

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/worktree"
)

// commitFix is Lectern's half of a task's CI fix. The fix request went to the
// task as a headless follow-up attempt, which usually cannot run git, so once
// that attempt has finished Lectern commits what it left in the worktree and
// pushes it to the PR branch, through the same code as the Commit → Push
// button. It does this once per attempt. It returns true when it acted and
// recorded the outcome, false when there is nothing to do yet.
func (w *Watcher) commitFix(ctx context.Context, ex executor.Executor, cw *store.CIWatch, headSHA string) bool {
	if w.liveSession(cw) != nil {
		return false // a live agent can run git itself, or its user can
	}
	att := w.fixAttempt(cw)
	if att == nil || att.ID == cw.PushedAttemptID || att.Status == "queued" || att.Status == "running" {
		return false
	}
	// someone has started more work on the task since; leave the worktree to it
	if n, _ := w.DB.Count("attempts", "task_id=? AND status IN ('queued','running')", *cw.TaskID); n > 0 {
		return false
	}
	// Record the attempt before touching git, so a failure below is reported
	// once and never retried into a second commit.
	done := map[string]any{"pushed_attempt_id": att.ID}
	if err := w.DB.Update("ci_watches", cw.ID, done); err != nil {
		w.Log.Warn("ci loop: recording the fix attempt failed", "watch", cw.ID, "err", err)
		return false
	}
	if att.Status != "done" {
		w.finish(cw, StateError, "the fix attempt did not finish, so Lectern did not commit or push it", done)
		return true
	}
	dir := att.WorktreePath
	if dir == "" {
		w.finish(cw, StateError, "the fix attempt's worktree was cleaned up, so there is nothing to push", done)
		return true
	}
	branch := cw.Branch
	if branch == "" {
		branch = att.Branch
	}
	status, err := ex.Run(ctx, "git status --porcelain", executor.RunOpts{Cwd: dir, Timeout: 30})
	if err == nil && !status.OK() {
		w.finish(cw, StateError, "Lectern could not read the fix attempt's worktree: "+
			clip(strings.TrimSpace(status.Stdout+status.Stderr), 300), done)
		return true
	}
	if err != nil {
		w.retryFix(cw, err)
		return true
	}
	var push map[string]any
	if strings.TrimSpace(status.Stdout) != "" {
		steps, err := worktree.CommitPushPR(ctx, ex, dir, branch, commitMessage(cw), true, false, "", "")
		var se *worktree.StepError
		switch {
		case errors.As(err, &se):
			w.finish(cw, StateError, "Lectern could not commit the fix: "+clip(se.Msg, 300), done)
			return true
		case err != nil:
			w.retryFix(cw, err)
			return true
		}
		push = steps[len(steps)-1]
	} else {
		// Nothing uncommitted: the agent may have committed without pushing.
		head, err := ex.Run(ctx, "git rev-parse HEAD", executor.RunOpts{Cwd: dir, Timeout: 30})
		if err != nil {
			w.retryFix(cw, err)
			return true
		}
		if !head.OK() || strings.TrimSpace(head.Stdout) == headSHA {
			w.finish(cw, StateError, "the fix attempt finished without changing anything", done)
			return true
		}
		if push, err = worktree.Push(ctx, ex, dir, branch); err != nil {
			w.retryFix(cw, err)
			return true
		}
	}
	if rc, _ := push["rc"].(int); push["step"] != "push" || rc != 0 {
		out, _ := push["output"].(string)
		w.finish(cw, StateError, "Lectern could not push the fix: "+clip(strings.TrimSpace(out), 300), done)
		return true
	}
	w.Log.Info("ci loop: committed and pushed a task's fix", "watch", cw.ID, "task", *cw.TaskID,
		"attempt", att.ID, "branch", branch)
	// the new commit shows up on the PR within seconds; look again soon
	w.schedule(cw, StateFailing, true, done)
	return true
}

// retryFix is an executor failure (the target unreachable, say) before
// anything was committed: forget the attempt so the next poll tries again.
func (w *Watcher) retryFix(cw *store.CIWatch, err error) {
	w.DB.Update("ci_watches", cw.ID, map[string]any{"pushed_attempt_id": cw.PushedAttemptID})
	w.transient(cw, err)
}

// fixAttempt is the attempt the latest fix request was handed to, or nil if
// it has not been handed to one yet.
func (w *Watcher) fixAttempt(cw *store.CIWatch) *store.Attempt {
	var status string
	var id sql.NullInt64
	err := w.DB.QueryRow(`SELECT status, attempt_id FROM task_messages WHERE task_id=? AND request_id=?`,
		*cw.TaskID, requestID(cw.ID, cw.Attempts)).Scan(&status, &id)
	if err != nil || status != "delivered" || !id.Valid {
		return nil
	}
	att, err := w.DB.Attempt(id.Int64)
	if err != nil {
		return nil
	}
	return att
}

// commitMessage names the checks being fixed, e.g. "Fix CI: test, lint".
func commitMessage(cw *store.CIWatch) string {
	names := store.UnjStrings(cw.FailingJSON)
	if len(names) == 0 {
		return "Fix CI"
	}
	return clip("Fix CI: "+strings.Join(names, ", "), 200)
}

// AttemptFinished is the scheduler telling the loop that a task attempt
// ended. If it was a CI fix, the watch looks again now rather than after its
// backoff, so Lectern commits and pushes the fix straight away.
func (w *Watcher) AttemptFinished(att *store.Attempt) {
	if err := w.DB.WakeCIWatches("task_id", att.TaskID, w.now()); err != nil {
		w.Log.Warn("ci loop: waking watches failed", "task", att.TaskID, "err", err)
	}
}

// Pushed is Lectern having pushed an owner's branch (the Commit → Push
// button): its active watches look again now, with their backoff reset.
func (w *Watcher) Pushed(o Owner) {
	var err error
	switch {
	case o.TaskID != nil:
		err = w.DB.WakeCIWatches("task_id", *o.TaskID, w.now())
	case o.SessionID != nil:
		err = w.DB.WakeCIWatches("session_id", *o.SessionID, w.now())
	}
	if err != nil {
		w.Log.Warn("ci loop: waking watches failed", "err", err)
	}
}
