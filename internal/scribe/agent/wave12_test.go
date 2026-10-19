package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Every provider attempt of an explore task counts, including the transient
// retries the agent makes inside its run, so the failure line does not say
// "after 1 attempt" for a task that retried three times.
func TestFailedTaskCountsItsInRunRetries(t *testing.T) {
	f := newFake(t)
	f.setPick(func(int, recordedReq) reply { return transientError() })
	dir := t.TempDir()
	log := &eventLog{}
	d := &Dispatcher{MaxConcurrent: 3, Sink: log.sink, retryWait: time.Millisecond}
	d.New = func(spec SubagentSpec, sink Sink) *Agent {
		opts := baseOptions(f, plainRegistry(), log, dir)
		opts.Sink, opts.ID = sink, spec.ID
		opts.MaxRetries, opts.RetryBase = 2, time.Millisecond
		return New(opts)
	}
	h := &dispatchHarness{t: t, f: f, log: log, dir: dir, d: d}
	res := h.run(`{"tasks":[{"id":"solo","role":"explore","task":"look at the parser"}]}`)
	// Explore tasks get the same attempt budget as code tasks: four runs of
	// three provider attempts each (the first and two retries), so 12 in all.
	if !strings.Contains(res.Output, "FAILED after 12 attempts") {
		t.Fatalf("four runs of three attempts should count as 12 attempts:\n%s", res.Output)
	}
}

// waitForPeerTool blocks until another agent of the dispatch has posted a
// finding, then returns without naming it. The finding can only reach the
// agent through the per-step peer feed.
type waitForPeerTool struct {
	id  string
	bus *AgentBus
}

func (waitForPeerTool) ReadOnly() bool { return true }

func (waitForPeerTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: "wait_for_peer", Description: "Wait until a peer has posted a finding.", Schema: tools.Schema(map[string]any{})}
}

