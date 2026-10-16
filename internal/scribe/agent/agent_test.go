package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func kindsEqual(got, want []EventKind) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestPlainTextAnswerEventsAndUsage(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{text: []string{"Hel", "lo ", "there"}, usage: &sseUsage{prompt: 1000, completion: 100, cached: 400}}),
		turn(sseTurn{text: []string{"Again."}, usage: &sseUsage{prompt: 500, completion: 50}}),
	))
	log := &eventLog{}
	a := New(baseOptions(f, tools.NewRegistry(), log, t.TempDir()))

	stats, err := a.Run(context.Background(), "hi", nil)
	must(t, err)

	want := []EventKind{EvTurnStart, EvStepStart, EvText, EvText, EvText, EvStepFinish, EvContext, EvTurnDone}
	if got := log.kinds(); !kindsEqual(got, want) {
		t.Fatalf("event order = %v, want %v", got, want)
	}
	done := log.of(EvTurnDone)[0]
	if done.Text != "Hello there" || stats.Error != "" || stats.Steps != 1 {
		t.Fatalf("final text %q, stats %+v", done.Text, stats)
	}
	ctxEv := log.of(EvContext)[0]
	if ctxEv.ContextUsed != 1100 || ctxEv.ContextWindow != priced.ContextWindow {
		t.Fatalf("context event used=%d window=%d, want 1100/%d", ctxEv.ContextUsed, ctxEv.ContextWindow, priced.ContextWindow)
	}
	// Adapter splits cached tokens out of the prompt count.
	if stats.Tokens.InputTokens != 600 || stats.Tokens.CacheReadTokens != 400 || stats.Tokens.OutputTokens != 100 {
		t.Fatalf("usage after turn 1 = %+v", stats.Tokens)
	}
	if want := 0.00342; math.Abs(stats.CostUSD-want) > 1e-9 {
		t.Fatalf("cost after turn 1 = %v, want %v", stats.CostUSD, want)
	}

	stats2, err := a.Run(context.Background(), "and again", nil)
	must(t, err)
	// The second request carries the first exchange: system, user, assistant, user.
	req := f.requests()[1]
	var roles []string
	for _, m := range req.body.Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "system,user,assistant,user" {
		t.Fatalf("second request roles = %v", roles)
	}
	total, cost := a.Usage()
	if total.InputTokens != 1100 || total.OutputTokens != 150 {
		t.Fatalf("cumulative usage = %+v", total)
	}
	if want := 0.00567; math.Abs(cost-want) > 1e-9 || math.Abs(stats2.CostUSD-want) > 1e-9 {
		t.Fatalf("cumulative cost = %v (stats %v), want %v", cost, stats2.CostUSD, want)
	}
}

func TestToolLoopSendsCallsAndResults(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi from file"), 0o644))
	reg := tools.NewRegistry()
	reg.Register(readTool{})
	reg.Register(writeTool{})

	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "call_read", name: "read", args: `{"path":"notes.txt"}`}}}),
		turn(sseTurn{text: []string{"It says hi."}}),
	))
	log := &eventLog{}
	a := New(baseOptions(f, reg, log, dir))

	stats, err := a.Run(context.Background(), "what does notes.txt say", nil)
	must(t, err)
	if stats.Error != "" || a.Messages()[len(a.Messages())-1].Text() != "It says hi." {
		t.Fatalf("turn did not finish cleanly: %+v", stats)
	}
	if strings.Join(stats.ToolCalls, ",") != "read" {
		t.Fatalf("stats.ToolCalls = %v", stats.ToolCalls)
	}

	req := f.requests()[1]
	var call *struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	for _, m := range msgsWithRole(req, "assistant") {
		if len(m.ToolCalls) > 0 {
			call = &m.ToolCalls[0]
		}
	}
	if call == nil || call.ID != "call_read" || call.Function.Name != "read" || call.Function.Arguments != `{"path":"notes.txt"}` {
		t.Fatalf("assistant tool_calls in second request = %+v", call)
	}
	res := toolMsg(t, req, "call_read")
	if res.text() != "hi from file" {
		t.Fatalf("tool message content = %q", res.text())
	}
	if len(log.of(EvToolRun)) != 1 || len(log.of(EvToolResult)) != 1 {
		t.Fatalf("tool events: run=%d result=%d", len(log.of(EvToolRun)), len(log.of(EvToolResult)))
	}
}

func TestWriteToolReportsFilesEdited(t *testing.T) {
	dir := t.TempDir()
	reg := tools.NewRegistry()
	reg.Register(writeTool{})
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "call_w", name: "write_file", args: `{"path":"out.txt","content":"x"}`}}}),
		turn(sseTurn{text: []string{"Written."}}),
	))
	a := New(baseOptions(f, reg, &eventLog{}, dir))
	stats, err := a.Run(context.Background(), "write it", nil)
	must(t, err)
	if strings.Join(stats.FilesEdited, ",") != "out.txt" {
		t.Fatalf("FilesEdited = %v", stats.FilesEdited)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "out.txt")); string(b) != "x" {
		t.Fatalf("file content = %q", b)
	}
}

func TestReadOnlyCallsRunConcurrently(t *testing.T) {
	b := newBarrier(2)
	reg := tools.NewRegistry()
	reg.Register(fnTool{name: "probe", ro: true, run: func(context.Context, *tools.Env, json.RawMessage) tools.Result {
		// Both calls must be in flight at once for either to pass the barrier.
		if !b.wait(3 * time.Second) {
			return tools.Errorf("ran alone: barrier timed out")
		}
		return tools.Result{Output: "both in flight"}
	}})
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{
			{id: "call_a", name: "probe", args: `{"id":"a"}`},
			{id: "call_b", name: "probe", args: `{"id":"b"}`},
		}}),
		turn(sseTurn{text: []string{"ok"}}),
	))
	a := New(baseOptions(f, reg, &eventLog{}, t.TempDir()))
	_, err := a.Run(context.Background(), "probe both", nil)
	must(t, err)
	req := f.requests()[1]
	for _, id := range []string{"call_a", "call_b"} {
		if got := toolMsg(t, req, id).text(); got != "both in flight" {
			t.Fatalf("%s result = %q", id, got)
		}
	}
}

func TestMutatingCallsRunInOrderAroundReadOnlyBatches(t *testing.T) {
	var rec recorder
	b := newBarrier(2)
	var mu sync.Mutex
	var finished []string
	done := func(s string) { mu.Lock(); finished = append(finished, s); mu.Unlock() }

	reg := tools.NewRegistry()
	reg.Register(fnTool{name: "probe", ro: true, run: func(_ context.Context, _ *tools.Env, args json.RawMessage) tools.Result {
		if !b.wait(3 * time.Second) {
			return tools.Errorf("probe ran alone")
		}
		done(label(args) + "-read")
		return tools.Result{Output: label(args) + " ok"}
	}})
	reg.Register(fnTool{name: "mutate", ro: false, run: func(_ context.Context, _ *tools.Env, args json.RawMessage) tools.Result {
		rec.enter(label(args))
		time.Sleep(15 * time.Millisecond)
		rec.exit()
		done(label(args) + "-write")
		return tools.Result{Output: label(args) + " written"}
	}})
	reg.Register(fnTool{name: "peek", ro: true, run: func(_ context.Context, _ *tools.Env, args json.RawMessage) tools.Result {
		done(label(args) + "-peek")
		return tools.Result{Output: label(args) + " peeked"}
	}})

	f := newFake(t)
	// read-only a and b form one parallel batch; mutate x runs alone; peek c is
	// its own batch after the write.
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{
			{id: "c1", name: "probe", args: `{"id":"a"}`},
			{id: "c2", name: "probe", args: `{"id":"b"}`},
			{id: "c3", name: "mutate", args: `{"id":"x"}`},
			{id: "c4", name: "peek", args: `{"id":"c"}`},
		}}),
		turn(sseTurn{calls: []sseCall{
			{id: "c5", name: "mutate", args: `{"id":"p"}`},
			{id: "c6", name: "mutate", args: `{"id":"q"}`},
			{id: "c7", name: "mutate", args: `{"id":"r"}`},
		}}),
		turn(sseTurn{text: []string{"done"}}),
	))
	a := New(baseOptions(f, reg, &eventLog{}, t.TempDir()))
	_, err := a.Run(context.Background(), "mixed", nil)
	must(t, err)

	mu.Lock()
	order := append([]string(nil), finished...)
	mu.Unlock()
	pos := func(s string) int {
		for i, v := range order {
			if v == s {
				return i
			}
		}
		t.Fatalf("%s never finished; order %v", s, order)
		return -1
	}
	if !(pos("x-write") > pos("a-read") && pos("x-write") > pos("b-read")) {
		t.Fatalf("mutate x ran before the read-only batch finished: %v", order)
	}
	if !(pos("c-peek") > pos("x-write")) {
		t.Fatalf("peek c ran before mutate x: %v", order)
	}

	// Consecutive mutations never overlap and keep their call order.
	seq, peak := rec.snapshot()
	if peak != 1 {
		t.Fatalf("mutating calls overlapped (peak %d)", peak)
	}
	if strings.Join(seq, ",") != "x,p,q,r" {
		t.Fatalf("mutating order = %v", seq)
	}

	// Results go back in call order, one per call ID.
	req := f.requests()[1]
	for i, id := range []string{"c1", "c2", "c3", "c4"} {
		got := msgsWithRole(req, "tool")[i]
		if got.ToolCallID != id {
			t.Fatalf("tool message %d answers %s, want %s", i, got.ToolCallID, id)
		}
	}
}

