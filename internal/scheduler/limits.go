package scheduler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/accounts"
	"github.com/JeremiahM37/lectern/v2/internal/agents"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/limits"
	"github.com/JeremiahM37/lectern/v2/internal/sinks"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Usage limits for headless tasks (docs/rate-limits.md). A limit seen in an
// attempt's stream opens a hold; when the attempt exits, the project's policy
// decides: requeue the same agent for the reset (wait), re-dispatch to the
// fallback agent now (handoff), or fail the task with one-tap choices
// (notify).

func (s *Scheduler) limitTiming() limits.Timing {
	if s.LimitTiming.MaxTries == 0 {
		return limits.DefaultTiming
	}
	return s.LimitTiming
}

// noticeLimit records a usage limit the moment an attempt's stream shows one.
func (s *Scheduler) noticeLimit(att *store.Attempt, ev agents.Event) {
	now := time.Now()
	hit, ok := limits.DetectEvent(ev.Type, ev.Payload, now)
	if !ok {
		return
	}
	task, err := s.DB.Task(att.TaskID)
	if err != nil {
		return
	}
	agent := firstNonEmpty(att.Agent, task.Agent, "claude")
	h, created := limits.RecordAttemptHit(s.DB, att, task, agent, hit, "stream", now)
	if created {
		s.Log.Info("usage limit detected", "task", task.ID, "attempt", att.ID, "pattern", hit.Pattern, "reset", hit.ResetAt)
	} else if h != nil && h.ResetAt == nil && !hit.ResetAt.IsZero() {
		s.DB.TransitionLimitHold(h.ID, h.State, map[string]any{"reset_at": float64(hit.ResetAt.Unix())})
	}
}

// limitStopped handles an attempt that exited because of its usage limit and
// reports whether it did; finalize then skips its ordinary failed/review path.
func (s *Scheduler) limitStopped(ctx context.Context, att *store.Attempt, rc int, result map[string]any) bool {
	now := time.Now()
	text, _ := result["result"].(string)
	textHit, byText := limits.DetectLines(text, now)
	isErr, _ := result["is_error"].(bool)
	h, err := s.DB.OpenLimitHoldForAttempt(att.ID)
	if err != nil {
		h = nil
	}
	task, terr := s.DB.Task(att.TaskID)
	if terr != nil {
		return false
	}
	if h == nil {
		if !byText {
			return false
		}
		h, _ = limits.RecordAttemptHit(s.DB, att, task, firstNonEmpty(att.Agent, task.Agent, "claude"), textHit, "result", now)
		if h == nil {
			return false
		}
	}
	if rc == 0 && !isErr && !byText {
		// The run got past it — a limit warning mid-run that did not stop it.
		s.DB.TransitionLimitHold(h.ID, h.State, map[string]any{"state": limits.StateCleared,
			"resolved_at": store.Now(), "note": "the attempt finished normally"})
		return false
	}
	if task.Status != "running" {
		return false // cancelled or moved by hand while it ran
	}
	p, _ := limits.Effective(s.DB, 0, &task.ProjectID)
	if h.Policy == limits.ModeSwap {
		err := s.ApplyTaskLimit(ctx, h, limits.ModeSwap, p)
		if err == nil {
			return true
		}
		// No account to move to: the policy's secondary mode applies.
		s.Log.Info("limited task has no account to swap to", "task", task.ID, "err", err)
		s.DB.TransitionLimitHold(h.ID, limits.StateWaiting, map[string]any{"policy": p.Secondary(),
			"note": "no account to swap to: " + err.Error()})
		h.Policy = p.Secondary()
	}
	if h.Policy == limits.ModeWait || h.Policy == limits.ModeHandoff {
		err := s.ApplyTaskLimit(ctx, h, h.Policy, p)
		if err == nil {
			return true
		}
		s.Log.Warn("could not continue a limited task", "task", task.ID, "err", err)
		s.DB.TransitionLimitHold(h.ID, limits.StateWaiting, map[string]any{"policy": limits.ModeNotify,
			"note": "could not continue automatically: " + err.Error()})
	}
	if others, _ := s.DB.Count("attempts", "task_id=? AND status IN ('queued','running') AND id!=?",
		att.TaskID, att.ID); others == 0 {
		s.setTaskStatus(task.ID, "failed")
	}
	body := fmt.Sprintf("%q hit its usage limit", clip(task.Title, 80))
	if h.ResetAt != nil {
		body += " · resets " + limits.FormatReset(time.Unix(int64(*h.ResetAt), 0), now)
	}
	s.Notifier.Notify("Task stopped by usage limit", body, fmt.Sprintf("/#task/%d", task.ID),
		&sinks.Extra{Kind: "limit", LimitID: h.ID})
	return true
}

