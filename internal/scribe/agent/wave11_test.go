package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// steerImage is a PNG-shaped image part for steering tests.
var steerImage = llm.Part{Type: llm.PartImage, MediaType: "image/png", Data: []byte("PNG-steering-bytes")}

// Steering that carries an image reaches the model in the same user turn as
// its text, and text parts sent as images are dropped.
func TestSteerWithImagesReachesTheModelWithItsImage(t *testing.T) {
	f := newFake(t)
	var a *Agent
	f.setPick(func(n int, _ recordedReq) reply {
		if n == 0 {
			a.SteerWithImages("look at this screenshot", []llm.Part{steerImage, {Type: llm.PartText, Text: "NOT-AN-IMAGE"}})
			return turn(sseTurn{calls: []sseCall{{id: "call_1", name: "read", args: `{"path":"x.txt"}`}}})
		}
		return turn(sseTurn{text: []string{"done"}})
	})
	a = New(baseOptions(f, plainRegistry(), &eventLog{}, t.TempDir()))
	if _, err := a.Run(context.Background(), "start", nil); err != nil {
		t.Fatal(err)
	}
	reqs := f.requests()
	if len(reqs) != 2 {
		t.Fatalf("want 2 model calls, got %d", len(reqs))
	}
	body := wireText(reqs[1])
	if !strings.Contains(body, "<steering>") || !strings.Contains(body, "look at this screenshot") {
		t.Fatalf("second request lacks the steering block:\n%s", body)
	}
	if !strings.Contains(body, "image_url") || !strings.Contains(body, base64.StdEncoding.EncodeToString(steerImage.Data)) {
		t.Fatalf("steering image did not reach the model")
	}
	if strings.Contains(body, "NOT-AN-IMAGE") {
		t.Fatalf("a text part sent as an image should be dropped")
	}
}

// The queue cap still applies to image steering, and Queued lists the text.
func TestSteeringQueueCapAndQueuedText(t *testing.T) {
	a := New(baseOptions(newFake(t), plainRegistry(), &eventLog{}, t.TempDir()))
	for i := 0; i < maxSteering; i++ {
		if !a.SteerWithImages("note", []llm.Part{steerImage}) {
			t.Fatalf("message %d refused before the cap", i+1)
		}
	}
	if a.SteerWithImages("one too many", nil) {
		t.Fatalf("the sixth steering message should be refused")
	}
	if got := a.Queued(); len(got) != maxSteering || got[0] != "note" {
		t.Fatalf("Queued = %q", got)
	}
	text, imgs := a.drainSteering()
	if !strings.Contains(text, "<steering>") || len(imgs) != maxSteering {
		t.Fatalf("drain = %q with %d images", text, len(imgs))
	}
	if q, _ := a.drainSteering(); q != "" {
		t.Fatalf("queue should be empty after a drain, got %q", q)
	}
}

// Steering that arrives while the history is being compacted is delivered
// after the summary, in the next model call, rather than waiting for a tool
// step that may never come.
func TestSteeringQueuedDuringCompactionIsDrainedAfterIt(t *testing.T) {
	f := newFake(t)
	var a *Agent
	f.setPick(func(n int, _ recordedReq) reply {
		switch n {
		case 0, 1, 2:
			usage := &sseUsage{prompt: 900, completion: 5}
			return turn(sseTurn{usage: usage, calls: []sseCall{{id: "call_" + string(rune('a'+n)), name: "read", args: `{"path":"x.txt"}`}}})
		}
		return turn(sseTurn{text: []string{"done"}})
	})
	opts := baseOptions(f, plainRegistry(), &eventLog{}, t.TempDir())
	opts.Model = fakeModel(f.srv.URL, "fake/model-a", llm.ModelInfo{ContextWindow: 1000})
	opts.CompactAt = 0.5
	opts.Compactor = compactFunc(func() {
		a.SteerWithImages("while you summarised: check the picture", []llm.Part{steerImage})
	})
	a = New(opts)
	if _, err := a.Run(context.Background(), "start", nil); err != nil {
		t.Fatal(err)
	}
	reqs := f.requests()
	if len(reqs) != 4 {
		t.Fatalf("want 4 model calls, got %d", len(reqs))
	}
	body := wireText(reqs[3])
	if !strings.Contains(body, "summary of earlier work") {
		t.Fatalf("the third call should start from the compacted history:\n%s", body)
	}
	if !strings.Contains(body, "while you summarised: check the picture") || !strings.Contains(body, base64.StdEncoding.EncodeToString(steerImage.Data)) {
		t.Fatalf("steering queued during compaction was not delivered after it")
	}
	if strings.Count(body, "<steering>") != 1 {
		t.Fatalf("steering should be delivered once")
	}
}

