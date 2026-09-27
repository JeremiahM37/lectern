package autonomy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExpertRecoverySelectorAndLegacyReports(t *testing.T) {
	for _, kind := range []string{"valid", "legacy", "missingkey", "invalidhex", "mixed", "diagnosis", "orphan"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := NewState("2026-09-26")
			s.RegisterTask("planner", 1)
			p := Proposal{ProjectID: 1, Title: "expert", Why: "new causal evidence", Acceptance: []string{"original requirements"}, ExpertRecoveryTaskID: 295, ExpertProgressKey: strings.Repeat("a", 64)}
			switch kind {
			case "legacy":
				p.ExpertRecoveryTaskID = 0
				p.ExpertProgressKey = ""
			case "missingkey":
				p.ExpertProgressKey = ""
			case "invalidhex":
				p.ExpertProgressKey = strings.Repeat("z", 64)
			case "mixed":
				p.RepairTaskID = 295
			case "diagnosis":
				p.DiagnoseTaskID = 295
			case "orphan":
				p.ExpertRecoveryTaskID = 0
			}
			raw, _ := json.Marshal(PlanReport{Items: []Proposal{p}})
			c := DefaultConfig()
			c.Enabled = true
			e := s.ApplyReport(c, 1, raw)
			if (e == nil) != (kind == "valid" || kind == "legacy") {
				t.Fatal(kind, e)
			}
		})
	}
}
func TestExpertRecoveryCannotMintDecisionRound(t *testing.T) {
	s, _ := NewState("2026-09-26")
	s.Phase = Build
	s.Items = []Proposal{{ExpertRecoveryTaskID: 295}}
	s.RegisterTask("builder", 2)
	c := DefaultConfig()
	c.Enabled = true
	if e := s.ApplyReport(c, 2, []byte(`{"outcome":"decision","summary":"more work","evidence":["proposal"],"decision":{"title":"expand","rationale":"needed","alternatives":["stop"],"risks":["scope"]}}`)); e == nil {
		t.Fatal("new implementation round allowed")
	}
	if s.Phase != Build || s.Step != 0 {
		t.Fatal("forbidden report mutated state")
	}
}
