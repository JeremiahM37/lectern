package app

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
)

// Sending from an earlier checkpoint drops the later turns from the saved
// transcript as well as from the conversation, so the rebuilt view matches.
func TestBranchFromCutsTheTranscript(t *testing.T) {
	be, _ := testBackend(t)
	runTurn(t, be, "first question", nil)
	waitIdle(t, be)
	runTurn(t, be, "second question", nil)
	waitIdle(t, be)

	prompts := func() []string {
		var out []string
		for _, e := range be.Transcript() {
			if e.Kind == agent.EvTurnStart && e.AgentID == "" && e.Prompt != "" {
				out = append(out, e.Prompt)
			}
		}
		return out
	}
	if got := prompts(); len(got) != 2 {
		t.Fatalf("before: turns %q", got)
	}
	if _, err := be.(*scribeBackend).BranchFrom(1); err != nil {
		t.Fatal(err)
	}
	if got := prompts(); len(got) != 1 || got[0] != "first question" {
		t.Fatalf("after: turns %q", got)
	}
}
