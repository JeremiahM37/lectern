package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// ---- test doubles ----------------------------------------------------------

// probeTool reports what the environment gives it: whether a dispatch guard
// is published, and the input it was run with.
type probeTool struct {
	guard atomic.Bool
	seen  chan json.RawMessage
}

func (p *probeTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: "probe", Description: "Probe.", Schema: tools.Schema(map[string]any{"path": tools.Str("p")}, "path")}
}
func (p *probeTool) ReadOnly() bool { return true }
func (p *probeTool) Run(_ context.Context, env *tools.Env, args json.RawMessage) tools.Result {
	g, ok := tools.Service[DispatchGuard](env, "dispatch")
	p.guard.Store(ok && g != nil && g.DispatchActive())
	if p.seen != nil {
		p.seen <- args
	}
	return tools.Result{Output: "probed"}
}

// hooksDouble records the lifecycle calls it receives. It implements every
// optional capability, so the agent's type assertions can be tested.
type hooksDouble struct {
	mu        sync.Mutex
	compacts  int
	notices   []string
	preTool   int
	rewrite   json.RawMessage // when set, PreToolUpdate replaces the input
	block     string          // when set, PreToolUpdate refuses the call
	postCalls int
}

func (h *hooksDouble) PreTool(context.Context, string, json.RawMessage) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.preTool++
	return ""
}
func (h *hooksDouble) PostTool(context.Context, string, json.RawMessage, tools.Result) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.postCalls++
	return ""
}
func (h *hooksDouble) Prompt(context.Context, string) (string, string) { return "", "" }
func (h *hooksDouble) Stop(context.Context, TurnStats)                 {}
func (h *hooksDouble) PreCompact(context.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.compacts++
}
func (h *hooksDouble) Notification(_ context.Context, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notices = append(h.notices, msg)
}
func (h *hooksDouble) PreToolUpdate(_ context.Context, _ string, args json.RawMessage) (json.RawMessage, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.block != "" {
		return args, h.block
	}
	if h.rewrite != nil {
		return h.rewrite, ""
	}
	return args, ""
}

// staticCompactor summarises nothing and keeps the last message.
type staticCompactor struct {
	err   error
	calls int
}

func (c *staticCompactor) Compact(_ context.Context, msgs []llm.Message) ([]llm.Message, string, error) {
	c.calls++
	if c.err != nil {
		return nil, "", c.err
	}
	return msgs[len(msgs)-1:], "summary", nil
}

// manyMessages is a history of n short user messages.
func manyMessages(n int) []llm.Message {
	out := make([]llm.Message, n)
	for i := range out {
		out[i] = llm.UserText("message")
	}
	return out
}

// guardDouble is a dispatch guard with a fixed answer.
type guardDouble bool

func (g guardDouble) DispatchActive() bool { return bool(g) }

// ---- guard -------------------------------------------------------------------

func TestDispatchGuardIsPublishedToTools(t *testing.T) {
	f := newFake(t)
	probe := &probeTool{}
	reg := tools.NewRegistry()
	reg.Register(probe)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "c1", name: "probe", args: `{"path":"x"}`}}}),
		turn(sseTurn{text: []string{"done"}}),
	))
	log := &eventLog{}
	opts := baseOptions(f, reg, log, t.TempDir())
	opts.Guard = guardDouble(true)
	a := New(opts)
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if !probe.guard.Load() {
		t.Fatal("tool did not see an active dispatch guard under Services[\"dispatch\"]")
	}
	if a.DispatchActive() != true {
		t.Fatal("agent with a guard that reports true must report active")
	}
}

func TestDispatchGuardOffWhenNothingDispatches(t *testing.T) {
	a := New(Options{})
	if a.DispatchActive() {
		t.Fatal("a fresh agent reports an active dispatch")
	}
	d := &Dispatcher{}
	if d.DispatchActive() {
		t.Fatal("a fresh dispatcher reports an active dispatch")
	}
}

