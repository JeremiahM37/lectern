package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// checkTool stands in for the project tool: a check that is always "run".
func checkTool() fnTool {
	return fnTool{name: "project", ro: false, run: func(context.Context, *tools.Env, json.RawMessage) tools.Result {
		return tools.Result{Output: "ok"}
	}}
}

// verifyScript returns a Verify hook that yields the given results in order
// (the last one repeats) and counts its calls.
func verifyScript(calls *atomic.Int64, results ...struct {
	ok     bool
	report string
}) func(context.Context) (string, bool, string) {
	return func(context.Context) (string, bool, string) {
		n := int(calls.Add(1)) - 1
		if n >= len(results) {
			n = len(results) - 1
		}
		return "go vet ./...", results[n].ok, results[n].report
	}
}

type result = struct {
	ok     bool
	report string
}

func autoCheckAgent(t *testing.T, f *fakeLLM, log *eventLog, dir string, verify func(context.Context) (string, bool, string)) *Agent {
	t.Helper()
	reg := tools.NewRegistry()
	reg.Register(writeTool{})
	reg.Register(checkTool())
	reg.Register(fnTool{name: "shell", ro: false, run: func(context.Context, *tools.Env, json.RawMessage) tools.Result {
		return tools.Result{Output: "ran"}
	}})
	o := baseOptions(f, reg, log, dir)
	o.Verify = verify
	return New(o)
}

func TestAutoCheckFeedsFailureBackAndStops(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "w1", name: "write_file", args: `{"path":"a.go","content":"x"}`}}}),
		turn(sseTurn{text: []string{"done"}}),
		turn(sseTurn{calls: []sseCall{{id: "w2", name: "write_file", args: `{"path":"a.go","content":"y"}`}}}),
		turn(sseTurn{text: []string{"fixed"}}),
	))
	var calls atomic.Int64
	log := &eventLog{}
	a := autoCheckAgent(t, f, log, t.TempDir(), verifyScript(&calls,
		result{ok: false, report: "a.go:1: undefined: x"},
		result{ok: true},
	))
	if _, err := a.Run(context.Background(), "edit a.go", nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("verify ran %d times, want 2", calls.Load())
	}
	reqs := f.requests()
	if len(reqs) != 4 {
		t.Fatalf("requests = %d, want 4", len(reqs))
	}
	if got := lastUserText(reqs[2]); !strings.Contains(got, "failed after your edits") || !strings.Contains(got, "undefined: x") {
		t.Fatalf("fix reminder not sent, last user text = %q", got)
	}
	var results []Event
	for _, e := range log.of(EvToolResult) {
		if e.Tool == "auto_check" {
			results = append(results, e)
		}
	}
	if len(results) != 2 || !results[0].Result.IsError || results[1].Result.IsError {
		t.Fatalf("auto_check results = %+v", results)
	}
	var shown bool
	for _, e := range log.of(EvToolRun) {
		shown = shown || (e.Tool == "auto_check" && strings.Contains(string(e.Args), "go vet"))
	}
	if !shown {
		t.Fatal("auto_check did not show its command")
	}
}

func TestAutoCheckSkippedWhenModelRanACheck(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "w1", name: "write_file", args: `{"path":"a.go","content":"x"}`}}}),
		turn(sseTurn{calls: []sseCall{{id: "p1", name: "project", args: `{"action":"test"}`}}}),
		turn(sseTurn{text: []string{"all green"}}),
	))
	var calls atomic.Int64
	a := autoCheckAgent(t, f, &eventLog{}, t.TempDir(), verifyScript(&calls, result{ok: false, report: "boom"}))
	if _, err := a.Run(context.Background(), "edit", nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("verify ran %d times after the model ran its own check", calls.Load())
	}
	if len(f.requests()) != 3 {
		t.Fatalf("requests = %d, want 3", len(f.requests()))
	}
}

func TestAutoCheckGivesUpAfterTwoFixRounds(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "w1", name: "write_file", args: `{"path":"a.go","content":"1"}`}}}),
		turn(sseTurn{text: []string{"done"}}),
		turn(sseTurn{calls: []sseCall{{id: "w2", name: "write_file", args: `{"path":"a.go","content":"2"}`}}}),
		turn(sseTurn{text: []string{"done"}}),
		turn(sseTurn{calls: []sseCall{{id: "w3", name: "write_file", args: `{"path":"a.go","content":"3"}`}}}),
		turn(sseTurn{text: []string{"gave up"}}),
	))
	var calls atomic.Int64
	log := &eventLog{}
	a := autoCheckAgent(t, f, log, t.TempDir(), verifyScript(&calls, result{ok: false, report: "still broken"}))
	stats, err := a.Run(context.Background(), "edit", nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || len(f.requests()) != 6 {
		t.Fatalf("verify=%d requests=%d, want 3 and 6", calls.Load(), len(f.requests()))
	}
	if len(log.of(EvWarning)) != 1 {
		t.Fatalf("want one warning after the rounds ran out, got %+v", log.of(EvWarning))
	}
	if stats.Steps != 6 {
		t.Fatalf("steps = %d", stats.Steps)
	}
}

func TestAutoCheckNoEditsNoCheck(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t, turn(sseTurn{text: []string{"just an answer"}})))
	var calls atomic.Int64
	a := autoCheckAgent(t, f, &eventLog{}, t.TempDir(), verifyScript(&calls, result{ok: false, report: "x"}))
	if _, err := a.Run(context.Background(), "question", nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("verify ran without edits")
	}
}

func TestAutoCheckNothingToCheckIsSilent(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "w1", name: "write_file", args: `{"path":"a.go","content":"x"}`}}}),
		turn(sseTurn{text: []string{"done"}}),
	))
	var calls atomic.Int64
	log := &eventLog{}
	a := autoCheckAgent(t, f, log, t.TempDir(), func(context.Context) (string, bool, string) {
		calls.Add(1)
		return "", true, ""
	})
	if _, err := a.Run(context.Background(), "edit", nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || len(f.requests()) != 2 || len(log.of(EvToolRun)) != 1 {
		t.Fatalf("verify=%d requests=%d: a project with no check should add nothing", calls.Load(), len(f.requests()))
	}
}

func TestIsCheckCall(t *testing.T) {
	cases := map[string]bool{
		`{"command":"go test ./..."}`: true,
		`{"command":"npm run build"}`: true,
		`{"command":"ls -la"}`:        false,
		`{"command":"cargo check"}`:   true,
		`not json`:                    false,
	}
	for args, want := range cases {
		if got := isCheckCall(llm.Part{ToolName: "shell", Args: json.RawMessage(args)}); got != want {
			t.Errorf("isCheckCall(%s) = %v, want %v", args, got, want)
		}
	}
	if !isCheckCall(llm.Part{ToolName: "project"}) || isCheckCall(llm.Part{ToolName: "read"}) {
		t.Fatal("project is a check, read is not")
	}
}
