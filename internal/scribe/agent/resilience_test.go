package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// ---- a fake llm.Provider that plays one script per request -----------------

// streamScript writes one model response to out. It returns when the reply
// is complete or ctx is cancelled; the caller closes out.
type streamScript func(ctx context.Context, out chan<- llm.Event)

type scriptProv struct {
	mu      sync.Mutex
	scripts []streamScript
	reqs    []llm.Request
}

func (p *scriptProv) ID() string                                      { return "script" }
func (p *scriptProv) Models(context.Context) ([]llm.ModelInfo, error) { return nil, nil }

func (p *scriptProv) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reqs) >= len(p.scripts) {
		return nil, fmt.Errorf("scriptProv: unexpected request %d", len(p.reqs)+1)
	}
	script := p.scripts[len(p.reqs)]
	p.reqs = append(p.reqs, req)
	ch := make(chan llm.Event)
	go func() {
		defer close(ch)
		script(ctx, ch)
	}()
	return ch, nil
}

func (p *scriptProv) requests() []llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.Request(nil), p.reqs...)
}

func emitEv(ctx context.Context, out chan<- llm.Event, ev llm.Event) bool {
	select {
	case out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// streamOf sends evs and returns.
func streamOf(evs ...llm.Event) streamScript {
	return func(ctx context.Context, out chan<- llm.Event) {
		for _, ev := range evs {
			if !emitEv(ctx, out, ev) {
				return
			}
		}
	}
}

// stallOf sends evs and then goes silent until the agent cancels it.
func stallOf(evs ...llm.Event) streamScript {
	return func(ctx context.Context, out chan<- llm.Event) {
		for _, ev := range evs {
			if !emitEv(ctx, out, ev) {
				return
			}
		}
		<-ctx.Done()
	}
}

func textEv(s string) llm.Event { return llm.Event{Type: llm.EvText, Text: s} }

func finishEv(r llm.FinishReason) llm.Event {
	return llm.Event{Type: llm.EvFinish, FinishReason: r}
}

func usageFinish(r llm.FinishReason, in, out int) llm.Event {
	return llm.Event{Type: llm.EvFinish, FinishReason: r, Usage: llm.Usage{InputTokens: in, OutputTokens: out}}
}

func toolStartEv(id, name string) llm.Event {
	return llm.Event{Type: llm.EvToolCallStart, ToolCallID: id, ToolName: name}
}

func toolEndEv(id, name string, args json.RawMessage) llm.Event {
	return llm.Event{Type: llm.EvToolCallEnd, ToolCallID: id, ToolName: name, Args: args}
}

// callScript is one tool call followed by a finish that asks for tools.
func callScript(id, name, rawArgs string) streamScript {
	return streamOf(toolStartEv(id, name), toolEndEv(id, name, llm.ToolArgs(rawArgs)), finishEv(llm.FinishToolCalls))
}

// echoRegistry has one mutating tool "echo" that counts its runs and returns
// output and meta.
func echoRegistry(runs *int, output string, meta map[string]any) *tools.Registry {
	reg := tools.NewRegistry()
	reg.Register(fnTool{name: "echo", run: func(context.Context, *tools.Env, json.RawMessage) tools.Result {
		*runs++
		return tools.Result{Output: output, Meta: meta}
	}})
	return reg
}

func rsAgent(t *testing.T, p *scriptProv, reg *tools.Registry, mut func(*Options)) (*Agent, *eventLog) {
	t.Helper()
	log := &eventLog{}
	o := Options{
		Model: Model{Ref: "script/m", ID: "m", Provider: p, Info: priced},
		Tools: reg,
		Env:   tools.Env{Workdir: t.TempDir()},
		Sink:  log.sink,
	}
	if mut != nil {
		mut(&o)
	}
	return New(o), log
}

// resultParts returns the tool results a request carries.
func resultParts(req llm.Request) []llm.Part {
	var out []llm.Part
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			if p.Type == llm.PartToolResult {
				out = append(out, p)
			}
		}
	}
	return out
}

// ---- C1: Tune, Redact, AfterTool --------------------------------------------

