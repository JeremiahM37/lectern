package chat

import (
	"testing"
	"time"
)

// A turn blocked on the plan review or a question waits for the user; the
// busy line must not claim it is reading code.
func TestPhaseWhileWaitingForTheUser(t *testing.T) {
	read := &Call{Tool: "read", Status: "done"}
	for _, tool := range []string{"plan", "ask_user"} {
		calls := []*Call{read, {Tool: tool, Status: "running"}}
		if got := phaseOf(calls); got != "review" {
			t.Fatalf("%s running: phase %q", tool, got)
		}
		if got := phraseFor("review", 30*time.Second); got != "Waiting for your answer" {
			t.Fatalf("phrase %q", got)
		}
	}
	if got := phaseOf([]*Call{read, {Tool: "plan", Status: "done"}}); got != "explore" {
		t.Fatalf("finished plan: phase %q", got)
	}
}
