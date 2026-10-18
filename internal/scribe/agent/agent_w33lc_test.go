package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Model log: 500 records, each with source, state and latency (spec 03 §9.4).

func TestModelLogKeepsTheNewest500Records(t *testing.T) {
	a := New(Options{})
	for i := range 600 {
		a.record(Event{Kind: EvStepFinish, Step: i})
	}
	evs := a.ModelEvents()
	if len(evs) != 500 {
		t.Fatalf("model log holds %d records, want 500", len(evs))
	}
	if evs[0].Step != 100 || evs[len(evs)-1].Step != 599 {
		t.Fatalf("model log runs from step %d to %d, want 100 to 599", evs[0].Step, evs[len(evs)-1].Step)
	}
}

func TestCompactionLogStillKeeps200(t *testing.T) {
	a := New(Options{})
	for i := range 300 {
		a.record(Event{Kind: EvCompactStart, Step: i})
	}
	if n := len(a.CompactionLog()); n != 200 {
		t.Fatalf("compaction log holds %d records, want 200", n)
	}
}

func TestSuccessfulCallIsRecordedWithSourceStateAndLatency(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t, turn(sseTurn{text: []string{"done"}})))
	log := &eventLog{}
	a := New(baseOptions(f, tools.NewRegistry(), log, t.TempDir()))
	if _, err := a.Run(context.Background(), "hi", nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	steps := modelRecords(a, EvStepFinish)
	if len(steps) != 1 {
		t.Fatalf("%d step records, want 1", len(steps))
	}
	e := steps[0]
	if e.Meta["source"] != callSourceMain || e.Meta["state"] != callStateOK {
		t.Fatalf("meta = %v, want source main and state ok", e.Meta)
	}
	if e.Duration <= 0 {
		t.Fatalf("latency = %v, want the call's duration", e.Duration)
	}
}

func TestFailedCallIsRecordedAsErrorWithLatency(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t, apiError(400, "", `{"error":{"message":"bad"}}`)))
	a := New(baseOptions(f, tools.NewRegistry(), &eventLog{}, t.TempDir()))
	a.Run(context.Background(), "hi", nil)
	errs := modelRecords(a, EvError)
	if len(errs) != 1 {
		t.Fatalf("%d error records, want 1", len(errs))
	}
	if errs[0].Meta["state"] != callStateError || errs[0].Duration <= 0 {
		t.Fatalf("error record meta=%v duration=%v", errs[0].Meta, errs[0].Duration)
	}
}

func TestEachFailedAttemptIsOneRecord(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t,
		apiError(503, "", `{"error":{"message":"overloaded"}}`),
		turn(sseTurn{text: []string{"done"}}),
	))
	opts := baseOptions(f, tools.NewRegistry(), &eventLog{}, t.TempDir())
	opts.MaxRetries = 1
	opts.RetryBase = 250 * time.Millisecond
	a := New(opts)
	if _, err := a.Run(context.Background(), "hi", nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	retries := modelRecords(a, EvRetry)
	steps := modelRecords(a, EvStepFinish)
	if len(retries) != 1 || len(steps) != 1 {
		t.Fatalf("retry records=%d step records=%d, want 1 and 1", len(retries), len(steps))
	}
	if retries[0].Meta["state"] != callStateError || retries[0].Duration <= 0 {
		t.Fatalf("failed attempt meta=%v duration=%v", retries[0].Meta, retries[0].Duration)
	}
	if steps[0].Meta["state"] != callStateOK {
		t.Fatalf("retry success meta=%v", steps[0].Meta)
	}
}

func TestSubagentCallsAreTaggedAsSubagent(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t, turn(sseTurn{text: []string{"done"}})))
	opts := baseOptions(f, tools.NewRegistry(), &eventLog{}, t.TempDir())
	opts.ID = "sub-1"
	a := New(opts)
	if _, err := a.Run(context.Background(), "hi", nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	steps := modelRecords(a, EvStepFinish)
	if len(steps) != 1 || steps[0].Meta["source"] != callSourceSubagent {
		t.Fatalf("subagent step records = %+v, want source subagent", steps)
	}
}