func TestUnknownToolAndPanicBecomeErrorResults(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(readTool{})
	reg.Register(writeTool{})
	reg.Register(fnTool{name: "boom", ro: false, run: func(context.Context, *tools.Env, json.RawMessage) tools.Result {
		panic("kaboom")
	}})
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{
			{id: "call_nope", name: "teleport", args: `{}`},
			{id: "call_boom", name: "boom", args: `{}`},
		}}),
		turn(sseTurn{text: []string{"Recovered."}}),
	))
	a := New(baseOptions(f, reg, &eventLog{}, t.TempDir()))
	stats, err := a.Run(context.Background(), "go", nil)
	must(t, err)
	if stats.Error != "" {
		t.Fatalf("loop should continue after tool failures, stats.Error=%q", stats.Error)
	}
	req := f.requests()[1]
	unknown := toolMsg(t, req, "call_nope")
	if !strings.Contains(unknown.text(), `unknown tool "teleport"`) ||
		!strings.Contains(unknown.text(), "Available tools: boom, read, write_file") {
		t.Fatalf("unknown-tool result = %q", unknown.text())
	}
	boom := toolMsg(t, req, "call_boom")
	if boom.text() != "error: internal error in boom: kaboom" {
		t.Fatalf("panic result = %q", boom.text())
	}
	if a.Messages()[len(a.Messages())-1].Text() != "Recovered." {
		t.Fatalf("final message not reached")
	}
}

func TestPlanModeRejectsMutatingTools(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("plan me"), 0o644))
	reg := tools.NewRegistry()
	reg.Register(readTool{})
	reg.Register(writeTool{})
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{
			{id: "call_r", name: "read", args: `{"path":"notes.txt"}`},
			{id: "call_w", name: "write_file", args: `{"path":"blocked.txt","content":"x"}`},
		}}),
		turn(sseTurn{text: []string{"Understood."}}),
	))
	opts := baseOptions(f, reg, &eventLog{}, dir)
	opts.Mode = "plan"
	a := New(opts)
	_, err := a.Run(context.Background(), "investigate", nil)
	must(t, err)

	req := f.requests()[1]
	if got := toolMsg(t, req, "call_r").text(); got != "plan me" {
		t.Fatalf("read-only call in plan mode = %q", got)
	}
	if got := toolMsg(t, req, "call_w").text(); !strings.Contains(got, "mode_restricted") {
		t.Fatalf("write in plan mode = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("write ran in plan mode: %v", err)
	}
}

func TestRetriesRateLimitThenSucceeds(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t,
		apiError(429, "0", `{"error":{"type":"rate_limit_error","message":"slow down"}}`),
		turn(sseTurn{text: []string{"ok"}}),
	))
	log := &eventLog{}
	a := New(baseOptions(f, tools.NewRegistry(), log, t.TempDir()))
	stats, err := a.Run(context.Background(), "hi", nil)
	must(t, err)
	if stats.Error != "" || f.count() != 2 {
		t.Fatalf("stats=%+v requests=%d", stats, f.count())
	}
	retries := log.of(EvRetry)
	// Retry-After: 0 means "retry now", so the delay is zero, not the default backoff.
	if len(retries) != 1 || retries[0].Category != llm.CatRateLimited || retries[0].Attempt != 1 || retries[0].Delay != 0 {
		t.Fatalf("retry events = %+v", retries)
	}
}