func TestDispatcherGuardIsActiveWhileDispatchRuns(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	gate := make(chan struct{})
	h.f.setPick(func(_ int, req recordedReq) reply {
		return h.f.gated(gate, turn(sseTurn{text: []string{"report"}}))
	})
	tr := tools.NewRegistry()
	h.d.Register(tr)
	dispatch, _ := tr.Get("dispatch")
	env := &tools.Env{Workdir: h.dir, CallID: "c", Mode: "default"}
	done := make(chan struct{})
	go func() {
		dispatch.Run(context.Background(), env, json.RawMessage(`{"tasks":[{"task":"look","role":"explore"}]}`))
		close(done)
	}()
	waitUntil(t, 5*time.Second, "dispatch to start", func() bool { return h.d.DispatchActive() })
	close(gate)
	<-done
	if h.d.DispatchActive() {
		t.Fatal("guard still active after the dispatch returned")
	}
}

// ---- hooks ---------------------------------------------------------------------

func TestPreCompactAndNotificationCallSites(t *testing.T) {
	f := newFake(t)
	// A 400 is not retried, so the turn ends with an error the user sees.
	f.setPick(scripted(t, apiError(400, "", `{"error":{"message":"bad request"}}`)))
	hk := &hooksDouble{}
	log := &eventLog{}
	opts := baseOptions(f, tools.NewRegistry(), log, t.TempDir())
	opts.Hooks = hk
	a := New(opts)
	a.SetMessages(manyMessages(10))
	a.opts.Compactor = &staticCompactor{}
	if err := a.Compact(context.Background(), "manual"); err != nil {
		t.Fatal(err)
	}
	if hk.compacts != 1 {
		t.Fatalf("PreCompact ran %d times, want 1", hk.compacts)
	}
	if _, err := a.Run(context.Background(), "hi", nil); err == nil {
		t.Fatal("expected the turn to fail")
	}
	if len(hk.notices) != 1 || !strings.Contains(hk.notices[0], "bad request") {
		t.Fatalf("notices = %v, want one naming the error", hk.notices)
	}
}

func TestNoNotificationForAbortedTurn(t *testing.T) {
	hk := &hooksDouble{}
	f := newFake(t)
	f.setPick(scripted(t, turn(sseTurn{text: []string{"ok"}})))
	opts := baseOptions(f, tools.NewRegistry(), &eventLog{}, t.TempDir())
	opts.Hooks = hk
	a := New(opts)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = a.Run(ctx, "hi", nil)
	if len(hk.notices) != 0 {
		t.Fatalf("aborted turn sent notices: %v", hk.notices)
	}
}

func TestPreToolUpdateRewritesInputAndBlocks(t *testing.T) {
	f := newFake(t)
	probe := &probeTool{seen: make(chan json.RawMessage, 2)}
	reg := tools.NewRegistry()
	reg.Register(probe)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "c1", name: "probe", args: `{"path":"original"}`}}}),
		turn(sseTurn{text: []string{"done"}}),
	))
	hk := &hooksDouble{rewrite: json.RawMessage(`{"path":"rewritten"}`)}
	opts := baseOptions(f, reg, &eventLog{}, t.TempDir())
	opts.Hooks = hk
	if _, err := New(opts).Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if got := <-probe.seen; string(got) != `{"path":"rewritten"}` {
		t.Fatalf("tool ran with %s, want the rewritten input", got)
	}
	if hk.preTool != 0 {
		t.Fatalf("PreTool ran %d times; a rewriting hook replaces it", hk.preTool)
	}

	// A refusal stops the call without running the tool.
	f2 := newFake(t)
	probe2 := &probeTool{seen: make(chan json.RawMessage, 1)}
	reg2 := tools.NewRegistry()
	reg2.Register(probe2)
	f2.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "c1", name: "probe", args: `{"path":"x"}`}}}),
		turn(sseTurn{text: []string{"done"}}),
	))
	opts2 := baseOptions(f2, reg2, &eventLog{}, t.TempDir())
	opts2.Hooks = &hooksDouble{block: "not today"}
	log2 := &eventLog{}
	opts2.Sink = log2.sink
	if _, err := New(opts2).Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-probe2.seen:
		t.Fatal("a blocked call ran the tool")
	default:
	}
	res := log2.of(EvToolResult)
	if len(res) != 1 || !res[0].Result.IsError || !strings.Contains(res[0].Result.Output, "not today") {
		t.Fatalf("blocked result = %+v", res)
	}
}