func (w waitForPeerTool) Run(ctx context.Context, _ *tools.Env, _ json.RawMessage) tools.Result {
	for {
		if len(w.bus.FindingsExcept(w.id)) > 0 {
			return tools.Result{Output: "ok", Summary: "peer is ready"}
		}
		select {
		case <-ctx.Done():
			return tools.Errorf("gave up waiting for a peer")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// A finding posted by one subagent appears in the next step of another
// subagent that is still running, without that subagent calling check_findings.
func TestPeerFindingReachesTheNextStepWithoutCheckFindings(t *testing.T) {
	f := newFake(t)
	f.setPick(func(_ int, req recordedReq) reply {
		switch {
		case strings.HasPrefix(firstUserText(req), "ALPHA-TASK"):
			if lastRole(req) == "tool" {
				return turn(sseTurn{text: []string{"alpha done"}})
			}
			return turn(sseTurn{calls: []sseCall{{id: "call_w", name: "wait_for_peer", args: "{}"}}})
		case strings.HasPrefix(firstUserText(req), "BETA-TASK"):
			if lastRole(req) == "tool" {
				return turn(sseTurn{text: []string{"beta done"}})
			}
			return turn(sseTurn{calls: []sseCall{{id: "call_f", name: "report_finding", args: `{"content":"the lexer lives in lexer.go:10","file":"lexer.go","line":"10"}`}}})
		}
		return providerError(500)
	})
	dir := t.TempDir()
	log := &eventLog{}
	d := &Dispatcher{MaxConcurrent: 3, Sink: log.sink, retryWait: time.Millisecond}
	d.New = func(spec SubagentSpec, sink Sink) *Agent {
		reg := plainRegistry()
		for _, bt := range BusTools(spec.ID, spec.Bus) {
			reg.Register(bt)
		}
		reg.Register(waitForPeerTool{id: spec.ID, bus: spec.Bus})
		opts := baseOptions(f, reg, log, dir)
		opts.Sink, opts.ID = sink, spec.ID
		opts.System = func(string) string { return "SYS " + spec.Role }
		opts.Peers = PeerFeed(spec.ID, spec.Bus)
		return New(opts)
	}
	h := &dispatchHarness{t: t, f: f, log: log, dir: dir, d: d}
	res := h.run(`{"tasks":[{"id":"alpha","role":"explore","task":"ALPHA-TASK wait for the lexer"},` +
		`{"id":"beta","role":"explore","task":"BETA-TASK find the lexer"}]}`)
	if res.IsError {
		t.Fatalf("dispatch failed:\n%s", res.Output)
	}
	// The feed is a user message after alpha's tool result, so it is in the
	// request that follows that result; find the alpha request that has it.
	var alphaNext string
	for _, r := range f.requests() {
		if strings.HasPrefix(firstUserText(r), "ALPHA-TASK") && strings.Contains(string(r.raw), "New findings from other subagents") {
			alphaNext = lastUserText(r)
		}
	}
	if !strings.Contains(alphaNext, "New findings from other subagents") || !strings.Contains(alphaNext, "lexer.go:10") {
		t.Fatalf("alpha's step after its tool call should carry beta's finding:\n%s", alphaNext)
	}
	if strings.Contains(alphaNext, "wait for the lexer") {
		t.Fatalf("a subagent should not be sent its own findings")
	}
}

// PeerFeed hands over each finding once, and never the agent's own.
func TestPeerFeedIsIncrementalAndSkipsOwnFindings(t *testing.T) {
	bus := NewAgentBus()
	feed := PeerFeed("a1", bus)
	if got := feed(); got != "" {
		t.Fatalf("nothing posted yet: %q", got)
	}
	_ = bus.PostFinding(Finding{Agent: "a1", Text: "mine"})
	_ = bus.PostFinding(Finding{Agent: "a2", Text: "theirs"})
	got := feed()
	if !strings.Contains(got, "- [a2] theirs") || strings.Contains(got, "mine") {
		t.Fatalf("feed = %q", got)
	}
	if again := feed(); again != "" {
		t.Fatalf("a finding should be handed over once, got %q", again)
	}
}

// A report over the inline limit keeps its head and tail, and the whole text
// is saved where the note says.
func TestLongReportIsArchivedWithHeadAndTail(t *testing.T) {
	dir := t.TempDir()
	report := strings.Repeat("a", 3000) + "MIDDLE" + strings.Repeat("z", 6000)
	out := archiveLong(report, dir, "call_1-a7")
	if !strings.HasPrefix(out, report[:2000]) || !strings.HasSuffix(out, report[len(report)-1000:]) {
		t.Fatalf("inline copy should be head 2000 and tail 1000")
	}
	if strings.Contains(out, "MIDDLE") {
		t.Fatalf("the middle should be cut from the inline copy")
	}
	path := filepath.Join(dir, "call_1-a7.txt")
	saved, err := os.ReadFile(path)
	if err != nil || string(saved) != report {
		t.Fatalf("archive should hold the full text: err=%v len=%d", err, len(saved))
	}
	if !strings.Contains(out, path) {
		t.Fatalf("inline copy should name the archive file:\n%s", out[2000:2200])
	}
	short := "small"
	if archiveLong(short, dir, "x") != short {
		t.Fatalf("a short report should be unchanged")
	}
}

// The cut never splits a multi-byte character, and with no archive directory
// the note says the full text was not kept.
func TestArchiveCutKeepsRunesWhole(t *testing.T) {
	report := strings.Repeat("é", 5000)
	out := archiveLong(report, "", "x")
	if !utf8.ValidString(out) {
		t.Fatalf("cut split a rune")
	}
	if !strings.Contains(out, "no archive directory is set") {
		t.Fatalf("missing note:\n%s", out)
	}
}

// A dispatch's long report is archived under its call id, and the output
// names the file.
func TestDispatchArchivesLongSubagentReport(t *testing.T) {
	long := strings.Repeat("finding ", 800)
	archive := t.TempDir()
	h := newDispatchHarness(t, plainRegistry)
	h.f.setPick(func(int, recordedReq) reply { return turn(sseTurn{text: []string{long}}) })
	h.d.ArchiveDir = archive
	res := h.run(`{"tasks":[{"id":"big","role":"explore","task":"write a long answer"}]}`)
	if !strings.Contains(res.Output, "bytes omitted, full output: ") {
		t.Fatalf("output should name the archive:\n%s", res.Output)
	}
	files, _ := filepath.Glob(filepath.Join(archive, "call_d-*.txt"))
	if len(files) != 1 {
		t.Fatalf("expected one archived report, got %v", files)
	}
	if saved, _ := os.ReadFile(files[0]); string(saved) != long {
		t.Fatalf("archive should hold the whole report")
	}
}

// The host can stop a dispatch while it runs, cancelling every subagent.
func TestCancelDispatchFromTheHostStopsARunningDispatch(t *testing.T) {
	f := newFake(t)
	f.setPick(func(int, recordedReq) reply { return f.hang("") })
	h := busDispatch(t, f)
	if err := h.d.CancelDispatch(); err == nil || !strings.Contains(err.Error(), "no dispatch is running") {
		t.Fatalf("nothing running yet: %v", err)
	}
	env := &tools.Env{Workdir: h.dir, CallID: "call_host", Mode: "default"}
	tr := tools.NewRegistry()
	h.d.Register(tr)
	dispatch, _ := tr.Get("dispatch")
	resCh := make(chan tools.Result, 1)
	go func() {
		resCh <- dispatch.Run(context.Background(), env, json.RawMessage(`{"tasks":[`+
			`{"id":"one","role":"explore","task":"HOST-ONE wait"},{"id":"two","role":"explore","task":"HOST-TWO wait"}]}`))
	}()
	deadline := time.Now().Add(5 * time.Second)
	for f.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := h.d.CancelDispatch(); err != nil {
		t.Fatalf("cancel running dispatch: %v", err)
	}
	if err := h.d.CancelDispatch(); err == nil || !strings.Contains(err.Error(), "already cancelled") {
		t.Fatalf("second cancel should say so: %v", err)
	}
	select {
	case res := <-resCh:
		if !strings.Contains(res.Output, "## Cancelled") || !strings.Contains(res.Summary, "cancelled") {
			t.Fatalf("result should report the cancellation:\n%s", res.Output)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("dispatch did not return after the host cancelled it")
	}
}

// firstUserText is the first user message of a request: the task text, which
// later messages (feeds, steering) do not replace.
func firstUserText(req recordedReq) string {
	for _, m := range req.body.Messages {
		if m.Role == "user" {
			return m.text()
		}
	}
	return ""
}
