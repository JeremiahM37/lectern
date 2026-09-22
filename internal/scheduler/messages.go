package scheduler

import (
	"context"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/state"
	"github.com/JeremiahM37/lectern/internal/store"
)

// DeliverMessages runs on the scheduler's own goroutine. Interrupts cannot race
// its poll/finalize path. Receipts and the new attempt commit in one transaction.
func (s *Scheduler) DeliverMessages(ctx context.Context) {
	rows, err := s.DB.Query(`SELECT DISTINCT task_id FROM task_messages WHERE status='pending' AND task_id NOT IN (SELECT task_id FROM task_takeovers) ORDER BY task_id`)
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
		if err := s.deliverTaskMessages(ctx, id); err != nil {
			s.Log.Warn("task message delivery deferred", "task", id, "err", err)
		}
	}
}

func (s *Scheduler) deliverTaskMessages(ctx context.Context, id int64) error {
	task, err := s.DB.Task(id)
	if err != nil {
		return err
	}
	messages, err := s.DB.TaskMessages(id)
	if err != nil {
		return err
	}
	var pending []*store.TaskMessage
	var texts []string
	interrupt := false
	for _, m := range messages {
		if m.Status == "pending" {
			pending = append(pending, m)
			texts = append(texts, m.Text)
			interrupt = interrupt || m.Interrupt
		}
	}
	if len(pending) == 0 {
		return nil
	}
	active, err := s.DB.AttemptsWhere("task_id=? AND status IN ('queued','running')", id)
	if err != nil {
		return err
	}
	if len(active) > 1 {
		return nil
	}
	if len(active) == 1 && active[0].Status == "running" {
		if !interrupt {
			return nil
		}
		c, err := s.contextFor(active[0])
		if err != nil {
			return err
		}
		if c.Target.Kind == "sandbox" {
			return nil
		}
		ex, err := s.attemptExecutor(active[0], c.Target)
		if err != nil {
			return err
		}
		_, err = ex.Run(ctx, fmt.Sprintf("tmux kill-session -t =lec-%d 2>/dev/null || true", active[0].ID), executor.RunOpts{Timeout: 20})
		if err != nil {
			return err
		}
		alive, err := ex.Run(ctx, fmt.Sprintf("tmux has-session -t =lec-%d 2>/dev/null", active[0].ID), executor.RunOpts{Timeout: 20})
		if err != nil {
			return err
		}
		if alive.OK() {
			return fmt.Errorf("current run is still alive; follow-up remains queued")
		}
		s.CancelAttempt(ctx, active[0])
		active = nil
	}
	var opts AttemptOpts
	var queueID *int64
	backlog := task.Status == "backlog" && len(active) == 0
	if len(active) == 1 {
		queueID = &active[0].ID
		opts.Prompt = active[0].Prompt
		if opts.Prompt == "" {
			opts.Prompt = task.Prompt
		}
	}
	feedback := strings.Join(texts, "\n\n")
	if queueID != nil {
		opts.Prompt += "\n\nADDITIONAL OPERATOR INSTRUCTIONS:\n" + feedback
	} else if !backlog {
		opts.Prompt = "Continue this task. The operator sent a follow-up; apply the requested changes in the existing worktree.\n\nORIGINAL TASK:\n" + task.Prompt + "\n\nOPERATOR MESSAGE:\n" + feedback
		if last, err := s.DB.LatestAttempt(id); err == nil {
			project, err := s.DB.Project(task.ProjectID)
			if err != nil {
				return err
			}
			target, err := s.DB.Target(project.TargetID)
			if err != nil {
				return err
			}
			if target != nil && target.Kind != "sandbox" {
				if last.WorktreePath == "" {
					for _, m := range pending {
						s.DB.Update("task_messages", m.ID, map[string]any{"status": "failed", "error": "The previous worktree was cleaned up. Start a new task to continue."})
					}
					return nil
				}
				opts.WorktreePath = last.WorktreePath
				opts.Branch = last.Branch
				opts.ResumeSession = last.SessionID
				opts.Model = last.Model
			}
			if result, ok := store.UnjObj(last.ResultJSON)["result"].(string); ok {
				opts.Prompt += "\n\nPREVIOUS RESULT:\n" + clip(result, 12000)
			}
		}
	}
	launchJSON := ""
	if !backlog && queueID == nil {
		project, err := s.DB.Project(task.ProjectID)
		if err != nil {
			return err
		}
		config, err := s.taskLaunchConfig(&store.Attempt{}, &runCtx{Task: task, Project: project})
		if err != nil {
			return err
		}
		launchJSON = store.J(config)
	}
	token, err := randomToken()
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Recheck after acquiring the database transaction. A browser may have queued
	// another attempt since the reads above; never run two writers in one worktree.
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM attempts WHERE task_id=? AND status IN ('queued','running')", id).Scan(&count); err != nil {
		return err
	}
	if (queueID == nil && count != 0) || (queueID != nil && count != 1) {
		return nil
	}
	var attemptID any
	if backlog {
		res, err := tx.Exec("UPDATE tasks SET prompt=prompt || ?,updated_at=? WHERE id=? AND status='backlog'", "\n\nADDITIONAL OPERATOR INSTRUCTIONS:\n"+feedback, store.Now(), id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return nil
		}
	} else if queueID != nil {
		res, err := tx.Exec("UPDATE attempts SET prompt=? WHERE id=? AND status='queued'", opts.Prompt, *queueID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return nil
		}
		attemptID = *queueID
	} else {
		var current string
		if err := tx.QueryRow("SELECT status FROM tasks WHERE id=?", id).Scan(&current); err != nil {
			return err
		}
		if err := state.Check(current, "queued"); err != nil {
			return err
		}
		var next int
		if err = tx.QueryRow("SELECT COALESCE(MAX(n),0)+1 FROM attempts WHERE task_id=?", id).Scan(&next); err != nil {
			return err
		}
		res, err := tx.Exec(`INSERT INTO attempts(task_id,n,status,token,prompt,resume_session,worktree_path,branch,model,launch_config_json)
	VALUES(?,?,'queued',?,?,?,?,?,?,?)`, id, next, token, opts.Prompt, opts.ResumeSession, opts.WorktreePath, opts.Branch, opts.Model, launchJSON)
		if err != nil {
			return err
		}
		aid, _ := res.LastInsertId()
		attemptID = aid
		if _, err = tx.Exec("UPDATE tasks SET status='queued',updated_at=? WHERE id=?", store.Now(), id); err != nil {
			return err
		}
	}
	for _, m := range pending {
		if _, err = tx.Exec("UPDATE task_messages SET status='delivered',attempt_id=? WHERE id=? AND status='pending'", attemptID, m.ID); err != nil {
			return fmt.Errorf("message receipt: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if fresh, err := s.DB.Task(id); err == nil {
		s.Bus.Publish("board", "task", fresh)
	}
	s.Bus.Publish("board", "task_message", map[string]any{"task_id": id})
	return nil
}