func TestNonRetryableErrorEndsTurn(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t,
		apiError(401, "", `{"error":{"message":"bad key","type":"invalid_request_error"}}`),
	))
	log := &eventLog{}
	a := New(baseOptions(f, tools.NewRegistry(), log, t.TempDir()))
	stats, err := a.Run(context.Background(), "hi", nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("Run error = %v", err)
	}
	if stats.Error == "" || f.count() != 1 {
		t.Fatalf("stats=%+v requests=%d", stats, f.count())
	}
	if len(log.of(EvRetry)) != 0 {
		t.Fatalf("auth error was retried")
	}
	errs := log.of(EvError)
	if len(errs) != 1 || errs[0].Category != llm.CatAuth {
		t.Fatalf("error events = %+v", errs)
	}
}

func TestFallbackAfterRetriesExhausted(t *testing.T) {
	primary := newFake(t)
	primary.setPick(scripted(t,
		apiError(503, "", `{"error":{"message":"overloaded"}}`),
		apiError(503, "", `{"error":{"message":"overloaded"}}`),
	))
	backup := newFake(t)
	backup.setPick(scripted(t, turn(sseTurn{text: []string{"from backup"}})))

	log := &eventLog{}
	opts := baseOptions(primary, tools.NewRegistry(), log, t.TempDir())
	opts.MaxRetries = 1
	opts.Fallback = []Model{fakeModel(backup.srv.URL, "fake/backup", priced)}
	a := New(opts)

	stats, err := a.Run(context.Background(), "hi", nil)
	must(t, err)
	if stats.Error != "" {
		t.Fatalf("stats.Error = %q", stats.Error)
	}
	fb := log.of(EvFallback)
	if len(fb) != 1 || fb[0].Model != "fake/backup" {
		t.Fatalf("fallback events = %+v", fb)
	}
	if primary.count() != 2 || backup.count() != 1 {
		t.Fatalf("requests primary=%d backup=%d", primary.count(), backup.count())
	}
	if a.CurrentModel().Ref != "fake/backup" {
		t.Fatalf("current model = %s", a.CurrentModel().Ref)
	}
	if got := log.of(EvTurnDone)[0].Text; got != "from backup" {
		t.Fatalf("final text = %q", got)
	}
}

func TestStalledStreamIsRetried(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t,
		f.hang(""), // headers, then silence
		turn(sseTurn{text: []string{"recovered"}}),
	))
	log := &eventLog{}
	opts := baseOptions(f, tools.NewRegistry(), log, t.TempDir())
	opts.StallTimeout = 200 * time.Millisecond
	a := New(opts)

	stats, err := a.Run(context.Background(), "hi", nil)
	must(t, err)
	if stats.Error != "" || f.count() != 2 {
		t.Fatalf("stats=%+v requests=%d", stats, f.count())
	}
	retries := log.of(EvRetry)
	if len(retries) != 1 || retries[0].Category != llm.CatStream || !strings.Contains(retries[0].Text, "stalled") {
		t.Fatalf("retry events = %+v", retries)
	}
}

func TestLengthFinishAutoContinuesTwice(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{text: []string{"part one"}, finish: "length"}),
		turn(sseTurn{text: []string{" part two"}, finish: "length"}),
		turn(sseTurn{text: []string{" part three"}, finish: "length"}),
	))
	a := New(baseOptions(f, tools.NewRegistry(), &eventLog{}, t.TempDir()))
	stats, err := a.Run(context.Background(), "write a lot", nil)
	must(t, err)

	// Two continuations are sent; the third cut-off ends the turn.
	if f.count() != 3 || stats.Steps != 3 {
		t.Fatalf("requests=%d steps=%d, want 3/3", f.count(), stats.Steps)
	}
	for _, i := range []int{1, 2} {
		users := msgsWithRole(f.requests()[i], "user")
		if last := users[len(users)-1].text(); !strings.Contains(last, "cut off by the output limit") {
			t.Fatalf("request %d did not get the continue message: %q", i+1, last)
		}
	}
	if got := a.Messages()[len(a.Messages())-1].Text(); got != " part three" {
		t.Fatalf("final text = %q", got)
	}
}

