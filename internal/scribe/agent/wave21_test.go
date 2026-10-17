package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// persistLog records every Persist call the agent makes.
type persistLog struct {
	mu    sync.Mutex
	calls []persistCall
}

type persistCall struct {
	msgs      []llm.Message
	rewritten bool
}

func (p *persistLog) save(msgs []llm.Message, rewritten bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, persistCall{msgs: msgs, rewritten: rewritten})
}

func (p *persistLog) last() persistCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[len(p.calls)-1]
}

// ---- 1. a panic in the turn goroutine ---------------------------------------

// A panic in the model loop (here, while the request is built for the second
// step, after a tool call) ends the turn with an error event, saves the
// history, and leaves the agent able to take the next prompt.
func TestPanicInTurnLoopEndsTurnSavesHistoryAndAgentStaysUsable(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(fnTool{name: "read", ro: true, run: func(context.Context, *tools.Env, json.RawMessage) tools.Result {
		return tools.Result{Output: "file body"}
	}})
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "c1", name: "read", args: `{"id":"a"}`}}}),
		turn(sseTurn{text: []string{"second answer"}}),
	))

	armed := true
	var mu sync.Mutex
	persisted := &persistLog{}
	log := &eventLog{}
	opts := baseOptions(f, reg, log, t.TempDir())
	opts.Persist = persisted.save
	// Tune runs inside the model loop, after the tool result is in the history.
	opts.Tune = func(_ Model, r *llm.Request) {
		mu.Lock()
		hot := armed
		mu.Unlock()
		if hot && len(r.Messages) >= 3 {
			panic("tune exploded")
		}
	}
	a := New(opts)

	stats, err := a.Run(context.Background(), "first", nil)
	if err == nil || !strings.Contains(err.Error(), "internal error: tune exploded") {
		t.Fatalf("Run error = %v, want the panic named", err)
	}
	if stats.Error != "internal error: tune exploded" {
		t.Fatalf("stats.Error = %q", stats.Error)
	}
	if stats.Duration == 0 {
		t.Fatalf("stats.Duration not set on a panicked turn")
	}

	errs := log.of(EvError)
	if len(errs) != 1 || errs[0].Text != "internal error: tune exploded" {
		t.Fatalf("error events = %+v, want one naming the panic", errs)
	}
	evs := log.snapshot()
	if n := len(evs); n < 2 || evs[n-2].Kind != EvError || evs[n-1].Kind != EvTurnDone {
		t.Fatalf("turn did not end with error then turn_done: %v", log.kinds())
	}
	if done := evs[len(evs)-1]; done.Stats == nil || done.Stats.Error == "" {
		t.Fatalf("turn_done does not carry the failure: %+v", done.Stats)
	}
	if a.Running() {
		t.Fatalf("agent still marked running after the panic")
	}

	// The history was saved with a full rewrite, and it holds the tool result
	// that ran before the panic.
	last := persisted.last()
	if !last.rewritten {
		t.Fatalf("emergency save was not a full rewrite")
	}
	if n := len(last.msgs); n == 0 || last.msgs[n-1].Role != llm.RoleTool {
		t.Fatalf("emergency save lost the tool result: %d messages", len(last.msgs))
	}

	// The next prompt runs normally.
	mu.Lock()
	armed = false
	mu.Unlock()
	stats, err = a.Run(context.Background(), "second", nil)
	if err != nil || stats.Error != "" {
		t.Fatalf("next turn failed: err=%v stats.Error=%q", err, stats.Error)
	}
	if got := a.Messages()[len(a.Messages())-1].Text(); got != "second answer" {
		t.Fatalf("next turn answer = %q", got)
	}
}

// ---- 2. final_response ------------------------------------------------------

