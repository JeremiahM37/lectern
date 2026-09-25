package autonomy

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func enabled() Config           { c := DefaultConfig(); c.Enabled = true; return c }
func number(v float64) *float64 { return &v }
func quota(now time.Time) []ProviderUsage {
	return []ProviderUsage{{ID: "codex", Status: "ok", UpdatedAt: number(float64(now.Unix())), Buckets: []UsageBucket{{ID: "codex", Windows: []UsageWindow{{Label: "Session", UsedPercent: number(20), RemainingPercent: number(80), ResetsAt: number(float64(now.Add(time.Hour).Unix()))}, {Label: "Weekly", UsedPercent: number(30), RemainingPercent: number(70), ResetsAt: number(float64(now.Add(24 * time.Hour).Unix()))}}}}}}
}

func TestContinuousDefaultsAndProposalBacklog(t *testing.T) {
	c := DefaultConfig()
	if !c.Continuous {
		t.Fatal("continuous mode must default on")
	}
	if err := json.Unmarshal([]byte(`{"enabled":true,"max_items_per_day":3}`), &c); err != nil || !c.Continuous {
		t.Fatalf("legacy config did not inherit continuous default: %+v, %v", c, err)
	}
	s, _ := NewState("2026-09-24")
	if err := s.RegisterTask("planner", 1); err != nil {
		t.Fatal(err)
	}
	proposal := `{"project_id":1,"title":"Deep work","why":"Value","acceptance":["Measured"],"continue_task_id":47,"ambition":"Systemic","novelty":"New method","score":91,"expert":true}`
	valid := `{"items":[` + proposal + `],"backlog":[` + proposal + `]}`
	for _, report := range []string{
		`{"items":[` + proposal + `],"backlog":[` + proposal + `,` + proposal + `]}`,
		strings.Replace(valid, `"score":91`, `"score":101`, 1),
		strings.Replace(valid, `"continue_task_id":47`, `"continue_task_id":-1`, 1),
	} {
		if err := s.ApplyReport(enabled(), 1, []byte(report)); err == nil {
			t.Fatalf("invalid proposal accepted: %s", report)
		}
	}
	if err := s.ApplyReport(enabled(), 1, []byte(valid)); err != nil {
		t.Fatal(err)
	}
	if len(s.Backlog) != 1 || !s.Items[0].Expert || s.Items[0].ContinueTaskID != 47 || s.Items[0].Score != 91 {
		t.Fatalf("plan lost new fields: %+v", s)
	}
}

const decisionBuild = `{"summary":"Investigated architecture","evidence":["Measured two paths"],"decision":{"title":"Change scheduler","rationale":"Current scheduling misses work","alternatives":["Keep current scheduler","Use event queue"],"risks":["Extra load"]}}`

func decisionReady(t *testing.T) *State {
	t.Helper()
	s, _ := NewState("2026-09-24")
	deliver(t, s, "planner", 1, validPlan)
	deliver(t, s, "auditor_a", 2, `{"approve":true,"reason":"Useful"}`)
	deliver(t, s, "auditor_b", 3, `{"approve":true,"reason":"Sound"}`)
	return s
}

