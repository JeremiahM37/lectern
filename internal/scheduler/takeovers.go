package scheduler

import (
	"context"
	"fmt"

	"github.com/JeremiahM37/lectern/v2/internal/agents"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// ProcessTakeovers runs before normal polling. Pending requests survive a
// control-plane restart; a reserved session ID makes launch idempotent.
func (s *Scheduler) ProcessTakeovers(ctx context.Context) {
	if _, ok := s.Sessions.(*sessions.Manager); !ok {
		return
	}
	rows, err := s.DB.Query("SELECT task_id FROM task_takeovers WHERE status='pending' ORDER BY created_at")
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if err := s.takeover(ctx, id); err != nil {
			// Cancellation leaves the request pending for the next service start.
			if ctx.Err() != nil {
				return
			}
			s.DB.Exec("UPDATE task_takeovers SET status='failed',error=? WHERE task_id=?", err.Error(), id)
			s.Log.Warn("task takeover paused", "task", id, "err", err)
		}
		if task, err := s.DB.Task(id); err == nil {
			s.Bus.Publish("board", "task", task)
		}
	}
}

func (s *Scheduler) takeover(ctx context.Context, id int64) error {
	tr, err := s.DB.Takeover(id)
	if err != nil {
		return err
	}
	if tr == nil {
		return nil
	}
	att, err := s.DB.Attempt(tr.AttemptID)
	if err != nil {
		return err
	}
	c, err := s.contextFor(att)
	if err != nil {
		return err
	}
	if c.Target.Kind == "sandbox" || att.SandboxVMID != "" {
		return fmt.Errorf("sandbox runs cannot be taken over")
	}
	ex, err := s.attemptExecutor(att, c.Target)
	if err != nil {
		return err
	}
	manager := s.Sessions.(*sessions.Manager)
	check, err := ex.Run(ctx, "test -d "+shellq.Quote(att.WorktreePath), executor.RunOpts{Timeout: 20})
	if err != nil {
		return err
	}
	if !check.OK() {
		return fmt.Errorf("the run's worktree is no longer available")
	}
	if _, err = s.drainEvents(ctx, ex, att, c, agents.RuntimeDir(att.WorktreePath)); err != nil {
		return err
	}
	att, err = s.DB.Attempt(att.ID)
	if err != nil {
		return err
	}
	env := s.Creds.BaseAgentEnv()
	for k, v := range projectEnv(c.Project) {
		env[k] = v
	}
	opts, err := manager.PrepareTakeover(ctx, ex, c.Task, c.Project, att, env)
	if err != nil {
		return err
	}
	if tr.SessionID == nil {
		// Reserve both the record and its linkage together, before stopping anything.
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		now := store.Now()
		res, err := tx.Exec(`INSERT INTO sessions(project_id,target_id,name,agent,model,workdir,tmux_session,status,origin,last_activity_at,created_at,updated_at)
   VALUES(?,?,?,?,?,?,'','starting','lectern',?,?,?)`, c.Project.ID, c.Target.ID, c.Task.Title, c.Task.Agent, firstNonEmpty(att.Model, c.Task.Model), att.WorktreePath, now, now, now)
		if err != nil {
			return err
		}
		sid, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE sessions SET tmux_session=? WHERE id=?", fmt.Sprintf("lec-s%d", sid), sid); err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE task_takeovers SET session_id=? WHERE task_id=?", sid, id); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		tr.SessionID = &sid
	}
	sess, err := s.DB.Session(*tr.SessionID)
	if err != nil {
		return err
	}
	// tmux kill-session is synchronous. Check on the target before permitting
	// another writer, including after an interrupted control-plane request.
	old := fmt.Sprintf("=lec-%d", att.ID)
	if _, err = ex.Run(ctx, "tmux kill-session -t "+shellq.Quote(old)+" 2>/dev/null || true", executor.RunOpts{Timeout: 20}); err != nil {
		return err
	}
	alive, err := ex.Run(ctx, "tmux has-session -t "+shellq.Quote(old)+" 2>/dev/null", executor.RunOpts{Timeout: 20})
	if err != nil {
		return err
	}
	if alive.RC != 1 {
		return fmt.Errorf("background run is still alive; takeover has not started")
	}
	// Capture the tail written between the first read and termination.
	if _, err = s.drainEvents(ctx, ex, att, c, agents.RuntimeDir(att.WorktreePath)); err != nil {
		return err
	}
	if latest, err := s.DB.Attempt(att.ID); err == nil && manager.ExactResumeID(c.Task.Agent, latest.SessionID) != "" {
		opts.ResumeID = latest.SessionID
		opts.Prime = ""
	}
	s.Broker.ExpireForAttempt(att.ID)
	if err = s.DB.Update("attempts", att.ID, map[string]any{"status": "cancelled", "finished_at": store.Now()}); err != nil {
		return err
	}
	if err = s.DB.Update("tasks", id, map[string]any{"status": "cancelled", "updated_at": store.Now()}); err != nil {
		return err
	}
	// An earlier process may have launched successfully just before restart.
	alive, err = ex.Run(ctx, "tmux has-session -t "+shellq.Quote("="+sess.TmuxSession)+" 2>/dev/null", executor.RunOpts{Timeout: 20})
	if err != nil {
		return err
	}
	if alive.RC != 0 && alive.RC != 1 {
		return fmt.Errorf("cannot check interactive session on target (exit %d)", alive.RC)
	}
	if !alive.OK() {
		opts.ReservedID = sess.ID
		if _, err = manager.Launch(ctx, opts); err != nil {
			return err
		}
	}
	_, err = s.DB.Exec("UPDATE sessions SET ended_at=NULL,status='starting' WHERE id=?", sess.ID)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec("UPDATE task_takeovers SET status='ready',error=? WHERE task_id=?", "", id)
	return err
}
