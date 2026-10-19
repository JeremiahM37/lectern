package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

// An approved plan runs as its own turn: the approving turn ends at the
// plan call, and RunPendingExecution starts the plan as a new turn.
func TestApprovedPlanRunsInItsOwnTurn(t *testing.T) {
	h := newPlanHarness(t, "plan", AnswerExecute)
	h.fake.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "call_p", name: "plan", args: planArgs}}}),
		turn(sseTurn{text: []string{"Plan done."}}),
	))
	_, err := h.agent.Run(context.Background(), "add tests", nil)
	must(t, err)
	if n := h.fake.count(); n != 1 {
		t.Fatalf("approving turn made %d model calls, want 1 (the plan call only)", n)
	}

	stats, ran, err := RunPendingExecution(context.Background(), h.agent, h.planner)
	must(t, err)
	if !ran || stats.Error != "" {
		t.Fatalf("pending plan ran=%v stats.Error=%q", ran, stats.Error)
	}
	if n := h.fake.count(); n != 2 {
		t.Fatalf("model called %d times, want 2", n)
	}
	var lead string
	for _, m := range msgsWithRole(h.fake.requests()[1], "user") {
		if strings.HasPrefix(m.text(), PlanExecuteLead) {
			lead = m.text()
		}
	}
	if !strings.Contains(lead, "Add parser tests") || !strings.Contains(lead, "Write table tests") {
		t.Fatalf("execution message lacks the approved plan:\n%s", lead)
	}

	// The plan is taken once: a second call starts no turn.
	if _, ran, _ := RunPendingExecution(context.Background(), h.agent, h.planner); ran {
		t.Fatal("approved plan ran twice")
	}

	// The decision is recorded: an approved outcome and a notice that says so.
	outcome := false
	for _, p := range outcomeEvents(h.log) {
		outcome = outcome || p.Outcome == "approved"
	}
	if !outcome {
		t.Fatal("no approved outcome recorded")
	}
	noticed := false
	for _, e := range h.log.of(EvNotice) {
		noticed = noticed || strings.Contains(e.Text, "decision to execute is recorded")
	}
	if !noticed {
		t.Fatal("no notice recording the execute decision")
	}
}

// A cancelled context drops the approved plan, so it cannot run later against
// a conversation the user has moved on from.
func TestCancelledContextDropsApprovedPlan(t *testing.T) {
	h := newPlanHarness(t, "plan", AnswerExecute)
	h.fake.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "call_p", name: "plan", args: planArgs}}}),
	))
	_, err := h.agent.Run(context.Background(), "add tests", nil)
	must(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ran, _ := RunPendingExecution(ctx, h.agent, h.planner); ran {
		t.Fatal("plan ran under a cancelled context")
	}
	if h.planner.takeExecute() != nil {
		t.Fatal("cancelled plan was not dropped")
	}
	if h.fake.count() != 1 {
		t.Fatalf("model called %d times, want 1", h.fake.count())
	}
}

// Without an approval there is nothing to run.
func TestRunPendingExecutionWithoutApprovalIsNoop(t *testing.T) {
	h := newPlanHarness(t, "plan", AnswerCancel)
	if _, ran, err := RunPendingExecution(context.Background(), h.agent, h.planner); ran || err != nil {
		t.Fatalf("ran=%v err=%v with nothing approved", ran, err)
	}
	if _, ran, _ := RunPendingExecution(context.Background(), h.agent, nil); ran {
		t.Fatal("nil planner ran a turn")
	}
}

// Calls that the loop does not emit as events still reach the model log,
// with the agent's tab, and never reach the sink.
func TestModelLogRecordsCompactionAndTab(t *testing.T) {
	h := newPlanHarness(t, "default")
	h.agent.opts.TabID = "tab-7"
	h.agent.logCall(Event{Kind: EvStepFinish, Model: "fake/model-a", Meta: callMeta(callSourceCompaction, callStateOK)})
	h.agent.logCall(Event{Kind: EvRetry, Model: "fake/model-a", Meta: callMeta(callSourceMain, callStateError), Text: "too long"})

	evs := h.agent.ModelEvents()
	if len(evs) != 2 {
		t.Fatalf("model log has %d entries, want 2: %+v", len(evs), evs)
	}
	if evs[0].Meta["source"] != callSourceCompaction || evs[0].Meta[metaTab] != "tab-7" {
		t.Fatalf("compaction entry = %+v", evs[0].Meta)
	}
	if len(h.log.of(EvStepFinish)) != 0 || len(h.log.of(EvRetry)) != 0 {
		t.Fatal("logCall reached the sink")
	}
}

// A compaction that is refused for too little history makes no call and is
// not logged; a successful one is.
func TestCompactionLogSkipsNotEnoughHistory(t *testing.T) {
	h := newPlanHarness(t, "default")
	h.agent.logCompaction(context.Background(), "fake/model-a", time.Now(), ErrNotEnoughHistory)
	if n := len(h.agent.ModelEvents()); n != 0 {
		t.Fatalf("model log has %d entries for a call that never happened", n)
	}
	h.agent.logCompaction(context.Background(), "fake/model-a", time.Now(), nil)
	evs := h.agent.ModelEvents()
	if len(evs) != 1 || evs[0].Kind != EvStepFinish || evs[0].Meta["state"] != callStateOK {
		t.Fatalf("successful compaction log = %+v", evs)
	}
}
