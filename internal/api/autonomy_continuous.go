package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// A completed checkpoint is not a completed autonomous experiment.
func autoCycleDue(a *autoRecord, now time.Time) bool {
	if !a.Config.Enabled {
		return false
	}
	loc, _ := time.LoadLocation(a.Config.Timezone)
	day := now.In(loc).Format("2006-01-02")
	if a.State == nil {
		return a.Config.Continuous || now.In(loc).Hour() >= a.Config.MorningHour || a.RequestedDay == day
	}
	if a.State.Phase != autonomy.Complete {
		return false
	}
	if !a.Config.Continuous {
		return a.State.Date != day && (now.In(loc).Hour() >= a.Config.MorningHour || a.RequestedDay == day)
	}
	if !a.NextCycleScheduled {
		delay := time.Minute
		if len(a.State.Items) == 0 {
			delay = 15 * time.Minute
		}
		a.NextCycleAt = now.Add(delay)
		a.NextCycleScheduled = true
	}
	a.Status = "between_cycles"
	a.Reason = "Checkpoint complete; next planning cycle starts automatically at " + a.NextCycleAt.In(loc).Format("15:04 MST")
	return !now.Before(a.NextCycleAt)
}

func (s *Server) archiveAutoCycle(a *autoRecord) error {
	if a.State == nil {
		return nil
	}
	// Retain complete records beyond the bounded in-memory/API history window.
	dir := filepath.Join(autoRoot, "records")
	if err := os.MkdirAll(dir, 0750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "cycle-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	_, err = f.WriteString(store.J(a.State))
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	cycle := a.State.Cycle
	if cycle == 0 {
		cycle = 1
	}
	return os.Rename(name, filepath.Join(dir, fmt.Sprintf("%s-%06d.json", a.State.Date, cycle)))
}

func autoNewCycle(a *autoRecord, now time.Time) {
	loc, _ := time.LoadLocation(a.Config.Timezone)
	next, _ := autonomy.NewState(now.In(loc).Format("2006-01-02"))
	next.Cycle = 1
	if a.State != nil {
		next.Cycle = a.State.Cycle + 1
		if next.Cycle < 2 {
			next.Cycle = 2
		}
		next.Backlog = append([]autonomy.Proposal(nil), a.State.Backlog...)
		// Migrate approvals from legacy states before their history rotates out.
		for _, as := range a.State.Assignments {
			if as.Role != "reviewer" || !as.Completed {
				continue
			}
			var verdict autonomy.Verdict
			if json.Unmarshal(a.State.Reports[as.TaskID], &verdict) != nil || verdict.Approve == nil {
				continue
			}
			for _, builder := range a.State.Assignments {
				if builder.Role == "builder" && builder.Completed && builder.Item == as.Item && builder.Round == as.Round && builder.Step == as.Step {
					if job := autoFindJob(a, builder.TaskID); job != nil {
						job.Approved = *verdict.Approve
						job.Rejected = !*verdict.Approve
						job.ReviewReason = verdict.Reason
						job.ReviewTaskID = as.TaskID
					}
				}
			}
		}
		a.Runs = append(a.Runs, a.State)
		if len(a.Runs) > 90 {
			// Backfill legacy rejections before removing their only in-memory evidence.
			for _, expired := range a.Runs[:len(a.Runs)-90] {
				for _, as := range expired.Assignments {
					if as.Role == "builder" && as.Completed {
						autoRejectedCheckpoint(a, as.TaskID)
					}
				}
			}
			a.Runs = a.Runs[len(a.Runs)-90:]
		}
	}
	a.State = next
	a.NextCycleScheduled = false
	a.NextCycleAt = time.Time{}
	a.Status = "plan"
	a.Reason = ""
	a.RetryAt = time.Time{}
}

func autoNextRole(a *autoRecord) string {
	for _, id := range a.State.ActiveTaskIDs() {
		if j := autoFindJob(a, id); j != nil {
			return j.Role
		}
	}
	state := *a.State
	if state.Phase == autonomy.Paused {
		state.Phase = state.ResumePhase
	}
	roles := state.NeededRoles()
	if len(roles) > 0 {
		return roles[0]
	}
	return "planner"
}

func autoModelChoice(role, provider string, expert bool, catalog []string) string {
	if provider == "claude" {
		if role == "builder" && !expert {
			return "sonnet"
		}
		return "opus"
	}
	options := []string{"gpt-6-astra", "gpt-6-sol", "gpt-5.6-sol"}
	if role == "builder" && !expert {
		options = []string{"gpt-6-luna", "gpt-5.6-luna", "gpt-5.4-mini"}
	}
	for _, want := range options {
		if contains(catalog, want) {
			return want
		}
	}
	return "" // Provider default if the local catalog has not advertised a tier.
}

func (s *Server) autoRoute(a *autoRecord, role string, now time.Time) (string, string, error) {
	order := []string{"codex", "claude"}
	if role == "auditor_a" || role == "reviewer" || role == "decision_a" {
		order = []string{"claude", "codex"}
	}
	expert := a.State.Item < len(a.State.Items) && a.State.Items[a.State.Item].Expert
	var catalog []string
	if home, err := os.UserHomeDir(); err == nil {
		if data, err := autoReadRegular(filepath.Join(home, ".codex", "models_cache.json"), 4<<20); err == nil {
			catalog = sessions.ParseModelCatalog(string(data))
		}
	}
	var failures []string
	for _, provider := range order {
		if err := autonomy.QuotaGate(a.Config, a.Quota.Providers, []string{provider}, now); err == nil {
			return provider, autoModelChoice(role, provider, expert, catalog), nil
		} else {
			failures = append(failures, err.Error())
		}
	}
	return "", "", fmt.Errorf("no provider above reserve with fresh usage: %s", strings.Join(failures, "; "))
}

// Running jobs remain pinned to the provider whose allowance they consume.
func (s *Server) autoQuotaProvider(a *autoRecord, now time.Time) (string, error) {
	for _, id := range a.State.ActiveTaskIDs() {
		if j := autoFindJob(a, id); j != nil && (j.Status == "running" || j.Status == "starting") {
			return j.Provider, autonomy.QuotaGate(a.Config, a.Quota.Providers, []string{j.Provider}, now)
		}
	}
	provider, _, err := s.autoRoute(a, autoNextRole(a), now)
	return provider, err
}

// The trusted controller only copies an owned completed builder artifact. The
// worker never chooses arbitrary host paths. Cross-project reuse is refused.
func (s *Server) autoContinuation(a *autoRecord, projectID, taskID int64) (*autoJob, error) {
	j := autoFindJob(a, taskID)
	if j == nil || j.Role != "builder" || j.Status != "done" {
		return nil, fmt.Errorf("continuation must name a completed workshop builder task")
	}
	task, err := s.DB.Task(taskID)
	if err != nil || task.ProjectID != projectID {
		return nil, fmt.Errorf("continuation project mismatch")
	}
	return j, nil
}

// Cross-cycle reuse is stricter than the within-cycle review/decision copies:
// only an explicitly approved final review can promote a builder checkpoint.
func autoCheckpointApproved(a *autoRecord, taskID int64) bool {
	if job := autoFindJob(a, taskID); job != nil && job.Approved && job.ReviewTaskID > 0 {
		if reviewer := autoFindJob(a, job.ReviewTaskID); reviewer != nil && reviewer.Role == "reviewer" && reviewer.Status == "done" {
			return true
		}
	}
	states := append([]*autonomy.State(nil), a.Runs...)
	if a.State != nil {
		states = append(states, a.State)
	}
	for _, state := range states {
		for _, builder := range state.Assignments {
			if builder.TaskID != taskID || builder.Role != "builder" || !builder.Completed {
				continue
			}
			for _, reviewer := range state.Assignments {
				if reviewer.Role != "reviewer" || !reviewer.Completed || reviewer.Item != builder.Item || reviewer.Round != builder.Round || reviewer.Step != builder.Step {
					continue
				}
				var verdict autonomy.Verdict
				if json.Unmarshal(state.Reports[reviewer.TaskID], &verdict) == nil && verdict.Approve != nil && *verdict.Approve {
					return true
				}
			}
		}
	}
	return false
}

func (s *Server) autoApprovedContinuation(a *autoRecord, projectID, taskID int64) (*autoJob, error) {
	j, err := s.autoContinuation(a, projectID, taskID)
	if err != nil {
		return nil, err
	}
	if !autoCheckpointApproved(a, taskID) {
		return nil, fmt.Errorf("continuation checkpoint has no approved final review; rejected or unresolved work cannot be promoted")
	}
	return j, nil
}

func (s *Server) copyAutoBuilder(ctx context.Context, a *autoRecord, jobID string, projectID int64) error {
	for i := len(a.State.Assignments) - 1; i >= 0; i-- {
		as := a.State.Assignments[i]
		if as.Role == "builder" && as.Item == a.State.Item && as.Completed {
			j, err := s.autoContinuation(a, projectID, as.TaskID)
			if err != nil {
				return err
			}
			_, err = s.runAutoCommand(ctx, "copy", "--job", jobID, "--from-job", j.ID)
			return err
		}
	}
	return fmt.Errorf("builder artifacts missing")
}
