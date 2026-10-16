package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// A pending subagent is cancelled in place and never starts; a finished one
// refuses; an unknown id names the problem.
func TestCancelAgentStatesOnTheBoard(t *testing.T) {
	var events []Event
	d := &Dispatcher{Sink: func(e Event) { events = append(events, e) }}
	if err := d.CancelAgent("a1"); err == nil || !strings.Contains(err.Error(), "no dispatch") {
		t.Fatalf("no dispatch yet: got %v", err)
	}

	run := newDispatchRun(d, &tools.Env{CallID: "c"})
	d.lastBoard.Store(run.board)
	idx := run.board.add("explore", "find callers")
	id := run.board.id(idx)

	if err := d.CancelAgent(id); err != nil {
		t.Fatalf("cancel pending: %v", err)
	}
	if got := run.board.infos[idx].Status; got != "cancelled" {
		t.Fatalf("pending subagent should read cancelled, got %q", got)
	}
	if run.board.register(id, func() {}) {
		t.Fatalf("a cancelled subagent must not register to run")
	}
	if err := d.CancelAgent(id); err == nil || !strings.Contains(err.Error(), "already cancelled") {
		t.Fatalf("cancelling twice should say it already happened: %v", err)
	}
	if err := d.CancelAgent("a99"); err == nil || !strings.Contains(err.Error(), "no subagent a99") {
		t.Fatalf("unknown id: %v", err)
	}
}

// A running subagent's context is cancelled, and the attempt reports the
// user's cancellation instead of a provider error.
func TestCancelRunningSubagentCancelsItsContext(t *testing.T) {
	d := &Dispatcher{}
	run := newDispatchRun(d, &tools.Env{CallID: "c"})
	d.lastBoard.Store(run.board)
	idx := run.board.add("explore", "slow question")
	id := run.board.id(idx)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sctx, scancel := context.WithCancel(ctx)
	defer scancel()
	if !run.board.register(id, scancel) {
		t.Fatalf("a fresh subagent should register")
	}
	run.board.update(idx, func(a *AgentInfo) { a.Status = "running" })
	if err := d.CancelAgent(id); err != nil {
		t.Fatalf("cancel running: %v", err)
	}
	if sctx.Err() == nil {
		t.Fatalf("cancelling should cancel the subagent's context")
	}
	if !run.board.wasStopped(id) {
		t.Fatalf("the board should remember the user stopped it")
	}
	// The attempt marks the subagent cancelled when it returns; then a
	// second cancel has nothing to stop.
	run.board.unregister(id)
	run.board.update(idx, func(a *AgentInfo) { a.Status = "cancelled" })
	if err := d.CancelAgent(id); err == nil || !strings.Contains(err.Error(), "already cancelled") {
		t.Fatalf("a stopped subagent should not be cancellable again: %v", err)
	}
}
