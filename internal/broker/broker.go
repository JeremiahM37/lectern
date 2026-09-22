// Package broker is the approval gate: the hook blocks here until a human — or
// expiry — decides.
package broker

import (
	"context"
	"encoding/json"
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
	summary, _ := toolInput["command"].(string)
	if summary == "" {
		summary, _ = toolInput["file_path"].(string)
	}
	if len(summary) > 120 {
		summary = summary[:120]
	}
	br.Notifier.Notify("Approval needed", toolName+": "+summary, "/#approvals",
		&sinks.Extra{Kind: "approval", ApprovalID: id})
	return id, nil
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
	br.Bus.Publish(taskChannel(fresh.TaskID), "approval", fresh)
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

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
