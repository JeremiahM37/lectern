package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

func TestAdmissionSurvivesReservationExhaustionAndHistoryRotation(t *testing.T) {
	s, a, project, source := repairFixture(t)
	a.Config.MaxRevisionRounds = 1
	if _, err := s.autoRepairContinuation(a, project, source); err != nil {
		t.Fatal(err)
	}
	j := &autoJob{ID: "bound-worker", TaskID: 101, Role: "builder", Status: "prepared", RepairSourceTaskID: source, RepairAttemptTaskID: 101}
	j.Admission = autoNewAdmission(a, j)
	if j.Admission == nil {
		t.Fatal("audited assignment lacks admission")
	}
	a.Jobs = append(a.Jobs, j)
	if len(s.autoRepairableArtifacts(a)) != 0 {
		t.Fatal("reservation must exhaust future availability")
	}
	if autoCheckpointApproved(a, source) {
		t.Fatal("reservation promoted rejected source")
	}
	a.Runs = nil
	a.State, _ = autonomy.NewState("2026-09-26")
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.autoJobReadBridge(j.ID)(w, httptest.NewRequest("GET", "/assignment?job_id=other&task_id=999", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var got autoAdmission
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.TaskID != j.TaskID || got.Proposal.RepairTaskID != source || got.RepairAttemptTaskID != 101 || got.Date != "2026-09-25" {
		t.Fatalf("wrong reservation: %+v", got)
	}
	for _, tc := range []struct {
		job, method string
		code        int
	}{{"other", "GET", 404}, {j.ID, "POST", 405}} {
		w = httptest.NewRecorder()
		s.autoJobReadBridge(tc.job)(w, httptest.NewRequest(tc.method, "/assignment", nil))
		if w.Code != tc.code {
			t.Fatalf("%+v: %d", tc, w.Code)
		}
	}
}

func TestAdmissionRequiresCurrentAuditsAndBuilder(t *testing.T) {
	_, a, _, _ := repairFixture(t)
	j := &autoJob{ID: "worker", TaskID: 101, Role: "planner"}
	if autoNewAdmission(a, j) != nil {
		t.Fatal("planner admitted")
	}
	j.Role = "builder"
	delete(a.State.Audits, "auditor_b")
	if autoNewAdmission(a, j) != nil {
		t.Fatal("missing independent audit admitted")
	}
}

func TestExplicitIncompleteReceiptCannotBePromotedByLegacyApproval(t *testing.T) {
	s, a, project, source := repairFixture(t)
	j := autoFindJob(a, source)
	j.Approved = true
	j.ReviewTaskID = a.Jobs[1].TaskID
	j.ReviewOutcome = "incomplete"
	// Retained legacy prose once said approve=true for an honest stop.
	a.Runs[0].Reports[j.ReviewTaskID] = json.RawMessage(`{"approve":true,"reason":"honest incomplete stop"}`)
	if err := s.saveAuto(a); err != nil {
		t.Fatal(err)
	}
	a, err := s.loadAuto()
	if err != nil {
		t.Fatal(err)
	}
	if autoCheckpointApproved(a, source) {
		t.Fatal("incomplete receipt promoted by legacy bool")
	}
	if _, err := s.autoApprovedContinuation(a, project, source); err == nil {
		t.Fatal("incomplete artifact continued as accepted")
	}
}