func TestModelLogErrorTextIsCappedButEventKeepsIt(t *testing.T) {
	long := strings.Repeat("x", 1200)
	f := newFake(t)
	f.setPick(scripted(t, apiError(400, "", `{"error":{"message":"`+long+`"}}`)))
	log := &eventLog{}
	a := New(baseOptions(f, tools.NewRegistry(), log, t.TempDir()))
	a.Run(context.Background(), "hi", nil)

	live := log.of(EvError)
	if len(live) != 1 || len(live[0].Text) <= modelErrorTextCap+3 {
		t.Fatalf("precondition: live error text is %d bytes, want more than the cap", len(live[0].Text))
	}
	logged := modelRecords(a, EvError)
	if len(logged) != 1 || len(logged[0].Text) > modelErrorTextCap+3 {
		t.Fatalf("logged error text is %d bytes, want at most %d", len(logged[0].Text), modelErrorTextCap+3)
	}
}

// Retry configuration: MaxAttempts stands in for both counts, RetryBase
// drives the backoff (spec 03 §10).

func TestMaxAttemptsStandsInForBothRetryKinds(t *testing.T) {
	cases := []struct {
		name         string
		opts         Options
		retry, stall int
	}{
		{"unset keeps the defaults", Options{}, 3, 3},
		{"max attempts sets both", Options{MaxAttempts: 5}, 5, 5},
		{"specific counts win", Options{MaxAttempts: 5, MaxRetries: 2, MaxStallRetries: 1}, 2, 1},
		{"one specific count keeps the other from max attempts", Options{MaxAttempts: 4, MaxRetries: 2}, 2, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := New(c.opts)
			if a.opts.MaxRetries != c.retry || a.opts.MaxStallRetries != c.stall {
				t.Fatalf("retries=%d stalls=%d, want %d and %d", a.opts.MaxRetries, a.opts.MaxStallRetries, c.retry, c.stall)
			}
		})
	}
}

func TestDefaultRetryBaseIsUnchanged(t *testing.T) {
	if got := New(Options{}).opts.RetryBase; got != time.Second {
		t.Fatalf("default RetryBase = %v, want 1s", got)
	}
}

func TestRetryBaseSetsTheBackoff(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t,
		apiError(503, "", `{"error":{"message":"overloaded"}}`),
		turn(sseTurn{text: []string{"done"}}),
	))
	log := &eventLog{}
	opts := baseOptions(f, tools.NewRegistry(), log, t.TempDir())
	opts.MaxRetries = 1
	opts.RetryBase = 400 * time.Millisecond
	a := New(opts)
	if _, err := a.Run(context.Background(), "hi", nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	retries := log.of(EvRetry)
	if len(retries) != 1 {
		t.Fatalf("%d retry events, want 1", len(retries))
	}
	// base×2^0 plus jitter of up to half the base.
	if d := retries[0].Delay; d < 400*time.Millisecond || d > 600*time.Millisecond {
		t.Fatalf("delay = %v, want between 400ms and 600ms", d)
	}
}

func TestNegativeRetryCountDoesNotRetryWithoutEnd(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t, apiError(503, "", `{"error":{"message":"overloaded"}}`)))
	opts := baseOptions(f, tools.NewRegistry(), &eventLog{}, t.TempDir())
	opts.MaxRetries = -1
	a := New(opts)
	a.Run(context.Background(), "hi", nil)
	if n := f.count(); n != 1 {
		t.Fatalf("%d requests with MaxRetries -1, want 1", n)
	}
}

// Stream watchdog: the force-resolve bound is configurable (spec 03 §5.3).

func TestForceResolveTimeoutDefaultAndClamp(t *testing.T) {
	cases := map[string]struct{ in, want time.Duration }{
		"unset":     {0, 5 * time.Second},
		"in range":  {2 * time.Second, 2 * time.Second},
		"too small": {100 * time.Millisecond, time.Second},
		"too large": {90 * time.Second, 30 * time.Second},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := New(Options{ForceResolveTimeout: c.in}).opts.ForceResolveTimeout
			if got != c.want {
				t.Fatalf("ForceResolveTimeout = %v, want %v", got, c.want)
			}
		})
	}
}

func TestDrainAfterStallGivesUpAfterTheConfiguredBound(t *testing.T) {
	a := New(Options{ForceResolveTimeout: time.Second})
	ch := make(chan llm.Event) // a provider that never closes its stream
	start := time.Now()
	a.drainAfterStall(ch)
	elapsed := time.Since(start)
	if elapsed < 900*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("drain returned after %v, want about the 1s bound", elapsed)
	}
}

// modelRecords is the model log filtered to one kind, oldest first.
func modelRecords(a *Agent, k EventKind) []Event {
	var out []Event
	for _, e := range a.ModelEvents() {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}
