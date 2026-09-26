package autonomy

import (
	"encoding/json"
	"testing"
)

func TestTypedOutcomesDoNotPromoteAnHonestBlockedStop(t *testing.T) {
	for _, outcome := range []string{"blocked", "incomplete", "completed"} {
		t.Run(outcome, func(t *testing.T) {
			s := decisionReady(t)
			if err := s.RegisterTask("builder", 4); err != nil {
				t.Fatal(err)
			}
			s.Assignments[len(s.Assignments)-1].ReportVersion = 2
			if err := s.ApplyReport(enabled(), 4, []byte(`{"summary":"stopped","evidence":["checked files"]}`)); err == nil {
				t.Fatal("new builder omitted outcome")
			}
			b := []byte(`{"outcome":"` + outcome + `","summary":"result","evidence":["checked files"]}`)
			if err := s.ApplyReport(enabled(), 4, b); err != nil {
				t.Fatal(err)
			}
			if err := s.RegisterTask("reviewer", 5); err != nil {
				t.Fatal(err)
			}
			s.Assignments[len(s.Assignments)-1].ReportVersion = 2
			// Restart preserves the requirement and the builder's evidence.
			raw, _ := json.Marshal(s)
			var restored State
			if err := json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			s = &restored
			for _, invalid := range []string{
				`{"approve":true,"reason":"honest report"}`,
				`{"outcome":"blocked","approve":true,"reason":"honest stop"}`,
				`{"outcome":"incomplete","approve":true,"reason":"honest stop"}`,
			} {
				before, _ := json.Marshal(s)
				if err := s.ApplyReport(enabled(), 5, []byte(invalid)); err == nil {
					t.Fatal("invalid approval accepted", invalid)
				}
				after, _ := json.Marshal(s)
				if string(before) != string(after) {
					t.Fatal("failed validation mutated state")
				}
			}
			approval := []byte(`{"outcome":"completed","approve":true,"reason":"independently tested"}`)
			if outcome == "completed" {
				if err := s.ApplyReport(enabled(), 5, approval); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := s.ApplyReport(enabled(), 5, approval); err == nil {
					t.Fatal("reviewer promoted unfinished builder")
				}
				if err := s.ApplyReport(enabled(), 5, []byte(`{"outcome":"`+outcome+`","approve":false,"reason":"prerequisite absent"}`)); err != nil {
					t.Fatal(err)
				}
				if s.Item != 0 {
					t.Fatal("uncompleted item advanced")
				}
			}
		})
	}
}

func TestLegacyBuildAndReviewRemainValid(t *testing.T) {
	s := decisionReady(t)
	deliver(t, s, "builder", 4, `{"summary":"result","evidence":["tested"]}`)
	deliver(t, s, "reviewer", 5, `{"approve":true,"reason":"verified"}`)
}

func TestReadyForReviewAutomaticallySchedulesIndependentReviewer(t *testing.T) {
	s := decisionReady(t)
	if err := s.RegisterTask("builder", 4); err != nil {
		t.Fatal(err)
	}
	s.Assignments[len(s.Assignments)-1].ReportVersion = 2
	if err := s.ApplyReport(enabled(), 4, []byte(`{"outcome":"ready_for_review","summary":"implementation and tests finished; awaits independent review","evidence":["full tests passed"]}`)); err != nil {
		t.Fatal(err)
	}
	if s.Phase != Review {
		t.Fatal("builder needs manual intervention instead of automatic review")
	}
	roles := s.NeededRoles()
	if len(roles) != 1 || roles[0] != "reviewer" {
		t.Fatal(roles)
	}
	if err := s.RegisterTask("reviewer", 5); err != nil {
		t.Fatal(err)
	}
	s.Assignments[len(s.Assignments)-1].ReportVersion = 2
	if err := s.ApplyReport(enabled(), 5, []byte(`{"outcome":"completed","approve":true,"reason":"independently reproduced all acceptance checks"}`)); err != nil {
		t.Fatal(err)
	}
	if s.Phase != Complete {
		t.Fatal(s.Phase)
	}
}