// A model that delivers its answer with final_response ends the turn there:
// the answer is streamed as text, becomes the turn's final answer, and no
// further model request is sent.
func TestFinalResponseIsTheTurnsAnswerAndEndsIt(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(fnTool{name: "read", ro: true, run: func(context.Context, *tools.Env, json.RawMessage) tools.Result {
		return tools.Result{Output: "ok"}
	}})
	(&Planner{}).Register(reg)
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "c1", name: "read", args: `{"id":"a"}`}}}),
		turn(sseTurn{calls: []sseCall{{id: "c2", name: "final_response", args: `{"answer":"Done: three files changed."}`}}}),
	))
	log := &eventLog{}
	a := New(baseOptions(f, reg, log, t.TempDir()))

	stats, err := a.Run(context.Background(), "go", nil)
	must(t, err)
	if stats.Error != "" {
		t.Fatalf("stats.Error = %q", stats.Error)
	}
	if n := f.count(); n != 2 {
		t.Fatalf("model requests = %d, want 2 (the turn must end after final_response)", n)
	}
	texts := log.of(EvText)
	if len(texts) != 1 || texts[0].Text != "Done: three files changed." {
		t.Fatalf("text events = %+v, want the answer once", texts)
	}
	evs := log.snapshot()
	done := evs[len(evs)-1]
	if done.Kind != EvTurnDone || done.Text != "Done: three files changed." {
		t.Fatalf("turn_done = %+v, want the answer as its final text", done)
	}
	if got := toolMsg(t, f.requests()[1], "c1").text(); got != "ok" {
		t.Fatalf("earlier tool result = %q", got)
	}
	if got := a.Messages()[len(a.Messages())-1]; got.Role != llm.RoleTool {
		t.Fatalf("history should end with the final_response result, got role %s", got.Role)
	}
}

// An empty answer is refused and the turn goes on, so the model can answer.
func TestFinalResponseWithoutAnswerKeepsTurnGoing(t *testing.T) {
	reg := tools.NewRegistry()
	(&Planner{}).Register(reg)
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "c1", name: "final_response", args: `{"answer":"   "}`}}}),
		turn(sseTurn{text: []string{"Here is the answer."}}),
	))
	a := New(baseOptions(f, reg, &eventLog{}, t.TempDir()))

	stats, err := a.Run(context.Background(), "go", nil)
	must(t, err)
	if stats.Error != "" || f.count() != 2 {
		t.Fatalf("turn should continue after a refused final_response: error=%q requests=%d", stats.Error, f.count())
	}
	refused := toolMsg(t, f.requests()[1], "c1")
	if !strings.Contains(refused.text(), "needs the answer text") {
		t.Fatalf("refusal = %q", refused.text())
	}
	if got := a.Messages()[len(a.Messages())-1].Text(); got != "Here is the answer." {
		t.Fatalf("final answer = %q", got)
	}
}

// Restricted modes refuse final_response at execution time (the mode gate),
// so the answer there stays in text.
func TestFinalResponseRefusedInRestrictedMode(t *testing.T) {
	reg := tools.NewRegistry()
	(&Planner{}).Register(reg)
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "c1", name: "final_response", args: `{"answer":"Plan: do X."}`}}}),
		turn(sseTurn{text: []string{"Understood."}}),
	))
	opts := baseOptions(f, reg, &eventLog{}, t.TempDir())
	opts.Mode = "plan"
	a := New(opts)

	_, err := a.Run(context.Background(), "investigate", nil)
	must(t, err)
	refused := toolMsg(t, f.requests()[1], "c1")
	if !strings.Contains(refused.text(), "mode_restricted") {
		t.Fatalf("final_response in plan mode = %q", refused.text())
	}
	if got := a.Messages()[len(a.Messages())-1].Text(); got != "Understood." {
		t.Fatalf("final answer = %q", got)
	}
}

// final_response is in the main agent's registry only when the planner
// registers its tools; the answer is then taken from the last successful
// call in the step.
func TestFinalAnswerReadsLastSuccessfulCall(t *testing.T) {
	calls := []llm.Part{
		{Type: llm.PartToolCall, ToolCallID: "a", ToolName: "final_response", Args: json.RawMessage(`{"answer":"first"}`)},
		{Type: llm.PartToolCall, ToolCallID: "b", ToolName: "read", Args: json.RawMessage(`{}`)},
		{Type: llm.PartToolCall, ToolCallID: "c", ToolName: "final_response", Args: json.RawMessage(`{"answer":"second"}`)},
		{Type: llm.PartToolCall, ToolCallID: "d", ToolName: "final_response", Args: json.RawMessage(`{"answer":"refused"}`)},
	}
	results := []llm.Part{
		{Type: llm.PartToolResult, Result: "ok"},
		{Type: llm.PartToolResult, Result: "ok"},
		{Type: llm.PartToolResult, Result: "ok"},
		{Type: llm.PartToolResult, Result: "mode_restricted", IsError: true},
	}
	got, ok := finalAnswer(calls, results)
	if !ok || got != "second" {
		t.Fatalf("finalAnswer = %q, %v; want second, true", got, ok)
	}
	if _, ok := finalAnswer(calls[:1], []llm.Part{{IsError: true}}); ok {
		t.Fatalf("a refused call must not end the turn")
	}
}
