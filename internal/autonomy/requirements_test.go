package autonomy

import (
	"encoding/json"
	"testing"
)

func TestOptionalRequirementsPreserveLegacyReports(t *testing.T) {
	for _, typed := range []bool{false, true} {
		s, _ := NewState("2026-09-26")
		s.Phase = Build
		s.Items = []Proposal{{ProjectID: 1, Title: "test"}}
		s.RegisterTask("builder", 2)
		r := BuildReport{Outcome: "blocked", Summary: "missing test dependency", Evidence: []string{"import failed"}}
		if typed {
			r.Requirements = []Requirement{{Capability: "python_wheels", SchemaVersion: 1, Requirements: []string{"django==5.2.12"}, Imports: []string{"django"}, Condition: "offline_imports_available", Evidence: []string{"source requirements"}}}
		}
		raw, _ := json.Marshal(r)
		cfg := DefaultConfig()
		cfg.Enabled = true
		if err := s.ApplyReport(cfg, 2, raw); err != nil {
			t.Fatal(err)
		}
	}
}
func TestRequirementSchemaRejectsUnboundedAndMalformedRequests(t *testing.T) {
	r := Requirement{Capability: "python_wheels", SchemaVersion: 1, Condition: "offline_imports_available", Evidence: []string{"missing module"}}
	if err := ValidateRequirements([]Requirement{r}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRequirements([]Requirement{r, r, r, r, r}); err == nil {
		t.Fatal("unbounded requirements")
	}
	r.Capability = "arbitrary shell command"
	if err := ValidateRequirements([]Requirement{r}); err == nil {
		t.Fatal("invalid capability")
	}
}

func TestNodeTypedRequirementPreservesNormalReportLifecycle(t *testing.T) {
	s, _ := NewState("2026-09-26")
	s.Phase = Build
	s.Items = []Proposal{{ProjectID: 1, Title: "actual Node test"}}
	s.RegisterTask("builder", 2)
	r := BuildReport{Outcome: "blocked", Summary: "MCP executable absent", Evidence: []string{"actual npx failure"}, Requirements: []Requirement{{Capability: "node_packages", SchemaVersion: 1, Requirements: []string{"@playwright/mcp@0.0.80"}, Binaries: []string{"playwright-mcp"}, Condition: "offline_node_available", Evidence: []string{"source exact invocation"}}}}
	raw, _ := json.Marshal(r)
	cfg := DefaultConfig()
	cfg.Enabled = true
	if e := s.ApplyReport(cfg, 2, raw); e != nil {
		t.Fatal(e)
	}
	r.Requirements[0].PackageJSON = string(make([]byte, 513))
	if e := ValidateRequirements(r.Requirements); e == nil {
		t.Fatal("unbounded archive path")
	}
}
