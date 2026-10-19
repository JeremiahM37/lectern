package tui

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
)

// The Errors column counts every failed attempt, not only final EvError
// events: a retried step and a failed compaction call are errors the user
// needs to see in the model log.
func TestModelRowsCountsRetriesAndCompactionFailures(t *testing.T) {
	failed := map[string]any{"state": "error"}
	evs := []agent.Event{
		{Kind: agent.EvStepFinish, Model: "m1", Meta: map[string]any{"state": "ok"}},
		{Kind: agent.EvRetry, Model: "m1", Meta: failed},
		{Kind: agent.EvStepFinish, Model: "m1", Meta: failed}, // compaction call that failed
		{Kind: agent.EvError, Model: "m1", Meta: failed},      // final failure: counted once
		{Kind: agent.EvError, Model: "m1"},                    // error with no call state
	}
	rows := modelRows(evs)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].calls != 2 {
		t.Errorf("calls = %d, want 2 (every step that finished, failed or not)", rows[0].calls)
	}
	if rows[0].errors != 4 {
		t.Errorf("errors = %d, want 4", rows[0].errors)
	}
}