func TestSteeringIsDeliveredAtStepBoundary(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	reg := tools.NewRegistry()
	reg.Register(fnTool{name: "slow", ro: true, run: func(context.Context, *tools.Env, json.RawMessage) tools.Result {
		close(started)
		<-release
		return tools.Result{Output: "slow done"}
	}})
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "call_1", name: "slow", args: `{}`}}}),
		turn(sseTurn{text: []string{"Switching."}}),
	))
	log := &eventLog{}
	a := New(baseOptions(f, reg, log, t.TempDir()))

	done := make(chan error, 1)
	go func() {
		_, err := a.Run(context.Background(), "do it", nil)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("tool never started")
	}
	// The queue holds five messages; the sixth is refused.
	for i := 0; i < 5; i++ {
		if !a.Steer(fmt.Sprintf("note %d", i)) {
			t.Fatalf("steer %d refused before the queue is full", i)
		}
	}
	if a.Steer("note 5") {
		t.Fatal("sixth steer accepted; queue should hold five")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	req := f.requests()[1]
	last := msgsWithRole(req, "user")
	steer := last[len(last)-1].text()
	if !strings.Contains(steer, "<steering>") || !strings.Contains(steer, "note 0") || !strings.Contains(steer, "note 4") {
		t.Fatalf("second request steering block = %q", steer)
	}
	if strings.Contains(steer, "note 5") {
		t.Fatalf("refused steer leaked into the request")
	}
	if n := len(log.of(EvSteering)); n != 5 {
		t.Fatalf("EvSteering count = %d, want 5", n)
	}
	if len(a.Queued()) != 0 {
		t.Fatalf("queue not drained: %v", a.Queued())
	}
}

func TestAbortCancelsStreamingTurn(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t, f.hang("partial")))
	log := &eventLog{}
	a := New(baseOptions(f, tools.NewRegistry(), log, t.TempDir()))

	type result struct {
		stats TurnStats
		err   error
	}
	done := make(chan result, 1)
	go func() {
		st, err := a.Run(context.Background(), "long task", nil)
		done <- result{st, err}
	}()
	waitUntil(t, 5*time.Second, "first streamed text", func() bool { return len(log.of(EvText)) > 0 })
	a.Abort()

	select {
	case r := <-done:
		if !errors.Is(r.err, context.Canceled) {
			t.Fatalf("Run error = %v, want context.Canceled", r.err)
		}
		if !r.stats.Aborted || r.stats.Error != "" {
			t.Fatalf("stats = %+v", r.stats)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Abort")
	}
	if a.Running() {
		t.Fatal("agent still marked running after abort")
	}
}

func TestLoopHintAfterThreeIdenticalCalls(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(readTool{})
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("same"), 0o644))
	same := func(id string) sseTurn {
		return sseTurn{calls: []sseCall{{id: id, name: "read", args: `{"path":"a.txt"}`}}}
	}
	f := newFake(t)
	f.setPick(scripted(t,
		turn(same("call_1")),
		turn(same("call_2")),
		turn(same("call_3")),
		turn(sseTurn{text: []string{"giving up"}}),
	))
	a := New(baseOptions(f, reg, &eventLog{}, dir))
	_, err := a.Run(context.Background(), "loop", nil)
	must(t, err)

	const hint = "same tool call three times"
	reqs := f.requests()
	if strings.Contains(reqs[2].body.Messages[len(reqs[2].body.Messages)-1].text(), hint) {
		t.Fatal("hint fired after only two identical calls")
	}
	if !strings.Contains(lastUserText(reqs[3]), hint) {
		t.Fatalf("no loop hint after three identical calls: %q", lastUserText(reqs[3]))
	}
}

func TestMaxStepsStopsTheLoop(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(readTool{})
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644))
	f := newFake(t)
	f.setPick(func(n int, _ recordedReq) reply {
		return turn(sseTurn{calls: []sseCall{{id: fmt.Sprintf("call_%d", n), name: "read", args: fmt.Sprintf(`{"path":"a%d.txt"}`, n)}}})
	})
	opts := baseOptions(f, reg, &eventLog{}, dir)
	opts.MaxSteps = 2
	a := New(opts)
	stats, err := a.Run(context.Background(), "forever", nil)
	if err == nil || !strings.Contains(err.Error(), "max steps reached (2)") {
		t.Fatalf("Run error = %v", err)
	}
	if f.count() != 2 || stats.Steps != 2 || !strings.Contains(stats.Error, "max steps") {
		t.Fatalf("requests=%d stats=%+v", f.count(), stats)
	}
}

