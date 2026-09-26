package autonomy

import (
	"encoding/json"
	"strings"
	"testing"
)

const emptyJustified = `{"items":[],"no_work":{"reason":"Current opportunities have insufficient evidence of value","blockers":[],"exploration":[{"opportunity":"A distinct CPU-only source comparison","decision":"Inspected evidence does not establish an unmet need yet","evidence":["retained comparison and retrieval failure logs"]}]}}`

func emptyPlanState(t *testing.T, version int, report string) *State {
	t.Helper()
	s, err := NewState("2026-09-25")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RegisterTask("planner", 1); err != nil {
		t.Fatal(err)
	}
	s.Assignments[0].ReportVersion = version
	if err = s.ApplyReport(enabled(), 1, []byte(report)); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEmptyPlanRequiresBothAuditsAndNeverBuilds(t *testing.T) {
	for _, version := range []int{0, 3} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			report := emptyJustified
			if version == 0 {
				report = `{"items":[]}`
			}
			s := emptyPlanState(t, version, report)
			if s.Phase != Audit || strings.Join(s.NeededRoles(), ",") != "auditor_a,auditor_b" {
				t.Fatalf("audit bypass: %+v", s)
			}
			if s.RegisterTask("builder", 99) == nil {
				t.Fatal("empty plan dispatched builder")
			}
			if err := s.RegisterTask("auditor_a", 2); err != nil {
				t.Fatal(err)
			}
			if err := s.ApplyReport(enabled(), 2, []byte(`{"approve":true,"reason":"first independent no-work assessment"}`)); err != nil {
				t.Fatal(err)
			}
			if s.Phase != Audit {
				t.Fatal("single audit completed cycle")
			}
			// Restart retains exact no-work evidence and only the remaining audit.
			raw, _ := json.Marshal(s)
			var restored State
			if err := json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			s = &restored
			if strings.Join(s.NeededRoles(), ",") != "auditor_b" {
				t.Fatal(s.NeededRoles())
			}
			if version == 3 && (s.NoWork == nil || len(s.NoWork.Exploration) != 1) {
				t.Fatal("lost exploration evidence")
			}
			if err := s.RegisterTask("auditor_b", 3); err != nil {
				t.Fatal(err)
			}
			if err := s.ApplyReport(enabled(), 3, []byte(`{"approve":true,"reason":"second independent no-work assessment"}`)); err != nil {
				t.Fatal(err)
			}
			if s.Phase != Complete || len(s.NeededRoles()) != 0 || s.RegisterTask("builder", 99) == nil {
				t.Fatal("approved empty plan could build")
			}
		})
	}
}

func TestEmptyPlanRejectionUsesBoundedRevisions(t *testing.T) {
	c := enabled()
	s := emptyPlanState(t, 3, emptyJustified)
	id := int64(2)
	for round := 0; round <= c.MaxRevisionRounds; round++ {
		for _, role := range []string{"auditor_a", "auditor_b"} {
			if err := s.RegisterTask(role, id); err != nil {
				t.Fatal(err)
			}
			if err := s.ApplyReport(c, id, []byte(`{"approve":false,"reason":"Known blockers do not justify skipping distinct exploration"}`)); err != nil {
				t.Fatal(err)
			}
			id++
		}
		if round == c.MaxRevisionRounds {
			if s.Phase != Complete {
				t.Fatal("unbounded audit loop")
			}
			break
		}
		if s.Phase != Revise {
			t.Fatal("rejection skipped planner revision")
		}
		if err := s.RegisterTask("planner", id); err != nil {
			t.Fatal(err)
		}
		s.Assignments[len(s.Assignments)-1].ReportVersion = 3
		if err := s.ApplyReport(c, id, []byte(emptyJustified)); err != nil {
			t.Fatal(err)
		}
		id++
	}
	if len(s.NeededRoles()) != 0 {
		t.Fatal("exhausted empty plan dispatches work")
	}
}

func TestNoWorkSchemaFailureDoesNotMutateState(t *testing.T) {
	for _, report := range []string{
		`{"items":[]}`,
		`{"items":[],"no_work":{"reason":"x","exploration":[]}}`,
		strings.Replace(emptyJustified, `"retained comparison and retrieval failure logs"`, `" "`, 1),
		strings.Replace(emptyJustified, `"blockers":[]`, `"blockers":[{"key":"same","requirement":"need source","evidence":["a"]},{"key":"same","requirement":"need source","evidence":["b"]}]`, 1),
	} {
		s, _ := NewState("2026-09-25")
		_ = s.RegisterTask("planner", 1)
		s.Assignments[0].ReportVersion = 3
		before, _ := json.Marshal(s)
		if err := s.ApplyReport(enabled(), 1, []byte(report)); err == nil {
			t.Fatal("invalid empty report accepted", report)
		}
		after, _ := json.Marshal(s)
		if string(after) != string(before) {
			t.Fatal("validation mutated state")
		}
	}
}

func TestNoWorkDoesNotBypassOffOrLeakIntoSelectedRevision(t *testing.T) {
	s := emptyPlanState(t, 3, emptyJustified)
	_ = s.RegisterTask("auditor_a", 2)
	c := enabled()
	c.Enabled = false
	before, _ := json.Marshal(s)
	if s.ApplyReport(c, 2, []byte(`{"approve":true,"reason":"still off"}`)) == nil {
		t.Fatal("OFF bypass")
	}
	after, _ := json.Marshal(s)
	if string(before) != string(after) {
		t.Fatal("OFF mutated state")
	}
	c.Enabled = true
	_ = s.ApplyReport(c, 2, []byte(`{"approve":false,"reason":"feasible alternative"}`))
	_ = s.RegisterTask("auditor_b", 3)
	_ = s.ApplyReport(c, 3, []byte(`{"approve":true,"reason":"independent disagreement"}`))
	_ = s.RegisterTask("planner", 4)
	if err := s.ApplyReport(c, 4, []byte(validPlan)); err != nil {
		t.Fatal(err)
	}
	if s.NoWork != nil || len(s.Items) == 0 || len(s.Audits) != 0 {
		t.Fatal("stale no-work evidence or votes survived revision")
	}
}
