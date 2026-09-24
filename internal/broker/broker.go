// Package broker is the approval gate: the hook blocks here until a human — or
// expiry — decides.
package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/sinks"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Broker owns pending approvals and the channels blocked hooks wait on.
type Broker struct {
	DB       *store.DB
	Bus      *bus.Bus
	Notifier *sinks.Notifier
	// ExpireAfter is how long a pending approval lives before the system decides
	// for the operator.
	ExpireAfter time.Duration

	mu      sync.Mutex
	waiters map[int64]chan struct{}
}

// New builds a broker.
func New(db *store.DB, b *bus.Bus, n *sinks.Notifier, expireAfter time.Duration) *Broker {
	return &Broker{DB: db, Bus: b, Notifier: n, ExpireAfter: expireAfter,
		waiters: map[int64]chan struct{}{}}
}

func (br *Broker) waiter(id int64) chan struct{} {
	br.mu.Lock()
	defer br.mu.Unlock()
	if ch, ok := br.waiters[id]; ok {
		return ch
	}
	ch := make(chan struct{})
	br.waiters[id] = ch
	return ch
}

// Create records a tool call awaiting a decision.
//
// quiet=true means a policy auto-approval is about to follow: the row is written
// for the audit trail, but nobody's phone lights up.
func (br *Broker) Create(attemptID int64, toolName string, toolInput map[string]any, quiet bool) (int64, error) {
	raw, _ := json.Marshal(toolInput)
	id, err := br.DB.InsertApproval(attemptID, toolName, string(raw))
	if err != nil {
		return 0, err
	}
	br.waiter(id)
	if quiet {
		return id, nil
	}
	row, err := br.DB.Approval(id)
	if err != nil {
		return id, nil
	}
	br.Bus.Publish("board", "approval", row)
	br.Bus.Publish(taskChannel(row.TaskID), "approval", row)
	br.Notifier.Notify("Approval needed", toolName+": "+summarize(toolName, toolInput), "/#approvals",
		&sinks.Extra{Kind: "approval", ApprovalID: id})
	return id, nil
}

// summarize renders a tool call's input down to the one line an operator
// glances at in a push notification — shared by Create and CreateForSession.
func summarize(toolName string, toolInput map[string]any) string {
	summary, _ := toolInput["command"].(string)
	if summary == "" {
		summary, _ = toolInput["file_path"].(string)
	}
	if summary == "" {
		if raw, err := json.Marshal(toolInput); err == nil && string(raw) != "{}" {
			summary = string(raw)
		}
	}
	if len(summary) > 120 {
		summary = summary[:120]
	}
	return summary
}

// CreateForSession records a session's PermissionRequest hook call as an
// approval belonging to that session rather than a task attempt
// (docs/agent-events.md section 3). Unlike Create there is no server-side
// policy short-circuit: "always allow" is a task/project concept the
// decide-approval endpoint layers on afterwards, and it only ever looks at
// AttemptID, so a session-scoped row simply never matches it.
//
// The caller (internal/api's PermissionRequest handling) is what actually
// blocks the HTTP request open until this is decided — CreateForSession only
// records the row, wires the waiter channel, and fires the notification.
func (br *Broker) CreateForSession(sessionID int64, toolName string, toolInput map[string]any) (int64, error) {
	raw, _ := json.Marshal(toolInput)
	id, err := br.DB.InsertSessionApproval(sessionID, toolName, string(raw))
	if err != nil {
		return 0, err
	}
	br.waiter(id)
	row, err := br.DB.Approval(id)
	if err != nil {
		return id, nil
	}
	br.Bus.Publish("board", "approval", row)
	br.Bus.Publish(sessionChannel(sessionID), "approval", row)
	br.Notifier.Notify("Permission needed", toolName+": "+summarize(toolName, toolInput),
		fmt.Sprintf("/session/%d", sessionID), &sinks.Extra{Kind: "approval", ApprovalID: id})
	return id, nil
}