// ApplyTaskLimit continues a limited task: the same agent, held in the queue
// until the reset (mode wait), or the fallback agent now (mode handoff), in
// the attempt's own worktree so its partial work carries over. The hold's
// compare-and-swap makes it act at most once, including across a restart.
func (s *Scheduler) ApplyTaskLimit(ctx context.Context, h *store.LimitHold, mode string, p limits.Policy) error {
	if h.AttemptID == nil || h.TaskID == nil {
		return fmt.Errorf("not a task limit")
	}
	task, err := s.DB.Task(*h.TaskID)
	if err != nil {
		return err
	}
	att, err := s.DB.Attempt(*h.AttemptID)
	if err != nil {
		return err
	}
	resolved := limits.StateRequeued
	switch mode {
	case limits.ModeHandoff:
		resolved = limits.StateRedispatched
	case limits.ModeSwap:
		resolved = limits.StateSwapped
	}
	// A restart between creating the continuation and recording it leaves the
	// attempt queued and the hold open. Adopt that attempt instead of adding a
	// second one.
	var existing int64
	s.DB.QueryRow(`SELECT id FROM attempts WHERE task_id=? AND id>? AND status IN ('queued','running')
		ORDER BY id LIMIT 1`, task.ID, att.ID).Scan(&existing)
	if existing != 0 {
		s.DB.TransitionLimitHold(h.ID, h.State, map[string]any{"state": resolved,
			"resolved_at": store.Now(), "successor_id": existing})
		return nil
	}
	c, err := s.contextFor(att)
	if err != nil {
		return err
	}
	sandbox := c.Target.Kind == "sandbox"
	now := time.Now()
	agent := effAgent(c, att)
	opts := AttemptOpts{PermissionMode: att.PermissionMode}
	if !sandbox {
		opts.WorktreePath, opts.Branch = att.WorktreePath, att.Branch
	}
	var notBefore time.Time
	var from, to *store.Account
	switch mode {
	case limits.ModeSwap:
		if sandbox {
			return fmt.Errorf("a sandboxed attempt only sees its own login")
		}
		from = limits.Account(s.DB, c.Target.ID, agent, att.AccountID)
		limits.MarkLimited(s.DB, from, h, now)
		next, _, err := limits.NextAccount(s.DB, c.Target.ID, agent, from, p.AccountID, now)
		if err != nil {
			return err
		}
		to = next
		if !s.accountSignedIn(ctx, c, att, agent, next) {
			return fmt.Errorf("account %q is not signed in", next.Label)
		}
		opts.Agent, opts.Model, opts.AccountID = att.Agent, att.Model, &next.ID
		opts.Prompt = continuationPrompt(task, agent, false)
		if att.SessionID != "" && s.stageConversation(ctx, c, att, agent, from, next) {
			opts.ResumeSession = att.SessionID
			opts.Prompt = "You hit your usage limit and are now running under another account. " +
				"Continue the task where you left off; your partial work is in this worktree."
		}
	case limits.ModeWait:
		opts.Agent, opts.Model = att.Agent, att.Model
		var reset time.Time
		if h.ResetAt != nil {
			reset = time.Unix(0, int64(*h.ResetAt*1e9))
		}
		notBefore = s.limitTiming().DueAt(reset, h.Tries, now, nil)
		nb := float64(notBefore.UnixNano()) / 1e9
		opts.NotBefore = &nb
		if !sandbox && att.SessionID != "" {
			opts.ResumeSession = att.SessionID
			opts.Prompt = "Your usage limit has reset. Continue the task where you left off; " +
				"your partial work is in this worktree."
		} else {
			opts.Prompt = continuationPrompt(task, agent, sandbox)
		}
	case limits.ModeHandoff:
		if !p.HasFallback() {
			return fmt.Errorf("no fallback agent is configured")
		}
		opts.Agent, opts.Model = p.FallbackAgent, p.FallbackModel
		if p.FallbackProfileID != 0 && opts.Agent == "" {
			profile, err := s.DB.LaunchProfile(p.FallbackProfileID)
			if err != nil {
				return fmt.Errorf("fallback launch profile: %w", err)
			}
			opts.Agent, opts.Model = profile.Agent, firstNonEmpty(opts.Model, profile.Model)
		}
		if opts.Agent == agent && opts.Model == att.Model {
			return fmt.Errorf("the fallback is the agent that hit the limit")
		}
		opts.Prompt = continuationPrompt(task, agent, sandbox)
	default:
		return fmt.Errorf("unknown limit mode %q", mode)
	}
	next, err := s.CreateAttempt(task, opts)
	if err != nil {
		return err
	}
	fields := map[string]any{"state": resolved,
		"resolved_at": store.Now(), "successor_id": next.ID, "policy": mode, "due_at": opts.NotBefore}
	if to != nil {
		fields["account_from"], fields["account_to"] = limits.AccountRef(from), to.ID
	}
	won, err := s.DB.TransitionLimitHold(h.ID, limits.StateWaiting, fields)
	if err != nil || !won {
		s.DB.Exec(`DELETE FROM attempts WHERE id=? AND status='queued'`, next.ID)
		return limits.ErrConflict
	}
	// The task waits in the queue with its continuation — unless another
	// attempt (an A/B sibling) is still running, which keeps it running.
	if others, _ := s.DB.Count("attempts", "task_id=? AND status='running' AND id!=?", task.ID, att.ID); others == 0 {
		if err := s.DB.Update("tasks", task.ID, map[string]any{"status": "queued", "updated_at": store.Now()}); err == nil {
			if fresh, err := s.DB.Task(task.ID); err == nil {
				s.Bus.Publish("board", "task", fresh)
			}
		}
	}
	title, body := "Task paused by usage limit", fmt.Sprintf("%q will resume at %s", clip(task.Title, 80),
		limits.FormatReset(notBefore, now))
	switch mode {
	case limits.ModeHandoff:
		title, body = "Task handed off", fmt.Sprintf("%q hit its usage limit — continuing on %s",
			clip(task.Title, 80), firstNonEmpty(opts.Agent+modelSuffix(opts.Model), p.Fallback()))
	case limits.ModeSwap:
		title, body = "Task swapped account", fmt.Sprintf("%q hit its usage limit on %s — continuing on %s",
			clip(task.Title, 80), limits.AccountLabel(from), to.Label)
	}
	s.Log.Info("limited task continued", "task", task.ID, "mode", mode, "attempt", next.ID)
	s.Notifier.Notify(title, body, fmt.Sprintf("/#task/%d", task.ID), &sinks.Extra{Kind: "limit", LimitID: h.ID})
	return nil
}

