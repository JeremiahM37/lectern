package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func TestDispatchRunsExploreAgentsWithConcurrencyLimit(t *testing.T) {
	gate := make(chan struct{})
	f := newFake(t)
	f.setPick(func(_ int, req recordedReq) reply {
		task := openingOf(req)
		for _, m := range []string{"TASK-1", "TASK-2", "TASK-3"} {
			if strings.Contains(task, m) {
				// Hold every answer until the test has checked the limit.
				return f.gated(gate, turn(sseTurn{text: []string{"report " + m}}))
			}
		}
		return apiError(500, "", `{"error":{"message":"unknown task"}}`)
	})

	dir := t.TempDir()
	log := &eventLog{}
	reg := tools.NewRegistry()
	reg.Register(readTool{})
	d := &Dispatcher{
		MaxConcurrent: 2,
		Sink:          log.sink,
		New: func(spec SubagentSpec, sink Sink) *Agent {
			role, id := spec.Role, spec.ID
			opts := baseOptions(f, reg, log, dir)
			opts.Sink = sink
			opts.ID = id
			opts.System = func(mode string) string {
				return BuildSystem(PromptInput{Workdir: dir, Subagent: role}, mode)
			}
			return New(opts)
		},
	}
	tr := tools.NewRegistry()
	d.Register(tr)
	dispatch, ok := tr.Get("dispatch")
	if !ok {
		t.Fatal("dispatch tool not registered")
	}

	args := `{"tasks":[` +
		`{"task":"TASK-1 find the parsers","role":"explore"},` +
		`{"task":"TASK-2 find the lexers","role":"explore"},` +
		`{"task":"TASK-3 find the writers","role":"explore"}]}`
	resCh := make(chan tools.Result, 1)
	go func() {
		resCh <- dispatch.Run(context.Background(), &tools.Env{Workdir: dir, CallID: "call_d", Mode: "default"}, json.RawMessage(args))
	}()

	// Two agents occupy the slots; the third must wait for one to finish.
	waitUntil(t, 5*time.Second, "two subagents in flight", func() bool {
		cur, _ := f.inFlight()
		return cur == 2
	})
	time.Sleep(150 * time.Millisecond)
	if n := f.count(); n != 2 {
		t.Fatalf("%d subagent requests started; the limit is 2", n)
	}
	close(gate)

	var res tools.Result
	select {
	case res = <-resCh:
	case <-time.After(10 * time.Second):
		t.Fatal("dispatch did not return")
	}
	if _, peak := f.inFlight(); peak != 2 {
		t.Fatalf("peak concurrency = %d, want 2", peak)
	}
	if res.IsError || res.Summary != "3/3 agents finished" {
		t.Fatalf("result = %+v", res)
	}
	for _, m := range []string{"report TASK-1", "report TASK-2", "report TASK-3"} {
		if !strings.Contains(res.Output, m) {
			t.Fatalf("reports missing %q:\n%s", m, res.Output)
		}
	}

	// Each subagent is announced and ends in state done.
	last := map[string]string{}
	for _, e := range log.of(EvSubagent) {
		if e.Agent == nil {
			t.Fatal("EvSubagent without agent info")
		}
		last[e.Agent.ID] = e.Agent.Status
	}
	if len(last) != 3 {
		t.Fatalf("saw %d subagent ids, want 3: %v", len(last), last)
	}
	for id, st := range last {
		if st != "done" {
			t.Fatalf("subagent %s finished with status %q", id, st)
		}
	}
}

func TestDispatchRejectsCodeAgentsInPlanMode(t *testing.T) {
	f := newFake(t) // no script: any request would fail the test
	tr := tools.NewRegistry()
	d := &Dispatcher{New: func(spec SubagentSpec, sink Sink) *Agent {
		role := spec.Role
		t.Errorf("subagent %s started in plan mode", role)
		return nil
	}}
	d.Register(tr)
	dispatch, _ := tr.Get("dispatch")

	args := `{"tasks":[{"task":"edit parser.go","role":"code","files":["parser.go"]}]}`
	res := dispatch.Run(context.Background(), &tools.Env{Mode: "plan", CallID: "c"}, json.RawMessage(args))
	if !res.IsError || !strings.Contains(res.Output, "mode_restricted") {
		t.Fatalf("result = %+v", res)
	}
	if f.count() != 0 {
		t.Fatalf("model was called %d times", f.count())
	}
}