func TestTuneEditsEveryRequest(t *testing.T) {
	runs := 0
	p := &scriptProv{scripts: []streamScript{
		callScript("c1", "echo", `{"id":"a"}`),
		streamOf(textEv("done"), finishEv(llm.FinishStop)),
	}}
	tuned := 0
	a, _ := rsAgent(t, p, echoRegistry(&runs, "ok", nil), func(o *Options) {
		o.Tune = func(_ Model, r *llm.Request) {
			tuned++
			r.MaxTokens = 4242
		}
	})
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	reqs := p.requests()
	if tuned != 2 || len(reqs) != 2 {
		t.Fatalf("tune calls=%d requests=%d, want 2 and 2", tuned, len(reqs))
	}
	for i, r := range reqs {
		if r.MaxTokens != 4242 {
			t.Errorf("request %d MaxTokens=%d, Tune did not reach the provider", i, r.MaxTokens)
		}
	}
}

func TestRedactScrubsOutputBeforeHistoryAndEvents(t *testing.T) {
	const secret = "sk-SECRET123"
	runs := 0
	p := &scriptProv{scripts: []streamScript{
		callScript("c1", "echo", `{"id":"a"}`),
		streamOf(textEv("done"), finishEv(llm.FinishStop)),
	}}
	a, log := rsAgent(t, p, echoRegistry(&runs, "key is "+secret, nil), func(o *Options) {
		o.Redact = func(s string) string { return strings.ReplaceAll(s, secret, "[redacted]") }
	})
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	res := resultParts(p.requests()[1])
	if len(res) != 1 || strings.Contains(res[0].Result, secret) || !strings.Contains(res[0].Result, "[redacted]") {
		t.Fatalf("model saw tool result %+v", res)
	}
	for _, e := range log.of(EvToolResult) {
		if e.Result == nil || strings.Contains(e.Result.Output, secret) {
			t.Fatalf("event leaked the secret: %+v", e.Result)
		}
	}
	for _, m := range a.Messages() {
		for _, part := range m.Parts {
			if strings.Contains(part.Result, secret) || strings.Contains(part.Text, secret) {
				t.Fatalf("history kept the secret in %+v", part)
			}
		}
	}
}

func TestAfterToolRunsBeforeRedact(t *testing.T) {
	const secret = "sk-SECRET123"
	runs := 0
	p := &scriptProv{scripts: []streamScript{
		callScript("c1", "echo", `{"id":"a"}`),
		streamOf(textEv("done"), finishEv(llm.FinishStop)),
	}}
	var seenName, seenArgs string
	var seenBeforeRedact string
	a, log := rsAgent(t, p, echoRegistry(&runs, "clean", nil), func(o *Options) {
		o.AfterTool = func(_ context.Context, name string, args json.RawMessage, res *tools.Result) {
			seenName, seenArgs = name, string(args)
			// Added here, so only a Redact that runs after AfterTool removes it.
			res.Output += " " + secret
			seenBeforeRedact = res.Output
		}
		o.Redact = func(s string) string { return strings.ReplaceAll(s, secret, "[redacted]") }
	})
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if seenName != "echo" || seenArgs != `{"id":"a"}` || !strings.Contains(seenBeforeRedact, secret) {
		t.Fatalf("AfterTool saw name=%q args=%q output=%q", seenName, seenArgs, seenBeforeRedact)
	}
	res := log.of(EvToolResult)
	if len(res) != 1 || strings.Contains(res[0].Result.Output, secret) {
		t.Fatalf("redaction did not run after AfterTool: %+v", res)
	}
}

// ---- C2: subagent usage ------------------------------------------------------

