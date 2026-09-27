//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// fanout measures how long a board update takes to reach every open
// /api/stream subscriber. A probe renames a session; the clock starts just
// before the PATCH is sent and stops when each client reads the event.
type fanout struct {
	mu       sync.Mutex
	clients  int
	sent     map[string]time.Time
	got      map[string]map[int]bool
	delays   series
	received int
}

func newFanout(clients int) *fanout {
	return &fanout{clients: clients, sent: map[string]time.Time{}, got: map[string]map[int]bool{}}
}

func (f *fanout) expect(name string, at time.Time) {
	f.mu.Lock()
	f.sent[name] = at
	f.got[name] = map[int]bool{}
	f.mu.Unlock()
}

func (f *fanout) seen(client int, ev sseEvent) {
	if ev.Event != "session" || !strings.Contains(string(ev.Data), `"probe-`) {
		return
	}
	var row struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(ev.Data, &row) != nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	sent, ok := f.sent[row.Name]
	if !ok || f.got[row.Name][client] {
		return
	}
	f.got[row.Name][client] = true
	f.received++
	f.delays.add(ev.At.Sub(sent))
}

func (f *fanout) result() (Stats, int, int) {
	f.mu.Lock()
	probes, received := len(f.sent), f.received
	f.mu.Unlock()
	return f.delays.stats(), probes, probes*f.clients - received
}

// approver plays the operator: it watches the board stream and approves
// every pending approval the moment it appears, recording when it saw it and
// when it answered. The agents record when they asked and when they were
// unblocked; result joins the two by the tool command, which is unique per
// request.
type approver struct {
	t *tier

	mu        sync.Mutex
	windowOn  bool
	windowAt  time.Time
	windowEnd time.Time
	seen      map[int64]bool
	visibleAt map[string]time.Time
	decideAt  map[string]time.Time
	decide    series
	failed    int
}

func newApprover(t *tier) *approver {
	return &approver{t: t, seen: map[int64]bool{}, visibleAt: map[string]time.Time{}, decideAt: map[string]time.Time{}}
}

func (a *approver) open() {
	a.mu.Lock()
	a.windowOn, a.windowAt = true, time.Now()
	a.mu.Unlock()
}

func (a *approver) close() {
	a.mu.Lock()
	a.windowOn, a.windowEnd = false, time.Now()
	a.mu.Unlock()
}

func (a *approver) onEvent(ev sseEvent) {
	if ev.Event != "approval" {
		return
	}
	var row struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
		Input  struct {
			Command string `json:"command"`
		} `json:"input"`
	}
	if json.Unmarshal(ev.Data, &row) != nil || row.Status != "pending" {
		return
	}
	a.mu.Lock()
	if a.seen[row.ID] {
		a.mu.Unlock()
		return
	}
	a.seen[row.ID] = true
	a.visibleAt[row.Input.Command] = ev.At
	a.mu.Unlock()
	go func() {
		at := time.Now()
		code, _, d, err := a.t.do("POST", fmt.Sprintf("/api/approvals/%d/decision", row.ID),
			map[string]any{"decision": "approved"})
		a.mu.Lock()
		defer a.mu.Unlock()
		if err != nil || code != 200 {
			a.failed++
			a.t.countErr("POST /api/approvals/{id}/decision")
			return
		}
		a.decideAt[row.Input.Command] = at
		if a.windowOn {
			a.decide.add(d)
		}
	}()
}

type approvalResult struct {
	requested, approved, other          int
	visible, decide, unblock, roundTrip Stats
}

// result joins the agents' logs ("approval SEQ T0 T3 RC DECISION") with what
// the operator saw. Only requests that began inside the window count; one
// that the operator saw but that never returned to its agent is "other".
func (a *approver) result(logs map[string][]string) approvalResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	from, to := a.windowAt, a.windowEnd
	var vis, unb, rt series
	var r approvalResult
	answered := map[string]bool{}
	for name, lines := range logs {
		for _, line := range lines {
			f := strings.Fields(line)
			if len(f) < 6 || f[0] != "approval" {
				continue
			}
			t0, t3 := nanos(f[2]), nanos(f[3])
			if t0.Before(from) || t0.After(to) {
				continue
			}
			key := fmt.Sprintf("make test # stress %s %s", name, f[1])
			answered[key] = true
			r.requested++
			if f[4] == "0" && f[5] == "allow" {
				r.approved++
			} else {
				r.other++
			}
			rt.add(t3.Sub(t0))
			if v, ok := a.visibleAt[key]; ok {
				vis.add(v.Sub(t0))
			}
			if d, ok := a.decideAt[key]; ok {
				unb.add(t3.Sub(d))
			}
		}
	}
	for key, at := range a.visibleAt {
		if !answered[key] && !at.Before(from) && !at.After(to) {
			r.requested++
			r.other++
		}
	}
	r.visible, r.decide, r.unblock, r.roundTrip = vis.stats(), a.decide.stats(), unb.stats(), rt.stats()
	return r
}

func nanos(s string) time.Time {
	n, _ := strconv.ParseInt(s, 10, 64)
	return time.Unix(0, n)
}
