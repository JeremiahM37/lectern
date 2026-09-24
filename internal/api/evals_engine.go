package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scheduler"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

const defaultEvalConcurrency = 2

func (s *Server) evalConcurrencyLimit() int {
	if raw := strings.TrimSpace(s.DB.Setting("eval_concurrency")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	return defaultEvalConcurrency
}

// RunEvalsTick is the scheduler.Scheduler.Evals callback (see app.go): once
// per scheduler tick, it materialises newly-queued runs into their result
// cells, dispatches the next queued cells up to the run's concurrency limit,
// and grades cells whose dispatched task has finished. It reuses the ordinary
// task/attempt machinery end to end — an eval cell IS a task, so nothing here
// touches tmux, drivers or worktrees directly.
func (s *Server) RunEvalsTick(ctx context.Context) {
	limit := s.evalConcurrencyLimit()
	rows, err := s.DB.Query(`SELECT id FROM eval_runs WHERE status IN ('queued','running') ORDER BY id`)
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
		run, err := s.DB.EvalRun(id)
		if err != nil {
			continue
		}
		s.tickEvalRun(ctx, run, limit)
	}
}

func (s *Server) tickEvalRun(ctx context.Context, run *store.EvalRun, limit int) {
	suite, err := s.DB.EvalSuite(run.SuiteID)
	if err != nil {
		return // the suite was deleted out from under a live run — nothing to do
	}
	if run.Status == "queued" {
		if err := s.materializeEvalRun(run, suite); err != nil {
			s.Log.Error("eval run materialize failed", "run", run.ID, "err", err)
			return
		}
		s.DB.Update("eval_runs", run.ID, map[string]any{"status": "running"})
		run.Status = "running"
	}

	results, err := s.DB.EvalResults(run.ID)
	if err != nil {
		return
	}

	// grade every cell whose dispatched task has finished
	inFlight := 0
	allTerminal := true
	for _, res := range results {
		if res.Status != "running" {
			if res.Status == "queued" {
				allTerminal = false
			}
			continue
		}
		if res.TaskID == nil {
			continue
		}
		task, err := s.DB.Task(*res.TaskID)
		if err != nil {
			// the task vanished (deleted from the board by hand) — call it an error
			// rather than spinning on it forever
			s.DB.Update("eval_results", res.ID, map[string]any{"status": "error",
				"check_output_tail": "task was deleted"})
			continue
		}
		if task.Status == "queued" || task.Status == "running" {
			inFlight++
			allTerminal = false
			continue
		}
		s.gradeEvalResult(res, task)
	}

	if run.Status == "running" {
		var variants []store.EvalVariant
		json.Unmarshal([]byte(run.VariantsJSON), &variants)
		cases, err := s.DB.EvalCases(run.SuiteID)
		if err == nil {
			byID := map[int64]*store.EvalCase{}
			for _, c := range cases {
				byID[c.ID] = c
			}
			for _, res := range results {
				if inFlight >= limit {
					break
				}
				if res.Status != "queued" {
					continue
				}
				c, ok := byID[res.CaseID]
				if !ok || res.VariantIdx >= len(variants) {
					s.DB.Update("eval_results", res.ID, map[string]any{"status": "error",
						"check_output_tail": "case or variant no longer exists"})
					continue
				}
				if err := s.dispatchEvalCell(ctx, run, suite, c, variants[res.VariantIdx], res); err != nil {
					s.DB.Update("eval_results", res.ID, map[string]any{"status": "error",
						"check_output_tail": clip(err.Error(), 1000)})
					continue
				}
				inFlight++
				allTerminal = false
			}
		}
	}

	if allTerminal && run.Status == "running" {
		s.DB.Update("eval_runs", run.ID, map[string]any{"status": "done"})
		s.Bus.Publish("board", "eval_run", run)
	}
}

