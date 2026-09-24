package agentevents

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// StopListener is how the session checks runner (internal/checks) learns
// that an agent turn ended, without this package needing to import checks —
// *checks.Runner satisfies this interface structurally, wired up in
// internal/app. See docs/agent-events.md section 4.
type StopListener interface {
	OnAgentStop(sessionID int64)
}

// Ingester turns hook payloads and statusline snapshots into session state
// and usage. It owns no HTTP: internal/api's hook handlers resolve the
// session and its token, then call in here.
type Ingester struct {
	DB  *store.DB
	Bus *bus.Bus
	// Stop, when set, is told about every Stop (or codex AgentTurnComplete)
	// hook that actually changes a session's state — the trigger a session's
	// check command runs on. Nil is a valid, checks-disabled configuration
	// (every test that builds an Ingester by hand predates this field).
	Stop StopListener
}

// New builds an Ingester.
func New(db *store.DB, b *bus.Bus) *Ingester { return &Ingester{DB: db, Bus: b} }

// NewHookToken generates a session's hook secret. Hex, not base64: it goes
// straight into a shell-quoted env value and a JSON settings file, and hex
// needs no quoting consideration either place.
func NewHookToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ValidToken reports whether supplied is this session's hook token, compared
// in constant time. An empty session token (one launched before this
// feature, or a "discovered" adopted session that was never handed one)
// never matches anything, including an empty supplied value.
func ValidToken(sessionToken, supplied string) bool {
	if sessionToken == "" || supplied == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(sessionToken), []byte(supplied)) == 1
}

// notificationProbe pulls just the one field IngestEvent needs out of a
// Claude Notification body. Decode failures are silent: an event this
// package does not understand about is still accepted (docs/agent-events.md:
// "Unknown events are accepted and ignored").
type notificationProbe struct {
	NotificationType string `json:"notification_type"`
}

// IngestEvent applies one hook event to a session and returns the state it
// landed in (which may be unchanged, or "" if the event carries no state).
//
// Extension point for a later worker: when event is EventPermissionRequest
// and the session's permission mode is "ask" (docs/agent-events.md section
// 3), this is where a broker.Create + hold-for-decision belongs, answering
// with the real allow/deny decision instead of the unconditional `{}` this
// pass returns. For now every PermissionRequest is recorded as
// waiting_permission and answered `{}` (Claude/Codex both then fall back to
// their own terminal approval prompt), so it is safe to ship ahead of that
// worker.
func (in *Ingester) IngestEvent(s *store.Session, event string, body []byte) (state string, changed bool, err error) {
	notificationType := ""
	if event == EventNotification && len(body) > 0 {
		var probe notificationProbe
		_ = json.Unmarshal(body, &probe)
		notificationType = probe.NotificationType
	}
	now := store.Now()
	newState, ok := MapEventState(event, notificationType)
	fields := map[string]any{"hook_seen_at": now, "updated_at": now}
	previous := s.AgentState
	if ok {
		fields["agent_state"] = newState
		fields["state_source"] = SourceHook
		fields["state_at"] = now
		if status, sok := StatusForState(newState); sok {
			fields["status"] = status
		}
	}
	if err := in.DB.Update("sessions", s.ID, fields); err != nil {
		return "", false, err
	}
	// Checks (docs/agent-events.md section 4): a Stop hook — or codex's
	// synthesized AgentTurnComplete equivalent, see EventAgentTurnComplete's
	// doc comment — is the "an agent stopped, there may be something new to
	// check" signal, independent of whether agent_state actually changed (a
	// session can Stop repeatedly in the same idle state, and each one is a
	// legitimate new turn). The runner's own fingerprint dedupes anything
	// where the worktree genuinely has not moved, so firing unconditionally
	// here is cheap and cannot double-run a real check.
	if in.Stop != nil && (event == EventStop || event == EventAgentTurnComplete) {
		in.Stop.OnAgentStop(s.ID)
	}
	if !ok {
		return "", false, nil
	}
	if newState == previous {
		return newState, false, nil
	}
	if fresh, ferr := in.DB.Session(s.ID); ferr == nil {
		in.publishSession(fresh)
	}
	payload := map[string]any{"id": s.ID, "state": newState, "previous": previous, "source": SourceHook, "at": now}
	in.Bus.Publish("board", "session.state", payload)
	in.Bus.Publish(fmt.Sprintf("session:%d", s.ID), "session.state", payload)
	return newState, true, nil
}

