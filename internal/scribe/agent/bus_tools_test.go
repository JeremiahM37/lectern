package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func busTool(t *testing.T, bus *AgentBus, id, name string) tools.Tool {
	t.Helper()
	for _, tl := range BusTools(id, bus) {
		if tl.Spec().Name == name {
			return tl
		}
	}
	t.Fatalf("no bus tool %s", name)
	return nil
}

func TestReportFindingAcceptsNearMissArguments(t *testing.T) {
	bus := NewAgentBus()
	rf := busTool(t, bus, "a1", "report_finding")
	res := rf.Run(context.Background(), nil, json.RawMessage(`{"text":"  parse drops blank lines  ","path":"internal/x.go","line":"42"}`))
	if res.IsError {
		t.Fatalf("near-miss args refused: %s", res.Output)
	}
	fs := bus.Findings()
	if len(fs) != 1 || fs[0].Text != "parse drops blank lines" || fs[0].File != "internal/x.go" || fs[0].Line != 42 || fs[0].Agent != "a1" {
		t.Fatalf("finding = %+v", fs)
	}
}

func TestReportFindingErrorsSayWhatToDo(t *testing.T) {
	bus := NewAgentBus()
	rf := busTool(t, bus, "a1", "report_finding")
	res := rf.Run(context.Background(), nil, json.RawMessage(`{"file":"a.go"}`))
	if !res.IsError || !strings.Contains(res.Output, "say what you found") {
		t.Fatalf("empty finding: %+v", res)
	}
	big := strings.Repeat("x", maxFindingBytes+1)
	raw, _ := json.Marshal(map[string]string{"content": big})
	res = rf.Run(context.Background(), nil, raw)
	if !res.IsError || !strings.Contains(res.Output, "post the rest as a second finding") {
		t.Fatalf("oversize finding: %+v", res)
	}
}

func TestPostFindingCapsTheBus(t *testing.T) {
	bus := NewAgentBus()
	chunk := strings.Repeat("y", maxFindingBytes)
	n := 0
	for ; ; n++ {
		if err := bus.PostFinding(Finding{Agent: "a1", Text: chunk}); err != nil {
			if !strings.Contains(err.Error(), "full") {
				t.Fatalf("cap error should say the bus is full: %v", err)
			}
			break
		}
	}
	if n != maxFindingsBytes/maxFindingBytes {
		t.Fatalf("accepted %d findings before the cap, want %d", n, maxFindingsBytes/maxFindingBytes)
	}
}

func TestCheckFindingsShowsPeersOnly(t *testing.T) {
	bus := NewAgentBus()
	mine := busTool(t, bus, "a1", "report_finding")
	peers := busTool(t, bus, "a2", "check_findings")
	cf := busTool(t, bus, "a1", "check_findings")

	if out := cf.Run(context.Background(), nil, nil).Output; !strings.Contains(out, "No findings") {
		t.Fatalf("empty bus: %q", out)
	}
	mine.Run(context.Background(), nil, json.RawMessage(`{"content":"config read twice","file":"cfg.go","line":9}`))
	if out := cf.Run(context.Background(), nil, nil).Output; strings.Contains(out, "config read twice") {
		t.Fatalf("an agent should not see its own finding back: %q", out)
	}
	busTool(t, bus, "a3", "report_finding").Run(context.Background(), nil, json.RawMessage(`{"content":"tests use a fake clock"}`))
	out := peers.Run(context.Background(), nil, nil).Output
	if !strings.Contains(out, "- [a1] cfg.go:9: config read twice") || !strings.Contains(out, "- [a3] tests use a fake clock") {
		t.Fatalf("peer list = %q", out)
	}
}

// A task waiting on its depends_on tasks gives up after the wait, and says so.
func TestAwaitDepsGivesUpAfterTheWait(t *testing.T) {
	d := &Dispatcher{}
	run := newDispatchRun(d, &tools.Env{CallID: "c"})
	idx := run.board.add("explore", "later")
	id := run.board.id(idx)
	done := []chan struct{}{make(chan struct{})} // the dependency never finishes
	err := run.awaitDeps(context.Background(), idx, []int{0}, done, []taskResult{{}}, []string{"first"}, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "gave up after") || !strings.Contains(err.Error(), "first") {
		t.Fatalf("timeout error = %v", err)
	}
	if run.board.wasStopped(id) {
		t.Fatalf("a timeout is not a user cancel")
	}
}

// /cancel frees a task that is waiting on its dependencies at once, without
// waiting for them, and the other waiting task keeps waiting.
func TestCancelFreesAWaitingTaskOnly(t *testing.T) {
	d := &Dispatcher{}
	run := newDispatchRun(d, &tools.Env{CallID: "c"})
	d.lastBoard.Store(run.board)
	dep := run.board.add("explore", "dependency")
	waiting := run.board.add("explore", "waits and is cancelled")
	other := run.board.add("explore", "waits and is kept")
	waitingID := run.board.id(waiting)
	done := []chan struct{}{make(chan struct{}), make(chan struct{})}
	results := []taskResult{{}, {}, {}}

	errs := make(chan error, 2)
	go func() {
		errs <- run.awaitDeps(context.Background(), waiting, []int{dep}, done, results, []string{"dep", "x", "y"}, time.Minute)
	}()
	go func() {
		errs <- run.awaitDeps(context.Background(), other, []int{dep}, done, results, []string{"dep", "x", "y"}, time.Minute)
	}()
	// Let both block on the dependency, then cancel only the first.
	time.Sleep(20 * time.Millisecond)
	if err := d.CancelAgent(waitingID); err != nil {
		t.Fatalf("cancel waiting task: %v", err)
	}
	select {
	case err := <-errs:
		if err != errCancelledByUser {
			t.Fatalf("cancelled waiter returned %v, want the user cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not free the waiting task")
	}
	select {
	case err := <-errs:
		t.Fatalf("the other waiter returned early: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(done[dep])
	if err := <-errs; err != nil {
		t.Fatalf("the other waiter should run once its dependency finishes: %v", err)
	}
}
