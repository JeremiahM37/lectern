package autonomy

import (
	"strings"
	"testing"
)

func TestPrivateIntegrationSelectorValidation(t *testing.T) {
	base := Proposal{ProjectID: 1, IntegrationTaskID: 3, IntegrationPaths: []string{"src/**"}}
	if !validPrivateProposal(base) || proposalSources(base) != 1 {
		t.Fatal("unpinned planner input must be pinnable")
	}
	base.IntegrationPin = strings.Repeat("a", 64)
	if !validPrivateProposal(base) {
		t.Fatal("valid pinned input")
	}
	both := base
	both.SourceIntegrationID = strings.Repeat("b", 64)
	if proposalSources(both) != 2 {
		t.Fatal("combined sources escaped exclusivity")
	}
	bad := base
	bad.IntegrationTaskID = 0
	if validPrivateProposal(bad) {
		t.Fatal("orphan authority")
	}
	bad = base
	bad.DiagnoseTaskID = 2
	if validPrivateProposal(bad) {
		t.Fatal("integration combined diagnosis")
	}
	plain := Proposal{SourceIntegrationID: strings.Repeat("c", 64)}
	if !validPrivateProposal(plain) || proposalSources(plain) != 1 {
		t.Fatal("private consumer unavailable")
	}
	plain.SourceIntegrationID = "refs/heads/main"
	if validPrivateProposal(plain) {
		t.Fatal("arbitrary ref consumer")
	}
}

func TestPrivateIntegrationCannotMintDecisionRound(t *testing.T) {
	s, _ := NewState("2026-09-26")
	s.Phase = Build
	s.Items = []Proposal{{IntegrationTaskID: 3}}
	s.RegisterTask("builder", 2)
	c := DefaultConfig()
	c.Enabled = true
	if e := s.ApplyReport(c, 2, []byte(`{"outcome":"decision","summary":"expand","evidence":["proposal"],"decision":{"title":"expand","rationale":"needed","alternatives":["stop"],"risks":["scope"]}}`)); e == nil {
		t.Fatal("integration created a new execution round")
	}
	if s.Phase != Build || s.Step != 0 {
		t.Fatal("rejected decision mutated assignment")
	}
}
