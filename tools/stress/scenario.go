package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"syscall"
	"time"
)

// ---- unreachable target ------------------------------------------------------
//
// With -hang-at set, the driver freezes one SSH target (SIGSTOP on its SSH
// server: connections are accepted by the kernel and then nothing answers,
// which is what a wedged or partitioned machine looks like) partway through
// the window, and measures whether the other machines' session status stays
// fresh. The stand-in agents then redraw a clock every second, so a session's
// last_activity_at moves on every successful status poll; "staleness" is how
// long ago that was, sampled from the list the dashboard polls.

type hangWatch struct {
	mu        sync.Mutex
	target    string // name of the frozen target
	hungAt    time.Time
	resumedAt time.Time
	before    series    // staleness on the other targets before the freeze
	during    series    // … while it is frozen
	hungStale series    // staleness on the frozen target, for contrast
	flagged   time.Time // first time every frozen-target row showed unreachable
	falseFlag int       // rows on healthy targets shown unreachable
	recovered time.Time // first time after resume no row showed unreachable
}

type listRow struct {
	ID             int64    `json:"id"`
	TargetName     string   `json:"target_name"`
	LastActivityAt *float64 `json:"last_activity_at"`
	TargetReach    *struct {
		Unreachable bool `json:"unreachable"`
	} `json:"target_reach"`
}

// observe folds one /api/sessions response into the watch.
func (h *hangWatch) observe(at time.Time, body []byte, mine map[int64]bool) {
	var rows []listRow
	if json.Unmarshal(body, &rows) != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	hung := !h.hungAt.IsZero() && h.resumedAt.IsZero()
	allFlagged, anyFlagged, onHung := true, false, 0
	for _, r := range rows {
		if !mine[r.ID] {
			continue
		}
		flagged := r.TargetReach != nil && r.TargetReach.Unreachable
		anyFlagged = anyFlagged || flagged
		if r.TargetName == h.target {
			onHung++
			allFlagged = allFlagged && flagged
			if hung && r.LastActivityAt != nil {
				h.hungStale.add(at.Sub(time.Unix(0, int64(*r.LastActivityAt*1e9))))
			}
			continue
		}
		if flagged {
			h.falseFlag++
		}
		if r.LastActivityAt == nil {
			continue
		}
		stale := at.Sub(time.Unix(0, int64(*r.LastActivityAt*1e9)))
		switch {
		case h.hungAt.IsZero():
			h.before.add(stale)
		case hung:
			h.during.add(stale)
		}
	}
	if hung && onHung > 0 && allFlagged && h.flagged.IsZero() {
		h.flagged = at
	}
	if !h.resumedAt.IsZero() && !anyFlagged && h.recovered.IsZero() {
		h.recovered = at
	}
}

// HangResult is the unreachable-target scenario's outcome.
type HangResult struct {
	Target               string  `json:"target"`
	FrozenSeconds        float64 `json:"frozen_s"`
	StalenessBefore      Stats   `json:"healthy_staleness_before"`
	StalenessDuring      Stats   `json:"healthy_staleness_during"`
	FrozenTargetStale    Stats   `json:"frozen_target_staleness"`
	SecondsToUnreachable float64 `json:"seconds_to_unreachable"` // -1: never shown
	HealthyFlagged       int     `json:"healthy_rows_shown_unreachable"`
	SecondsToRecover     float64 `json:"seconds_to_recover"` // -1: not within the limit
}

func (h *hangWatch) freeze(pid int) {
	h.mu.Lock()
	h.hungAt = time.Now()
	h.mu.Unlock()
	syscall.Kill(pid, syscall.SIGSTOP)
}

func (h *hangWatch) resume(pid int) {
	syscall.Kill(pid, syscall.SIGCONT)
	h.mu.Lock()
	h.resumedAt = time.Now()
	h.mu.Unlock()
}

func (h *hangWatch) result() *HangResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := &HangResult{Target: h.target, StalenessBefore: h.before.stats(), StalenessDuring: h.during.stats(),
		FrozenTargetStale: h.hungStale.stats(), HealthyFlagged: h.falseFlag,
		SecondsToUnreachable: -1, SecondsToRecover: -1}
	if !h.hungAt.IsZero() && !h.resumedAt.IsZero() {
		r.FrozenSeconds = round(h.resumedAt.Sub(h.hungAt).Seconds())
	}
	if !h.flagged.IsZero() {
		r.SecondsToUnreachable = round(h.flagged.Sub(h.hungAt).Seconds())
	}
	if !h.recovered.IsZero() {
		r.SecondsToRecover = round(h.recovered.Sub(h.resumedAt).Seconds())
	}
	return r
}

// ---- many open terminals -----------------------------------------------------

// HoldResult is the many-open-terminals check: open K web terminals and keep
// every websocket open, as K browser tabs would, then count how many are
// still connected.
type HoldResult struct {
	Opened    int      `json:"opened"`
	StillOpen int      `json:"still_open"`
	Refused   int      `json:"refused"`
	Notices   int      `json:"retire_notices"`
	Errors    []string `json:"errors,omitempty"`
}

func (t *tier) holdTerminals(ids []int64, k int) *HoldResult {
	res := &HoldResult{}
	type held struct {
		conn   net.Conn
		closed chan struct{}
	}
	var open []held
	for _, id := range ids[:min(k, len(ids))] {
		code, out, _, err := t.do("POST", fmt.Sprintf("/api/sessions/%d/terminal", id), map[string]any{})
		if err != nil || code != 200 {
			res.Refused++
			if len(res.Errors) < 5 {
				res.Errors = append(res.Errors, fmt.Sprintf("session %d: %d %s %v", id, code, out, err))
			}
			continue
		}
		var r struct {
			URL    string `json:"url"`
			Notice string `json:"notice"`
		}
		json.Unmarshal(out, &r)
		if r.Notice != "" {
			res.Notices++
		}
		_, conn, br, err := ttydOpen(t.base, r.URL, 20*time.Second)
		if err != nil {
			if len(res.Errors) < 5 {
				res.Errors = append(res.Errors, fmt.Sprintf("session %d websocket: %v", id, err))
			}
			continue
		}
		h := held{conn: conn, closed: make(chan struct{})}
		go func(br *bufio.Reader, done chan struct{}) {
			defer close(done)
			for {
				op, _, err := readFrame(br)
				if err != nil || op == 0x8 {
					return
				}
			}
		}(br, h.closed)
		open = append(open, h)
		res.Opened++
	}
	time.Sleep(3 * time.Second)
	for _, h := range open {
		select {
		case <-h.closed:
		default:
			res.StillOpen++
		}
		writeFrame(h.conn, 0x8, nil)
		h.conn.Close()
	}
	return res
}