func TestSubagentUsageCountsTowardTurnTotals(t *testing.T) {
	sub := map[string]any{"subagent_usage": llm.Usage{InputTokens: 1000, OutputTokens: 200}}
	runs := 0
	p := &scriptProv{scripts: []streamScript{
		streamOf(toolStartEv("c1", "echo"), toolEndEv("c1", "echo", llm.ToolArgs(`{"id":"a"}`)), usageFinish(llm.FinishToolCalls, 10, 5)),
		streamOf(textEv("done"), usageFinish(llm.FinishStop, 10, 5)),
	}}
	a, _ := rsAgent(t, p, echoRegistry(&runs, "ok", sub), nil)
	stats, err := a.Run(context.Background(), "go", nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Tokens.InputTokens != 1020 || stats.Tokens.OutputTokens != 210 {
		t.Fatalf("turn tokens = %+v, want input 1020 output 210", stats.Tokens)
	}
	if u, _ := a.Usage(); u.InputTokens != 1020 || u.OutputTokens != 210 {
		t.Fatalf("session tokens = %+v", u)
	}
	// Main model prices: 2 steps of (10 in, 5 out), plus the subagent's
	// tokens estimated at the same prices because no cost was reported.
	want := 2*(10*3+5*15)/1e6 + (1000*3+200*15)/1e6
	if d := stats.CostUSD - want; d > 1e-12 || d < -1e-12 {
		t.Fatalf("cost = %v, want %v", stats.CostUSD, want)
	}
}

func TestSubagentReportedCostIsUsedWhenGiven(t *testing.T) {
	sub := map[string]any{
		"subagent_usage":    llm.Usage{InputTokens: 1000, OutputTokens: 200},
		"subagent_cost_usd": 0.5,
	}
	runs := 0
	p := &scriptProv{scripts: []streamScript{
		streamOf(toolStartEv("c1", "echo"), toolEndEv("c1", "echo", llm.ToolArgs(`{"id":"a"}`)), usageFinish(llm.FinishToolCalls, 10, 5)),
		streamOf(textEv("done"), usageFinish(llm.FinishStop, 10, 5)),
	}}
	a, _ := rsAgent(t, p, echoRegistry(&runs, "ok", sub), nil)
	stats, err := a.Run(context.Background(), "go", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := 2*(10*3+5*15)/1e6 + 0.5
	if d := stats.CostUSD - want; d > 1e-12 || d < -1e-12 {
		t.Fatalf("cost = %v, want %v", stats.CostUSD, want)
	}
}

// ---- 5.4 malformed tool arguments -------------------------------------------

func TestMalformedToolArgsAreRefusedAndTheModelIsTold(t *testing.T) {
	raw := `{"id":"` + strings.Repeat("y", 300) // truncated, never closed
	runs := 0
	p := &scriptProv{scripts: []streamScript{
		callScript("c1", "echo", raw), // provider passes the raw text as a JSON string
		streamOf(textEv("ok"), finishEv(llm.FinishStop)),
	}}
	a, log := rsAgent(t, p, echoRegistry(&runs, "should not run", nil), nil)
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("tool ran %d times with malformed arguments", runs)
	}
	res := resultParts(p.requests()[1])
	if len(res) != 1 || !res[0].IsError {
		t.Fatalf("model saw %+v, want one error result", res)
	}
	msg := res[0].Result
	if !strings.Contains(msg, "not valid JSON") || !strings.Contains(msg, fmt.Sprintf("%q", fragment(raw, 200))) {
		t.Fatalf("error does not carry the parse error and fragment: %q", msg)
	}
	if strings.Contains(msg, raw) {
		t.Fatalf("error carried more than the first 200 characters: %q", msg)
	}
	if got := log.of(EvToolResult); len(got) != 1 || got[0].Result == nil || !got[0].Result.IsError {
		t.Fatalf("tool_result events = %+v", got)
	}
}

func TestPlainInvalidArgsBytesAreAlsoRefused(t *testing.T) {
	runs := 0
	p := &scriptProv{scripts: []streamScript{
		streamOf(toolStartEv("c1", "echo"), toolEndEv("c1", "echo", json.RawMessage(`{"id":`)), finishEv(llm.FinishToolCalls)),
		streamOf(textEv("ok"), finishEv(llm.FinishStop)),
	}}
	a, _ := rsAgent(t, p, echoRegistry(&runs, "ok", nil), nil)
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	res := resultParts(p.requests()[1])
	if runs != 0 || len(res) != 1 || !res[0].IsError || !strings.Contains(res[0].Result, "not valid JSON") {
		t.Fatalf("runs=%d results=%+v", runs, res)
	}
}

func TestEmptyArgsMeansNoArgumentsAndStillRuns(t *testing.T) {
	runs := 0
	p := &scriptProv{scripts: []streamScript{
		streamOf(toolStartEv("c1", "echo"), toolEndEv("c1", "echo", nil), finishEv(llm.FinishToolCalls)),
		streamOf(textEv("ok"), finishEv(llm.FinishStop)),
	}}
	a, _ := rsAgent(t, p, echoRegistry(&runs, "ran", nil), nil)
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("tool with empty arguments ran %d times, want 1", runs)
	}
}

