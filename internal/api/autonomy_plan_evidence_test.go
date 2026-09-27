package api

import (
	"context"
	"encoding/json"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"strings"
	"testing"
)

func planEvidenceFixture() *autoRecord {
	st, _ := autonomy.NewState("2026-09-26")
	st.Phase = autonomy.Audit
	st.Revision = 1
	st.Assignments = []autonomy.Assignment{{TaskID: 1, Role: "planner", Round: 0, Completed: true}, {TaskID: 2, Role: "planner", Round: 1, Completed: true}}
	return &autoRecord{Config: autonomy.DefaultConfig(), State: st, Jobs: []*autoJob{
		{ID: "11111111-1111-4111-8111-111111111111", TaskID: 1, Role: "planner", Status: "done"},
		{ID: "22222222-2222-4222-8222-222222222222", TaskID: 2, Role: "planner", Status: "done"}}}
}
func TestPlanEvidenceBindsCurrentRevisionAfterReload(t *testing.T) {
	a := planEvidenceFixture()
	raw, _ := json.Marshal(a)
	var restored autoRecord
	json.Unmarshal(raw, &restored)
	j, err := autoPlanEvidence(&restored)
	if err != nil || j.TaskID != 2 {
		t.Fatalf("wrong planner: %v %v", j, err)
	}
	// A resumed final job replaces an interrupted receipt, not the assignment.
	restored.Jobs[1].Status = "stopped"
	resumed := *restored.Jobs[1]
	resumed.ID = "33333333-3333-4333-8333-333333333333"
	resumed.Status = "done"
	restored.Jobs = append(restored.Jobs, &resumed)
	j, err = autoPlanEvidence(&restored)
	if err != nil || j.ID != resumed.ID {
		t.Fatalf("wrong resumed source: %v %v", j, err)
	}
}
func TestPlanEvidenceRejectsUnavailableOrAmbiguousSources(t *testing.T) {
	for _, kind := range []string{"running", "missing", "role", "path", "old revision", "unfinished", "ambiguous", "phase"} {
		t.Run(kind, func(t *testing.T) {
			a := planEvidenceFixture()
			switch kind {
			case "running":
				a.Jobs[1].Status = "running"
			case "missing":
				a.Jobs = a.Jobs[:1]
			case "role":
				a.Jobs[1].Role = "auditor_a"
			case "path":
				a.Jobs[1].ID = "../../outside"
			case "old revision":
				a.State.Revision = 2
			case "unfinished":
				a.State.Assignments[1].Completed = false
			case "ambiguous":
				a.State.Assignments = append(a.State.Assignments, a.State.Assignments[1])
			case "phase":
				a.State.Phase = autonomy.Build
			}
			if _, err := autoPlanEvidence(a); err == nil {
				t.Fatal("invalid source accepted")
			}
		})
	}
}
func TestPlanEvidenceAuditorsShareSourceNotVerdicts(t *testing.T) {
	s := autoTestServer(t)
	a := planEvidenceFixture()
	yes := true
	a.State.Audits = map[string]autonomy.Verdict{"auditor_a": {Approve: &yes, Reason: "MASKED-PEER-VERDICT"}}
	for _, role := range []string{"auditor_a", "auditor_b"} {
		p := s.autoPrompt(context.Background(), a, role, &store.Project{})
		if !strings.Contains(p, autoPlanEvidencePath(a.Jobs[1])) || !strings.Contains(p, "read-only") {
			t.Fatal("snapshot guidance absent")
		}
		if strings.Contains(p, "MASKED-PEER-VERDICT") {
			t.Fatal("peer verdict leaked")
		}
	}
}
