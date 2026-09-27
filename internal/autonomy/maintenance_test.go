package autonomy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMaintenanceActualPlanValidationAndExclusiveSelectors(t *testing.T) {
	base := Proposal{ProjectID: 1, Title: "Measured resource limits", Why: "bounded monitoring", Acceptance: []string{"sensor remains fresh"}, Maintenance: &MaintenanceProposal{TargetID: "local", ServiceID: "temp-api", ObservationID: strings.Repeat("a", 64), Limits: MaintenanceLimits{50, 256 << 20, 64}}}
	for _, kind := range []string{"valid", "source", "repair", "continue", "documentation", "expert", "integration", "diagnosis", "environment", "path"} {
		t.Run(kind, func(t *testing.T) {
			p := base
			m := *base.Maintenance
			p.Maintenance = &m
			switch kind {
			case "source":
				p.SourceRevision = strings.Repeat("b", 40)
			case "repair":
				p.RepairTaskID = 2
			case "continue":
				p.ContinueTaskID = 2
			case "documentation":
				p.DocumentationTaskID = 2
			case "expert":
				p.ExpertRecoveryTaskID = 2
				p.ExpertProgressKey = strings.Repeat("c", 64)
			case "integration":
				p.SourceIntegrationID = strings.Repeat("c", 64)
			case "diagnosis":
				p.DiagnoseRequirement = strings.Repeat("d", 64)
			case "environment":
				p.EnvironmentDiagnosisTaskID = 2
			case "path":
				p.Maintenance.ServiceID = "../temp-api"
			}
			s, _ := NewState("2026-09-26")
			s.RegisterTask("planner", 1)
			c := DefaultConfig()
			c.Enabled = true
			raw, _ := json.Marshal(PlanReport{Items: []Proposal{p}})
			e := s.ApplyReport(c, 1, raw)
			if (e == nil) != (kind == "valid") {
				t.Fatalf("%s result %v", kind, e)
			}
		})
	}
}
func TestMaintenanceAuditPinDoesNotAllowDecisionExpansion(t *testing.T) {
	s, _ := NewState("2026-09-26")
	s.Phase = Build
	s.Items = []Proposal{{Maintenance: &MaintenanceProposal{}}}
	s.RegisterTask("builder", 2)
	c := DefaultConfig()
	c.Enabled = true
	if s.ApplyReport(c, 2, []byte(`{"outcome":"decision","summary":"expand","evidence":["x"],"decision":{"title":"expand","rationale":"needed","alternatives":["stop"],"risks":["scope"]}}`)) == nil {
		t.Fatal("expanded host authority through decision")
	}
}
