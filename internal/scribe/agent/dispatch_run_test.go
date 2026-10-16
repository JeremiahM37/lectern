package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// dispatchHarness runs the dispatch tool with real subagents whose model
// requests go to the scripted server. Every subagent gets a fresh registry
// from newReg and works in dir.
type dispatchHarness struct {
	t   *testing.T
	f   *fakeLLM
	log *eventLog
	dir string
	d   *Dispatcher
}

func newDispatchHarness(t *testing.T, newReg func() *tools.Registry) *dispatchHarness {
	t.Helper()
	f := newFake(t)
	h := &dispatchHarness{t: t, f: f, log: &eventLog{}, dir: t.TempDir()}
	h.d = &Dispatcher{MaxConcurrent: 3, Sink: h.log.sink, retryWait: time.Millisecond}
	h.d.New = func(spec SubagentSpec, sink Sink) *Agent {
		role, id := spec.Role, spec.ID
		opts := baseOptions(f, newReg(), h.log, h.dir)
		opts.Sink = sink
		opts.ID = id
		opts.System = func(string) string { return "SYS " + role }
		return New(opts)
	}
	return h
}

// run dispatches args and returns the tool result.
func (h *dispatchHarness) run(args string) tools.Result {
	h.t.Helper()
	tr := tools.NewRegistry()
	h.d.Register(tr)
	dispatch, ok := tr.Get("dispatch")
	if !ok {
		h.t.Fatal("dispatch tool not registered")
	}
	env := &tools.Env{Workdir: h.dir, CallID: "call_d", Mode: "default"}
	resCh := make(chan tools.Result, 1)
	go func() { resCh <- dispatch.Run(context.Background(), env, json.RawMessage(args)) }()
	select {
	case res := <-resCh:
		return res
	case <-time.After(15 * time.Second):
		h.t.Fatal("dispatch did not return")
		return tools.Result{}
	}
}

func plainRegistry() *tools.Registry {
	r := tools.NewRegistry()
	r.Register(readTool{})
	r.Register(writeTool{})
	return r
}

// openingOf is the task a request is for: its last user message up to the
// first blank line. Later paragraphs (dependency reports, the peer list) name
// other tasks, so routing must not match them.
func openingOf(req recordedReq) string {
	text := lastUserText(req)
	if i := strings.Index(text, "\n\n"); i >= 0 {
		return text[:i]
	}
	return text
}

// requestsMentioning counts the requests whose task (see openingOf) contains s.
func requestsMentioning(f *fakeLLM, s string) int {
	n := 0
	for _, req := range f.requests() {
		if strings.Contains(openingOf(req), s) {
			n++
		}
	}
	return n
}

// requestMentioning returns the first request whose last user message contains s.
func requestMentioning(t *testing.T, f *fakeLLM, s string) recordedReq {
	t.Helper()
	for _, req := range f.requests() {
		if strings.Contains(lastUserText(req), s) {
			return req
		}
	}
	t.Fatalf("no model request mentions %q", s)
	return recordedReq{}
}

func lastRole(req recordedReq) string {
	if n := len(req.body.Messages); n > 0 {
		return req.body.Messages[n-1].Role
	}
	return ""
}

// writeThenReport answers a code task: the first request writes path, and the
// request after the tool result reports text.
func writeThenReport(path, content, text string, usage *sseUsage) func(recordedReq) reply {
	return func(req recordedReq) reply {
		if lastRole(req) == "tool" {
			return turn(sseTurn{text: []string{text}, usage: usage})
		}
		args, _ := json.Marshal(map[string]string{"path": path, "content": content})
		return turn(sseTurn{calls: []sseCall{{id: "call_write", name: "write_file", args: string(args)}}, usage: usage})
	}
}

func providerError(status int) reply {
	return apiError(status, "", fmt.Sprintf(`{"error":{"message":"status %d"}}`, status))
}

// Retry-After: 0 makes the agent's own transient retries immediate.
func transientError() reply {
	return apiError(500, "0", `{"error":{"message":"upstream overloaded"}}`)
}

func TestDispatchDependentStartsAfterDependencyAndReceivesItsReport(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	gate := make(chan struct{})
	h.f.setPick(func(_ int, req recordedReq) reply {
		task := openingOf(req)
		switch {
		case strings.Contains(task, "ALPHA-TASK"):
			return h.f.gated(gate, turn(sseTurn{text: []string{"alpha-report-ok"}}))
		case strings.Contains(task, "BETA-TASK"):
			return turn(sseTurn{text: []string{"beta-report-ok"}})
		}
		return providerError(500)
	})

	args := `{"tasks":[` +
		`{"id":"alpha","role":"explore","task":"ALPHA-TASK find the parser"},` +
		`{"id":"beta","role":"explore","task":"BETA-TASK use the parser","depends_on":["alpha"]}]}`
	resCh := make(chan tools.Result, 1)
	go func() { resCh <- h.run(args) }()

	waitUntil(t, 5*time.Second, "alpha in flight", func() bool {
		cur, _ := h.f.inFlight()
		return cur == 1
	})
	time.Sleep(150 * time.Millisecond)
	if n := requestsMentioning(h.f, "BETA-TASK"); n != 0 {
		t.Fatalf("beta started %d time(s) while alpha was still running", n)
	}
	close(gate)

	var res tools.Result
	select {
	case res = <-resCh:
	case <-time.After(10 * time.Second):
		t.Fatal("dispatch did not return")
	}
	if res.IsError || res.Summary != "2/2 agents finished" {
		t.Fatalf("result = %+v", res)
	}
	var req recordedReq
	for _, r := range h.f.requests() {
		if strings.Contains(openingOf(r), "BETA-TASK") {
			req = r
			break
		}
	}
	if !strings.Contains(lastUserText(req), "### alpha\nalpha-report-ok") {
		t.Fatalf("beta's task does not carry alpha's report:\n%s", lastUserText(req))
	}
}

