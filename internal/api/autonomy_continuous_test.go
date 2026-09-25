package api

import (
	"encoding/json"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"testing"
	"time"
)

func TestAutonomyContinuousStartsAtNightAndAdvancesSameDay(t *testing.T) {
	a := &autoRecord{Config: autonomy.DefaultConfig()}
	a.Config.Enabled = true
	now := time.Date(2026, 9, 24, 2, 0, 0, 0, time.UTC)
	if !autoCycleDue(a, now) {
		t.Fatal("continuous work waited for morning")
	}
	autoNewCycle(a, now)
	a.State.Phase = autonomy.Complete
	a.State.Items = []autonomy.Proposal{{ProjectID: 1, Title: "milestone"}}
	a.State.Backlog = []autonomy.Proposal{{ProjectID: 1, Title: "long project", Score: 90}}
	if autoCycleDue(a, now) {
		t.Fatal("no completion cooldown")
	}
	data, _ := json.Marshal(a)
	var restored autoRecord
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if !autoCycleDue(&restored, now.Add(time.Minute)) {
		t.Fatal("restart lost next-cycle schedule")
	}
	autoNewCycle(&restored, now.Add(time.Minute))
	if restored.State.Cycle != 2 || restored.State.Phase != autonomy.Plan || len(restored.State.Backlog) != 1 || len(restored.Runs) != 1 {
		t.Fatalf("lost continuity: %+v", restored)
	}
	if len(restored.State.Assignments) != 0 {
		t.Fatal("previous assignments reused")
	}
	restored.Config.Enabled = false
	if autoCycleDue(&restored, now.Add(time.Hour)) {
		t.Fatal("OFF dispatched work")
	}
}

func TestAutonomyEmptyPlanDoesNotSpin(t *testing.T) {
	a := &autoRecord{Config: autonomy.DefaultConfig()}
	a.Config.Enabled = true
	now := time.Now()
	autoNewCycle(a, now)
	a.State.Phase = autonomy.Complete
	if autoCycleDue(a, now) || a.NextCycleAt.Sub(now) != 15*time.Minute {
		t.Fatal("empty plan busy loop")
	}
}

func TestAutonomyLegacyConfigEnablesContinuousWithoutEnablingToggle(t *testing.T) {
	s := autoTestServer(t)
	if err := s.DB.SetSetting(autoKey, `{"config":{"enabled":false,"timezone":"America/Denver","morning_hour":8,"reserve_percent":10,"margin_percent":5,"max_revision_rounds":2,"max_items_per_day":3}}`); err != nil {
		t.Fatal(err)
	}
	a, err := s.loadAuto()
	if err != nil {
		t.Fatal(err)
	}
	if !a.Config.Continuous || a.Config.Enabled {
		t.Fatal("wrong compatibility defaults")
	}
}

func TestAutonomyModelsUseCheaperBuildersAndStrongReview(t *testing.T) {
	catalog := []string{"gpt-6-astra", "gpt-6-luna"}
	if autoModelChoice("builder", "codex", false, catalog) != "gpt-6-luna" {
		t.Fatal("builder not economical")
	}
	for _, role := range []string{"planner", "auditor_b", "decision_b", "reviewer"} {
		if autoModelChoice(role, "codex", false, catalog) != "gpt-6-astra" {
			t.Fatal("review not strong")
		}
	}
	if autoModelChoice("builder", "codex", true, catalog) != "gpt-6-astra" {
		t.Fatal("approved expert ignored")
	}
	if autoModelChoice("builder", "claude", false, nil) != "sonnet" {
		t.Fatal("fallback not economical")
	}
	if autoModelChoice("decision_a", "claude", false, nil) != "opus" {
		t.Fatal("decision review not strong")
	}
	if autoModelChoice("builder", "codex", false, nil) != "" {
		t.Fatal("invented unavailable model")
	}
}

