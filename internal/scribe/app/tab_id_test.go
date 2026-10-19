package app

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
)

// The lead agent's options carry the TUI tab that owns the session, so the
// model log can record which tab made each call.
func TestAgentOptionsCarryTheTabID(t *testing.T) {
	a := &App{Workdir: t.TempDir(), opts: Options{TabID: "tab-7"}}
	if got := a.agentOptions(agent.Model{}, "auto").TabID; got != "tab-7" {
		t.Fatalf("agent TabID = %q, want tab-7", got)
	}
	none := &App{Workdir: t.TempDir()}
	if got := none.agentOptions(agent.Model{}, "auto").TabID; got != "" {
		t.Fatalf("no tab set: agent TabID = %q, want empty", got)
	}
}
