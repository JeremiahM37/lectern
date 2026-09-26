package autonomy

import (
	"encoding/json"
	"testing"
)

func TestDocumentationSelectorIsExplicitAndExclusive(t *testing.T) {
	for _, kind := range []string{"valid", "mixed", "negative"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := NewState("2026-09-26")
			s.RegisterTask("planner", 1)
			p := Proposal{ProjectID: 1, Title: "documentary", Why: "evidence inaccurate", Acceptance: []string{"preserve original requirements"}, DocumentationTaskID: 10}
			if kind == "mixed" {
				p.ContinueTaskID = 11
			}
			if kind == "negative" {
				p.DocumentationTaskID = -1
			}
			raw, _ := json.Marshal(PlanReport{Items: []Proposal{p}})
			cfg := DefaultConfig()
			cfg.Enabled = true
			err := s.ApplyReport(cfg, 1, raw)
			if (err == nil) != (kind == "valid") {
				t.Fatal(kind, err)
			}
		})
	}
}
func TestDocumentationDecisionCannotCreateNewImplementationRound(t *testing.T) {
	s, _ := NewState("2026-09-26")
	s.Phase = Build
	s.Items = []Proposal{{DocumentationTaskID: 10}}
	s.RegisterTask("builder", 2)
	raw := []byte(`{"outcome":"decision","summary":"expand","evidence":["proposal"],"decision":{"title":"code changes","rationale":"needed","alternatives":["stop"],"risks":["scope"]}}`)
	cfg := DefaultConfig()
	cfg.Enabled = true
	if err := s.ApplyReport(cfg, 2, raw); err == nil {
		t.Fatal("completion escaped into decision rounds")
	}
	if s.Step != 0 || s.Phase != Build {
		t.Fatal("state changed on forbidden decision")
	}
}
