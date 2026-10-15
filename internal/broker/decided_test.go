package broker

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// A decision that stands is handed to OnDecided with its final status and
// who decided it. A refused second decision is not.
func TestOnDecidedSeesOnlyStandingDecisions(t *testing.T) {
	br, _, attempt, _ := newBroker(t)
	var seen []*store.Approval
	br.OnDecided = func(row *store.Approval) { seen = append(seen, row) }

	id, err := br.Create(attempt, "Bash", map[string]any{"command": "ls"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if br.Decide(id, "approved", "", "user") == nil {
		t.Fatal("decision refused")
	}
	if len(seen) != 1 || seen[0].Status != "approved" || seen[0].DecidedBy != "user" || seen[0].DecidedAt == nil {
		t.Fatalf("callback rows: %+v", seen)
	}
	br.Decide(id, "denied", "", "user")
	if len(seen) != 1 {
		t.Fatalf("a refused second decision reached OnDecided: %d calls", len(seen))
	}
}

// With no callback set, deciding works exactly as before.
func TestDecideWithoutCallbackIsUnchanged(t *testing.T) {
	br, _, attempt, _ := newBroker(t)
	id, _ := br.Create(attempt, "Bash", map[string]any{"command": "ls"}, false)
	if br.Decide(id, "denied", "no", "user") == nil {
		t.Fatal("decision refused with a nil callback")
	}
}
