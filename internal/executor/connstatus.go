package executor

import (
	"sync"
	"time"
)

// ConnStatus is what the UI shows about a remote connection: whether it is up,
// whether Lectern is reconnecting after a drop, and how often that happened.
// Every command re-dials a dropped connection on its own; this only makes
// that visible.
type ConnStatus struct {
	// State is "idle" (never used), "connected", "reconnecting" (dropped;
	// the next command dials again) or "down" (the last dial failed).
	State      string  `json:"state"`
	Since      float64 `json:"since,omitempty"`
	LastOK     float64 `json:"last_ok,omitempty"`
	LastError  string  `json:"last_error,omitempty"`
	Reconnects int     `json:"reconnects"`
	Transport  string  `json:"transport"`
	Via        string  `json:"via,omitempty"`
}

// StatusReporter is an executor that knows its connection state.
type StatusReporter interface {
	ConnStatus() ConnStatus
}

// connTracker is the bookkeeping behind ConnStatus.
type connTracker struct {
	mu        sync.Mutex
	st        ConnStatus
	everUp    bool
	transport string
	via       string
}

func (c *connTracker) set(state, errText string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := float64(time.Now().Unix())
	if state == "connected" {
		if c.everUp && c.st.State != "connected" {
			c.st.Reconnects++
		}
		c.everUp = true
		c.st.LastOK = now
		errText = ""
	}
	if c.st.State != state {
		c.st.Since = now
	}
	c.st.State = state
	if errText != "" || state == "connected" {
		c.st.LastError = errText
	}
}

func (c *connTracker) ok() {
	c.mu.Lock()
	c.st.LastOK = float64(time.Now().Unix())
	c.mu.Unlock()
}

func (c *connTracker) status() ConnStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.st
	if out.State == "" {
		out.State = "idle"
	}
	out.Transport, out.Via = c.transport, c.via
	return out
}