// ---- fallback, timeout, sequencing ----------------------------------------

func TestPerModelFallbackChainOverridesShared(t *testing.T) {
	f := newFake(t)
	// 503 is retried; after the retries the chain moves on to the override.
	f.setPick(scripted(t, apiError(503, "", `{"error":{"message":"busy"}}`), apiError(503, "", `{"error":{"message":"busy"}}`), turn(sseTurn{text: []string{"from the override"}})))
	log := &eventLog{}
	opts := baseOptions(f, tools.NewRegistry(), log, t.TempDir())
	opts.MaxRetries = 1
	opts.RetryBase = time.Millisecond
	opts.Model = fakeModel(f.srv.URL, "fake/primary", priced)
	opts.Fallback = []Model{fakeModel(f.srv.URL, "fake/shared", priced)}
	opts.FallbackFor = map[string][]Model{
		"fake/primary": {fakeModel(f.srv.URL, "fake/override", priced)},
	}
	if _, err := New(opts).Run(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	fb := log.of(EvFallback)
	if len(fb) != 1 || fb[0].Model != "fake/override" {
		t.Fatalf("fallback events = %+v, want one to fake/override", fb)
	}
}

func TestPerModelFallbackUsesSharedListWhenNoOverride(t *testing.T) {
	a := New(Options{
		Model:       fakeModel("http://x", "p/one", priced),
		Fallback:    []Model{{Ref: "p/shared"}},
		FallbackFor: map[string][]Model{"p/other": {{Ref: "p/else"}}},
	})
	if got := a.fallbackFor("p/one"); len(got) != 1 || got[0].Ref != "p/shared" {
		t.Fatalf("fallbackFor(p/one) = %+v, want the shared list", got)
	}
	if got := a.fallbackFor("p/other"); len(got) != 1 || got[0].Ref != "p/else" {
		t.Fatalf("fallbackFor(p/other) = %+v, want its own list", got)
	}
}

// hangingTool never returns unless it is told to, and ignores its context.
type hangingTool struct{ release chan struct{} }

func (hangingTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: "hang", Description: "Hangs.", Schema: tools.Schema(map[string]any{})}
}
func (hangingTool) ReadOnly() bool { return true }
func (h hangingTool) Run(context.Context, *tools.Env, json.RawMessage) tools.Result {
	<-h.release
	return tools.Result{Output: "late"}
}

func TestToolTimeoutAbandonsHungTool(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	reg := tools.NewRegistry()
	reg.Register(hangingTool{release: release})
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "c1", name: "hang", args: `{}`}}}),
		turn(sseTurn{text: []string{"moved on"}}),
	))
	log := &eventLog{}
	opts := baseOptions(f, reg, log, t.TempDir())
	opts.ToolTimeout = 50 * time.Millisecond
	a := New(opts)
	start := time.Now()
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("turn took %s; the hung tool was not abandoned", time.Since(start))
	}
	res := log.of(EvToolResult)
	if len(res) != 1 || !res[0].Result.IsError || !strings.Contains(res[0].Result.Output, "did not finish") {
		t.Fatalf("tool result = %+v", res)
	}
}

func TestToolTimeoutSkipsDispatchAndUserTools(t *testing.T) {
	for _, name := range []string{"dispatch", "ask_user", "plan"} {
		if !unbounded(name) {
			t.Errorf("%s should not be held to the tool timeout", name)
		}
	}
	for _, name := range []string{"shell", "read", "edit_file"} {
		if unbounded(name) {
			t.Errorf("%s should be held to the tool timeout", name)
		}
	}
}

