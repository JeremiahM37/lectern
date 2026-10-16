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
	"github.com/JeremiahM37/lectern/v2/internal/policy"
	"github.com/JeremiahM37/lectern/v2/internal/sinks"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/witness"
)

// Broker owns pending approvals and the channels blocked hooks wait on.
type Broker struct {
	DB       *store.DB
	Bus      *bus.Bus
	Notifier *sinks.Notifier
	// ExpireAfter is how long a pending approval lives before the system decides
	// for the operator.
	ExpireAfter time.Duration
	// Witness reads the git state of the worktree an approval acts on. nil
	// turns the feature off: no approval records a witness and none is
	// re-checked. It is called only for effectful calls (witness.IsEffectful)
	// that a person is asked about; a failure means no witness, never an error.
	Witness func(a *store.Approval) (*witness.Witness, error)
	// OnDecided, when set, is told about every decision that stood, with the
	// approval as it now is (final status, decided_by, decided_at). It is how
	// internal/decisionlog records approvals without this package importing
	// it. nil is a valid configuration: no recording.
	OnDecided func(row *store.Approval)

	mu           sync.Mutex
	waiters      map[int64]chan struct{}
	sessionRules map[int64]policy.Policy
}

// New builds a broker.
func New(db *store.DB, b *bus.Bus, n *sinks.Notifier, expireAfter time.Duration) *Broker {
	return &Broker{DB: db, Bus: b, Notifier: n, ExpireAfter: expireAfter,
		waiters: map[int64]chan struct{}{}, sessionRules: map[int64]policy.Policy{}}
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
	// A quiet call is a policy auto-approval: nobody looks at it, so there is
	// no state a person approved and nothing to re-check.
	if !quiet {
		br.recordWitness(id, toolName, toolInput)
	}
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

// recordWitness stores the git state an effectful call was requested against,
// so Decide can check it again before the call is allowed to run. A failure to
// read the state leaves the row without a witness and never blocks the request.
func (br *Broker) recordWitness(id int64, toolName string, toolInput map[string]any) {
	if br.Witness == nil || !witness.IsEffectful(toolName, toolInput) {
		return
	}
	row, err := br.DB.Approval(id)
	if err != nil {
		return
	}
	w, err := br.Witness(row)
	if err != nil || w == nil {
		return
	}
	raw, err := json.Marshal(w)
	if err != nil {
		return
	}
	_ = br.DB.Update("approvals", id, map[string]any{"witness_json": string(raw)})
}

// witnessDrift says how the worktree has changed since the approval was
// requested, or "" when it has not. It fails closed: if the state cannot be
// read again, the approval is treated as stale, because the gate cannot tell
// that nothing changed.
func (br *Broker) witnessDrift(row *store.Approval) string {
	var was witness.Witness
	if err := json.Unmarshal([]byte(row.WitnessJSON), &was); err != nil {
		return "could not read the state it was requested against"
	}
	now, err := br.Witness(row)
	if err != nil || now == nil {
		return "could not re-check the worktree"
	}
	return was.Drift(*now)
}

// summarize renders a tool call's input down to the one line an operator
// glances at in a push notification — shared by Create and CreateForSession.
func summarize(toolName string, toolInput map[string]any) string {
	summary, _ := toolInput["command"].(string)
	if summary == "" {
		summary, _ = toolInput["file_path"].(string)
	}
	if summary == "" {
		// A Scribe question (AskUser) carries its text here, not a command.
		summary, _ = toolInput["question"].(string)
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
	br.recordWitness(id, toolName, toolInput)
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

// SessionRuleAllows reports whether an earlier "allow for this session"
// decision already covers this exact tool call. It reuses internal/policy's
// project-rule matcher (Bash matches on the command's first token, other
// tools match wholesale) rather than inventing a second pattern language —
// the only difference from a project's "always allow" is where the rule
// lives: in memory, keyed by session, cleared at ClearSessionRules rather
// than persisted to a project row.
func (br *Broker) SessionRuleAllows(sessionID int64, toolName string, toolInput map[string]any) bool {
	br.mu.Lock()
	rules, ok := br.sessionRules[sessionID]
	br.mu.Unlock()
	if !ok {
		return false
	}
	return policy.Matches(rules, toolName, toolInput)
}

// AllowForSession records that this tool (or, for Bash, this exact command's
// first token) should no longer interrupt this one session. It is the
// "Allow for this session" graduation between a single approval and a
// project-wide "always allow": scoped to one session's lifetime, held only
// in memory, and gone the moment ClearSessionRules runs (session end).
func (br *Broker) AllowForSession(sessionID int64, toolName string, toolInput map[string]any) {
	if !policy.BroadenableForSession(toolName, toolInput) {
		// "sudo …", "bash -c …" and the like: the one approval still
		// stands, but no session-wide rule is remembered for it.
		return
	}
	rule := policy.PatternFor(toolName, toolInput)
	br.mu.Lock()
	defer br.mu.Unlock()
	br.sessionRules[sessionID] = policy.AddRule(br.sessionRules[sessionID], rule)
}

// ClearSessionRules forgets a session's "allow for this session" rules. A
// session id is never reused, but nothing frees the entry otherwise, so this
// is called when a session ends to keep the map from growing unbounded.
func (br *Broker) ClearSessionRules(sessionID int64) {
	br.mu.Lock()
	delete(br.sessionRules, sessionID)
	br.mu.Unlock()
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
	// An approval is granted against the state the person saw. If that state
	// has moved, the approval does not stand: the hook gets a deny that says so,
	// and the agent re-runs the command to ask again. A deny is never blocked.
	if decision == "approved" && row.WitnessJSON != "" {
		if drift := br.witnessDrift(row); drift != "" {
			decision = "stale"
			note = "state changed since this was requested: " + drift + "; re-run the command to ask again"
			decidedBy = "witness"
		}
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
	// Every phone that was asked now drops the question, whoever answered it
	// and wherever. A policy decision follows a quiet Create that notified
	// nobody, so there is nothing to withdraw.
	if decidedBy != "policy" {
		br.Notifier.Dismiss(sinks.ApprovalTag(id), dismissTitle(decision, decidedBy), "Nothing left to do here.")
	}
	if decision == "stale" {
		br.Notifier.Notify("Approval out of date", fresh.Note, "/#approvals", nil)
	}
	if br.OnDecided != nil {
		br.OnDecided(fresh)
	}
	return fresh
}

func dismissTitle(decision, decidedBy string) string {
	switch {
	case decision == "expired":
		return "Approval expired"
	case decision == "stale":
		return "Approval out of date"
	case decidedBy == "system":
		return "Approval no longer needed"
	case decision == "approved":
		return "Approved on another device"
	default:
		return "Denied on another device"
	}
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

// ExpireForGoneSessions resolves every approval still pending for a session
// that has ended or whose agent has exited. Nothing can deliver a decision to
// either, so leaving it pending only shows "needs you" for a process that no
// longer exists. Swept on the scheduler's tick, so every way a session ends —
// End, a dead pane, an agent that quit or crashed — is covered by one rule.
func (br *Broker) ExpireForGoneSessions() int {
	rows, err := br.DB.ApprovalsByStatus("pending")
	if err != nil {
		return 0
	}
	n := 0
	for _, r := range rows {
		if r.SessionID == 0 {
			continue
		}
		reason := ""
		if sess, err := br.DB.Session(r.SessionID); err != nil {
			reason = "the session no longer exists"
		} else if sess.EndedAt != nil {
			reason = "the session ended before a decision arrived"
		} else if sess.AgentExitedAt != nil {
			reason = "the agent exited before a decision arrived"
		}
		if reason != "" && br.Decide(r.ID, "expired", reason, "system") != nil {
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
