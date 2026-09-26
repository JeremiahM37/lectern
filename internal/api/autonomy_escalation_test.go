package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

func repeatedRepairFixture() *autoRecord {
	return &autoRecord{Config: autonomy.DefaultConfig(), State: &autonomy.State{Items: []autonomy.Proposal{{RepairTaskID: 3}}}, Jobs: []*autoJob{
		{TaskID: 1, Role: "builder", Status: "done", Rejected: true, ReviewTaskID: 2, ReviewReason: "incorrect output"},
		{TaskID: 2, Role: "reviewer", Status: "done"},
		{TaskID: 3, Role: "builder", Status: "done", RepairSourceTaskID: 1, RepairAttemptTaskID: 3, Rejected: true, ReviewTaskID: 4, ReviewReason: "repair still incorrect"},
		{TaskID: 4, Role: "reviewer", Status: "done"},
	}}
}

func TestAutonomyEscalatesRepeatedReviewedFailure(t *testing.T) {
	a := repeatedRepairFixture()
	before, _ := json.Marshal(a.State.Items)
	if !autoExpertBuilder(a) {
		t.Fatal("repeated reviewed failure not escalated")
	}
	if autoModelChoice("builder", "codex", autoExpertBuilder(a), []string{"gpt-6-astra", "gpt-6-luna"}) != "gpt-6-astra" {
		t.Fatal("strong Codex tier not selected")
	}
	if autoModelChoice("builder", "claude", autoExpertBuilder(a), nil) != "opus" {
		t.Fatal("strong Claude tier not selected")
	}
	after, _ := json.Marshal(a.State.Items)
	if string(before) != string(after) {
		t.Fatal("routing changed audited proposal")
	}
	if len(a.Jobs) != 4 || a.Jobs[2].RepairAttemptTaskID != 3 {
		t.Fatal("routing reset repair history")
	}
}

func TestAutonomyEscalationNeedsTwoIndependentRejections(t *testing.T) {
	for _, name := range []string{"first repair", "process failure", "review pending", "approved repair", "no original rejection", "ordinary builder"} {
		t.Run(name, func(t *testing.T) {
			a := repeatedRepairFixture()
			switch name {
			case "first repair":
				a.State.Items[0].RepairTaskID = 1
			case "process failure":
				a.Jobs[2].Status = "failed"
			case "review pending":
				a.Jobs[3].Status = "running"
			case "approved repair":
				a.Jobs[2].Approved = true
				a.Jobs[2].Rejected = false
				a.Jobs[2].ReviewOutcome = "completed"
			case "no original rejection":
				a.Jobs[0].Rejected = false
			case "ordinary builder":
				a.State.Items[0].RepairTaskID = 0
			}
			if autoExpertBuilder(a) {
				t.Fatal("unnecessary escalation")
			}
		})
	}
}

func TestAutonomyEscalationRetainsQuotaGate(t *testing.T) {
	a := repeatedRepairFixture()
	// No fresh quota means neither provider may run, even on a strong-model repair.
	if _, _, err := (&Server{}).autoRoute(a, "builder", time.Now()); err == nil {
		t.Fatal("escalation bypassed quota")
	}
}