// slowTool records how many copies run at once.
type slowTool struct {
	cur, peak *atomic.Int32
}

func (slowTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: "slow", Description: "Slow.", Schema: tools.Schema(map[string]any{"n": tools.Str("n")}, "n")}
}
func (slowTool) ReadOnly() bool { return true }
func (s slowTool) Run(context.Context, *tools.Env, json.RawMessage) tools.Result {
	n := s.cur.Add(1)
	for {
		p := s.peak.Load()
		if n <= p || s.peak.CompareAndSwap(p, n) {
			break
		}
	}
	time.Sleep(30 * time.Millisecond)
	s.cur.Add(-1)
	return tools.Result{Output: "ok"}
}

func sequencingPeak(t *testing.T, sequential bool) int32 {
	t.Helper()
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "a", name: "slow", args: `{"n":"1"}`}, {id: "b", name: "slow", args: `{"n":"2"}`}}}),
		turn(sseTurn{text: []string{"done"}}),
	))
	var cur, peak atomic.Int32
	reg := tools.NewRegistry()
	reg.Register(slowTool{cur: &cur, peak: &peak})
	opts := baseOptions(f, reg, &eventLog{}, t.TempDir())
	opts.SequentialTools = sequential
	if _, err := New(opts).Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	return peak.Load()
}

func TestSequentialToolsRunInOrder(t *testing.T) {
	if p := sequencingPeak(t, true); p != 1 {
		t.Fatalf("sequential peak concurrency = %d, want 1", p)
	}
	if p := sequencingPeak(t, false); p < 2 {
		t.Fatalf("parallel peak concurrency = %d, want at least 2 for two read-only calls", p)
	}
}

// ---- context estimate, logs, notices -----------------------------------------

func TestContextEstimateBeforeAnyResponse(t *testing.T) {
	a := New(Options{System: func(string) string { return strings.Repeat("s", 400) }, Model: Model{Info: priced}})
	if used, est := a.ContextEstimate(); est || used != 0 {
		t.Fatalf("empty agent: used=%d estimated=%v, want 0 false", used, est)
	}
	a.SetMessages([]llm.Message{llm.UserText(strings.Repeat("u", 800))})
	a.sysChars = 400 // set as request() would
	used, est := a.ContextEstimate()
	if !est || used != (400+800)/4 {
		t.Fatalf("estimate used=%d estimated=%v, want %d true", used, est, (400+800)/4)
	}
	a.mu.Lock()
	a.lastIn = 1234
	a.mu.Unlock()
	if used, est := a.ContextEstimate(); est || used != 1234 {
		t.Fatalf("after a response: used=%d estimated=%v, want 1234 false", used, est)
	}
}

func TestFirstStepEmitsEstimatedContext(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t, turn(sseTurn{text: []string{"hi"}})))
	log := &eventLog{}
	a := New(baseOptions(f, tools.NewRegistry(), log, t.TempDir()))
	if _, err := a.Run(context.Background(), "hello there", nil); err != nil {
		t.Fatal(err)
	}
	ctxs := log.of(EvContext)
	if len(ctxs) == 0 || !ctxs[0].Estimated || ctxs[0].ContextUsed <= 0 {
		t.Fatalf("first context event = %+v, want an estimate", ctxs)
	}
}

func TestErrorEventCarriesHTTPStatus(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t, apiError(400, "", `{"error":{"message":"bad"}}`)))
	log := &eventLog{}
	a := New(baseOptions(f, tools.NewRegistry(), log, t.TempDir()))
	a.Run(context.Background(), "hi", nil)
	errs := log.of(EvError)
	if len(errs) != 1 || errs[0].Status != 400 {
		t.Fatalf("error events = %+v, want one with status 400", errs)
	}
}

