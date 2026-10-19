package agent

import (
	"context"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// A step that ends with no text and no tool call is asked again, and the empty
// reply is not kept in the history (QA41: a reasoning model stopped after one
// token and the turn ended with no answer).
func TestEmptyStepIsRetriedAndTheAnswerKept(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{usage: &sseUsage{prompt: 1000, completion: 1}}),
		turn(sseTurn{text: []string{"The answer."}, usage: &sseUsage{prompt: 1000, completion: 20}}),
	))
	log := &eventLog{}
	a := New(baseOptions(f, tools.NewRegistry(), log, t.TempDir()))

	stats, err := a.Run(context.Background(), "explain it", nil)
	must(t, err)
	if done := log.of(EvTurnDone)[0]; done.Text != "The answer." || stats.Error != "" {
		t.Fatalf("final %q, error %q; want the retried answer and no error", done.Text, stats.Error)
	}
	for _, m := range a.Messages() {
		if m.Role == "assistant" && m.Text() == "" && len(m.ToolCalls()) == 0 {
			t.Fatal("the empty reply was kept in the history")
		}
	}
}

// Two empty steps in a row are the limit: the turn then ends, so it cannot
// loop forever.
func TestEmptyStepRetriesAreBounded(t *testing.T) {
	f := newFake(t)
	empty := turn(sseTurn{usage: &sseUsage{prompt: 1000, completion: 1}})
	f.setPick(scripted(t, empty, empty, empty, empty))
	log := &eventLog{}
	a := New(baseOptions(f, tools.NewRegistry(), log, t.TempDir()))
	if _, err := a.Run(context.Background(), "explain it", nil); err != nil {
		t.Fatal(err)
	}
	if n := len(f.reqs); n != 3 {
		t.Fatalf("requests = %d, want 3 (the first step and two retries)", n)
	}
}