// IngestStatusline applies one Claude statusline snapshot: latest values onto
// the session row, and the DELTA since the session's previous snapshot into
// today's usage_daily bucket, so repeated statuslines (roughly one per
// render tick) never double-count the cumulative totals Claude reports.
//
// A malformed body degrades to "touch hook_seen_at and stop" rather than
// erroring — the statusline script's curl is fire-and-forget from the
// target's shell prompt, with nothing there to look at a non-2xx response.
func (in *Ingester) IngestStatusline(s *store.Session, body []byte) error {
	now := store.Now()
	p, err := ParseStatusline(body)
	if err != nil || p == nil {
		return in.DB.Update("sessions", s.ID, map[string]any{"hook_seen_at": now, "updated_at": now})
	}

	// cost.total_cost_usd genuinely is cumulative for the session, so the
	// session's own previously-stored cost_usd is the right delta baseline
	// (sessions.cost_usd exists precisely to be that "latest value" per
	// docs/agent-events.md's "usage_samples-free design"). context_window's
	// total_input/output_tokens are NOT cumulative (see statusline.go) — they
	// are this request's footprint, repeated on every statusline render tick
	// even when nothing changed. There is no cumulative-token field to store
	// on the session row for that reason; instead the delta baseline is
	// whatever usage_daily already has booked for this session, across every
	// day, so a rendered-but-unchanged tick contributes nothing and actual
	// growth in the live request's footprint is what gets booked. A negative
	// delta (a smaller request than last tick, e.g. after compaction, or a
	// resumed session whose counters start over) is dropped to zero rather
	// than subtracted — this package cannot tell "context shrank" from
	// "counter reset" and must not invent negative usage either way.
	costDelta := deltaFloat(s.CostUSD, p.Cost.TotalCostUSD)
	bookedInput, bookedOutput, err := in.DB.UsageDailySessionTotals(s.ID)
	if err != nil {
		return err
	}
	inputDelta := deltaInt(&bookedInput, p.ContextWindow.TotalInputTokens)
	outputDelta := deltaInt(&bookedOutput, p.ContextWindow.TotalOutputTokens)

	model := p.Model.ID
	fields := map[string]any{
		"hook_seen_at":   now,
		"updated_at":     now,
		"usage_at":       now,
		"context_pct":    p.ContextWindow.UsedPercentage,
		"context_tokens": p.ContextWindow.TotalInputTokens,
		"context_size":   p.ContextWindow.ContextWindowSize,
		"cost_usd":       p.Cost.TotalCostUSD,
		"lines_added":    p.Cost.TotalLinesAdded,
		"lines_removed":  p.Cost.TotalLinesRemoved,
		"rate_5h_pct":    p.RateLimits.FiveHour.UsedPercentage,
		"rate_5h_reset":  p.RateLimits.FiveHour.ResetsAt,
		"rate_7d_pct":    p.RateLimits.SevenDay.UsedPercentage,
		"rate_7d_reset":  p.RateLimits.SevenDay.ResetsAt,
	}
	if model != "" {
		fields["model"] = model
	}
	if err := in.DB.Update("sessions", s.ID, fields); err != nil {
		return err
	}

	date := time.Now().UTC().Format("2006-01-02")
	if err := in.DB.UpsertUsageDelta(date, s.ID, s.Agent, model, costDelta, inputDelta, outputDelta); err != nil {
		return err
	}

	// Account-wide rate limits (Claude's are per-account, not per-session):
	// keep the latest snapshot in settings so a surface with no single
	// session in view can still show it.
	rl, _ := json.Marshal(map[string]any{
		"five_hour": map[string]any{"used_percentage": p.RateLimits.FiveHour.UsedPercentage, "resets_at": p.RateLimits.FiveHour.ResetsAt},
		"seven_day": map[string]any{"used_percentage": p.RateLimits.SevenDay.UsedPercentage, "resets_at": p.RateLimits.SevenDay.ResetsAt},
		"at":        now,
	})
	_ = in.DB.SetSetting("rate_limits", string(rl))

	fresh, ferr := in.DB.Session(s.ID)
	if ferr != nil {
		return nil
	}
	in.publishSession(fresh)
	usagePayload := map[string]any{
		"id": s.ID, "model": model, "context_pct": p.ContextWindow.UsedPercentage,
		"context_tokens": p.ContextWindow.TotalInputTokens, "context_size": p.ContextWindow.ContextWindowSize,
		"cost_usd": p.Cost.TotalCostUSD, "lines_added": p.Cost.TotalLinesAdded, "lines_removed": p.Cost.TotalLinesRemoved,
		"rate_5h_pct": p.RateLimits.FiveHour.UsedPercentage, "rate_5h_reset": p.RateLimits.FiveHour.ResetsAt,
		"rate_7d_pct": p.RateLimits.SevenDay.UsedPercentage, "rate_7d_reset": p.RateLimits.SevenDay.ResetsAt,
		"at": now,
	}
	in.Bus.Publish("board", "session.usage", usagePayload)
	in.Bus.Publish(fmt.Sprintf("session:%d", s.ID), "session.usage", usagePayload)
	return nil
}

// publishSession re-emits the whole session row on the "session" event, in
// the same shape and on the same two channels internal/sessions.Manager's own
// publish uses — so the existing UI, which only knows that event, keeps
// working unchanged when a hook (rather than a screen poll) is what moved
// the row.
func (in *Ingester) publishSession(s *store.Session) {
	in.Bus.Publish("board", "session", s)
	in.Bus.Publish(fmt.Sprintf("session:%d", s.ID), "session", s)
}

func deltaFloat(oldValue *float64, newValue float64) float64 {
	old := 0.0
	if oldValue != nil {
		old = *oldValue
	}
	if d := newValue - old; d > 0 {
		return d
	}
	return 0
}

func deltaInt(oldValue *int, newValue int) int {
	old := 0
	if oldValue != nil {
		old = *oldValue
	}
	if d := newValue - old; d > 0 {
		return d
	}
	return 0
}
