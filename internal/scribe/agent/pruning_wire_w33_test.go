package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// With pruning on for the main agent, the request carries shortened old tool
// results while the history keeps them whole; off, nothing changes.
func TestPruningShortensOldResultsInTheRequestOnly(t *testing.T) {
	a := New(baseOptions(newFake(t), lookRegistry(), &eventLog{}, t.TempDir()))
	long := strings.Repeat("line of output\n", 400)
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("c%d", i)
		a.msgs = append(a.msgs, callMsg(id, "look", "{}"), resultMsg(id, "look", long, false))
	}
	size := func() int {
		n := 0
		for _, m := range a.request().Messages {
			for _, p := range m.Parts {
				n += len(p.Result)
			}
		}
		return n
	}
	full := size()
	a.SetPruning(config.PruningMain)
	pruned := size()
	if pruned >= full {
		t.Fatalf("pruning on: request results %d, want fewer than %d", pruned, full)
	}
	for _, m := range a.msgs {
		for _, p := range m.Parts {
			if p.Result != "" && p.Result != long {
				t.Fatal("pruning changed the history itself")
			}
		}
	}
}
