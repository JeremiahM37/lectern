package tui

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// sdW29Log records shutdown calls across every fake backend, in order.
type sdW29Log struct {
	mu     sync.Mutex
	events []string
}

func (l *sdW29Log) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, s)
}

func (l *sdW29Log) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

// sdW29 is a backend that records every shutdown step it receives. Each step
// can be made to fail or hang, so a test can check that the step after it
// still runs.
type sdW29 struct {
	*FakeBackend
	name string
	log  *sdW29Log
	jobs []Job

	stuckRunning bool          // Running stays true after an abort
	block        chan struct{} // when set, CloseProviders waits on it
	providerErr  error
	closeErr     error
	groupErr     error
}

func newSdW29(name string, log *sdW29Log) *sdW29 {
	return &sdW29{FakeBackend: NewFakeBackend(), name: name, log: log}
}

func (s *sdW29) Abort() { s.log.add("abort:" + s.name) }

func (s *sdW29) Running() bool { return s.stuckRunning }

func (s *sdW29) Jobs() []Job { return s.jobs }

func (s *sdW29) KillJob(id string) error {
	s.log.add("kill:" + s.name + ":" + id)
	return nil
}

func (s *sdW29) CloseProviders() error {
	s.log.add("providers:" + s.name)
	if s.block != nil {
		<-s.block
	}
	return s.providerErr
}

func (s *sdW29) Close() error {
	s.log.add("close:" + s.name)
	return s.closeErr
}

func (s *sdW29) KillProcessGroups() error {
	s.log.add("groups:" + s.name)
	return s.groupErr
}

// withW29Bounds shortens every shutdown bound for the length of one test.
func withW29Bounds(t *testing.T, wait time.Duration) {
	t.Helper()
	pc, jk, sc, pg := providerCloseWait, jobKillWait, sessionCloseWait, processGroupWait
	providerCloseWait, jobKillWait, sessionCloseWait, processGroupWait = wait, wait, wait, wait
	t.Cleanup(func() {
		providerCloseWait, jobKillWait, sessionCloseWait, processGroupWait = pc, jk, sc, pg
	})
}

func TestEndSessionsRunsStepsInSpecOrderW29(t *testing.T) {
	log := &sdW29Log{}
	a := newSdW29("a", log)
	a.jobs = []Job{{ID: "j1", Status: "running"}, {ID: "j2", Status: "exited"}}
	b := newSdW29("b", log)

	errs := EndSessions([]Backend{a, b}, time.Second)
	if len(errs) != 0 {
		t.Fatalf("errors = %v, want none", errs)
	}
	want := []string{
		"abort:a", "abort:b",
		"providers:a", "providers:b",
		"kill:a:j1",
		"close:a", "close:b",
		"groups:a", "groups:b",
	}
	got := log.snapshot()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("call order:\n got  %v\n want %v", got, want)
	}
}

func TestEndSessionsSkipsStepsABackendDoesNotHave(t *testing.T) {
	errs := EndSessions([]Backend{NewFakeBackend()}, time.Second)
	if len(errs) != 0 {
		t.Fatalf("a plain backend should close cleanly, got %v", errs)
	}
}

func TestEndSessionsBoundsAHangingProviderStepW29(t *testing.T) {
	withW29Bounds(t, 30*time.Millisecond)
	log := &sdW29Log{}
	a := newSdW29("a", log)
	a.block = make(chan struct{})
	t.Cleanup(func() { close(a.block) })

	start := time.Now()
	errs := EndSessions([]Backend{a}, time.Second)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("EndSessions waited %v behind a hung provider step", elapsed)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "close providers") {
		t.Fatalf("errors = %v, want one close providers timeout", errs)
	}
	if !strings.Contains(strings.Join(log.snapshot(), " "), "close:a") {
		t.Fatalf("the session close did not run after the hung step: %v", log.snapshot())
	}
}

func TestEndSessionsKeepsGoingAfterFailuresW29(t *testing.T) {
	log := &sdW29Log{}
	a := newSdW29("a", log)
	a.providerErr = errors.New("connection pool busy")
	a.closeErr = errors.New("save failed")
	a.groupErr = errors.New("job still running")

	errs := EndSessions([]Backend{a}, time.Second)
	if len(errs) != 3 {
		t.Fatalf("errors = %v, want three (providers, close, groups)", errs)
	}
	for i, want := range []string{"close providers", "close session", "kill process groups"} {
		if !strings.Contains(errs[i].Error(), want) {
			t.Errorf("errs[%d] = %q, want it to name %q", i, errs[i], want)
		}
	}
	if got := strings.Join(log.snapshot(), " "); got != "abort:a providers:a close:a groups:a" {
		t.Fatalf("calls = %q, want every step to run in order", got)
	}
}

func TestEndSessionsReportsTurnThatWillNotStopW29(t *testing.T) {
	log := &sdW29Log{}
	a := newSdW29("a", log)
	a.stuckRunning = true

	errs := EndSessions([]Backend{a}, 20*time.Millisecond)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "stop running turns") {
		t.Fatalf("errors = %v, want the turn that would not stop", errs)
	}
	if !strings.Contains(strings.Join(log.snapshot(), " "), "close:a") {
		t.Fatalf("the session was not closed after the turn failed to stop: %v", log.snapshot())
	}
}

func TestBoundedStepReturnsAndRecoversPanicW29(t *testing.T) {
	err := boundedStep("x", time.Second, func() error { panic("boom") })
	if err == nil || !strings.Contains(err.Error(), "panic: boom") {
		t.Fatalf("err = %v, want the panic returned", err)
	}
	if err := boundedStep("y", time.Second, func() error { return nil }); err != nil {
		t.Fatalf("a clean step returned %v", err)
	}
}

func TestRecordShutdownErrorsKeepsFailuresW29(t *testing.T) {
	shutdownErrs.Lock()
	shutdownErrs.list = nil
	shutdownErrs.Unlock()
	t.Cleanup(func() {
		shutdownErrs.Lock()
		shutdownErrs.list = nil
		shutdownErrs.Unlock()
	})

	recordShutdownErrors(nil)
	if n := len(ShutdownErrors()); n != 0 {
		t.Fatalf("no errors recorded, got %d", n)
	}
	recordShutdownErrors([]error{errors.New("one"), errors.New("two")})
	got := ShutdownErrors()
	if len(got) != 2 || got[0].Error() != "one" || got[1].Error() != "two" {
		t.Fatalf("ShutdownErrors = %v, want [one two]", got)
	}
}
