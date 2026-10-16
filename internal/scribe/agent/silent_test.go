package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// TestSilentStopAfterToolsIsNudgedOnce: a model that ends with an empty reply
// right after tool results gets one reminder to carry on or answer.
func TestSilentStopAfterToolsIsNudgedOnce(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0o644))
	reg := tools.NewRegistry()
	reg.Register(readTool{})
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "c1", name: "read", args: `{"path":"notes.txt"}`}}}),
		turn(sseTurn{}), // silent stop after the tool result
		turn(sseTurn{text: []string{"It says hi."}}),
	))
	a := New(baseOptions(f, reg, &eventLog{}, dir))
	stats, err := a.Run(context.Background(), "read notes.txt", nil)
	must(t, err)
	if got := a.Messages()[len(a.Messages())-1].Text(); got != "It says hi." {
		t.Fatalf("final = %q (stats %+v)", got, stats)
	}
	if f.count() != 3 {
		t.Fatalf("model calls = %d, want 3 (one nudge)", f.count())
	}
}

func TestRefusalPointsReadOnlyModesAtTheirOwnOutput(t *testing.T) {
	if !strings.Contains(restrictedNext("plan"), "plan tool") {
		t.Fatal("plan mode should end in the plan tool")
	}
	for _, m := range []string{"architect", "socratic", "challenge"} {
		if strings.Contains(restrictedNext(m), "plan tool") {
			t.Fatalf("%s mode refusal sends the model to the plan tool", m)
		}
	}
}
