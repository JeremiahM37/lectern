package limits

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/accounts"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Account swap (docs/accounts.md): a limited agent is restarted under another
// signed-in account of the same CLI and resumes the same conversation. The
// hold records both ends (account_from, account_to) when the swap starts, so
// a restart can tell a swap that never happened from one that did. The
// session's own account_id changing to account_to is the commit point.

// StateSwapping is an open hold whose agent is being restarted under another
// account. StateSwapped resolves a task hold whose continuation was queued
// under another account.
const (
	StateSwapping = "swapping"
	StateSwapped  = "swapped"
)

// ActionSwap moves the work to another account now.
const ActionSwap = "swap"

// ErrNoAccount means every other account of the CLI is limited, or there is
// no other account.
var ErrNoAccount = errors.New("no other account is available")

// ErrAgentExited is a swap whose agent quit right after restarting under the
// new account (it could not find the conversation, or was not signed in).
var ErrAgentExited = errors.New("the agent exited")

// swapTimeout bounds one swap: staging the conversation, restarting the
// agent and waiting for it to show its prompt.
const swapTimeout = 4 * time.Minute

// Account is where a session or attempt runs: its registered account, the
// registered default login when it has none, or nil for an unregistered
// default login.
func Account(db *store.DB, targetID int64, agent string, accountID *int64) *store.Account {
	if accountID != nil {
		if a, err := db.Account(*accountID); err == nil && a.TargetID == targetID && a.Agent == agent {
			return a
		}
		return nil
	}
	if a, err := db.DefaultAccountFor(targetID, agent); err == nil {
		return a
	}
	return nil
}

// Candidates is every account of one CLI on one target as the selection sees
// it: its recorded limit plus the fullest usage window the CLI last reported.
func Candidates(db *store.DB, targetID int64, agent string) []accounts.Candidate {
	rows, err := db.AccountsFor(targetID, agent)
	if err != nil {
		return nil
	}
	out := make([]accounts.Candidate, 0, len(rows))
	for _, a := range rows {
		c := accounts.Candidate{ID: a.ID, Label: a.Label, LimitedAt: fromUnix(a.LimitedAt), LimitedUntil: fromUnix(a.LimitedUntil)}
		if s, err := db.LatestAccountUsage(targetID, agent, a.ID, a.Default()); err == nil && s != nil {
			for _, w := range []struct {
				pct   *int
				reset *float64
			}{{s.Rate5hPct, s.Rate5hReset}, {s.Rate7dPct, s.Rate7dReset}} {
				if w.pct != nil && *w.pct >= c.UsagePct {
					c.UsagePct, c.UsageReset = *w.pct, fromUnix(w.reset)
				}
			}
		}
		out = append(out, c)
	}
	return out
}

// NextAccount picks where a limited session or attempt should move: the
// operator's choice when one is given (if it is usable), otherwise the next
// free account in rotation order.
func NextAccount(db *store.DB, targetID int64, agent string, current *store.Account, chosen int64, now time.Time) (*store.Account, time.Time, error) {
	if !accounts.Supported(agent) {
		return nil, time.Time{}, fmt.Errorf("%s has no account support", agent)
	}
	var currentID int64
	if current != nil {
		currentID = current.ID
	}
	cands := Candidates(db, targetID, agent)
	if chosen != 0 {
		for _, c := range cands {
			if c.ID == chosen && c.ID != currentID {
				a, err := db.Account(c.ID)
				return a, time.Time{}, err
			}
		}
		return nil, time.Time{}, fmt.Errorf("choose another account of this %s on this machine", agent)
	}
	next, soonest, ok := accounts.Next(cands, currentID, now)
	if !ok {
		return nil, soonest, ErrNoAccount
	}
	a, err := db.Account(next.ID)
	return a, time.Time{}, err
}

// SwapTo is the account a swap on this hold would move to right now, for
// the card's "Swap to …" button; nil when there is none.
func SwapTo(db *store.DB, h *store.LimitHold, targetID int64, accountID *int64) *store.Account {
	if h == nil || !accounts.Supported(h.Agent) {
		return nil
	}
	current := Account(db, targetID, h.Agent, accountID)
	a, _, err := NextAccount(db, targetID, h.Agent, current, 0, time.Now())
	if err != nil {
		return nil
	}
	return a
}

// AccountLabel names an account for a push; nil is the default login.
func AccountLabel(a *store.Account) string {
	if a == nil {
		return "the default login"
	}
	return a.Label
}

// AccountRef is an account's id, 0 for an unregistered default login.
func AccountRef(a *store.Account) int64 {
	if a == nil {
		return 0
	}
	return a.ID
}

// MarkLimited records the limit against the account the hold happened on.
func MarkLimited(db *store.DB, a *store.Account, h *store.LimitHold, now time.Time) {
	if a == nil {
		return
	}
	db.MarkAccountLimited(a.ID, unix(now), h.ResetAt)
}

// swapping reports whether this process has a swap in flight for a session.
func (t *Tracker) swapping(id int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.swaps[id]
}

// Wait blocks until every swap this tracker started has finished. Tests use it.
func (t *Tracker) Wait() { t.wg.Wait() }

// tickSwap is a waiting hold under the swap policy.
func (t *Tracker) tickSwap(ctx context.Context, h *store.LimitHold, sess *store.Session, p Policy) {
	if t.swapping(sess.ID) {
		return
	}
	now := t.now()
	obs, ok := t.pane(sess.ID)
	if !ok {
		return
	}
	_, limited := DetectTail(obs.text, TailLines, now)
	if !limited && obs.busy {
		t.resolve(h, StateWaiting, StateCleared, "the agent is working again", nil)
		return
	}
	current := Account(t.DB, sess.TargetID, sess.Agent, sess.AccountID)
	// The restart already happened (an interrupted swap, or a nudge that
	// did not land): what is left is to nudge the resumed agent.
	if h.AccountTo != nil && AccountRef(current) == *h.AccountTo && !limited {
		if h.Tries > t.timing().MaxTries {
			t.giveUp(h, sess)
			return
		}
		t.nudge(ctx, h, sess)
		return
	}
	if h.Tries >= t.timing().MaxTries {
		t.giveUp(h, sess)
		return
	}
	t.startSwap(h, sess, p, 0, true)
}

// startSwap moves a waiting session hold to another account, or to the
// policy's secondary mode when there is none (auto: the policy, not an
// operator, asked for it). chosen is an operator's pick.
func (t *Tracker) startSwap(h *store.LimitHold, sess *store.Session, p Policy, chosen int64, auto bool) error {
	now := t.now()
	current := Account(t.DB, sess.TargetID, sess.Agent, sess.AccountID)
	MarkLimited(t.DB, current, h, now)
	next, soonest, err := NextAccount(t.DB, sess.TargetID, sess.Agent, current, chosen, now)
	if err != nil {
		if auto {
			t.swapFallback(h, sess, p, err, soonest)
		}
		return err
	}
	from, to := AccountRef(current), next.ID
	won, err := t.DB.TransitionLimitHold(h.ID, StateWaiting, map[string]any{"state": StateSwapping,
		"policy": ModeSwap, "account_from": from, "account_to": to, "tries": h.Tries + 1,
		"note": "swapping to " + next.Label})
	if err != nil {
		return err
	}
	if !won {
		return ErrConflict
	}
	if t.Sessions == nil {
		return fmt.Errorf("sessions are not available")
	}
	t.log().Info("swapping account after usage limit", "session", sess.ID, "from", from, "to", to)
	t.mu.Lock()
	if t.swaps == nil {
		t.swaps = map[int64]bool{}
	}
	t.swaps[sess.ID] = true
	t.mu.Unlock()
	t.publishSession(sess.ID)
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		defer func() {
			t.mu.Lock()
			delete(t.swaps, sess.ID)
			t.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), swapTimeout)
		defer cancel()
		committed, err := t.Sessions.SwapAccount(ctx, sess.ID, to)
		t.finishSwap(ctx, h.ID, sess, current, next, p, committed, err)
	}()
	return nil
}

// finishSwap records how a swap ended. committed means the agent is running
// under the new account; only then is the nudge sent, and it is recorded
// first, exactly like a wait-and-resume nudge.
func (t *Tracker) finishSwap(ctx context.Context, holdID int64, sess *store.Session, from, to *store.Account, p Policy, committed bool, err error) {
	label := sessionLabel(sess)
	url := fmt.Sprintf("/session/%d", sess.ID)
	if !committed {
		reason := "the swap failed"
		if err != nil {
			reason = err.Error()
		}
		fields := map[string]any{"state": StateWaiting, "policy": p.Secondary(), "note": "account swap failed: " + reason}
		if p.Secondary() == ModeWait {
			h, _ := t.DB.LimitHold(holdID)
			if h != nil {
				fields["due_at"] = unix(t.timing().DueAt(fromUnix(h.ResetAt), 0, t.now(), t.Rand))
			}
		}
		if ok, _ := t.DB.TransitionLimitHold(holdID, StateSwapping, fields); ok {
			t.push("Account swap failed", label+": "+reason, url, "limit", sess.ID, holdID)
			t.publishSession(sess.ID)
		}
		return
	}
	if errors.Is(err, ErrAgentExited) {
		// Nothing is running to nudge; the card offers Revive.
		if ok, _ := t.DB.TransitionLimitHold(holdID, StateSwapping, map[string]any{"state": StateWaiting,
			"policy": ModeNotify, "note": "account swap failed: " + err.Error()}); ok {
			t.push("Account swap failed", label+": "+err.Error(), url, "limit", sess.ID, holdID)
			t.publishSession(sess.ID)
		}
		return
	}
	t.push("Swapped account", fmt.Sprintf("%s hit its usage limit on %s — continuing on %s", label,
		AccountLabel(from), AccountLabel(to)), url, "limit_swapped", sess.ID, holdID)
	if err != nil {
		// Running under the new account, but it never showed a prompt to
		// type into. Leave the nudge to the next tick.
		t.DB.TransitionLimitHold(holdID, StateSwapping, map[string]any{"state": StateWaiting,
			"due_at": unix(t.now()), "note": "swapped to " + to.Label + "; " + err.Error()})
		t.publishSession(sess.ID)
		return
	}
	now := t.now()
	won, _ := t.DB.TransitionLimitHold(holdID, StateSwapping, map[string]any{"state": StateResuming,
		"nudged_at": unix(now), "note": "swapped to " + to.Label})
	if !won || t.Sessions == nil {
		return
	}
	if err := t.Sessions.SendNudge(ctx, sess.ID, NudgeText); err != nil {
		t.DB.TransitionLimitHold(holdID, StateResuming, map[string]any{"state": StateWaiting,
			"due_at": unix(now), "note": "could not send the nudge: " + err.Error()})
	}
	t.publishSession(sess.ID)
}

// swapFallback applies the policy's secondary mode when no account is free.
func (t *Tracker) swapFallback(h *store.LimitHold, sess *store.Session, p Policy, cause error, soonest time.Time) {
	mode := p.Secondary()
	fields := map[string]any{"policy": mode, "note": cause.Error()}
	if mode == ModeWait {
		fields["due_at"] = unix(t.timing().DueAt(fromUnix(h.ResetAt), h.Tries, t.now(), t.Rand))
	}
	if ok, _ := t.DB.TransitionLimitHold(h.ID, StateWaiting, fields); !ok {
		return
	}
	body := sessionLabel(sess) + ": every account of " + sess.Agent + " is limited"
	if !soonest.IsZero() {
		body += " (the next one frees up " + FormatReset(soonest, t.now()) + ")"
	}
	switch mode {
	case ModeWait:
		body += " — Lectern will resume it at the reset"
	case ModeHandoff:
		body += " — handing off to " + p.Fallback()
	}
	t.push("No account to swap to", body, fmt.Sprintf("/session/%d", sess.ID), "limit", sess.ID, h.ID)
	t.publishSession(sess.ID)
}

// recoverSwap handles a hold left in "swapping" by a process that is gone
// (a restart mid-swap). If the session already runs under the new account the
// restart happened after the commit: only the nudge is left. Otherwise the
// agent was never restarted under it, and the swap can be tried again.
func (t *Tracker) recoverSwap(h *store.LimitHold, sess *store.Session) {
	if t.swapping(sess.ID) {
		return
	}
	// updated_at is written with the wall clock, so it is compared with it.
	if store.Now()-h.UpdatedAt < 30 {
		return
	}
	current := Account(t.DB, sess.TargetID, sess.Agent, sess.AccountID)
	note := "the account swap was interrupted before the restart; trying again"
	if h.AccountTo != nil && AccountRef(current) == *h.AccountTo {
		note = "the account swap was interrupted after the restart; resuming"
	}
	if ok, _ := t.DB.TransitionLimitHold(h.ID, StateSwapping, map[string]any{"state": StateWaiting,
		"due_at": unix(t.now()), "note": note}); ok {
		t.log().Info("recovered an interrupted account swap", "session", sess.ID, "note", note)
		t.publishSession(sess.ID)
	}
}