// WaitOnce blocks for exactly one decide-or-timeout window and returns
// whatever the approval's state is at that point. It is for a caller that
// holds a single HTTP request open — a session's PermissionRequest hook,
// which is one request/response, not the task hook's repeated long-poll
// loop — so unlike Wait it never consults ExpireAfter (the caller's own
// timeout IS the expiry) and always resolves "still pending after timeout"
// to expired rather than leaving that to a next poll that will never come.
func (br *Broker) WaitOnce(ctx context.Context, id int64, timeout time.Duration) *store.Approval {
	row, err := br.DB.Approval(id)
	if err != nil {
		return nil
	}
	if row.Status != "pending" {
		return row
	}
	ch := br.waiter(id)
	select {
	case <-ch:
	case <-time.After(timeout):
		br.Decide(id, "expired", "no decision within the hold window", "system")
	case <-ctx.Done():
	}
	fresh, err := br.DB.Approval(id)
	if err != nil {
		return row
	}
	return fresh
}

// Decide resolves a pending approval. Returns nil if it was not pending — a
// double decision is a conflict, not a silent overwrite.
func (br *Broker) Decide(id int64, decision, note, decidedBy string) *store.Approval {
	row, err := br.DB.Approval(id)
	if err != nil || row.Status != "pending" {
		return nil
	}
	now := store.Now()
	if err := br.DB.Update("approvals", id, map[string]any{
		"status": decision, "note": note, "decided_by": decidedBy, "decided_at": now,
	}); err != nil {
		return nil
	}
	br.mu.Lock()
	if ch, ok := br.waiters[id]; ok {
		close(ch)
		delete(br.waiters, id)
	}
	br.mu.Unlock()

	fresh, err := br.DB.Approval(id)
	if err != nil {
		return nil
	}
	br.Bus.Publish("board", "approval", fresh)
	if fresh.SessionID != 0 {
		br.Bus.Publish(sessionChannel(fresh.SessionID), "approval", fresh)
	} else {
		br.Bus.Publish(taskChannel(fresh.TaskID), "approval", fresh)
	}
	return fresh
}

// ExpireForAttempt resolves any approval still pending for an attempt that has
// ended. Without this they hang 'pending' forever: the board badge sticks and
// the blocked hook never gets a decision.
func (br *Broker) ExpireForAttempt(attemptID int64) int {
	rows, err := br.DB.ApprovalsByStatus("pending")
	if err != nil {
		return 0
	}
	n := 0
	for _, r := range rows {
		if r.AttemptID != attemptID {
			continue
		}
		if br.Decide(r.ID, "expired", "attempt ended before decision", "system") != nil {
			n++
		}
	}
	return n
}

// ExpireForSession resolves any approval still pending for a session whose
// terminal answered elsewhere — a PostToolUse or Notification hook event
// arriving proves the session has moved past whatever it was blocked on, so
// a still-pending row is stale and must not linger (docs/agent-events.md
// section 3: "If the session's terminal answers first ..., expire it").
// Mirrors ExpireForAttempt for the same reason: without this the board badge
// and the session card would show a decision that can never actually be
// delivered anywhere.
func (br *Broker) ExpireForSession(sessionID int64) int {
	rows, err := br.DB.ApprovalsByStatus("pending")
	if err != nil {
		return 0
	}
	n := 0
	for _, r := range rows {
		if r.SessionID != sessionID {
			continue
		}
		if br.Decide(r.ID, "expired", "the session moved on before a decision arrived", "system") != nil {
			n++
		}
	}
	return n
}

// Wait is the hook's long-poll: it returns once the approval is decided, expires,
// or the poll window closes.
func (br *Broker) Wait(ctx context.Context, id int64, timeout time.Duration) *store.Approval {
	row, err := br.DB.Approval(id)
	if err != nil {
		return nil
	}
	if row.Status != "pending" {
		return row
	}
	if store.Now()-row.CreatedAt > br.ExpireAfter.Seconds() {
		br.Decide(id, "expired", "", "system")
	} else {
		ch := br.waiter(id)
		select {
		case <-ch:
		case <-time.After(timeout):
		case <-ctx.Done():
		}
	}
	fresh, err := br.DB.Approval(id)
	if err != nil {
		return row
	}
	return fresh
}

func taskChannel(taskID int64) string {
	return "task:" + itoa(taskID)
}

func sessionChannel(sessionID int64) string {
	return "session:" + itoa(sessionID)
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
