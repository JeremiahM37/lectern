package api

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

// A rejected final review is evidence for a new repair proposal, never approval.
func autoRejectedCheckpoint(a *autoRecord, taskID int64) (int64, string, bool) {
	job := autoFindJob(a, taskID)
	if job == nil || job.Role != "builder" || job.Status != "done" || autoCheckpointApproved(a, taskID) {
		return 0, "", false
	}
	if job.Rejected && job.ReviewTaskID > 0 && job.ReviewReason != "" {
		if r := autoFindJob(a, job.ReviewTaskID); r != nil && r.Role == "reviewer" && r.Status == "done" {
			return r.TaskID, job.ReviewReason, true
		}
	}
	states := append([]*autonomy.State(nil), a.Runs...)
	if a.State != nil {
		states = append(states, a.State)
	}
	for _, state := range states {
		for _, b := range state.Assignments {
			if b.TaskID != taskID || b.Role != "builder" || !b.Completed {
				continue
			}
			for _, r := range state.Assignments {
				if r.Role != "reviewer" || !r.Completed || r.Item != b.Item || r.Round != b.Round || r.Step != b.Step {
					continue
				}
				receipt := autoFindJob(a, r.TaskID)
				if receipt == nil || receipt.Role != "reviewer" || receipt.Status != "done" {
					continue
				}
				var v autonomy.Verdict
				if json.Unmarshal(state.Reports[r.TaskID], &v) == nil && v.Approve != nil && !*v.Approve && v.Reason != "" {
					job.Rejected = true
					job.ReviewTaskID = r.TaskID
					job.ReviewReason = v.Reason
					return r.TaskID, v.Reason, true
				}
			}
		}
	}
	return 0, "", false
}

func autoRepairRoot(a *autoRecord, taskID int64) int64 {
	seen := map[int64]bool{}
	for taskID > 0 {
		if seen[taskID] {
			return 0
		}
		seen[taskID] = true
		j := autoFindJob(a, taskID)
		if j == nil {
			return 0
		}
		if j.RepairSourceTaskID == 0 {
			return taskID
		}
		taskID = j.RepairSourceTaskID
	}
	return 0
}
func (s *Server) autoRepairContinuation(a *autoRecord, projectID, taskID int64) (*autoJob, error) {
	j, err := s.autoContinuation(a, projectID, taskID)
	if err != nil {
		return nil, err
	}
	if _, _, ok := autoRejectedCheckpoint(a, taskID); !ok {
		return nil, fmt.Errorf("repair_task_id must name an explicitly rejected final-review checkpoint")
	}
	root := autoRepairRoot(a, taskID)
	if root == 0 {
		return nil, fmt.Errorf("repair lineage is unavailable")
	}
	attempts := map[int64]bool{}
	for _, prior := range a.Jobs {
		if prior.RepairSourceTaskID > 0 && autoRepairRoot(a, prior.TaskID) == root {
			attempt := prior.RepairAttemptTaskID
			if attempt == 0 {
				attempt = prior.TaskID
			}
			attempts[attempt] = true
		}
	}
	if len(attempts) >= a.Config.MaxRevisionRounds {
		return nil, fmt.Errorf("repair checkpoint exhausted its %d repair attempts; choose other work", a.Config.MaxRevisionRounds)
	}
	return j, nil
}
func autoRepairAudited(a *autoRecord) bool {
	if a.State == nil || a.State.Phase != autonomy.Build {
		return false
	}
	for _, role := range []string{"auditor_a", "auditor_b"} {
		v, ok := a.State.Audits[role]
		if !ok || v.Approve == nil || !*v.Approve {
			return false
		}
	}
	return true
}
func (s *Server) validateAutoSources(a *autoRecord, items []autonomy.Proposal) error {
	for i, p := range items {
		if p.ContinueTaskID > 0 && p.RepairTaskID > 0 {
			return fmt.Errorf("item %d: continuation and repair are mutually exclusive", i)
		}
		var err error
		if p.ContinueTaskID > 0 {
			_, err = s.autoApprovedContinuation(a, p.ProjectID, p.ContinueTaskID)
		}
		if p.RepairTaskID > 0 {
			_, err = s.autoRepairContinuation(a, p.ProjectID, p.RepairTaskID)
		}
		if err != nil {
			return fmt.Errorf("item %d source invalid: %w; choose /artifacts for continuation or /repairable for a separately audited repair", i, err)
		}
	}
	return nil
}
func (s *Server) autoRepairableArtifacts(a *autoRecord) []map[string]any {
	rows := []map[string]any{}
	seen := map[int64]bool{}
	for i := len(a.Jobs) - 1; i >= 0 && len(rows) < 60; i-- {
		j := a.Jobs[i]
		if seen[j.TaskID] {
			continue
		}
		seen[j.TaskID] = true
		review, reason, ok := autoRejectedCheckpoint(a, j.TaskID)
		if !ok {
			continue
		}
		task, err := s.DB.Task(j.TaskID)
		if err != nil {
			continue
		}
		if _, err = s.autoRepairContinuation(a, task.ProjectID, j.TaskID); err != nil {
			continue
		}
		rows = append(rows, map[string]any{"task_id": j.TaskID, "project_id": task.ProjectID, "title": task.Title, "review_task_id": review, "rejection": reason, "approved": false})
	}
	return rows
}

// Old plans cannot be silently reinterpreted as repairs. Retire only this known
// blocked selection; a fresh planner and both auditors must authorize new work.
// The scheduler invokes this only after the enabled and live-quota gates.
func (s *Server) recoverRejectedContinuation(a *autoRecord) bool {
	const legacy = "continuation checkpoint has no approved final review; rejected or unresolved work cannot be promoted"
	if !a.Config.Enabled || a.State == nil || a.State.Phase != autonomy.Paused || a.State.ResumePhase != autonomy.Build || a.State.Reason != legacy || len(a.State.ActiveTaskIDs()) != 0 {
		return false
	}
	if a.State.Item < 0 || a.State.Item >= len(a.State.Items) {
		return false
	}
	item := a.State.Items[a.State.Item]
	if item.ContinueTaskID <= 0 || item.RepairTaskID != 0 {
		return false
	}
	if _, err := s.autoContinuation(a, item.ProjectID, item.ContinueTaskID); err != nil {
		return false
	}
	if _, _, ok := autoRejectedCheckpoint(a, item.ContinueTaskID); !ok {
		return false
	}
	a.State.Phase = autonomy.Complete
	a.State.Reason = fmt.Sprintf("Cycle abandoned: task %d is rejected, not an approved continuation. Artifacts retained, no approval granted. Replan using an explicit repair_task_id from /repairable or choose other work.", item.ContinueTaskID)
	a.Status = "complete"
	a.Reason = a.State.Reason
	a.RetryAt = time.Time{}
	return true
}
