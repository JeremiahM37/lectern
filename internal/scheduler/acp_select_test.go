package scheduler

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/agents"
	"github.com/JeremiahM37/lectern/v2/internal/drivers"
)

// selectDriver is the one place a queued attempt's driver kind is decided.
// An ACP-configured custom agent must win regardless of permission mode —
// drivers.Select's (agent, builtin) signature has no way to see a custom
// agent's definition, which is exactly why this wrapper exists instead of
// changing that signature (see selectDriver's doc comment in scheduler.go).
func TestSelectDriverPicksACPRegardlessOfPermissionMode(t *testing.T) {
	acpDef := agents.TaskDefinition{Name: "claude-code-acp", ACP: &agents.ACPDefinition{Command: "npx"}}
	for _, mode := range []string{"default", "acceptEdits", "plan", "bypassPermissions", "steerable", ""} {
		cfg := agents.TaskLaunchConfig{Agent: "claude-code-acp", Definition: acpDef}
		if got := selectDriver(cfg, mode); got != drivers.KindACP {
			t.Errorf("mode %q: selectDriver = %q, want %q", mode, got, drivers.KindACP)
		}
	}
}

func TestSelectDriverFallsBackToSelectWithoutACP(t *testing.T) {
	cfg := agents.TaskLaunchConfig{Agent: "claude", Definition: agents.TaskDefinition{Name: "claude", Builtin: true}}
	if got := selectDriver(cfg, "steerable"); got != drivers.KindClaudeSteer {
		t.Errorf("selectDriver = %q, want %q", got, drivers.KindClaudeSteer)
	}
	generic := agents.TaskLaunchConfig{Agent: "mycli", Definition: agents.TaskDefinition{Name: "mycli"}}
	if got := selectDriver(generic, "acceptEdits"); got != drivers.KindGeneric {
		t.Errorf("selectDriver = %q, want %q", got, drivers.KindGeneric)
	}
}