func TestDecisionRequiresTwoIndependentAuditsAndResumes(t *testing.T) {
	s := decisionReady(t)
	deliver(t, s, "builder", 4, decisionBuild)
	if s.Phase != DecisionAudit || s.Step != 0 || s.Decision == nil {
		t.Fatalf("decision did not enter audit: %+v", s)
	}
	if err := s.RegisterTask("builder", 5); err == nil {
		t.Fatal("builder dispatched while decision pending")
	}
	if err := s.RegisterTask("decision_a", 4); err == nil {
		t.Fatal("builder's task ID reused for self-approval")
	}
	deliver(t, s, "decision_a", 5, `{"approve":true,"reason":"Evidence supports it"}`)
	if s.Phase != DecisionAudit || len(s.DecisionAudits) != 1 {
		t.Fatal("one verdict advanced decision")
	}
	if err := s.ApplyReport(enabled(), 5, []byte(`{"approve":true,"reason":"Again"}`)); err == nil {
		t.Fatal("duplicate decision verdict accepted")
	}
	if err := s.RegisterTask("decision_b", 5); err == nil {
		t.Fatal("one owned task accepted for both verdicts")
	}
	if err := s.RegisterTask("decision_b", 6); err != nil {
		t.Fatal(err)
	}
	s.Pause("quota")
	now := time.Unix(1800000000, 0)
	if err := s.Resume(enabled(), quota(now), []string{"codex"}, now); err != nil {
		t.Fatal(err)
	}
	if s.Phase != DecisionAudit || len(s.NeededRoles()) != 0 {
		t.Fatal("resume lost outstanding decision task")
	}
	if err := s.ApplyReport(enabled(), 6, []byte(`{"approve":true,"reason":"Independent check"}`)); err != nil {
		t.Fatal(err)
	}
	if s.Phase != Build || s.Step != 1 || len(s.DecisionAudits) != 2 || s.Decision == nil {
		t.Fatalf("two approvals did not resume builder with context: %+v", s)
	}
	if s.Assignments[len(s.Assignments)-1].Step != 0 {
		t.Fatal("decision assignment step changed")
	}
	if err := s.RegisterTask("builder", 7); err != nil {
		t.Fatal(err)
	}
	if s.Assignments[len(s.Assignments)-1].Step != 1 {
		t.Fatal("new builder assignment lacks incremented step")
	}
	if err := s.ApplyReport(enabled(), 7, []byte(`{"summary":"Implemented approved direction","evidence":["Tests pass"]}`)); err != nil || s.Phase != Review {
		t.Fatalf("ordinary build could not finish: %v, %+v", err, s)
	}
}

