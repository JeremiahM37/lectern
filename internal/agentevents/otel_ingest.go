package agentevents

import (
	"fmt"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// IngestOTelMetrics applies one Claude Code OTLP/HTTP JSON metrics export to
// a session (see docs/outcomes.md). Like IngestStatusline, a malformed body
// degrades to "touch hook_seen_at" rather than erroring: the OTel SDK's own
// exporter treats a non-2xx as a delivery failure and retries/backs off,
// which is worse than silently accepting a body lectern could not parse.
//
// cost.usage/lines_of_code.count are genuinely cumulative for the session's
// whole process lifetime (same shape as Claude's statusline cost.total_cost_usd
// — see IngestStatusline's comment), so the delta math is the same: diff
// against the session's own previously-stored value. Once this ever succeeds
// for a session, otel_active_at is set and stays set — see IngestStatusline's
// own guard for why that matters.
func (in *Ingester) IngestOTelMetrics(s *store.Session, body []byte) error {
	now := store.Now()
	m, err := ParseOTLPMetrics(body)
	if err != nil || m == nil {
		return in.DB.Update("sessions", s.ID, map[string]any{"hook_seen_at": now, "updated_at": now})
	}

	costDelta := deltaFloat(s.CostUSD, m.CostUSD)
	bookedInput, bookedOutput, err := in.DB.UsageDailySessionTotals(s.ID)
	if err != nil {
		return err
	}
	inputDelta := deltaInt(&bookedInput, int(m.InputTokens))
	outputDelta := deltaInt(&bookedOutput, int(m.OutputTokens))

	model := m.Model
	fields := map[string]any{
		"hook_seen_at":   now,
		"updated_at":     now,
		"usage_at":       now,
		"otel_active_at": now,
		"cost_usd":       m.CostUSD,
		"lines_added":    m.LinesAdded,
		"lines_removed":  m.LinesRemoved,
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

	fresh, ferr := in.DB.Session(s.ID)
	if ferr != nil {
		return nil
	}
	in.publishSession(fresh)
	usagePayload := map[string]any{
		"id": s.ID, "model": model, "cost_usd": m.CostUSD,
		"lines_added": m.LinesAdded, "lines_removed": m.LinesRemoved,
		"source": "otel", "at": now,
	}
	in.Bus.Publish("board", "session.usage", usagePayload)
	in.Bus.Publish(fmt.Sprintf("session:%d", s.ID), "session.usage", usagePayload)
	return nil
}

// IngestOTelLogs applies one Claude Code OTLP/HTTP JSON logs export to a
// session. Today this only marks the session OTel-active (a claude_code.
// api_request event proves the exporter is live even before the next 60s
// metrics tick) and, when a log record carried its own cost/token figures
// (see OTelAPIRequest's doc comment), books them the same delta way
// IngestOTelMetrics does for the periodic metric — never both for the same
// dollar, since api_request cost is additive per discrete request while
// cost.usage is cumulative, and the two are reconciled the same way any two
// deltas from different sources would be: whichever arrives first for a
// given slice of spend books it, and the other's next cumulative reading
// simply has a smaller delta left over.
func (in *Ingester) IngestOTelLogs(s *store.Session, body []byte) error {
	now := store.Now()
	l, err := ParseOTLPLogs(body)
	if err != nil || l == nil {
		return in.DB.Update("sessions", s.ID, map[string]any{"hook_seen_at": now, "updated_at": now})
	}
	fields := map[string]any{"hook_seen_at": now, "updated_at": now, "otel_active_at": now}
	if err := in.DB.Update("sessions", s.ID, fields); err != nil {
		return err
	}
	if l.CostUSD == 0 && l.InputTokens == 0 && l.OutputTokens == 0 {
		return nil
	}
	date := time.Now().UTC().Format("2006-01-02")
	return in.DB.UpsertUsageDelta(date, s.ID, s.Agent, l.Model, l.CostUSD, int(l.InputTokens), int(l.OutputTokens))
}

// IngestOTelMetricsForAttempt applies one OTLP/HTTP JSON metrics export to a
// headless task attempt. Unlike a session, an attempt's process is one-shot
// and short-lived, so there is no baseline to diff against: the freshest
// cumulative reading simply replaces whatever was stored before (see
// store.UpsertOtelAttemptUsage's own doc comment).
func IngestOTelMetricsForAttempt(db *store.DB, attemptID int64, body []byte) error {
	m, err := ParseOTLPMetrics(body)
	if err != nil || m == nil {
		return nil // best-effort, same leniency as the session path
	}
	return db.UpsertOtelAttemptUsage(attemptID, m.Model, m.CostUSD, m.InputTokens, m.OutputTokens,
		m.LinesAdded, m.LinesRemoved, m.PullRequests, m.Commits, store.Now())
}

// IngestOTelLogsForAttempt applies one OTLP/HTTP JSON logs export to a
// headless task attempt. api_request events are additive per request, so
// unlike the metrics path this ADDS onto whatever cost/tokens are already
// recorded for the attempt rather than replacing them — the two sources
// still can't double count each other for the reason IngestOTelLogs
// documents, and a metrics export that lands afterwards simply reports the
// same cumulative total these adds summed to.
func IngestOTelLogsForAttempt(db *store.DB, attemptID int64, body []byte) error {
	l, err := ParseOTLPLogs(body)
	if err != nil || l == nil || l.APIRequests == 0 {
		return nil
	}
	existing, err := db.OtelAttemptUsageRow(attemptID)
	if err != nil {
		return err
	}
	model, cost, in, out := l.Model, l.CostUSD, l.InputTokens, l.OutputTokens
	linesAdded, linesRemoved, prs, commits := int64(0), int64(0), int64(0), int64(0)
	if existing != nil {
		if model == "" {
			model = existing.Model
		}
		cost += existing.CostUSD
		in += existing.InputTokens
		out += existing.OutputTokens
		linesAdded, linesRemoved, prs, commits = existing.LinesAdded, existing.LinesRemoved, existing.PullRequests, existing.Commits
	}
	return db.UpsertOtelAttemptUsage(attemptID, model, cost, in, out, linesAdded, linesRemoved, prs, commits, store.Now())
}