func TestModelLogKeepsCallsRetriesAndErrors(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t,
		apiError(400, "", `{"error":{"message":"bad"}}`),
	))
	a := New(baseOptions(f, tools.NewRegistry(), &eventLog{}, t.TempDir()))
	a.Run(context.Background(), "hi", nil)
	var kinds []EventKind
	for _, e := range a.ModelEvents() {
		kinds = append(kinds, e.Kind)
	}
	if len(kinds) != 1 || kinds[0] != EvError {
		t.Fatalf("model log kinds = %v, want [error]", kinds)
	}
}

func TestCompactionLogAndNotice(t *testing.T) {
	f := newFake(t)
	log := &eventLog{}
	a := New(baseOptions(f, tools.NewRegistry(), log, t.TempDir()))
	a.SetMessages(manyMessages(10))
	a.opts.Compactor = &staticCompactor{}
	if err := a.Compact(context.Background(), "manual"); err != nil {
		t.Fatal(err)
	}
	var kinds []EventKind
	for _, e := range a.CompactionLog() {
		kinds = append(kinds, e.Kind)
	}
	if len(kinds) != 2 || kinds[0] != EvCompactStart || kinds[1] != EvCompactDone {
		t.Fatalf("compaction log = %v, want start then done", kinds)
	}

	// Too little history is a note, not a warning.
	a2 := New(baseOptions(f, tools.NewRegistry(), log, t.TempDir()))
	a2.opts.Compactor = &staticCompactor{err: ErrNotEnoughHistory}
	log.mu.Lock()
	log.evs = nil
	log.mu.Unlock()
	if err := a2.Compact(context.Background(), "manual"); !errors.Is(err, ErrNotEnoughHistory) {
		t.Fatalf("err = %v, want ErrNotEnoughHistory", err)
	}
	if len(log.of(EvNotice)) != 1 || len(log.of(EvWarning)) != 0 {
		t.Fatalf("events = %v, want one notice and no warning", log.kinds())
	}
}

func TestAutoCompactWaitsForSixMessages(t *testing.T) {
	c := &staticCompactor{}
	a := New(Options{Compactor: c, CompactAt: 0.7, Model: Model{Info: llm.ModelInfo{ContextWindow: 1000}}})
	a.lastIn = 900
	a.SetMessages(manyMessages(5))
	if err := a.maybeCompact(context.Background()); err != nil || c.calls != 0 {
		t.Fatalf("5 messages: err=%v calls=%d, want no compaction", err, c.calls)
	}
	a.SetMessages(manyMessages(6))
	a.lastIn = 900
	if err := a.maybeCompact(context.Background()); err != nil || c.calls != 1 {
		t.Fatalf("6 messages: err=%v calls=%d, want one compaction", err, c.calls)
	}
}

func TestDefaultCompactThresholdIsSeventyPercent(t *testing.T) {
	if a := New(Options{}); a.opts.CompactAt != 0.7 {
		t.Fatalf("CompactAt = %v, want 0.7", a.opts.CompactAt)
	}
}

// ---- feature switches and post-dispatch passes -------------------------------

func TestDefaultFeaturesMatchSpec(t *testing.T) {
	got := DefaultFeatures()
	want := Features{TierRouting: true, DispatchCache: true, TargetFileValidation: true}
	if got != want {
		t.Fatalf("DefaultFeatures() = %+v, want %+v", got, want)
	}
	if (&Dispatcher{}).features() != want {
		t.Fatal("a dispatcher without Features does not use the defaults")
	}
}

