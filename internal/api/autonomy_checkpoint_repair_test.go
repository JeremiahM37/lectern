package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func repairFixture(t *testing.T) (*Server, *autoRecord, int64, int64) {
	t.Helper()
	s := autoTestServer(t)
	target, err := s.DB.InsertTarget(&store.Target{Name: "repair", Kind: "mock"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.DB.InsertProject(&store.Project{Name: "repair", TargetID: target.ID, RepoPath: "/unused"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.DB.InsertTask(&store.Task{ProjectID: p.ID, Title: "rejected checkpoint", Status: "review", CreatedBy: autoOwner})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.DB.InsertTask(&store.Task{ProjectID: p.ID, Title: "review", Status: "done", CreatedBy: autoOwner})
	if err != nil {
		t.Fatal(err)
	}
	old, _ := autonomy.NewState("2026-09-24")
	old.Assignments = []autonomy.Assignment{{TaskID: b.ID, Role: "builder", Completed: true}, {TaskID: r.ID, Role: "reviewer", Completed: true}}
	old.Reports[r.ID] = json.RawMessage(`{"approve":false,"reason":"existing tests still fail"}`)
	state, _ := autonomy.NewState("2026-09-25")
	state.Phase = autonomy.Build
	yes := true
	state.Audits = map[string]autonomy.Verdict{"auditor_a": {Approve: &yes, Reason: "repair justified"}, "auditor_b": {Approve: &yes, Reason: "independent repair audit"}}
	state.Items = []autonomy.Proposal{{ProjectID: p.ID, RepairTaskID: b.ID, Title: "repair", Why: "failed tests", Acceptance: []string{"tests pass"}}}
	a := &autoRecord{Config: autonomy.DefaultConfig(), State: state, Runs: []*autonomy.State{old}, Jobs: []*autoJob{{TaskID: b.ID, Role: "builder", Status: "done"}, {TaskID: r.ID, Role: "reviewer", Status: "done"}}}
	a.Config.Enabled = true
	return s, a, p.ID, b.ID
}
func TestRejectedCheckpointRepairNeverPromotesAndSurvivesRotation(t *testing.T) {
	s, a, project, id := repairFixture(t)
	if _, err := s.autoApprovedContinuation(a, project, id); err == nil {
		t.Fatal("rejected checkpoint promoted")
	}
	if _, err := s.autoRepairContinuation(a, project, id); err != nil {
		t.Fatal(err)
	}
	if !autoRepairAudited(a) {
		t.Fatal("audited repair rejected")
	}
	if len(s.autoRepairableArtifacts(a)) != 1 {
		t.Fatal("legacy rejected checkpoint not discoverable")
	}
	a.Runs = nil
	if _, err := s.autoRepairContinuation(a, project, id); err != nil {
		t.Fatal("rejection receipt lost after history rotation", err)
	}
	if autoCheckpointApproved(a, id) {
		t.Fatal("repair granted approval")
	}
	if _, err := s.autoRepairContinuation(a, project+1, id); err == nil {
		t.Fatal("cross-project repair")
	}
	a.State.Audits = map[string]autonomy.Verdict{}
	if autoRepairAudited(a) {
		t.Fatal("unaudited repair permitted")
	}
}
func TestRepairRejectsMissingMismatchedOrNonfinalVerdicts(t *testing.T) {
	for _, kind := range []string{"missing receipt", "live reviewer", "decision", "different step", "different round", "approved", "missing verdict"} {
		t.Run(kind, func(t *testing.T) {
			s, a, project, id := repairFixture(t)
			state := a.Runs[0]
			switch kind {
			case "missing receipt":
				a.Jobs = a.Jobs[:1]
			case "live reviewer":
				a.Jobs[1].Status = "running"
			case "decision":
				state.Assignments[1].Role = "decision_a"
			case "different step":
				state.Assignments[1].Step = 1
			case "different round":
				state.Assignments[1].Round = 1
			case "approved":
				state.Reports[a.Jobs[1].TaskID] = json.RawMessage(`{"approve":true,"reason":"passed"}`)
			case "missing verdict":
				state.Reports = map[int64]json.RawMessage{}
			}
			if _, err := s.autoRepairContinuation(a, project, id); err == nil {
				t.Fatal("invalid repair accepted")
			}
		})
	}
}
func TestRepairBudgetCountsUniqueAttemptsAcrossLineage(t *testing.T) {
	s, a, project, id := repairFixture(t)
	// Repeated receipts for a resumed worker count once, not as extra repair attempts.
	a.Jobs = append(a.Jobs, &autoJob{TaskID: 101, Role: "builder", Status: "stopped", RepairSourceTaskID: id}, &autoJob{TaskID: 101, Role: "builder", Status: "done", RepairSourceTaskID: id})
	if _, err := s.autoRepairContinuation(a, project, id); err != nil {
		t.Fatal(err)
	}
	// A resumed builder after an approved decision shares the attempt identity.
	a.Jobs = append(a.Jobs, &autoJob{TaskID: 102, Role: "builder", Status: "done", RepairSourceTaskID: id, RepairAttemptTaskID: 101})
	if _, err := s.autoRepairContinuation(a, project, id); err != nil {
		t.Fatal("decision checkpoint counted as new repair", err)
	}
	// A repair of that rejected resumed builder still belongs to the same root.
	a.Jobs = append(a.Jobs, &autoJob{TaskID: 103, Role: "builder", Status: "prepared", RepairSourceTaskID: 102, RepairAttemptTaskID: 103})
	if _, err := s.autoRepairContinuation(a, project, id); err == nil {
		t.Fatal("unbounded repair loop")
	}
}
func TestLegacyRejectedContinuationReplansWithoutGrantingApproval(t *testing.T) {
	s, a, _, id := repairFixture(t)
	a.State.Items[0].RepairTaskID = 0
	a.State.Items[0].ContinueTaskID = id
	a.State.Pause("continuation checkpoint has no approved final review; rejected or unresolved work cannot be promoted")
	a.Config.Enabled = false
	if s.recoverRejectedContinuation(a) {
		t.Fatal("OFF changed state")
	}
	a.Config.Enabled = true
	if !s.recoverRejectedContinuation(a) || a.State.Phase != autonomy.Complete {
		t.Fatal("known blocker remained paused")
	}
	if autoCheckpointApproved(a, id) || a.State.Items[0].RepairTaskID != 0 {
		t.Fatal("old plan reinterpreted as approval")
	}
	now := time.Now()
	if autoCycleDue(a, now) {
		t.Fatal("no cooldown")
	}
	if !autoCycleDue(a, now.Add(time.Minute)) {
		t.Fatal("did not schedule replan")
	}
}
func TestPlannerSourcesRejectInvalidContinuationBeforeAudits(t *testing.T) {
	s, a, project, id := repairFixture(t)
	if err := s.validateAutoSources(a, []autonomy.Proposal{{ProjectID: project, ContinueTaskID: id}}); err == nil {
		t.Fatal("rejected source reached audits")
	}
	if err := s.validateAutoSources(a, []autonomy.Proposal{{ProjectID: project, RepairTaskID: id}}); err != nil {
		t.Fatal(err)
	}
	if err := s.validateAutoSources(a, []autonomy.Proposal{{ProjectID: project, ContinueTaskID: id, RepairTaskID: id}}); err == nil {
		t.Fatal("ambiguous source")
	}
}

func TestLegacyRepairReceiptBackfilledBeforeHistoryRotation(t *testing.T) {
	s, a, project, id := repairFixture(t)
	for len(a.Runs) < 90 {
		state, _ := autonomy.NewState("2026-09-24")
		a.Runs = append(a.Runs, state)
	}
	autoNewCycle(a, time.Now())
	if len(a.Runs) != 90 || !a.Jobs[0].Rejected {
		t.Fatal("expired rejection not persisted")
	}
	if _, err := s.autoRepairContinuation(a, project, id); err != nil {
		t.Fatal(err)
	}
}

func TestRepairEvidenceIsSeparateAndNeverApproval(t *testing.T) {
	s, a, projectID, builderID := repairFixture(t)
	reviewer := a.Jobs[1]
	reviewer.ID = "11111111-1111-4111-8111-111111111111"
	rows := s.autoRepairableArtifacts(a)
	want := "/work/.lectern-review/" + reviewer.ID + "/work"
	if len(rows) != 1 || rows[0]["review_evidence"] != want || rows[0]["approved"] != false {
		t.Fatalf("missing separate unapproved review evidence: %#v", rows)
	}
	project, err := s.DB.Project(projectID)
	if err != nil {
		t.Fatal(err)
	}
	prompt := s.autoPrompt(context.Background(), a, "builder", project)
	if !strings.Contains(prompt, want) || !strings.Contains(prompt, "not approval") || !strings.Contains(prompt, "existing tests still fail") {
		t.Fatal("repair prompt omits reviewer evidence or rejection boundary")
	}
	if autoCheckpointApproved(a, builderID) {
		t.Fatal("review evidence granted approval")
	}
}