type testHooks struct {
	mu     sync.Mutex
	block  map[string]string
	extra  string
	pre    []string
	stops  []TurnStats
	postOn string
	post   string
}

func (h *testHooks) PreTool(_ context.Context, tool string, _ json.RawMessage) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pre = append(h.pre, tool)
	return h.block[tool]
}

func (h *testHooks) PostTool(_ context.Context, tool string, _ json.RawMessage, _ tools.Result) string {
	if tool == h.postOn {
		return h.post
	}
	return ""
}

func (h *testHooks) Prompt(context.Context, string) (string, string) { return h.extra, "" }

func (h *testHooks) Stop(_ context.Context, st TurnStats) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stops = append(h.stops, st)
}

func TestHooksBlockAnnotateAndObserve(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644))
	reg := tools.NewRegistry()
	reg.Register(readTool{})
	reg.Register(writeTool{})
	h := &testHooks{
		block:  map[string]string{"write_file": "policy says no"},
		extra:  "Be terse.",
		postOn: "read",
		post:   "lint: clean",
	}
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{
			{id: "call_w", name: "write_file", args: `{"path":"blocked.txt","content":"x"}`},
			{id: "call_r", name: "read", args: `{"path":"a.txt"}`},
		}}),
		turn(sseTurn{text: []string{"Done."}}),
	))
	opts := baseOptions(f, reg, &eventLog{}, dir)
	opts.Hooks = h
	a := New(opts)
	_, err := a.Run(context.Background(), "go", nil)
	must(t, err)

	req := f.requests()[0]
	if !strings.Contains(lastUserText(req), "<system-reminder>\nBe terse.\n</system-reminder>") {
		t.Fatalf("prompt hook context missing: %q", lastUserText(req))
	}
	next := f.requests()[1]
	if got := toolMsg(t, next, "call_w").text(); got != "error: blocked by a hook: policy says no" {
		t.Fatalf("blocked call result = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("blocked write ran: %v", err)
	}
	if got := toolMsg(t, next, "call_r").text(); got != "hi\n\nlint: clean" {
		t.Fatalf("post-tool annotation missing: %q", got)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if strings.Join(h.pre, ",") != "write_file,read" {
		t.Fatalf("PreTool saw %v", h.pre)
	}
	if len(h.stops) != 1 || h.stops[0].Steps != 2 {
		t.Fatalf("Stop hook got %+v", h.stops)
	}
}

func TestPersistSeesGrowingHistory(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644))
	reg := tools.NewRegistry()
	reg.Register(readTool{})
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "call_1", name: "read", args: `{"path":"a.txt"}`}}}),
		turn(sseTurn{text: []string{"ok"}}),
	))
	var mu sync.Mutex
	var sizes []int
	var rewrites []bool
	var last []llm.Message
	opts := baseOptions(f, reg, &eventLog{}, dir)
	opts.Persist = func(msgs []llm.Message, rewritten bool) {
		mu.Lock()
		defer mu.Unlock()
		sizes = append(sizes, len(msgs))
		rewrites = append(rewrites, rewritten)
		last = msgs
	}
	a := New(opts)
	_, err := a.Run(context.Background(), "go", nil)
	must(t, err)

	mu.Lock()
	defer mu.Unlock()
	// user, assistant(call), tool result, assistant(final): at least one save each.
	if len(sizes) < 4 || sizes[0] != 1 {
		t.Fatalf("persist sizes = %v", sizes)
	}
	for i := 1; i < len(sizes); i++ {
		if sizes[i] < sizes[i-1] {
			t.Fatalf("history shrank without compaction: %v", sizes)
		}
	}
	for _, r := range rewrites {
		if r {
			t.Fatal("persist reported a rewrite without compaction")
		}
	}
	if len(last) != len(a.Messages()) {
		t.Fatalf("last persisted snapshot has %d messages, agent has %d", len(last), len(a.Messages()))
	}
}
