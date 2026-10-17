package tui

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// sdLog is the order of calls a test backend saw, across all backends.
type sdLog struct {
	mu     sync.Mutex
	events []string
}

func (l *sdLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, s)
}

func (l *sdLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

// sdRecorder records the shutdown calls. Its turn stops only after an abort,
// and then keeps reporting running for saveDelay more polls, which stands in
// for a turn that takes a while to save.
type sdRecorder struct {
	*FakeBackend
	name      string
	log       *sdLog
	saveDelay int
	jobs      []Job

	mu      sync.Mutex
	aborted bool
	idle    bool
}

func (r *sdRecorder) Abort() {
	r.log.add("abort:" + r.name)
	r.mu.Lock()
	r.aborted = true
	r.mu.Unlock()
}

func (r *sdRecorder) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.aborted || r.idle {
		return false
	}
	if r.saveDelay > 0 {
		r.saveDelay--
		return true
	}
	r.idle = true
	r.log.add("idle:" + r.name)
	return false
}

func (r *sdRecorder) Jobs() []Job { return r.jobs }

func (r *sdRecorder) KillJob(id string) error {
	r.log.add("kill:" + r.name + ":" + id)
	return nil
}

func (r *sdRecorder) Close() error {
	r.log.add("close:" + r.name)
	return nil
}

func newSdRecorder(name string, log *sdLog, saveDelay int) *sdRecorder {
	fb := NewFakeBackend()
	fb.Delay = 0
	return &sdRecorder{FakeBackend: fb, name: name, log: log, saveDelay: saveDelay}
}

func sdIndexOf(events []string, want string) int {
	for i, e := range events {
		if e == want {
			return i
		}
	}
	return -1
}

// No session closes while another session's turn is still saving: a closed
// tab's MCP servers must not go down under a turn that is still running.
func TestEndSessionsClosesNothingWhileAnyTurnIsSaving(t *testing.T) {
	log := &sdLog{}
	a := newSdRecorder("a", log, 0)
	b := newSdRecorder("b", log, 5)
	EndSessions([]Backend{a, b}, 5*time.Second)

	ev := log.snapshot()
	idleB := sdIndexOf(ev, "idle:b")
	if idleB < 0 {
		t.Fatalf("b's turn never stopped: %v", ev)
	}
	for _, e := range ev {
		if strings.HasPrefix(e, "close:") && sdIndexOf(ev, e) < idleB {
			t.Fatalf("%s ran before b's turn stopped: %v", e, ev)
		}
	}
}

// The steps run in the spec's order: abort, wait for the turns, kill the
// processes, then close the sessions.
func TestEndSessionsRunsStepsInOrder(t *testing.T) {
	log := &sdLog{}
	a := newSdRecorder("a", log, 0)
	a.jobs = []Job{{ID: "j1", Status: "running"}}
	EndSessions([]Backend{a}, 5*time.Second)

	want := []string{"abort:a", "idle:a", "kill:a:j1", "close:a"}
	got := log.snapshot()
	if len(got) != len(want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("calls = %v, want %v", got, want)
		}
	}
}

// Only jobs still running are killed; one that has exited is left alone.
func TestEndSessionsKillsOnlyRunningJobs(t *testing.T) {
	log := &sdLog{}
	a := newSdRecorder("a", log, 0)
	a.jobs = []Job{
		{ID: "live", Status: "running"},
		{ID: "done", Status: "exited", ExitCode: 0},
	}
	EndSessions([]Backend{a}, 5*time.Second)

	if sdIndexOf(log.snapshot(), "kill:a:live") < 0 {
		t.Fatalf("the running job should be killed: %v", log.snapshot())
	}
	if sdIndexOf(log.snapshot(), "kill:a:done") >= 0 {
		t.Fatalf("an exited job must not be killed: %v", log.snapshot())
	}
}

// A turn that never stops does not hold the quit forever: the wait gives up
// at the timeout and the session is still closed.
func TestEndSessionsGivesUpWaitingAfterTimeout(t *testing.T) {
	log := &sdLog{}
	stuck := newSdRecorder("stuck", log, 1<<30)
	start := time.Now()
	EndSessions([]Backend{stuck}, 50*time.Millisecond)

	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("EndSessions waited %v past a 50ms timeout", elapsed)
	}
	if sdIndexOf(log.snapshot(), "close:stuck") < 0 {
		t.Fatalf("a stuck session must still close: %v", log.snapshot())
	}
}

// The quit screen's own command goes through the same order, so the TUI and
// the signal path cannot drift apart.
func TestStartShutdownEndsTabsInSpecOrder(t *testing.T) {
	m, _ := newTestModel(t, 120, 40)
	settle(m)
	m.phase = phaseChat

	log := &sdLog{}
	first := newSdRecorder("first", log, 0)
	second := newSdRecorder("second", log, 3)
	second.jobs = []Job{{ID: "j9", Status: "running"}}
	m.tabs[0].be = first
	m.tabs = append(m.tabs, &tab{id: 99, name: "second", be: second})

	cmd := m.startShutdown()
	if cmd == nil {
		t.Fatalf("startShutdown returned no command")
	}
	if msg := cmd(); msg == nil {
		t.Fatalf("the shutdown command should report closedMsg")
	}

	ev := log.snapshot()
	closeFirst := sdIndexOf(ev, "close:first")
	if closeFirst < 0 || sdIndexOf(ev, "idle:second") > closeFirst {
		t.Fatalf("first closed before second's turn stopped: %v", ev)
	}
	if sdIndexOf(ev, "kill:second:j9") < 0 || sdIndexOf(ev, "kill:second:j9") > sdIndexOf(ev, "close:second") {
		t.Fatalf("second's running job should be killed before it closes: %v", ev)
	}
}
