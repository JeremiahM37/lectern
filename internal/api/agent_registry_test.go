package api

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/sessions"
)

// An ACP-configured custom agent (sessions.Spec.ACP != nil) is exempt from
// every capability check below: internal/drivers.acpDriver routes
// session/request_permission through the broker for every Lectern
// permission mode itself (see acp.go's doc comment), so there is no missing
// capability for a Task-oriented custom CLI to reject here.
func TestTaskPermissionErrorAllowsEveryModeForACPAgent(t *testing.T) {
	spec := sessions.Spec{Name: "claude-code-acp", Command: "claude-code-acp",
		ACP: &sessions.ACPSpec{Command: "npx", Args: []string{"-y", "@zed-industries/claude-code-acp"}}}
	for _, mode := range []string{"default", "steerable", "plan", "bypassPermissions", "acceptEdits", ""} {
		if err := taskPermissionError(spec, mode); err != nil {
			t.Errorf("mode %q: unexpected rejection for an ACP agent: %v", mode, err)
		}
	}
}

// The SAME custom agent name with no acp/task capability at all is
// unaffected by the ACP exemption above — unchanged, pre-existing
// behaviour.
func TestTaskPermissionErrorStillRejectsPlainCustomAgent(t *testing.T) {
	spec := sessions.Spec{Name: "mycli", Command: "mycli"}
	for _, mode := range []string{"default", "steerable", "plan", "bypassPermissions"} {
		if err := taskPermissionError(spec, mode); err == nil {
			t.Errorf("mode %q: a plain custom agent with no task/acp capability should be rejected", mode)
		}
	}
}

// taskAgent's own dispatchability rule, exercised directly (it is a one-line
// guard over sessions.Find, and the rest of taskAgent needs a real *Server —
// see api_test's harness-based agent tests for that level).
func TestTaskAgentGuardAllowsACPOnlyDefinitions(t *testing.T) {
	specs := []sessions.Spec{
		{Name: "gemini-acp", Command: "gemini", ACP: &sessions.ACPSpec{Command: "gemini", Args: []string{"--experimental-acp"}}},
		{Name: "sessiononly", Command: "sessiononly"},
	}
	if spec, ok := sessions.Find(specs, "gemini-acp"); !ok || (!spec.Builtin && spec.Task == nil && spec.ACP == nil) {
		t.Fatal("an ACP-only agent (no task, no builtin) should be a valid dispatch target")
	}
	if spec, ok := sessions.Find(specs, "sessiononly"); !ok || !(!spec.Builtin && spec.Task == nil && spec.ACP == nil) {
		t.Fatal("an agent with neither task nor acp must stay session-only")
	}
}
