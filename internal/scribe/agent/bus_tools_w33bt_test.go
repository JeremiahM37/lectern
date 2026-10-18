package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// The lead agent's cancel_dispatch is named like the subagent's, must be
// read-only so it runs beside the dispatch it stops, and asks for an id.
func TestLeadCancelToolSpec(t *testing.T) {
	tl := DispatchCancelTool(&Dispatcher{})
	if tl.Spec().Name != "cancel_dispatch" {
		t.Fatalf("name = %q", tl.Spec().Name)
	}
	if !tl.ReadOnly() {
		t.Fatalf("the lead cancel must be read-only, or it waits for the dispatch it stops")
	}
	if !strings.Contains(string(tl.Spec().Schema), `"required":["id"]`) {
		t.Fatalf("schema should require id: %s", tl.Spec().Schema)
	}
}

// Outside a running dispatch there is nothing to stop, and the message says
// so rather than reporting an agent as already finished.
func TestLeadCancelRefusesWhenNoDispatchRuns(t *testing.T) {
	d := &Dispatcher{}
	run := newDispatchRun(d, &tools.Env{CallID: "c"})
	d.lastBoard.Store(run.board)
	run.board.add("explore", "old question") // a finished dispatch keeps its ids

	res := DispatchCancelTool(d).Run(context.Background(), nil, json.RawMessage(`{"id":"a1"}`))
	if !res.IsError || !strings.Contains(res.Output, "no dispatch is running") {
		t.Fatalf("idle session: %+v", res)
	}
	res = DispatchCancelTool(nil).Run(context.Background(), nil, json.RawMessage(`{"id":"all"}`))
	if !res.IsError || !strings.Contains(res.Output, "no subagents") {
		t.Fatalf("nil dispatcher: %+v", res)
	}
}

// An id is required, and "all" is the only other value that is special.
func TestLeadCancelNeedsAnID(t *testing.T) {
	d := &Dispatcher{}
	d.active.Store(1)
	for _, raw := range []string{`{}`, `{"id":"  "}`, ``} {
		res := DispatchCancelTool(d).Run(context.Background(), nil, json.RawMessage(raw))
		if !res.IsError || !strings.Contains(res.Output, "by its id") {
			t.Fatalf("args %s: %+v", raw, res)
		}
	}
}

// Cancelling one id stops that subagent only, through the same board state
// /cancel uses; a second cancel and an unknown id each say what happened.
func TestLeadCancelByIDStopsOneSubagent(t *testing.T) {
	d := &Dispatcher{}
	d.active.Store(1)
	run := newDispatchRun(d, &tools.Env{CallID: "c"})
	d.lastBoard.Store(run.board)
	first := run.board.id(run.board.add("explore", "first question"))
	idx := run.board.add("explore", "second question")
	second := run.board.id(idx)
	tl := DispatchCancelTool(d)

	res := tl.Run(context.Background(), nil, json.RawMessage(`{"id":"`+second+`"}`))
	if res.IsError || !strings.Contains(res.Output, "Cancelled subagent "+second) {
		t.Fatalf("cancel %s: %+v", second, res)
	}
	if got := run.board.infos[idx].Status; got != "cancelled" {
		t.Fatalf("%s should read cancelled, got %q", second, got)
	}
	if got := run.board.infos[0].Status; got != "pending" || run.board.infos[0].ID != first {
		t.Fatalf("%s should be untouched, got %q", first, got)
	}
	res = tl.Run(context.Background(), nil, json.RawMessage(`{"id":"`+second+`"}`))
	if !res.IsError || !strings.Contains(res.Output, "already cancelled") {
		t.Fatalf("second cancel should say so: %+v", res)
	}
	res = tl.Run(context.Background(), nil, json.RawMessage(`{"id":"a99"}`))
	if !res.IsError || !strings.Contains(res.Output, "no subagent a99") {
		t.Fatalf("unknown id: %+v", res)
	}
}

// "all" stops the whole dispatch once, and a second "all" says it is done.
func TestLeadCancelAllStopsTheDispatch(t *testing.T) {
	d := &Dispatcher{}
	d.active.Store(1)
	bus := NewAgentBus()
	stopped := 0
	bus.SetAbort(func() { stopped++ })
	d.lastBus.Store(bus)
	tl := DispatchCancelTool(d)

	res := tl.Run(context.Background(), nil, json.RawMessage(`{"id":"all"}`))
	if res.IsError || stopped != 1 || !strings.Contains(res.Output, "Cancelled the running dispatch") {
		t.Fatalf("all: stopped=%d res=%+v", stopped, res)
	}
	res = tl.Run(context.Background(), nil, json.RawMessage(`{"id":"all"}`))
	if !res.IsError || !strings.Contains(res.Output, "already cancelled") || stopped != 1 {
		t.Fatalf("second all: stopped=%d res=%+v", stopped, res)
	}
}

// The real path: a dispatch runs two hanging subagents in the background,
// the lead cancels one by id while the dispatch is still running, the other
// keeps running, and then "all" ends the dispatch.
func TestLeadCancelStopsASubagentWhileTheDispatchRuns(t *testing.T) {
	f := newFake(t)
	f.setPick(func(int, recordedReq) reply { return f.hang("") })
	h := busDispatch(t, f)

	env := &tools.Env{Workdir: h.dir, CallID: "call_lead", Mode: "default"}
	tr := tools.NewRegistry()
	h.d.Register(tr)
	dispatch, _ := tr.Get("dispatch")
	resCh := make(chan tools.Result, 1)
	go func() {
		resCh <- dispatch.Run(context.Background(), env, json.RawMessage(`{"tasks":[`+
			`{"id":"one","role":"explore","task":"W33-ONE wait"},{"id":"two","role":"explore","task":"W33-TWO wait"}]}`))
	}()
	deadline := time.Now().Add(5 * time.Second)
	for f.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	lead := DispatchCancelTool(h.d)
	betaID := ""
	for _, info := range h.d.Status() {
		if info.Task == "W33-TWO wait" {
			betaID = info.ID
		}
	}
	if betaID == "" {
		t.Fatalf("the second subagent is not on the board: %+v", h.d.Status())
	}
	if res := lead.Run(context.Background(), nil, json.RawMessage(`{"id":"`+betaID+`"}`)); res.IsError {
		t.Fatalf("cancel %s during the dispatch: %+v", betaID, res)
	}
	// A running subagent is marked cancelled when its attempt returns, which
	// is after the cancel, so poll for it rather than read it straight away.
	statusOf := func(task string) string {
		for _, info := range h.d.Status() {
			if info.Task == task {
				return info.Status
			}
		}
		return ""
	}
	for end := time.Now().Add(5 * time.Second); statusOf("W33-TWO wait") != "cancelled" && time.Now().Before(end); {
		time.Sleep(10 * time.Millisecond)
	}
	if got := statusOf("W33-TWO wait"); got != "cancelled" {
		t.Fatalf("the cancelled subagent should read cancelled, got %q", got)
	}
	if got := statusOf("W33-ONE wait"); got == "cancelled" {
		t.Fatalf("the sibling must keep running after one id is cancelled, got %q", got)
	}

	if res := lead.Run(context.Background(), nil, json.RawMessage(`{"id":"all"}`)); res.IsError {
		t.Fatalf("cancel all: %+v", res)
	}
	select {
	case res := <-resCh:
		if !strings.Contains(res.Output, "## Cancelled") {
			t.Fatalf("the dispatch should report the cancellation:\n%s", res.Output)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the dispatch did not return after cancel all")
	}
}