func TestDecisionRejectionBoundedAndInvalidReports(t *testing.T) {
	s := decisionReady(t)
	if err := s.RegisterTask("builder", 4); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"summary":"Work","evidence":["Proof"],"decision":{"title":"","rationale":"Why","alternatives":["A"],"risks":["R"]}}`,
		`{"summary":"Work","evidence":["Proof"],"decision":{"title":"X","rationale":"Why","alternatives":[""],"risks":["R"]}}`,
		`{"summary":"Work","evidence":[],"decision":{"title":"X","rationale":"Why","alternatives":["A"],"risks":["R"]}}`,
	} {
		if err := s.ApplyReport(enabled(), 4, []byte(raw)); err == nil || s.Phase != Build {
			t.Fatalf("invalid decision report changed state: %v, %+v", err, s)
		}
	}
	for round := 0; round < 4; round++ {
		builder := int64(4 + round*3)
		if round > 0 {
			if err := s.RegisterTask("builder", builder); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.ApplyReport(enabled(), builder, []byte(decisionBuild)); err != nil {
			t.Fatal(err)
		}
		deliver(t, s, "decision_a", builder+1, `{"approve":true,"reason":"Plausible"}`)
		deliver(t, s, "decision_b", builder+2, `{"approve":false,"reason":"Try the alternative"}`)
		if s.Step != round+1 || s.Decision == nil || len(s.DecisionAudits) != 2 || *s.DecisionAudits["decision_b"].Approve {
			t.Fatalf("rejection context lost: %+v", s)
		}
		if round < 3 && (s.Phase != Build || len(s.NeededRoles()) != 1) {
			t.Fatalf("round %d did not return to builder: %+v", round, s)
		}
	}
	if s.Phase != Complete || !strings.Contains(strings.ToLower(s.Reason), "four rounds") || len(s.NeededRoles()) != 0 {
		t.Fatalf("disagreement was not bounded: %+v", s)
	}
}

func TestDecisionStepResetsForNextItem(t *testing.T) {
	s, _ := NewState("2026-09-24")
	plan := `{"items":[{"project_id":1,"title":"First","why":"Value","acceptance":["Proof"]},{"project_id":2,"title":"Second","why":"Value","acceptance":["Proof"]}]}`
	deliver(t, s, "planner", 1, plan)
	deliver(t, s, "auditor_a", 2, `{"approve":true,"reason":"Useful"}`)
	deliver(t, s, "auditor_b", 3, `{"approve":true,"reason":"Useful"}`)
	deliver(t, s, "builder", 4, decisionBuild)
	deliver(t, s, "decision_a", 5, `{"approve":true,"reason":"Sound"}`)
	deliver(t, s, "decision_b", 6, `{"approve":true,"reason":"Sound"}`)
	deliver(t, s, "builder", 7, `{"summary":"Done","evidence":["Check"]}`)
	deliver(t, s, "reviewer", 8, `{"approve":true,"reason":"Verified"}`)
	if s.Phase != Build || s.Item != 1 || s.Step != 0 || s.Decision != nil || s.DecisionAudits != nil {
		t.Fatalf("new item retained prior decision: %+v", s)
	}
	if len(s.NeededRoles()) != 1 || s.NeededRoles()[0] != "builder" {
		t.Fatalf("next builder not dispatched: %+v", s.NeededRoles())
	}
}

func TestApprovedDecisionRoundsAreAlsoBounded(t *testing.T) {
	s := decisionReady(t)
	for round := 0; round < 4; round++ {
		builder := int64(4 + round*3)
		deliver(t, s, "builder", builder, decisionBuild)
		deliver(t, s, "decision_a", builder+1, `{"approve":true,"reason":"Sound"}`)
		deliver(t, s, "decision_b", builder+2, `{"approve":true,"reason":"Independent proof"}`)
		if s.Phase != Build || s.Step != round+1 || len(s.Reports) != 3+3*(round+1) {
			t.Fatalf("approved round %d lost state: %+v", round, s)
		}
	}
	deliver(t, s, "builder", 16, decisionBuild)
	if s.Phase != Complete || !strings.Contains(s.Reason, "four rounds") || len(s.Reports) != 16 {
		t.Fatalf("fifth major decision escaped bound: %+v", s)
	}
}

func TestQuotaReserveAndBadTelemetry(t *testing.T) {
	now := time.Unix(1800000000, 0)
	for _, tc := range []struct {
		name   string
		change func([]ProviderUsage)
		pass   bool
	}{
		{"healthy", func([]ProviderUsage) {}, true},
		{"weekly reserve", func(p []ProviderUsage) {
			w := &p[0].Buckets[0].Windows[1]
			w.UsedPercent = number(85)
			w.RemainingPercent = number(15)
		}, false},
		{"session reserve", func(p []ProviderUsage) {
			w := &p[0].Buckets[0].Windows[0]
			w.UsedPercent = number(90)
			w.RemainingPercent = number(10)
		}, false},
		{"stale timestamp", func(p []ProviderUsage) { p[0].UpdatedAt = number(float64(now.Add(-361 * time.Second).Unix())) }, false},
		{"stale status", func(p []ProviderUsage) { p[0].Status = "stale" }, false},
		{"passed reset", func(p []ProviderUsage) { p[0].Buckets[0].Windows[0].ResetsAt = number(float64(now.Unix())) }, false},
		{"missing window", func(p []ProviderUsage) { p[0].Buckets[0].Windows = nil }, false},
		{"weekly missing", func(p []ProviderUsage) { p[0].Buckets[0].Windows = p[0].Buckets[0].Windows[:1] }, false},
		{"codex weekly-only plan", func(p []ProviderUsage) { p[0].Buckets[0].Windows = p[0].Buckets[0].Windows[1:] }, true},
		{"five-hour session", func(p []ProviderUsage) { p[0].Buckets[0].Windows[0].Label = "5-hour" }, true},
		{"unknown session label still constrains codex", func(p []ProviderUsage) { p[0].Buckets[0].Windows[0].Label = "primary" }, true},
		{"missing remaining", func(p []ProviderUsage) { p[0].Buckets[0].Windows[0].RemainingPercent = nil }, false},
		{"inconsistent", func(p []ProviderUsage) { p[0].Buckets[0].Windows[0].RemainingPercent = number(99) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := quota(now)
			tc.change(p)
			err := QuotaGate(enabled(), p, []string{"codex"}, now)
			if (err == nil) != tc.pass {
				t.Fatalf("gate=%v, want pass %v", err, tc.pass)
			}
		})
	}
	if QuotaGate(enabled(), quota(now), []string{"claude"}, now) == nil {
		t.Fatal("missing provider accepted")
	}
	if QuotaGate(enabled(), append(quota(now), quota(now)...), []string{"codex"}, now) == nil {
		t.Fatal("duplicate provider accepted")
	}
	claude := quota(now)
	claude[0].ID = "claude"
	claude[0].Buckets[0].Windows = claude[0].Buckets[0].Windows[1:]
	if QuotaGate(enabled(), claude, []string{"claude"}, now) == nil {
		t.Fatal("Claude missing session window accepted")
	}
}

const validPlan = `{"items":[{"project_id":1,"title":"Useful work","why":"Fix a daily frustration","acceptance":["Regression reproduced and fixed"]}]}`

func deliver(t *testing.T, s *State, role string, id int64, report string) {
	t.Helper()
	if err := s.RegisterTask(role, id); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyReport(enabled(), id, []byte(report)); err != nil {
		t.Fatal(err)
	}
}

func TestAuditsRequireIndependentAgreementAndBoundRevision(t *testing.T) {
	s, _ := NewState("2026-09-24")
	for round := 0; round < 3; round++ {
		base := int64(round*3 + 1)
		deliver(t, s, "planner", base, validPlan)
		deliver(t, s, "auditor_a", base+1, `{"approve":true,"reason":"Useful"}`)
		if s.Phase != Audit {
			t.Fatal("single audit allowed work")
		}
		if err := s.RegisterTask("auditor_b", base+1); err == nil {
			t.Fatal("same task could audit twice")
		}
		deliver(t, s, "auditor_b", base+2, `{"approve":false,"reason":"Missing value"}`)
		if round < 2 && s.Phase != Revise {
			t.Fatalf("round %d: %s", round, s.Phase)
		}
	}
	if s.Phase != Complete || s.Revision != 2 {
		t.Fatalf("unbounded revisions: %+v", s)
	}
	if len(s.NeededRoles()) != 0 {
		t.Fatal("work dispatched despite disagreement")
	}
}

func TestRestartDuplicatesAndReview(t *testing.T) {
	s, _ := NewState("2026-09-24")
	deliver(t, s, "planner", 1, validPlan)
	deliver(t, s, "auditor_a", 2, `{"approve":true,"reason":"Useful"}`)
	if err := s.RegisterTask("auditor_b", 3); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s)
	var restored State
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	s = &restored
	if len(s.NeededRoles()) != 0 {
		t.Fatal("restart would launch duplicate auditor")
	}
	before, _ := json.Marshal(s)
	if s.ApplyReport(enabled(), 2, []byte(`{"approve":false,"reason":"duplicate"}`)) == nil {
		t.Fatal("duplicate accepted")
	}
	after, _ := json.Marshal(s)
	if string(before) != string(after) {
		t.Fatal("duplicate mutated state")
	}
	if err := s.ApplyReport(enabled(), 3, []byte(`{"approve":true,"reason":"Sound"}`)); err != nil {
		t.Fatal(err)
	}
	if s.Phase != Build {
		t.Fatal(s.Phase)
	}
	deliver(t, s, "builder", 4, `{"summary":"Fixed","evidence":["Regression test passed"]}`)
	if s.Phase != Review {
		t.Fatal("build skipped review")
	}
	deliver(t, s, "reviewer", 5, `{"approve":true,"reason":"Verified"}`)
	if s.Phase != Complete || len(s.ActiveTaskIDs()) != 0 {
		t.Fatalf("not completed: %+v", s)
	}
}

func TestStrictReportsAndPause(t *testing.T) {
	s, _ := NewState("2026-09-24")
	if err := s.RegisterTask("planner", 1); err != nil {
		t.Fatal(err)
	}
	for _, report := range []string{`{"items":[],"command":"rm"}`, validPlan + ` {}`, `{"items":null}`, `{"items":[{"project_id":0}]}`, `{"items":null,"items":[]}`} {
		if s.ApplyReport(enabled(), 1, []byte(report)) == nil {
			t.Fatalf("accepted %s", report)
		}
	}
	s.Pause("quota unavailable")
	if s.ApplyReport(enabled(), 1, []byte(validPlan)) == nil {
		t.Fatal("paused report advanced")
	}
	now := time.Unix(1800000000, 0)
	if s.Resume(enabled(), nil, []string{"codex"}, now) == nil {
		t.Fatal("resumed without quota")
	}
	if err := s.Resume(enabled(), quota(now), []string{"codex"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyReport(enabled(), 1, []byte(validPlan)); err != nil {
		t.Fatal(err)
	}
}