func continuationPrompt(task *store.Task, agent string, sandbox bool) string {
	where := "Its partial work is in this worktree — check git status and the diff first, then finish the task."
	if sandbox {
		where = "Its sandbox is gone, so start from the repository as it is."
	}
	return fmt.Sprintf("A previous attempt at this task by %s was stopped by its usage limit before it finished. %s\n\nTASK:\n%s",
		agent, where, firstNonEmpty(task.Prompt, task.Title))
}

func modelSuffix(model string) string {
	if model == "" {
		return ""
	}
	return " · " + model
}

// accountDir is the config directory an attempt's account runs from, or ""
// for the CLI's default login (and for a sandbox, which has only its own).
func (s *Scheduler) accountDir(att *store.Attempt, agent string, sandbox bool) string {
	if att.AccountID == nil || sandbox || accounts.EnvKey(agent) == "" {
		return ""
	}
	a, err := s.DB.Account(*att.AccountID)
	if err != nil || a.Agent != agent {
		return ""
	}
	return a.Dir
}

// accountBase is the config directory the task's agent uses by default (its
// definition's own variable, usually unset).
func (s *Scheduler) accountBase(c *runCtx, att *store.Attempt, agent string) string {
	if def, err := s.taskLaunchConfig(att, c); err == nil {
		return def.Env[accounts.EnvKey(agent)]
	}
	return ""
}

// accountSignedIn checks, without reading it, that the account's credential
// file exists; an unreachable target counts as signed in (the attempt will
// say otherwise).
func (s *Scheduler) accountSignedIn(ctx context.Context, c *runCtx, att *store.Attempt, agent string, a *store.Account) bool {
	ex, err := s.Reg.For(c.Target)
	if err != nil {
		return true
	}
	dir := a.Dir
	if dir == "" {
		dir = s.accountBase(c, att, agent)
	}
	r, err := ex.Run(ctx, accounts.StatusCommand(agent, dir), executor.RunOpts{Timeout: 10})
	return err != nil || strings.TrimSpace(r.Stdout) != "signed-out"
}

// stageConversation copies an attempt's conversation into the next account's
// directory so the continuation can resume it there, and reports whether it
// did. When it cannot, the continuation starts from the task prompt instead.
func (s *Scheduler) stageConversation(ctx context.Context, c *runCtx, att *store.Attempt, agent string, from, to *store.Account) bool {
	ex, err := s.Reg.For(c.Target)
	if err != nil {
		return false
	}
	base := s.accountBase(c, att, agent)
	dir := func(a *store.Account) string {
		if a != nil && a.Dir != "" {
			return a.Dir
		}
		return base
	}
	cmd, err := accounts.StageCommand(agent, dir(from), dir(to), att.SessionID, att.WorktreePath)
	if err != nil {
		return false
	}
	r, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 60})
	if err != nil || !r.OK() {
		s.Log.Warn("could not copy a limited task's conversation to the next account", "attempt", att.ID, "err", err)
		return false
	}
	return true
}
