package autonomy

import (
	"encoding/json"
	"testing"
	"time"
)

func enabled() Config           { c := DefaultConfig(); c.Enabled = true; return c }
func number(v float64) *float64 { return &v }
func quota(now time.Time) []ProviderUsage {
	return []ProviderUsage{{ID: "codex", Status: "ok", UpdatedAt: number(float64(now.Unix())), Buckets: []UsageBucket{{ID: "codex", Windows: []UsageWindow{{Label: "Session", UsedPercent: number(20), RemainingPercent: number(80), ResetsAt: number(float64(now.Add(time.Hour).Unix()))}, {Label: "Weekly", UsedPercent: number(30), RemainingPercent: number(70), ResetsAt: number(float64(now.Add(24 * time.Hour).Unix()))}}}}}}
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