func TestAutonomyContinuationRequiresOwnedCompletedMatchingProject(t *testing.T) {
	s := autoTestServer(t)
	target, err := s.DB.InsertTarget(&store.Target{Name: "test", Kind: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := s.DB.InsertProject(&store.Project{Name: "test", TargetID: target.ID, RepoPath: "/unused"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.DB.InsertTask(&store.Task{ProjectID: project.ID, Title: "checkpoint", Status: "done", CreatedBy: autoOwner})
	if err != nil {
		t.Fatal(err)
	}
	a := &autoRecord{Jobs: []*autoJob{{ID: "owned", TaskID: task.ID, Role: "builder", Status: "done"}}}
	if _, err = s.autoContinuation(a, project.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.autoContinuation(a, project.ID+1, task.ID); err == nil {
		t.Fatal("cross-project artifact accepted")
	}
	if _, err = s.autoContinuation(a, project.ID, 999); err == nil {
		t.Fatal("unowned artifact accepted")
	}
	a.Jobs[0].Status = "running"
	if _, err = s.autoContinuation(a, project.ID, task.ID); err == nil {
		t.Fatal("live artifact copied")
	}
}

func TestAutonomyBudgetFallsBackButPinsRunningProvider(t *testing.T) {
	s := autoTestServer(t)
	a := &autoRecord{Config: autonomy.DefaultConfig()}
	a.Config.Enabled = true
	a.State, _ = autonomy.NewState("2026-09-24")
	now := time.Now()
	stamp := float64(now.Unix())
	remaining := 60.0
	used := 40.0
	reset := stamp + 86400
	a.Quota.Providers = []autonomy.ProviderUsage{{ID: "codex", Status: "ok", UpdatedAt: &stamp, Buckets: []autonomy.UsageBucket{{ID: "codex", Windows: []autonomy.UsageWindow{{Label: "Weekly", UsedPercent: &used, RemainingPercent: &remaining, ResetsAt: &reset}}}}}, {ID: "claude", Status: "unavailable"}}
	provider, _, err := s.autoRoute(a, "auditor_a", now)
	if err != nil || provider != "codex" {
		t.Fatalf("unused healthy allowance stranded: %s %v", provider, err)
	}
	a.State.Assignments = []autonomy.Assignment{{TaskID: 3, Role: "planner"}}
	a.Jobs = []*autoJob{{TaskID: 3, Role: "planner", Provider: "claude", Status: "running"}}
	if _, err = s.autoQuotaProvider(a, now); err == nil {
		t.Fatal("running Claude escaped its own quota check")
	}
}

func TestAutonomyContinuationCannotLaunderRejectedDecisions(t *testing.T) {
	state, _ := autonomy.NewState("2026-09-24")
	state.Assignments = []autonomy.Assignment{{TaskID: 1, Role: "builder", Completed: true}, {TaskID: 2, Role: "decision_a", Completed: true}, {TaskID: 3, Role: "decision_b", Completed: true}}
	state.Reports[2] = json.RawMessage(`{"approve":false,"reason":"unsafe direction"}`)
	state.Reports[3] = json.RawMessage(`{"approve":true,"reason":"ok"}`)
	a := &autoRecord{Runs: []*autonomy.State{state}}
	if autoCheckpointApproved(a, 1) {
		t.Fatal("unresolved/rejected decision promoted")
	}
	state.Assignments = append(state.Assignments, autonomy.Assignment{TaskID: 4, Role: "reviewer", Completed: true})
	state.Reports[4] = json.RawMessage(`{"approve":false,"reason":"failed tests"}`)
	if autoCheckpointApproved(a, 1) {
		t.Fatal("rejected review promoted")
	}
	state.Reports[4] = json.RawMessage(`{"approve":true,"reason":"independent tests passed"}`)
	// A final review at another step cannot approve the earlier decision checkpoint.
	state.Assignments[3].Step = 1
	if autoCheckpointApproved(a, 1) {
		t.Fatal("different checkpoint review used")
	}
	state.Assignments[0].Step = 1
	if !autoCheckpointApproved(a, 1) {
		t.Fatal("approved matching checkpoint rejected")
	}
}

func TestAutonomyApprovalSurvivesHistoryRotation(t *testing.T) {
	a := &autoRecord{Jobs: []*autoJob{{TaskID: 1, Role: "builder", Status: "done", Approved: true, ReviewTaskID: 2}, {TaskID: 2, Role: "reviewer", Status: "done"}}}
	if !autoCheckpointApproved(a, 1) {
		t.Fatal("durable approval depended on history window")
	}
	a.Jobs[1].Status = "running"
	if autoCheckpointApproved(a, 1) {
		t.Fatal("unfinished reviewer approved artifact")
	}
}