// materializeEvalRun creates every (case, variant, repeat) cell as a queued
// eval_results row. Guarded by "no rows yet" so a run that crashed mid-tick
// is resumed, not duplicated, on the next one.
func (s *Server) materializeEvalRun(run *store.EvalRun, suite *store.EvalSuite) error {
	if n, err := s.DB.Count("eval_results", "run_id=?", run.ID); err != nil || n > 0 {
		return err
	}
	cases, err := s.DB.EvalCases(suite.ID)
	if err != nil {
		return err
	}
	var variants []store.EvalVariant
	if err := json.Unmarshal([]byte(run.VariantsJSON), &variants); err != nil {
		return fmt.Errorf("run variants are unreadable: %w", err)
	}
	repeats := run.Repeats
	if repeats <= 0 {
		repeats = 1
	}
	for _, c := range cases {
		for vi := range variants {
			for ri := 0; ri < repeats; ri++ {
				if _, err := s.DB.InsertEvalResult(&store.EvalResult{
					RunID: run.ID, CaseID: c.ID, VariantIdx: vi, RepeatIdx: ri, Status: "queued",
				}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// dispatchEvalCell creates and queues the one task+attempt that runs a single
// (case, variant, repeat) cell, sharing Best-of-N's own attempt creation path
// (scheduler.CreateAttempt) so an eval cell is scheduled, driven and
// auto-verified exactly like any other attempt.
func (s *Server) dispatchEvalCell(ctx context.Context, run *store.EvalRun, suite *store.EvalSuite,
	c *store.EvalCase, v store.EvalVariant, res *store.EvalResult) error {
	prompt := c.Prompt
	if strings.TrimSpace(c.SetupCommand) != "" {
		prompt = fmt.Sprintf("Before doing anything else, run this setup command in the repo "+
			"root and make sure it succeeds:\n\n```\n%s\n```\n\n%s", c.SetupCommand, prompt)
	}
	task, err := s.DB.InsertTask(&store.Task{
		ProjectID: suite.ProjectID,
		Title: clip(fmt.Sprintf("[eval] %s / %s (v%d r%d)", suite.Name, c.Name,
			oneBased(res.VariantIdx), res.RepeatIdx+1), 120),
		Prompt: prompt, Status: "queued", Agent: v.Agent, Model: v.Model,
		PermissionMode: v.PermissionMode, BaseBranch: c.BaseRef,
		LabelsJSON: store.J([]string{"eval"}), CreatedBy: "eval", CheckCommand: c.CheckCommand,
	})
	if err != nil {
		return err
	}
	att, err := s.Sched.CreateAttempt(task, scheduler.AttemptOpts{})
	if err != nil {
		return err
	}
	s.DB.Update("eval_results", res.ID, map[string]any{
		"status": "running", "task_id": task.ID, "attempt_id": att.ID})
	s.Bus.Publish("board", "task", task)
	return nil
}

func oneBased(i int) int { return i + 1 }

// gradeEvalResult turns a finished task into a scored eval_results row. A
// task's own failure (the agent errored/crashed, exit_code != 0) is "error";
// otherwise the case's check_command result decides pass/fail, falling back
// to the attempt's own success when no check ran at all (nothing configured
// on the case OR the project).
func (s *Server) gradeEvalResult(res *store.EvalResult, task *store.Task) {
	fields := map[string]any{}
	att, err := s.DB.LatestAttempt(task.ID)
	if err != nil {
		fields["status"] = "error"
		fields["check_output_tail"] = "no attempt recorded"
		s.DB.Update("eval_results", res.ID, fields)
		return
	}
	if att.StartedAt != nil && att.FinishedAt != nil {
		d := *att.FinishedAt - *att.StartedAt
		fields["duration_s"] = d
	}
	result := store.UnjObj(att.ResultJSON)
	if v, ok := result["cost_usd"]; ok {
		fields["cost_usd"] = v
	}
	if v, ok := result["input_tokens"]; ok {
		fields["input_tokens"] = v
	}
	if v, ok := result["output_tokens"]; ok {
		fields["output_tokens"] = v
	}
	diffStat := store.UnjList(att.DiffStatJSON)
	fields["diff_files"] = len(diffStat)
	lines := 0
	for _, raw := range diffStat {
		if m, ok := raw.(map[string]any); ok {
			if a, ok := m["additions"].(float64); ok {
				lines += int(a)
			}
			if d, ok := m["deletions"].(float64); ok {
				lines += int(d)
			}
		}
	}
	fields["diff_lines"] = lines

	if att.ExitCode == nil || *att.ExitCode != 0 || task.Status == "failed" || task.Status == "cancelled" {
		fields["status"] = "error"
		if v, ok := result["error"]; ok {
			fields["check_output_tail"] = clip(fmt.Sprint(v), 1000)
		}
		s.DB.Update("eval_results", res.ID, fields)
		return
	}
	verify := store.UnjObj(att.VerifyJSON)
	if len(verify) > 0 {
		rc := -1
		if f, ok := verify["rc"].(float64); ok {
			rc = int(f)
		}
		fields["check_rc"] = rc
		fields["check_output_tail"] = clip(fmt.Sprint(verify["output"]), 1000)
		if rc == 0 {
			fields["status"] = "passed"
		} else {
			fields["status"] = "failed"
		}
	} else {
		// nothing to check against — the agent's own success is the signal
		fields["status"] = "passed"
	}
	s.DB.Update("eval_results", res.ID, fields)
}

// cancelEvalRunNow stops a run immediately: every attempt still live is
// cancelled through the scheduler (the same path the board's Cancel button
// uses), and every cell that had not finished is marked "error" so the run
// reads as complete rather than stuck.
func (s *Server) cancelEvalRunNow(ctx context.Context, run *store.EvalRun) error {
	results, err := s.DB.EvalResults(run.ID)
	if err != nil {
		return err
	}
	for _, res := range results {
		if res.Status != "queued" && res.Status != "running" {
			continue
		}
		if res.AttemptID != nil {
			if att, err := s.DB.Attempt(*res.AttemptID); err == nil &&
				(att.Status == "queued" || att.Status == "running") {
				s.Sched.CancelAttempt(ctx, att)
			}
		}
		s.DB.Update("eval_results", res.ID, map[string]any{
			"status": "error", "check_output_tail": "cancelled by operator"})
	}
	return s.DB.Update("eval_runs", run.ID, map[string]any{"status": "cancelled"})
}