// wireText is a request body with JSON's \u003c escapes turned back into
// the characters they stand for, so the text can be searched.
func wireText(req recordedReq) string {
	return strings.NewReplacer(`\u003c`, "<", `\u003e`, ">").Replace(string(req.raw))
}

// compactFunc is a Compactor that runs hook, then keeps a one-message summary.
type compactFunc func()

func (c compactFunc) Compact(_ context.Context, _ []llm.Message) ([]llm.Message, string, error) {
	c()
	return []llm.Message{llm.UserText("summary of earlier work")}, "summary of earlier work", nil
}

// busDispatch is a dispatch whose subagents get the bus tools, as the app
// gives them, so cancel_dispatch and check_findings are reachable.
func busDispatch(t *testing.T, f *fakeLLM) *dispatchHarness {
	t.Helper()
	h := &dispatchHarness{t: t, f: f, log: &eventLog{}, dir: t.TempDir()}
	h.d = &Dispatcher{MaxConcurrent: 3, Sink: h.log.sink, retryWait: time.Millisecond}
	h.d.New = func(spec SubagentSpec, sink Sink) *Agent {
		reg := plainRegistry()
		for _, bt := range BusTools(spec.ID, spec.Bus) {
			reg.Register(bt)
		}
		opts := baseOptions(f, reg, h.log, h.dir)
		opts.Sink = sink
		opts.ID = spec.ID
		opts.System = func(string) string { return "SYS " + spec.Role }
		return New(opts)
	}
	return h
}

// One subagent calls cancel_dispatch while another is still running: the
// running one stops, the dispatch returns promptly, and the report says so.
func TestCancelDispatchStopsTheOtherAgentsAndReportsIt(t *testing.T) {
	f := newFake(t)
	f.setPick(func(_ int, req recordedReq) reply {
		switch {
		case strings.Contains(openingOf(req), "ALPHA-TASK"):
			return turn(sseTurn{calls: []sseCall{{id: "call_c", name: "cancel_dispatch", args: `{"reason":"the parser is missing"}`}}})
		case strings.Contains(openingOf(req), "BETA-TASK"):
			return f.hang("")
		}
		return providerError(500)
	})
	h := busDispatch(t, f)
	args := `{"tasks":[` +
		`{"id":"alpha","role":"explore","task":"ALPHA-TASK decide to stop"},` +
		`{"id":"beta","role":"explore","task":"BETA-TASK keep going"}]}`
	res := h.run(args)
	if !strings.Contains(res.Output, "## Cancelled") || !strings.Contains(res.Summary, "cancelled") {
		t.Fatalf("result should report the cancellation:\nsummary=%q\n%s", res.Summary, res.Output)
	}
	var beta AgentInfo
	for _, info := range h.d.Status() {
		if info.Task == "BETA-TASK keep going" {
			beta = info
		}
	}
	if beta.Status != "cancelled" || beta.Error != errDispatchCancelled.Error() {
		t.Fatalf("the running sibling should be cancelled, got status=%q error=%q", beta.Status, beta.Error)
	}
}

// cancel_dispatch refuses a second call and names the bus it needs.
func TestCancelDispatchToolCancelsOnce(t *testing.T) {
	bus := NewAgentBus()
	calls := 0
	bus.SetAbort(func() { calls++ })
	var tool tools.Tool
	for _, bt := range BusTools("a1", bus) {
		if bt.Spec().Name == "cancel_dispatch" {
			tool = bt
		}
	}
	if tool == nil {
		t.Fatal("cancel_dispatch is not among the bus tools")
	}
	first := tool.Run(context.Background(), &tools.Env{}, json.RawMessage(`{"reason":"stop"}`))
	if first.IsError || !strings.Contains(first.Output, "Cancelled the dispatch") || !strings.Contains(first.Output, "Reason: stop") {
		t.Fatalf("first cancel: %+v", first)
	}
	second := tool.Run(context.Background(), &tools.Env{}, json.RawMessage(`{}`))
	if !strings.Contains(second.Output, "already cancelled") {
		t.Fatalf("second cancel should say so: %q", second.Output)
	}
	if calls != 1 || !bus.Aborted() {
		t.Fatalf("the dispatch should be cancelled exactly once (calls=%d)", calls)
	}
	// Without a dispatch the tool says what is wrong rather than failing silently.
	none := cancelDispatchTool{id: "a1"}.Run(context.Background(), &tools.Env{}, nil)
	if !none.IsError || !strings.Contains(none.Output, "not part of a dispatch") {
		t.Fatalf("no bus: %+v", none)
	}
}