func TestDispatchSkipsTaskWhoseDependencyFailed(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	h.f.setPick(func(_ int, req recordedReq) reply {
		if strings.Contains(openingOf(req), "ALPHA-TASK") {
			return providerError(400) // not retryable: fails at once
		}
		return turn(sseTurn{text: []string{"should not run"}})
	})

	res := h.run(`{"tasks":[` +
		`{"id":"alpha","role":"explore","task":"ALPHA-TASK find the parser"},` +
		`{"id":"beta","role":"explore","task":"BETA-TASK use the parser","depends_on":["alpha"]}]}`)
	if n := requestsMentioning(h.f, "BETA-TASK"); n != 0 {
		t.Fatalf("beta ran although alpha failed (%d requests)", n)
	}
	if !strings.Contains(res.Output, "FAILED: dependency alpha failed") {
		t.Fatalf("output does not explain the skip:\n%s", res.Output)
	}
	if !res.IsError {
		t.Fatalf("every task failed, but IsError is false: %+v", res)
	}
}

func TestDispatchRejectsBadDependenciesBeforeAnyModelCall(t *testing.T) {
	cases := map[string]struct{ args, want string }{
		"unknown id": {
			`{"tasks":[{"id":"a","role":"explore","task":"A","depends_on":["zzz"]}]}`,
			`depends on "zzz"`,
		},
		"cycle": {
			`{"tasks":[{"id":"a","role":"explore","task":"A","depends_on":["b"]},{"id":"b","role":"explore","task":"B","depends_on":["a"]}]}`,
			"dependency cycle",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newDispatchHarness(t, plainRegistry)
			res := h.run(tc.args)
			if !res.IsError || !strings.Contains(res.Output, tc.want) {
				t.Fatalf("result = %+v, want error containing %q", res, tc.want)
			}
			if h.f.count() != 0 {
				t.Fatalf("model was called %d times", h.f.count())
			}
		})
	}
}

func TestDispatchRetriesCodeTaskThatEditedNothingAndSumsUsage(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	usage := &sseUsage{prompt: 100, completion: 10}
	write := writeThenReport("notes.txt", "hello\n", "wrote notes", usage)
	h.f.setPick(func(_ int, req recordedReq) reply {
		task := lastUserText(req)
		if !strings.Contains(task, "CODE-NOEDIT") {
			return providerError(400)
		}
		if strings.Contains(task, "finished without editing") {
			return write(req)
		}
		return turn(sseTurn{text: []string{"nothing to change"}, usage: usage})
	})

	res := h.run(`{"tasks":[{"task":"CODE-NOEDIT add the notes file","role":"code","files":["notes.txt"]}]}`)
	if res.IsError || res.Summary != "1/1 agents finished" {
		t.Fatalf("result = %+v", res)
	}
	if got, _ := os.ReadFile(filepath.Join(h.dir, "notes.txt")); string(got) != "hello\n" {
		t.Fatalf("retry did not write the file; content = %q", got)
	}
	if !strings.Contains(res.Output, "retried once: the previous attempt finished without editing any file") {
		t.Fatalf("output does not mention the retry:\n%s", res.Output)
	}
	if len(res.Files) != 1 || res.Files[0] != "notes.txt" {
		t.Fatalf("Files = %v", res.Files)
	}
	// One attempt that answered in text, then a retry: write, then report.
	if h.f.count() != 3 {
		t.Fatalf("%d model requests, want 3", h.f.count())
	}
	if !strings.Contains(lastUserText(h.f.requests()[1]), "finished without editing any file") {
		t.Fatal("the retry request does not carry the retry note")
	}
	u, ok := res.Meta["subagent_usage"].(llm.Usage)
	if !ok {
		t.Fatalf("Meta[subagent_usage] = %T, want llm.Usage", res.Meta["subagent_usage"])
	}
	// Three requests of 100 input and 10 output tokens, from both attempts.
	if u.InputTokens != 300 || u.OutputTokens != 30 {
		t.Fatalf("subagent usage = %+v, want input 300 output 30", u)
	}
}