// ---- 5.4 content filter -------------------------------------------------------

func TestContentFilterEndsTheTurnWithAnError(t *testing.T) {
	p := &scriptProv{scripts: []streamScript{
		streamOf(textEv("I cannot help with"), finishEv(llm.FinishFilter)),
	}}
	a, log := rsAgent(t, p, tools.NewRegistry(), nil)
	stats, err := a.Run(context.Background(), "go", nil)
	if err == nil || !strings.Contains(stats.Error, "content filter") {
		t.Fatalf("err=%v stats.Error=%q, want a content filter error", err, stats.Error)
	}
	if n := len(p.requests()); n != 1 {
		t.Fatalf("content filter was retried: %d requests", n)
	}
	errs := log.of(EvError)
	if len(errs) != 1 || !strings.Contains(errs[0].Text, "content filter") {
		t.Fatalf("error events = %+v", errs)
	}
	if last := a.Messages()[len(a.Messages())-1]; last.Role != llm.RoleUser {
		t.Fatalf("filtered reply was kept in history: %+v", last)
	}
}

// ---- 5.3 stall watchdog -------------------------------------------------------

func TestStalledAnswerResumesFromItsPartialText(t *testing.T) {
	p := &scriptProv{scripts: []streamScript{
		stallOf(textEv("The quick brown ")),
		streamOf(textEv("fox jumps."), finishEv(llm.FinishStop)),
	}}
	a, log := rsAgent(t, p, tools.NewRegistry(), func(o *Options) {
		o.FirstTokenTimeout = 5 * time.Second
		o.InterTokenTimeout = 100 * time.Millisecond
	})
	stats, err := a.Run(context.Background(), "go", nil)
	if err != nil || stats.Error != "" {
		t.Fatalf("err=%v stats=%+v", err, stats)
	}
	reqs := p.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests=%d, want 2", len(reqs))
	}
	msgs := reqs[1].Messages
	n := len(msgs)
	if n < 3 || msgs[n-2].Role != llm.RoleAssistant || msgs[n-2].Text() != "The quick brown " ||
		msgs[n-1].Role != llm.RoleUser || msgs[n-1].Text() != "Continue." {
		t.Fatalf("resume request ends with %+v", msgs[n-2:])
	}
	var streamed strings.Builder
	for _, e := range log.of(EvText) {
		streamed.WriteString(e.Text)
	}
	if streamed.String() != "The quick brown fox jumps." {
		t.Fatalf("streamed text %q, want the partial and the continuation once each", streamed.String())
	}
	if got := log.of(EvTurnDone)[0].Text; got != "The quick brown fox jumps." {
		t.Fatalf("final answer %q", got)
	}
	// The resume messages are for the request only; history keeps one answer.
	last := a.Messages()[len(a.Messages())-1]
	if last.Role != llm.RoleAssistant || last.Text() != "The quick brown fox jumps." {
		t.Fatalf("history ends with %+v", last)
	}
	retries := log.of(EvRetry)
	if len(retries) != 1 || retries[0].Category != llm.CatStream || !strings.Contains(retries[0].Text, "stalled") {
		t.Fatalf("retry events = %+v", retries)
	}
}

func TestFirstTokenTimeoutGovernsSilenceBeforeContent(t *testing.T) {
	p := &scriptProv{scripts: []streamScript{
		stallOf(), // no text, no tool call
		streamOf(textEv("ok"), finishEv(llm.FinishStop)),
	}}
	a, log := rsAgent(t, p, tools.NewRegistry(), func(o *Options) {
		o.FirstTokenTimeout = 100 * time.Millisecond
		o.InterTokenTimeout = 30 * time.Second
	})
	start := time.Now()
	stats, err := a.Run(context.Background(), "go", nil)
	if err != nil || stats.Error != "" {
		t.Fatalf("err=%v stats=%+v", err, stats)
	}
	if el := time.Since(start); el > 10*time.Second {
		t.Fatalf("took %v: the 30s inter-token timeout applied before content", el)
	}
	if r := log.of(EvRetry); len(r) != 1 || !strings.Contains(r[0].Text, "100ms") {
		t.Fatalf("retry events = %+v", r)
	}
}

