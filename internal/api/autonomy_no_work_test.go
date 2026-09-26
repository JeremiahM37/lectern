package api

import (
	"context"
	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"strings"
	"testing"
)

func TestNoWorkPromptHasBothSchemasAndIndependentAuditEvidence(t *testing.T) {
	s := autoTestServer(t)
	state, _ := autonomy.NewState("2026-09-25")
	a := &autoRecord{Config: autonomy.DefaultConfig(), State: state}
	prompt := s.autoPrompt(context.Background(), a, "planner", &store.Project{Name: "fixture"})
	for _, want := range []string{"Nonempty plan example", `Empty plan example`, `"no_work":`, `"blockers":`, `"exploration":`, "Do not invent blockers"} {
		if !strings.Contains(prompt, want) {
			t.Fatal("missing planner contract", want)
		}
	}
	state.Phase = autonomy.Audit
	state.NoWork = &autonomy.NoWorkReport{Reason: "inspect this actual reason"}
	yes := true
	state.Audits["auditor_a"] = autonomy.Verdict{Approve: &yes, Reason: "PEER_PRIVATE_VOTE"}
	prompt = s.autoPrompt(context.Background(), a, "auditor_b", &store.Project{Name: "fixture"})
	if !strings.Contains(prompt, "inspect this actual reason") || !strings.Contains(prompt, "declining work is justified") || strings.Contains(prompt, "PEER_PRIVATE_VOTE") {
		t.Fatal("no-work audit evidence or vote independence broken")
	}
}