func TestVerdictOf(t *testing.T) {
	cases := map[string]string{
		"looks fine\nVERDICT: PASS":                          "pass",
		"problems below\nVERDICT: FAIL\n- nil deref":         "fail",
		"verdict: pass":                                      "pass",
		"**VERDICT: FAIL**":                                  "fail",
		"VERDICT: PASS\nthen changed my mind\nVERDICT: FAIL": "fail",
		"no verdict line at all":                             "unknown",
		"VERDICT: maybe":                                     "unknown",
	}
	for in, want := range cases {
		if got := verdictOf(in); got != want {
			t.Errorf("verdictOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDesloppifyAndReviewRunWhenEnabled(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	usage := &sseUsage{prompt: 10, completion: 1}
	write := writeThenReport("notes.txt", "hello\n", "wrote notes", usage)
	h.f.setPick(func(_ int, req recordedReq) reply {
		task := lastUserText(req)
		switch {
		case strings.Contains(task, "SLOT-CODE"):
			return write(req)
		case strings.Contains(task, "Clean up the code"):
			return turn(sseTurn{text: []string{"removed nothing; kept everything"}, usage: usage})
		case strings.Contains(task, "Review the edits"):
			return turn(sseTurn{text: []string{"reads fine\nVERDICT: FAIL"}, usage: usage})
		}
		return providerError(400)
	})
	h.d.Features = &Features{Desloppify: true, VerifyEdits: true, TargetFileValidation: true, TierRouting: true, DispatchCache: true}

	res := h.run(`{"tasks":[{"task":"SLOT-CODE add notes","role":"code","files":["notes.txt"]}]}`)
	if res.IsError {
		t.Fatalf("dispatch failed: %+v", res)
	}
	for _, want := range []string{"## Cleanup", "removed nothing; kept everything", "## Review", "VERDICT: FAIL"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("output lacks %q:\n%s", want, res.Output)
		}
	}
	if res.Meta["review"] != "fail" || !strings.Contains(res.Summary, "review failing") {
		t.Fatalf("review meta/summary = %v / %q", res.Meta["review"], res.Summary)
	}
	if got, _ := os.ReadFile(filepath.Join(h.dir, "notes.txt")); string(got) != "hello\n" {
		t.Fatalf("file content = %q", got)
	}
}

func TestPostDispatchPassesStayOffByDefault(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	usage := &sseUsage{prompt: 10, completion: 1}
	write := writeThenReport("notes.txt", "hello\n", "wrote notes", usage)
	h.f.setPick(func(_ int, req recordedReq) reply {
		if strings.Contains(lastUserText(req), "SLOT-CODE") {
			return write(req)
		}
		t.Errorf("a post-dispatch pass ran with default features: %q", lastUserText(req))
		return providerError(400)
	})
	res := h.run(`{"tasks":[{"task":"SLOT-CODE add notes","role":"code","files":["notes.txt"]}]}`)
	if strings.Contains(res.Output, "## Cleanup") || strings.Contains(res.Output, "## Review") {
		t.Fatalf("default output has post-dispatch sections:\n%s", res.Output)
	}
}

func TestTargetFileValidationCanBeTurnedOff(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	h.f.setPick(func(_ int, req recordedReq) reply {
		return turn(sseTurn{text: []string{"report"}})
	})
	args := `{"tasks":[{"task":"CODE-TGT edit it","role":"code","files":["missing_file.go"]}]}`

	res := h.run(args)
	if !strings.Contains(res.Output, "dropped target missing_file.go") {
		t.Fatalf("default run does not note the missing target:\n%s", res.Output)
	}
	h.d.Features = &Features{TierRouting: true, DispatchCache: true}
	res = h.run(args)
	if strings.Contains(res.Output, "dropped target") {
		t.Fatalf("validation off still drops the missing target:\n%s", res.Output)
	}
}

func TestDispatcherStatusListsNewestDispatch(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	if h.d.Status() != nil {
		t.Fatal("status before any dispatch is not nil")
	}
	h.f.setPick(func(int, recordedReq) reply { return turn(sseTurn{text: []string{"report"}}) })
	h.run(`{"tasks":[{"task":"first look","role":"explore"},{"task":"second look","role":"explore"}]}`)
	st := h.d.Status()
	if len(st) != 2 || st[0].Task != "first look" || st[1].Status != "done" {
		t.Fatalf("status = %+v", st)
	}
}
