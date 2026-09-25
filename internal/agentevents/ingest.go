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
	// OnHookEvent, when set, is called synchronously at the end of every
	// IngestEvent — success, no-op (an event that carries no state, like
	// PreCompact), or an unchanged state — with the raw event name and
	// body. This is how internal/alerts (docs/agent-events.md section 3)
	// observes things that never reach the bus as structured session
	// fields: a PreCompact hook's "trigger" and a Stop hook's
	// "last_assistant_message". Kept fast: it must never block or slow a
	// hook response, so a subscriber only ever queues work here.
	OnHookEvent func(sess *store.Session, event string, body []byte, state string, changed bool)
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

// codexNotifyProbe pulls the thread id out of an AgentTurnComplete body
// (internal/agentevents/codex_settings.go's notify script wraps codex's
// notify payload under "codex_notify"; the field name is "thread-id",
// hyphenated, exactly as codex's own binary strings list it). This is what
// lets the codex rollout reader (codex_rollout.go) find this session's exact
// ~/.codex/sessions/*/*/*/rollout-*-<id>.jsonl file without guessing from
// cwd or start time.
type codexNotifyProbe struct {
	CodexNotify struct {
		ThreadID string `json:"thread-id"`
	} `json:"codex_notify"`
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
	if in.OnHookEvent != nil {
		// Named returns: this sees whatever state/changed end up being at
		// whichever return statement below actually runs. Skipped on a DB
		// error — nothing here was durably recorded, so there is nothing
		// honest to alert on.
		defer func() {
			if err == nil {
				in.OnHookEvent(s, event, body, state, changed)
			}
		}()
	}
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
	if event == EventPreCompact {
		fields["precompact_at"] = now
	}
	if event == EventAgentTurnComplete && len(body) > 0 {
		var np codexNotifyProbe
		if err := json.Unmarshal(body, &np); err == nil && np.CodexNotify.ThreadID != s.CodexThreadID &&
			ValidCodexThreadID(np.CodexNotify.ThreadID) {
			fields["codex_thread_id"] = np.CodexNotify.ThreadID
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
	// Precedence (docs/outcomes.md): once this session's OTLP exporter has
	// reported in even once, otel_active_at is set and stays set — from then
	// on OTel owns cost_usd/lines_added/lines_removed/model and every
	// usage_daily booking for this session, so the statusline tick that still
	// arrives every render must not also book its own (necessarily less
	// exact) numbers on top. It keeps updating everything OTel does not
	// cover — context window and account-wide rate limits — unconditionally.
	otelActive := s.OtelActiveAt != nil

	var costDelta float64
	var inputDelta, outputDelta int
	if !otelActive {
		costDelta = deltaFloat(s.CostUSD, p.Cost.TotalCostUSD)
		bookedInput, bookedOutput, err := in.DB.UsageDailySessionTotals(s.ID)
		if err != nil {
			return err
		}
		inputDelta = deltaInt(&bookedInput, p.ContextWindow.TotalInputTokens)
		outputDelta = deltaInt(&bookedOutput, p.ContextWindow.TotalOutputTokens)
	}

	model := p.Model.ID
	fields := map[string]any{
		"hook_seen_at":     now,
		"updated_at":       now,
		"usage_at":         now,
		"context_used_pct": p.ContextWindow.UsedPercentage,
		"context_tokens":   p.ContextWindow.TotalInputTokens,
		"context_size":     p.ContextWindow.ContextWindowSize,
		"rate_5h_pct":      p.RateLimits.FiveHour.UsedPercentage,
		"rate_5h_reset":    p.RateLimits.FiveHour.ResetsAt,
		"rate_7d_pct":      p.RateLimits.SevenDay.UsedPercentage,
		"rate_7d_reset":    p.RateLimits.SevenDay.ResetsAt,
	}
	if !otelActive {
		fields["cost_usd"] = p.Cost.TotalCostUSD
		fields["lines_added"] = p.Cost.TotalLinesAdded
		fields["lines_removed"] = p.Cost.TotalLinesRemoved
		if model != "" {
			fields["model"] = model
		}
	}
	if err := in.DB.Update("sessions", s.ID, fields); err != nil {
		return err
	}

	// Account-wide rate limits (Claude's are per-account, not per-session):
	// keep the latest snapshot in settings so a surface with no single
	// session in view can still show it. Unconditional — OTel carries no
	// equivalent of this, so the statusline stays the only source for it
	// even once a session's OTel precedence has taken over cost/tokens.
	rl, _ := json.Marshal(map[string]any{
		"five_hour": map[string]any{"used_percentage": p.RateLimits.FiveHour.UsedPercentage, "resets_at": p.RateLimits.FiveHour.ResetsAt},
		"seven_day": map[string]any{"used_percentage": p.RateLimits.SevenDay.UsedPercentage, "resets_at": p.RateLimits.SevenDay.ResetsAt},
		"at":        now,
	})
	_ = in.DB.SetSetting("rate_limits", string(rl))

	if otelActive {
		fresh, ferr := in.DB.Session(s.ID)
		if ferr != nil {
			return nil
		}
		in.publishSession(fresh)
		return nil
	}

	date := time.Now().UTC().Format("2006-01-02")
	if err := in.DB.UpsertUsageDelta(date, s.ID, s.Agent, model, costDelta, inputDelta, outputDelta); err != nil {
		return err
	}

	fresh, ferr := in.DB.Session(s.ID)
	if ferr != nil {
		return nil
	}
	in.publishSession(fresh)
	usagePayload := map[string]any{
		"id": s.ID, "model": model, "context_used_pct": p.ContextWindow.UsedPercentage,
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

// IngestCodexUsage applies one codex rollout usage reading (see
// codex_rollout.go) to a session — the codex equivalent of IngestStatusline.
// Codex's total_token_usage is genuinely cumulative for the whole thread
// (unlike Claude's confusingly-named total_input_tokens, which is a
// per-request footprint — see the comment on IngestStatusline), so the same
// "diff against whatever usage_daily already booked" delta math applies
// unchanged. There is no cost figure: docs/agent-events.md calls for tokens
// instead of a dollar amount for codex, so cost_usd is left untouched.
func (in *Ingester) IngestCodexUsage(s *store.Session, usage *CodexUsage) error {
	now := store.Now()
	bookedInput, bookedOutput, err := in.DB.UsageDailySessionTotals(s.ID)
	if err != nil {
		return err
	}
	inputDelta := deltaInt(&bookedInput, usage.InputTokens)
	outputDelta := deltaInt(&bookedOutput, usage.OutputTokens)

	fields := map[string]any{
		"hook_seen_at":     now,
		"updated_at":       now,
		"usage_at":         now,
		"context_used_pct": usage.ContextPct,
		"context_tokens":   usage.ContextTokens,
		"context_size":     usage.ContextSize,
	}
	if usage.Model != "" {
		fields["model"] = usage.Model
	}
	if err := in.DB.Update("sessions", s.ID, fields); err != nil {
		return err
	}

	date := time.Now().UTC().Format("2006-01-02")
	if err := in.DB.UpsertUsageDelta(date, s.ID, s.Agent, usage.Model, 0, inputDelta, outputDelta); err != nil {
		return err
	}

	fresh, ferr := in.DB.Session(s.ID)
	if ferr != nil {
		return nil
	}
	in.publishSession(fresh)
	payload := map[string]any{
		"id": s.ID, "model": usage.Model, "context_used_pct": usage.ContextPct,
		"context_tokens": usage.ContextTokens, "context_size": usage.ContextSize, "at": now,
	}
	in.Bus.Publish("board", "session.usage", payload)
	in.Bus.Publish(fmt.Sprintf("session:%d", s.ID), "session.usage", payload)
	return nil
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