// Each task is told what its peers are doing, but not its own task.
func TestPeerObjectivesNameTheOtherTasks(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	f := h.f
	f.setPick(func(_ int, _ recordedReq) reply { return turn(sseTurn{text: []string{"ok"}}) })
	res := h.run(`{"tasks":[{"id":"alpha","role":"explore","task":"ALPHA-TASK find the parser"},` +
		`{"id":"beta","role":"explore","task":"BETA-TASK find the lexer"}]}`)
	if res.IsError {
		t.Fatalf("dispatch failed: %s", res.Output)
	}
	var alpha string
	for _, r := range f.requests() {
		if strings.Contains(openingOf(r), "ALPHA-TASK") {
			alpha = lastUserText(r)
		}
	}
	if !strings.Contains(alpha, "- beta (explore): BETA-TASK find the lexer") {
		t.Fatalf("alpha should be told about beta:\n%s", alpha)
	}
	if strings.Contains(alpha, "- alpha (explore)") {
		t.Fatalf("a task should not list itself as a peer:\n%s", alpha)
	}
}

// A single-task dispatch has no peers, so its task text is unchanged.
func TestPeerObjectivesSkipSingleTask(t *testing.T) {
	tasks := []subTask{{ID: "solo", Role: "explore", Task: "alone"}}
	plan, err := planDispatch(tasks)
	if err != nil {
		t.Fatal(err)
	}
	if got := withPeerObjectives("alone", tasks, plan, 0); got != "alone" {
		t.Fatalf("single task changed: %q", got)
	}
}

// A dependency that failed without a report says so, instead of looking
// like one that simply had nothing to say.
func TestDependencyReportWarnsWhenTheDependencyFailed(t *testing.T) {
	tasks := []subTask{
		{ID: "alpha", Role: "explore", Task: "A"},
		{ID: "beta", Role: "explore", Task: "B", DependsOn: []string{"alpha"}},
	}
	plan, err := planDispatch(tasks)
	if err != nil {
		t.Fatal(err)
	}
	results := []taskResult{{err: errBreakerOpen}, {}}
	got := withDependencyReports("B", plan, 1, results)
	if !strings.Contains(got, "(no report: this task failed: ") || !strings.Contains(got, errBreakerOpen.Error()) {
		t.Fatalf("failed dependency not flagged:\n%s", got)
	}
}

// Every task's footer lists the files it read and edited, after its report,
// and an empty report still shows the footer.
func TestTaskReportFooterListsReadAndEditedFiles(t *testing.T) {
	tasks := []subTask{{ID: "a", Role: "code", Task: "edit"}, {ID: "b", Role: "explore", Task: "look"}}
	plan, err := planDispatch(tasks)
	if err != nil {
		t.Fatal(err)
	}
	results := []taskResult{
		{report: "changed it", read: []string{"x.go"}, edited: []string{"y.go"}},
		{read: []string{"z.go"}},
	}
	out := renderTaskReports(tasks, plan, results)
	want := "changed it\nFiles read: x.go\nFiles edited: y.go\n"
	if !strings.Contains(out, want) {
		t.Fatalf("footer after report missing:\n%s", out)
	}
	if !strings.Contains(out, "No output from agent\nFiles read: z.go\n") {
		t.Fatalf("empty report should still show the footer:\n%s", out)
	}
}

// A failed task says how many attempts it made.
func TestFailureLineCountsAttempts(t *testing.T) {
	got := failureLine(taskResult{err: errBreakerOpen, attempts: 4})
	if !strings.HasPrefix(got, "FAILED after 4 attempts: ") {
		t.Fatalf("failure line = %q", got)
	}
	if got := failureLine(taskResult{err: errBreakerOpen}); strings.Contains(got, "attempt") {
		t.Fatalf("a task that never started should not count attempts: %q", got)
	}
}