func TestDispatchRetriesCodeTaskAfterRetryableProviderError(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	usage := &sseUsage{prompt: 100, completion: 10}
	write := writeThenReport("notes.txt", "hello\n", "wrote notes", usage)
	h.f.setPick(func(_ int, req recordedReq) reply {
		task := lastUserText(req)
		if !strings.Contains(task, "CODE-FLAKY") {
			return providerError(400)
		}
		if strings.Contains(task, "stopped on a provider error") {
			return write(req)
		}
		return transientError()
	})

	res := h.run(`{"tasks":[{"task":"CODE-FLAKY add the notes file","role":"code","files":["notes.txt"]}]}`)
	if res.IsError || res.Summary != "1/1 agents finished" {
		t.Fatalf("result = %+v\n%s", res, res.Output)
	}
	if !strings.Contains(res.Output, "retried once: the previous attempt stopped on a provider error") {
		t.Fatalf("output does not mention the retry:\n%s", res.Output)
	}
	if got, _ := os.ReadFile(filepath.Join(h.dir, "notes.txt")); string(got) != "hello\n" {
		t.Fatalf("retry did not write the file; content = %q", got)
	}
	// The agent's own transient retries (4 requests) fail the first attempt;
	// the dispatch retry then needs 2 requests (write, report).
	if h.f.count() != 6 {
		t.Fatalf("%d model requests, want 6", h.f.count())
	}
}

func TestDispatchCircuitBreakerStopsLaunchingAfterSixRetryableFailures(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	h.d.MaxConcurrent = 1 // one at a time, so exactly six fail before the breaker opens
	h.f.setPick(func(_ int, req recordedReq) reply {
		return transientError()
	})

	var tasks []string
	for i := 1; i <= 8; i++ {
		tasks = append(tasks, fmt.Sprintf(`{"role":"explore","task":"BRK-%d look"}`, i))
	}
	res := h.run(`{"tasks":[` + strings.Join(tasks, ",") + `]}`)
	// Each subagent makes 4 requests (the first and three retries) before it
	// fails, so six failed subagents mean 24 requests, and the last two never start.
	if n := h.f.count(); n != 24 {
		t.Fatalf("%d requests reached the model, want 24 (six subagents)", n)
	}
	if !strings.Contains(res.Output, "## Circuit breaker\nOpened after 6 provider failures; tasks that had not started were not run.") {
		t.Fatalf("output has no breaker section:\n%s", res.Output)
	}
	if got := strings.Count(res.Output, "FAILED: not started: 6 provider failures"); got != 2 {
		t.Fatalf("%d unstarted tasks named, want 2:\n%s", got, res.Output)
	}
	if !res.IsError {
		t.Fatalf("no task finished, but IsError is false")
	}
}

func TestDispatchBreakerIgnoresNonRetryableFailures(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	h.d.MaxConcurrent = 1
	h.f.setPick(func(_ int, req recordedReq) reply { return providerError(400) })
	res := h.run(`{"tasks":[` +
		`{"role":"explore","task":"BAD-1 look"},{"role":"explore","task":"BAD-2 look"},` +
		`{"role":"explore","task":"BAD-3 look"},{"role":"explore","task":"BAD-4 look"},` +
		`{"role":"explore","task":"BAD-5 look"},{"role":"explore","task":"BAD-6 look"},` +
		`{"role":"explore","task":"BAD-7 look"}]}`)
	if strings.Contains(res.Output, "## Circuit breaker") {
		t.Fatalf("a 400 is not retryable, so it must not open the breaker:\n%s", res.Output)
	}
	if n := h.f.count(); n != 7 {
		t.Fatalf("%d requests, want 7 (every task ran once)", n)
	}
}

func TestDispatchBreakerTripsOnTransientFailureAndSkipsCodeRetry(t *testing.T) {
	// Five failures are already recorded. The code task's first attempt is a
	// transient error (the agent retries it 4 times), which is the sixth
	// retryable failure: the breaker opens, so the dispatch retry must not run.
	h := newDispatchHarness(t, plainRegistry)
	h.f.setPick(func(_ int, req recordedReq) reply { return transientError() })
	run := newDispatchRun(h.d, &tools.Env{Workdir: h.dir, CallID: "call_d"})
	for i := 0; i < 5; i++ {
		run.recordFailure()
	}

	idx := run.board.add("code", "CODE-LAST edit")
	res := run.runTask(context.Background(), idx, "code", PurposeTask, "CODE-LAST edit")
	if res.err == nil || !providerFailure(context.Background(), res.err) {
		t.Fatalf("err = %v, want a provider failure", res.err)
	}
	if !run.tripped() {
		t.Fatalf("breaker is closed after %d failures", run.failureCount())
	}
	if n := h.f.count(); n != 4 {
		t.Fatalf("%d requests, want 4 (the agent's own retries only, no dispatch retry)", n)
	}
	if len(res.notes) != 0 {
		t.Fatalf("notes = %v, want none", res.notes)
	}
}
