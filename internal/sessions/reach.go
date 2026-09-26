package sessions

import (
	"sync"
	"time"
)

// Status polling is per target and independent: each machine is polled by its
// own worker, a bounded number at a time, under its own timeout. Before this,
// Poll visited targets one after another, so one unreachable machine stalled
// status for every other machine for up to the poll command's 45s timeout —
// and, because the scheduler waits for Poll, stalled task scheduling with it.
const (
	// MaxConcurrentTargetPolls bounds how many machines are polled at once.
	MaxConcurrentTargetPolls = 8
	// DefaultTargetPollTimeout bounds one target's whole poll (boot probe,
	// pane capture, recovery).
	DefaultTargetPollTimeout = 20 * time.Second
	// DefaultPollWait is how long Poll waits for this round's targets before
	// returning, which is also the most a slow target can delay the
	// scheduler's tick. A target still in flight keeps running in the
	// background and is skipped by later rounds until it finishes.
	DefaultPollWait = 500 * time.Millisecond
	// UnreachableAfter is how long a poll may run before its target is shown
	// as unreachable, without waiting for the timeout.
	UnreachableAfter = 8 * time.Second
	// maxPollBackoff caps the delay between retries of a failing target.
	maxPollBackoff = 60 * time.Second
)

// TargetReach is what the session list shows about a target's reachability.
type TargetReach struct {
	Unreachable bool    `json:"unreachable"`
	Since       float64 `json:"since,omitempty"` // unix seconds the problem started
	Error       string  `json:"error,omitempty"`
}

type targetPollState struct {
	mu        sync.Mutex // held while this target is being polled
	inFlight  bool
	started   time.Time
	failures  int
	failSince time.Time
	lastErr   string
	nextTry   time.Time
}

type reachTracker struct {
	mu      sync.Mutex
	targets map[int64]*targetPollState
	sem     chan struct{}
}

func (m *Manager) reach() *reachTracker {
	m.reachOnce.Do(func() {
		m.reachState = &reachTracker{targets: map[int64]*targetPollState{},
			sem: make(chan struct{}, MaxConcurrentTargetPolls)}
	})
	return m.reachState
}

func (r *reachTracker) state(id int64) *targetPollState {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.targets[id]
	if !ok {
		st = &targetPollState{}
		r.targets[id] = st
	}
	return st
}

// begin claims a round for the target: false while a poll is still in flight
// or a failing target is backing off.
func (r *reachTracker) begin(id int64, now time.Time) bool {
	st := r.state(id)
	r.mu.Lock()
	defer r.mu.Unlock()
	if st.inFlight || now.Before(st.nextTry) {
		return false
	}
	st.inFlight, st.started = true, now
	return true
}

// finish records a poll's outcome and reports whether reachability changed.
// round releases the claim begin took.
func (r *reachTracker) finish(id int64, started, now time.Time, errText string, round bool) (changed bool) {
	st := r.state(id)
	r.mu.Lock()
	defer r.mu.Unlock()
	wasDown := st.failures > 0 || (st.inFlight && now.Sub(st.started) > UnreachableAfter)
	if round {
		st.inFlight = false
	}
	if errText == "" {
		st.failures, st.lastErr, st.nextTry, st.failSince = 0, "", time.Time{}, time.Time{}
		return wasDown
	}
	if st.failures == 0 {
		st.failSince = started
	}
	st.failures++
	st.lastErr = errText
	backoff := time.Duration(1<<min(st.failures-1, 5)) * 2 * time.Second
	st.nextTry = now.Add(min(backoff, maxPollBackoff))
	return !wasDown
}

// release drops a claim that never ran.
func (r *reachTracker) release(id int64) {
	st := r.state(id)
	r.mu.Lock()
	st.inFlight = false
	r.mu.Unlock()
}

// Reach reports a target's reachability as the poller last saw it. A poll that
// has been running longer than UnreachableAfter already counts as unreachable.
func (m *Manager) Reach(targetID int64) TargetReach {
	r := m.reach()
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.targets[targetID]
	if !ok {
		return TargetReach{}
	}
	now := time.Now()
	switch {
	case st.failures > 0:
		return TargetReach{Unreachable: true, Since: unix(st.failSince), Error: st.lastErr}
	case st.inFlight && now.Sub(st.started) > UnreachableAfter:
		return TargetReach{Unreachable: true, Since: unix(st.started), Error: "not answering"}
	}
	return TargetReach{}
}

func unix(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }
