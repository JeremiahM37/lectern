package limits

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/agentevents"
	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/sinks"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Hold states. waiting, resuming and handing_off are open; the rest are
// written together with resolved_at.
const (
	StateWaiting    = "waiting"
	StateResuming   = "resuming"
	StateHandingOff = "handing_off"

	StateResumed      = "resumed"
	StateCleared      = "cleared"
	StateHandedOff    = "handed_off"
	StateRequeued     = "requeued"
	StateRedispatched = "redispatched"
	StateGaveUp       = "gave_up"
	StateDismissed    = "dismissed"
)

// NudgeText is what Lectern types into a session once its limit has reset.
// NudgeMarker, a distinctive part of it that fits on one terminal row, is how
// the verifier finds the nudge in the pane and reads only what came after it.
const (
	NudgeText   = "Your usage limit has reset. Please continue where you left off."
	NudgeMarker = "continue where you left off"
)

// TailLines is how much of the bottom of a pane counts as "what the agent is
// showing now".
const TailLines = 15

// SessionDriver is the slice of the session manager the tracker drives.
type SessionDriver interface {
	// SendNudge types text into the session without Lectern's automatic
	// memory context, so the pane shows exactly the nudge.
	SendNudge(ctx context.Context, id int64, text string) error
	// StartLimitHandoff starts a successor on the fallback agent in the same
	// workspace, primed from what Lectern captured (the limited agent cannot
	// write a handoff of its own), and resolves the hold once it is running.
	StartLimitHandoff(id int64, agent, model string, profileID int64) error
	InFlight(id int64) bool
	HandoffError(id int64) string
	// SwapAccount restarts the session's agent under another account of the
	// same CLI, resuming the same conversation (docs/accounts.md), and waits
	// for its prompt. committed reports whether the agent now runs under the
	// new account, even when err says it never reached a prompt.
	SwapAccount(ctx context.Context, id, accountID int64) (committed bool, err error)
}

// TaskActor requeues or re-dispatches a limited task attempt (the scheduler).
type TaskActor interface {
	ApplyTaskLimit(ctx context.Context, h *store.LimitHold, mode string, p Policy) error
}

// Tracker owns every limit hold's lifecycle. All state lives in limit_holds;
// the only memory it keeps is the most recent pane of each session, which the
// next poll refreshes.
type Tracker struct {
	DB       *store.DB
	Bus      *bus.Bus
	Notifier *sinks.Notifier
	Log      *slog.Logger
	Sessions SessionDriver
	Tasks    TaskActor
	Timing   Timing
	// Clock and Rand are test seams; nil means time.Now and math/rand.
	Clock func() time.Time
	Rand  func() float64

	mu    sync.Mutex
	panes map[int64]paneObs
	swaps map[int64]bool // sessions with a swap running in this process
	wg    sync.WaitGroup
}

type paneObs struct {
	text string
	busy bool
	at   time.Time
}

// New builds a tracker with the production timing.
func New(db *store.DB, b *bus.Bus, n *sinks.Notifier, log *slog.Logger) *Tracker {
	return &Tracker{DB: db, Bus: b, Notifier: n, Log: log, Timing: DefaultTiming}
}

func (t *Tracker) now() time.Time {
	if t.Clock != nil {
		return t.Clock()
	}
	return time.Now()
}

func (t *Tracker) timing() Timing {
	if t.Timing.MaxTries == 0 {
		return DefaultTiming
	}
	return t.Timing
}

func (t *Tracker) log() *slog.Logger {
	if t.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return t.Log
}

func unix(v time.Time) float64 { return float64(v.UnixNano()) / 1e9 }

func fromUnix(v *float64) time.Time {
	if v == nil || *v <= 0 {
		return time.Time{}
	}
	return time.Unix(0, int64(*v*1e9))
}

func openHold(db *store.DB, sessionID int64) (*store.LimitHold, error) {
	h, err := db.OpenLimitHoldForSession(sessionID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return h, err
}

// ---- observations ------------------------------------------------------------

// ObservePane is called by the session poll with every capture. busy is the
// poll's own "the agent is working" reading of the same pane. It only reads
// and writes the database; anything that types into a session happens in
// Tick.
func (t *Tracker) ObservePane(s *store.Session, pane string, busy bool) {
	if t == nil || s == nil || s.Agent == "shell" {
		return
	}
	now := t.now()
	t.mu.Lock()
	if t.panes == nil {
		t.panes = map[int64]paneObs{}
	}
	t.panes[s.ID] = paneObs{text: pane, busy: busy, at: now}
	t.mu.Unlock()

	h, err := openHold(t.DB, s.ID)
	if err != nil {
		return
	}
	if h == nil {
		// Only what came after Lectern's last nudge can be a new stop.
		visible := pane
		if after, found := afterNudge(pane); found {
			visible = after
		}
		hit, ok := DetectTail(visible, TailLines, now)
		// A limit line with the agent visibly working below it is history,
		// not a stop.
		if !ok || busy {
			return
		}
		// The message of a hold that was just resolved (resumed, handed off,
		// dismissed) can linger on screen; the same words are the same stop.
		// A new limit names a new reset, so its message differs.
		if last, err := t.DB.LatestLimitHoldForSession(s.ID); err == nil && last.Message == hit.Message {
			return
		}
		t.open(s, hit, "pane")
		return
	}
	switch h.State {
	case StateWaiting:
		hit, ok := DetectTail(pane, TailLines, now)
		if !ok && busy {
			// Someone typed into it, or the CLI resumed itself.
			t.resolve(h, StateWaiting, StateCleared, "the agent is working again", nil)
			return
		}
		if ok && h.ResetAt == nil && !hit.ResetAt.IsZero() {
			t.DB.TransitionLimitHold(h.ID, StateWaiting, map[string]any{"reset_at": unix(hit.ResetAt)})
		}
	case StateResuming:
		after, found := afterNudge(pane)
		if !found {
			return
		}
		if hit, ok := DetectLines(after, now); ok {
			t.stillLimited(h, hit)
		} else if busy {
			t.resumed(h, "the agent started working after the nudge")
		}
	}
}

// HandleHookEvent is chained onto agentevents.Ingester.OnHookEvent. Claude's
// StopFailure hook carries the API error ("rate_limit") and the message it
// showed; tool and Stop hooks after a nudge prove the agent really resumed.
func (t *Tracker) HandleHookEvent(s *store.Session, event string, body []byte, _ string, _ bool) {
	if t == nil || s == nil {
		return
	}
	now := t.now()
	switch event {
	case agentevents.EventStopFailure:
		var probe struct {
			Error        string `json:"error"`
			ErrorDetails any    `json:"error_details"`
			Last         string `json:"last_assistant_message"`
		}
		_ = json.Unmarshal(body, &probe)
		if probe.Error != "rate_limit" {
			return
		}
		hit, ok := DetectLines(probe.Last, now)
		if !ok {
			// "rate_limit" also covers a transient 429. Only a statusline
			// already at 100% makes an unrecognised message a usage limit.
			reset := statuslineReset(s, now)
			if reset.IsZero() {
				return
			}
			hit = Hit{Pattern: "claude-stop-failure", Agent: s.Agent, ResetAt: reset,
				Message: clip(firstNonEmpty(probe.Last, "Rate limited"), 300)}
		}
		h, err := openHold(t.DB, s.ID)
		if err != nil {
			return
		}
		if h == nil {
			t.open(s, hit, "hook")
		} else if h.State == StateResuming && h.NudgedAt != nil && unix(now) > *h.NudgedAt {
			t.stillLimited(h, hit)
		}
	case agentevents.EventPreToolUse, agentevents.EventPostToolUse, agentevents.EventStop:
		h, err := openHold(t.DB, s.ID)
		if err != nil || h == nil {
			return
		}
		switch {
		case h.State == StateResuming && h.NudgedAt != nil && unix(now) > *h.NudgedAt+1:
			t.resumed(h, "the agent ran a turn after the nudge ("+event+")")
		case h.State == StateWaiting && event != agentevents.EventStop && unix(now) > h.DetectedAt+5:
			// A tool call means the model answered: someone else got it going.
			t.resolve(h, StateWaiting, StateCleared, "the agent is working again", nil)
		}
	}
}

// statuslineReset is the reset of whichever Claude window the statusline
// shows at 100% (the later one if both are), or zero.
func statuslineReset(s *store.Session, now time.Time) time.Time {
	var reset time.Time
	for _, w := range []struct {
		pct   *int
		reset *float64
	}{{s.Rate5hPct, s.Rate5hReset}, {s.Rate7dPct, s.Rate7dReset}} {
		if w.pct == nil || *w.pct < 100 {
			continue
		}
		if r := fromUnix(w.reset); r.After(now) && r.After(reset) {
			reset = r
		}
	}
	return reset
}

// open records a new hold for a session and announces it.
func (t *Tracker) open(s *store.Session, hit Hit, source string) {
	now := t.now()
	p, _ := Effective(t.DB, s.ID, s.ProjectID)
	reset := hit.ResetAt
	if reset.IsZero() && s.Agent == "claude" {
		reset = statuslineReset(s, now)
	}
	h := &store.LimitHold{SessionID: &s.ID, Agent: s.Agent, Source: source, Pattern: hit.Pattern,
		Message: hit.Message, DetectedAt: unix(now), Policy: p.Mode, State: StateWaiting}
	if !reset.IsZero() {
		r := unix(reset)
		h.ResetAt = &r
	}
	if p.Mode == ModeWait {
		due := unix(t.timing().DueAt(reset, 0, now, t.Rand))
		h.DueAt = &due
	}
	fresh, created, err := t.DB.InsertLimitHold(h)
	if err != nil || !created {
		return
	}
	t.log().Info("usage limit detected", "session", s.ID, "agent", s.Agent, "pattern", hit.Pattern,
		"source", source, "reset", reset, "policy", p.Mode)
	title, body := announcement(sessionLabel(s), fresh, p)
	t.push(title, body, fmt.Sprintf("/session/%d", s.ID), "limit", s.ID, fresh.ID)
	t.publishSession(s.ID)
}

// RecordAttemptHit opens a hold for a headless task attempt. The scheduler
// decides what to do with it once the attempt exits (ApplyTaskLimit).
func RecordAttemptHit(db *store.DB, att *store.Attempt, task *store.Task, agent string, hit Hit, source string, now time.Time) (*store.LimitHold, bool) {
	p, _ := Effective(db, 0, &task.ProjectID)
	h := &store.LimitHold{TaskID: &task.ID, AttemptID: &att.ID, Agent: agent, Source: source,
		Pattern: hit.Pattern, Message: hit.Message, DetectedAt: unix(now), Policy: p.Mode, State: StateWaiting}
	if !hit.ResetAt.IsZero() {
		r := unix(hit.ResetAt)
		h.ResetAt = &r
	}
	fresh, created, err := db.InsertLimitHold(h)
	if err != nil {
		return nil, false
	}
	return fresh, created
}

// ---- the resume loop -----------------------------------------------------------

// Tick advances every open hold whose next step is due. It is wired onto the
// scheduler's tick, after the session poll.
func (t *Tracker) Tick(ctx context.Context) {
	if t == nil {
		return
	}
	holds, err := t.DB.OpenLimitHolds()
	if err != nil {
		return
	}
	for _, h := range holds {
		if ctx.Err() != nil {
			return
		}
		if h.SessionID != nil {
			t.tickSession(ctx, h)
		} else if h.AttemptID != nil {
			t.tickTask(h)
		}
	}
}

func (t *Tracker) tickSession(ctx context.Context, h *store.LimitHold) {
	now := t.now()
	sess, err := t.DB.Session(*h.SessionID)
	if err != nil || sess.EndedAt != nil {
		t.resolve(h, h.State, StateCleared, "the session ended", nil)
		return
	}
	tm := t.timing()
	switch h.State {
	case StateWaiting:
		switch h.Policy {
		case ModeHandoff:
			p, _ := Effective(t.DB, sess.ID, sess.ProjectID)
			if !p.HasFallback() {
				t.DB.TransitionLimitHold(h.ID, StateWaiting, map[string]any{"policy": ModeNotify,
					"note": "no fallback agent is configured"})
				return
			}
			t.startHandoff(h, sess, p)
		case ModeWait:
			due := fromUnix(h.DueAt)
			if due.IsZero() {
				d := unix(tm.DueAt(fromUnix(h.ResetAt), h.Tries, now, t.Rand))
				t.DB.TransitionLimitHold(h.ID, StateWaiting, map[string]any{"due_at": d})
				return
			}
			if now.Before(due) {
				return
			}
			obs, ok := t.pane(sess.ID)
			if !ok {
				return // no look at the pane yet since this process started
			}
			hit, limited := DetectTail(obs.text, TailLines, now)
			if !limited && obs.busy {
				t.resolve(h, StateWaiting, StateCleared, "the agent is working again", nil)
				return
			}
			// The CLI's own "continuing automatically" wait: give it the
			// settle window to continue by itself before typing into it.
			if limited && hit.SelfResume && now.Sub(due) < tm.Settle {
				return
			}
			t.nudge(ctx, h, sess)
		case ModeSwap:
			p, _ := Effective(t.DB, sess.ID, sess.ProjectID)
			t.tickSwap(ctx, h, sess, p)
		default:
			t.notifyReset(h, sessionLabel(sess), fmt.Sprintf("/session/%d", sess.ID), sess.ID, now)
		}
	case StateSwapping:
		t.recoverSwap(h, sess)
	case StateResuming:
		nudged := fromUnix(h.NudgedAt)
		if now.Sub(nudged) < tm.Settle {
			return
		}
		obs, ok := t.pane(sess.ID)
		if !ok || obs.at.Before(nudged) {
			return
		}
		after, found := afterNudge(obs.text)
		switch {
		case found:
			if hit, ok := DetectLines(after, now); ok {
				t.stillLimited(h, hit)
			} else {
				t.resumed(h, "no limit message followed the nudge")
			}
		case obs.busy:
			t.resumed(h, "the agent is working")
		default:
			// The nudge never reached the pane (a restart between recording
			// it and sending it, or a failed send). Retrying is not a double
			// resume: nothing was delivered the first time.
			if h.Tries >= tm.MaxTries {
				t.giveUp(h, sess)
				return
			}
			t.DB.TransitionLimitHold(h.ID, StateResuming, map[string]any{"state": StateWaiting,
				"due_at": unix(now), "note": "the nudge did not reach the session; retrying"})
		}
	case StateHandingOff:
		if t.Sessions != nil && t.Sessions.InFlight(sess.ID) {
			return
		}
		// updated_at is written with the wall clock, so it is compared with it.
		if store.Now()-h.UpdatedAt < 30 {
			return // the handoff is being started right now
		}
		if successor := t.successorSince(sess.ID, h.DetectedAt); successor != 0 {
			t.resolve(h, StateHandingOff, StateHandedOff, "", &successor)
			return
		}
		reason := "the handoff was interrupted"
		if t.Sessions != nil {
			if msg := t.Sessions.HandoffError(sess.ID); msg != "" {
				reason = msg
			}
		}
		if ok, _ := t.DB.TransitionLimitHold(h.ID, StateHandingOff, map[string]any{"state": StateWaiting,
			"policy": ModeNotify, "note": "handoff failed: " + reason}); ok {
			t.push("Handoff failed", fmt.Sprintf("%s is still stopped by its usage limit: %s", sessionLabel(sess), reason),
				fmt.Sprintf("/session/%d", sess.ID), "limit", sess.ID, h.ID)
			t.publishSession(sess.ID)
		}
	}
}

func (t *Tracker) tickTask(h *store.LimitHold) {
	if h.State != StateWaiting || h.TaskID == nil {
		return
	}
	task, err := t.DB.Task(*h.TaskID)
	if err != nil {
		t.resolve(h, h.State, StateCleared, "the task is gone", nil)
		return
	}
	t.notifyReset(h, fmt.Sprintf("Task %q", clip(task.Title, 60)), fmt.Sprintf("/#task/%d", task.ID), 0, t.now())
}

// notifyReset tells the operator once, for a hold left to them, that the
// window has reset and the work can be resumed.
func (t *Tracker) notifyReset(h *store.LimitHold, label, url string, sessionID int64, now time.Time) {
	reset := fromUnix(h.ResetAt)
	if h.Policy != ModeNotify || reset.IsZero() || now.Before(reset) || h.ResetNotifiedAt != nil {
		return
	}
	if ok, _ := t.DB.TransitionLimitHold(h.ID, StateWaiting, map[string]any{"reset_notified_at": unix(now)}); ok {
		t.push("Limit reset", label+"'s usage limit has reset — resume it or hand it off", url, "limit", sessionID, h.ID)
	}
}

func (t *Tracker) nudge(ctx context.Context, h *store.LimitHold, sess *store.Session) {
	now := t.now()
	// Record the nudge before sending it. Whichever process wins this swap is
	// the only one that types; a restart finds "resuming" and verifies
	// instead of nudging again.
	won, err := t.DB.TransitionLimitHold(h.ID, StateWaiting, map[string]any{"state": StateResuming,
		"nudged_at": unix(now), "tries": h.Tries + 1, "note": ""})
	if err != nil || !won || t.Sessions == nil {
		return
	}
	t.log().Info("resuming after usage limit", "session", sess.ID, "try", h.Tries+1)
	if err := t.Sessions.SendNudge(ctx, sess.ID, NudgeText); err != nil {
		due := unix(t.timing().DueAt(time.Time{}, h.Tries+1, now, t.Rand))
		t.DB.TransitionLimitHold(h.ID, StateResuming, map[string]any{"state": StateWaiting,
			"due_at": due, "note": "could not send the nudge: " + err.Error()})
	}
	t.publishSession(sess.ID)
}

// stillLimited handles a resume that ran into the limit again.
func (t *Tracker) stillLimited(h *store.LimitHold, hit Hit) {
	now := t.now()
	tm := t.timing()
	if h.Tries >= tm.MaxTries {
		if sess, err := t.DB.Session(*h.SessionID); err == nil {
			t.giveUp(h, sess)
		}
		return
	}
	fields := map[string]any{"state": StateWaiting, "message": hit.Message,
		"note": "still limited after the nudge"}
	reset := hit.ResetAt
	if !reset.IsZero() {
		fields["reset_at"] = unix(reset)
	}
	fields["due_at"] = unix(tm.DueAt(reset, h.Tries, now, t.Rand))
	if ok, _ := t.DB.TransitionLimitHold(h.ID, StateResuming, fields); ok {
		t.publishSession(*h.SessionID)
	}
}

func (t *Tracker) resumed(h *store.LimitHold, why string) {
	if !t.resolve(h, StateResuming, StateResumed, why, nil) {
		return
	}
	if h.AccountTo != nil {
		// The swap already told the operator where the work went; the new
		// account evidently works.
		t.DB.ClearAccountLimit(*h.AccountTo)
		return
	}
	if sess, err := t.DB.Session(*h.SessionID); err == nil {
		t.push("Resumed", sessionLabel(sess)+" is working again after its usage limit reset",
			fmt.Sprintf("/session/%d", sess.ID), "limit_resumed", sess.ID, h.ID)
	}
}

func (t *Tracker) giveUp(h *store.LimitHold, sess *store.Session) {
	if !t.resolve(h, h.State, StateGaveUp, fmt.Sprintf("still limited after %d tries", h.Tries), nil) {
		return
	}
	t.push("Could not resume", fmt.Sprintf("%s is still stopped by its usage limit after %d tries", sessionLabel(sess), h.Tries),
		fmt.Sprintf("/session/%d", sess.ID), "limit", sess.ID, h.ID)
}

func (t *Tracker) startHandoff(h *store.LimitHold, sess *store.Session, p Policy) error {
	won, err := t.DB.TransitionLimitHold(h.ID, StateWaiting, map[string]any{"state": StateHandingOff,
		"policy": ModeHandoff, "note": "handing off to " + p.Fallback()})
	if err != nil {
		return err
	}
	if !won {
		return fmt.Errorf("this limit is already being handled")
	}
	if t.Sessions == nil {
		return fmt.Errorf("sessions are not available")
	}
	t.log().Info("handing off after usage limit", "session", sess.ID, "to", p.Fallback())
	if err := t.Sessions.StartLimitHandoff(sess.ID, p.FallbackAgent, p.FallbackModel, p.FallbackProfileID); err != nil {
		t.DB.TransitionLimitHold(h.ID, StateHandingOff, map[string]any{"state": StateWaiting,
			"policy": ModeNotify, "note": "handoff failed: " + err.Error()})
		t.push("Handoff failed", sessionLabel(sess)+": "+err.Error(), fmt.Sprintf("/session/%d", sess.ID), "limit", sess.ID, h.ID)
		t.publishSession(sess.ID)
		return err
	}
	t.publishSession(sess.ID)
	return nil
}

// successorSince finds a session a handoff from sessionID started after a
// point in time, through the wrap it recorded.
func (t *Tracker) successorSince(sessionID int64, since float64) int64 {
	var id int64
	t.DB.QueryRow(`SELECT COALESCE(next_session_id, 0) FROM session_wraps
		WHERE session_id=? AND next_session_id IS NOT NULL AND created_at>=? ORDER BY id DESC LIMIT 1`,
		sessionID, since).Scan(&id)
	return id
}

// ---- choices -----------------------------------------------------------------

// Actions an operator can take on a hold from the card or the push.
const (
	ActionWait      = "wait"
	ActionResumeNow = "resume_now"
	ActionHandoff   = "handoff"
	ActionNotify    = "notify"
	ActionDismiss   = "dismiss"
)

// ErrConflict means the hold is no longer in a state the action applies to.
var ErrConflict = errors.New("this limit is already being handled")

// Choose applies an operator's choice. override, when non-nil, names the
// handoff destination; otherwise the effective policy's fallback is used.
func (t *Tracker) Choose(ctx context.Context, id int64, action string, override *Policy) (*store.LimitHold, error) {
	h, err := t.DB.LimitHold(id)
	if err != nil {
		return nil, err
	}
	if !h.Open() {
		return h, ErrConflict
	}
	now := t.now()
	var sess *store.Session
	var projectID *int64
	var sessionID int64
	if h.SessionID != nil {
		if sess, err = t.DB.Session(*h.SessionID); err != nil {
			return nil, err
		}
		projectID, sessionID = sess.ProjectID, sess.ID
	} else if h.TaskID != nil {
		task, err := t.DB.Task(*h.TaskID)
		if err != nil {
			return nil, err
		}
		projectID = &task.ProjectID
	}
	p, _ := Effective(t.DB, sessionID, projectID)
	if override != nil && override.HasFallback() {
		p.FallbackAgent, p.FallbackModel, p.FallbackProfileID = override.FallbackAgent, override.FallbackModel, override.FallbackProfileID
	}
	if override != nil {
		p.AccountID = override.AccountID
	}
	if action == ActionDismiss {
		if !t.resolve(h, h.State, StateDismissed, "dismissed", nil) {
			return h, ErrConflict
		}
		return t.DB.LimitHold(id)
	}
	if h.State != StateWaiting {
		return h, ErrConflict
	}
	if action == ActionHandoff && !p.HasFallback() {
		return h, fmt.Errorf("choose an agent to hand off to, or set a fallback in the limit policy")
	}
	if h.AttemptID != nil {
		mode := map[string]string{ActionWait: ModeWait, ActionResumeNow: ModeWait, ActionHandoff: ModeHandoff, ActionSwap: ModeSwap}[action]
		if mode == "" {
			t.DB.TransitionLimitHold(h.ID, StateWaiting, map[string]any{"policy": ModeNotify})
			return t.DB.LimitHold(id)
		}
		if t.Tasks == nil {
			return h, fmt.Errorf("the scheduler is not available")
		}
		if action == ActionResumeNow {
			// "Now" for a task means the requeued attempt is not held back.
			h.ResetAt = nil
		}
		if err := t.Tasks.ApplyTaskLimit(ctx, h, mode, p); err != nil {
			return h, err
		}
		return t.DB.LimitHold(id)
	}
	switch action {
	case ActionWait:
		due := unix(t.timing().DueAt(fromUnix(h.ResetAt), h.Tries, now, t.Rand))
		_, err = t.DB.TransitionLimitHold(h.ID, StateWaiting, map[string]any{"policy": ModeWait, "due_at": due})
	case ActionResumeNow:
		_, err = t.DB.TransitionLimitHold(h.ID, StateWaiting, map[string]any{"policy": ModeWait, "due_at": unix(now)})
	case ActionNotify:
		_, err = t.DB.TransitionLimitHold(h.ID, StateWaiting, map[string]any{"policy": ModeNotify, "due_at": nil})
	case ActionHandoff:
		err = t.startHandoff(h, sess, p)
	case ActionSwap:
		err = t.startSwap(h, sess, p, p.AccountID, false)
	default:
		return h, fmt.Errorf("action must be wait, resume_now, handoff, swap, notify or dismiss")
	}
	if err != nil {
		return h, err
	}
	t.publishSession(sess.ID)
	return t.DB.LimitHold(id)
}

// ---- helpers -----------------------------------------------------------------

func (t *Tracker) resolve(h *store.LimitHold, from, state, note string, successor *int64) bool {
	fields := map[string]any{"state": state, "resolved_at": store.Now()}
	if note != "" {
		fields["note"] = note
	}
	if successor != nil {
		fields["successor_id"] = *successor
	}
	ok, err := t.DB.TransitionLimitHold(h.ID, from, fields)
	if err != nil || !ok {
		return false
	}
	if h.SessionID != nil {
		t.log().Info("usage limit resolved", "session", *h.SessionID, "state", state, "note", note)
		t.publishSession(*h.SessionID)
	}
	return true
}

func (t *Tracker) pane(id int64) (paneObs, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	obs, ok := t.panes[id]
	return obs, ok
}

func (t *Tracker) publishSession(id int64) {
	if t.Bus == nil {
		return
	}
	if fresh, err := t.DB.Session(id); err == nil {
		t.Bus.Publish("board", "session", fresh)
		t.Bus.Publish(fmt.Sprintf("session:%d", id), "session", fresh)
	}
}

func (t *Tracker) push(title, body, url, kind string, sessionID, holdID int64) {
	if t.Notifier == nil {
		return
	}
	t.Notifier.Notify(title, body, url, &sinks.Extra{Kind: kind, SessionID: sessionID, LimitID: holdID})
}

// afterNudge returns the pane text after the last line carrying the nudge.
func afterNudge(pane string) (string, bool) {
	lines := strings.Split(pane, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], NudgeMarker) {
			return strings.Join(lines[i+1:], "\n"), true
		}
	}
	return "", false
}

func sessionLabel(s *store.Session) string {
	if s == nil || strings.TrimSpace(s.Name) == "" {
		return "A session"
	}
	return s.Name
}

// announcement is the push for a new hold.
func announcement(label string, h *store.LimitHold, p Policy) (string, string) {
	body := label + " hit its usage limit"
	if reset := fromUnix(h.ResetAt); !reset.IsZero() {
		body += " · resets " + FormatReset(reset, time.Now())
	}
	switch h.Policy {
	case ModeWait:
		body += " — Lectern will resume it then"
	case ModeHandoff:
		body += " — handing off to " + p.Fallback()
	case ModeSwap:
		body += " — moving it to another account"
	}
	return "Usage limit", body
}

// FormatReset renders a reset for a notification: a clock time today, a date
// otherwise, in the server's zone.
func FormatReset(reset, now time.Time) string {
	reset = reset.In(now.Location())
	if reset.YearDay() == now.YearDay() && reset.Year() == now.Year() {
		return reset.Format("3:04pm MST")
	}
	return reset.Format("Mon Jan 2 3:04pm MST")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