func TestInterTokenTimeoutGovernsSilenceAfterContent(t *testing.T) {
	p := &scriptProv{scripts: []streamScript{
		stallOf(textEv("hi")),
		streamOf(textEv(" there"), finishEv(llm.FinishStop)),
	}}
	a, log := rsAgent(t, p, tools.NewRegistry(), func(o *Options) {
		o.FirstTokenTimeout = 30 * time.Second
		o.InterTokenTimeout = 100 * time.Millisecond
	})
	start := time.Now()
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > 10*time.Second {
		t.Fatalf("took %v: the 30s first-token timeout applied after content", el)
	}
	if r := log.of(EvRetry); len(r) != 1 || !strings.Contains(r[0].Text, "100ms") {
		t.Fatalf("retry events = %+v", r)
	}
}

func TestStepTimeoutCapsAStreamThatKeepsGoing(t *testing.T) {
	drip := func(ctx context.Context, out chan<- llm.Event) {
		for {
			if !emitEv(ctx, out, textEv("x")) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	p := &scriptProv{scripts: []streamScript{drip, drip}}
	a, _ := rsAgent(t, p, tools.NewRegistry(), func(o *Options) {
		o.StepTimeout = 300 * time.Millisecond
		o.InterTokenTimeout = 5 * time.Second
		o.MaxStallRetries = 1
	})
	start := time.Now()
	stats, _ := a.Run(context.Background(), "go", nil)
	if !strings.Contains(stats.Error, "ran past") {
		t.Fatalf("stats.Error=%q, want the step cap", stats.Error)
	}
	if n := len(p.requests()); n != 2 {
		t.Fatalf("requests=%d, want the first attempt and one resume", n)
	}
	if el := time.Since(start); el > 10*time.Second {
		t.Fatalf("took %v", el)
	}
}

func TestStallResumesAreCappedAndGiveUp(t *testing.T) {
	p := &scriptProv{scripts: []streamScript{stallOf(), stallOf(), stallOf()}}
	a, log := rsAgent(t, p, tools.NewRegistry(), func(o *Options) {
		o.FirstTokenTimeout = 100 * time.Millisecond
		o.InterTokenTimeout = 100 * time.Millisecond
		o.MaxStallRetries = 2
	})
	stats, _ := a.Run(context.Background(), "go", nil)
	if n := len(p.requests()); n != 3 {
		t.Fatalf("requests=%d, want 3 (first attempt plus 2 resumes)", n)
	}
	if !strings.Contains(stats.Error, "giving up") {
		t.Fatalf("stats.Error=%q", stats.Error)
	}
	if r := log.of(EvRetry); len(r) != 2 {
		t.Fatalf("retry events=%d, want 2", len(r))
	}
	errs := log.of(EvError)
	if len(errs) != 1 || errs[0].Category != llm.CatStream {
		t.Fatalf("error events = %+v", errs)
	}
}

func TestStallAfterAToolCallRestartsInsteadOfResuming(t *testing.T) {
	runs := 0
	p := &scriptProv{scripts: []streamScript{
		stallOf(textEv("Let me check. "), toolStartEv("c1", "echo"), toolEndEv("c1", "echo", llm.ToolArgs(`{"id":"a"}`))),
		streamOf(textEv("done"), finishEv(llm.FinishStop)),
	}}
	a, _ := rsAgent(t, p, echoRegistry(&runs, "ok", nil), func(o *Options) {
		o.FirstTokenTimeout = 100 * time.Millisecond
		o.InterTokenTimeout = 100 * time.Millisecond
	})
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	reqs := p.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests=%d", len(reqs))
	}
	for _, m := range reqs[1].Messages {
		if m.Text() == "Continue." || m.Text() == "Let me check. " {
			t.Fatalf("restart request still carried the partial: %+v", m)
		}
	}
	if len(reqs[1].Messages) != 1 {
		t.Fatalf("restart request has %d messages, want the original prompt only", len(reqs[1].Messages))
	}
}

func TestStallThresholdDefaultsAndStallTimeoutCompatibility(t *testing.T) {
	def := New(Options{})
	if def.opts.FirstTokenTimeout != 180*time.Second || def.opts.InterTokenTimeout != 120*time.Second ||
		def.opts.StepTimeout != 900*time.Second || def.opts.MaxStallRetries != 3 {
		t.Fatalf("defaults = %v %v %v %d", def.opts.FirstTokenTimeout, def.opts.InterTokenTimeout, def.opts.StepTimeout, def.opts.MaxStallRetries)
	}
	old := New(Options{StallTimeout: 2 * time.Second})
	if old.opts.FirstTokenTimeout != 2*time.Second || old.opts.InterTokenTimeout != 2*time.Second {
		t.Fatalf("StallTimeout did not set both idle thresholds: %v %v", old.opts.FirstTokenTimeout, old.opts.InterTokenTimeout)
	}
	mixed := New(Options{StallTimeout: 2 * time.Second, FirstTokenTimeout: 7 * time.Second})
	if mixed.opts.FirstTokenTimeout != 7*time.Second || mixed.opts.InterTokenTimeout != 2*time.Second {
		t.Fatalf("explicit first-token timeout not kept: %v %v", mixed.opts.FirstTokenTimeout, mixed.opts.InterTokenTimeout)
	}
}

// A stall before any text keeps no partial, so the retry is the original request.
func TestStallWithNoTextRetriesTheOriginalRequest(t *testing.T) {
	p := &scriptProv{scripts: []streamScript{
		stallOf(),
		streamOf(textEv("ok"), finishEv(llm.FinishStop)),
	}}
	a, _ := rsAgent(t, p, tools.NewRegistry(), func(o *Options) {
		o.FirstTokenTimeout = 100 * time.Millisecond
	})
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	reqs := p.requests()
	if len(reqs[1].Messages) != len(reqs[0].Messages) {
		t.Fatalf("retry carried %d messages, original had %d", len(reqs[1].Messages), len(reqs[0].Messages))
	}
	if msg := (&stallError{after: time.Second}).Error(); !strings.Contains(msg, "stream stalled") {
		t.Fatalf("stall message %q is not classified as a stream error", msg)
	}
}

// BeforeTool runs once per call, with its name and arguments, before the
// tool itself runs (the cross-file check takes its baseline there).
func TestBeforeToolRunsBeforeTheTool(t *testing.T) {
	runs := 0
	p := &scriptProv{scripts: []streamScript{
		callScript("c1", "echo", `{"id":"a"}`),
		streamOf(textEv("done"), finishEv(llm.FinishStop)),
	}}
	var order []string
	a, _ := rsAgent(t, p, echoRegistry(&runs, "ok", nil), func(o *Options) {
		o.BeforeTool = func(_ context.Context, name string, args json.RawMessage) {
			order = append(order, fmt.Sprintf("before %s %s runs=%d", name, args, runs))
		}
		o.AfterTool = func(_ context.Context, name string, _ json.RawMessage, _ *tools.Result) {
			order = append(order, fmt.Sprintf("after runs=%d", runs))
		}
	})
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, "|") != `before echo {"id":"a"} runs=0|after runs=1` {
		t.Fatalf("order = %q", order)
	}
}

// The tool hooks learn the call's id from the context the agent passes.
func TestToolCallIDReachesBeforeTool(t *testing.T) {
	runs := 0
	p := &scriptProv{scripts: []streamScript{
		callScript("call-42", "echo", `{"id":"a"}`),
		streamOf(textEv("done"), finishEv(llm.FinishStop)),
	}}
	var seen string
	a, _ := rsAgent(t, p, echoRegistry(&runs, "ok", nil), func(o *Options) {
		o.AfterTool = func(ctx context.Context, _ string, _ json.RawMessage, _ *tools.Result) { seen = CallID(ctx) }
	})
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if seen != "call-42" {
		t.Fatalf("call id in the tool context = %q", seen)
	}
}
