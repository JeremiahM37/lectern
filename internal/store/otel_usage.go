package store

import (
	"database/sql"
	"errors"
)

// UpsertOtelAttemptUsage overwrites the latest OTLP metrics reading for one
// headless task attempt (see schema.go's otel_attempt_usage comment). Unlike
// UpsertUsageDelta this is NOT additive: a Claude Code OTLP export reports
// cumulative totals for the whole attempt process, and the attempt's process
// is short-lived and never resumed, so the newest export is simply the most
// accurate one — there is no baseline to diff against and nothing to double
// count.
func (db *DB) UpsertOtelAttemptUsage(attemptID int64, model string, costUSD float64,
	inputTokens, outputTokens, linesAdded, linesRemoved, pullRequests, commits int64, at float64) error {
	_, err := db.Exec(`INSERT INTO otel_attempt_usage(attempt_id, model, cost_usd,
			input_tokens, output_tokens, lines_added, lines_removed, pull_requests, commits, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(attempt_id) DO UPDATE SET
			model = excluded.model, cost_usd = excluded.cost_usd,
			input_tokens = excluded.input_tokens, output_tokens = excluded.output_tokens,
			lines_added = excluded.lines_added, lines_removed = excluded.lines_removed,
			pull_requests = excluded.pull_requests, commits = excluded.commits,
			updated_at = excluded.updated_at`,
		attemptID, model, costUSD, inputTokens, outputTokens, linesAdded, linesRemoved,
		pullRequests, commits, at)
	return err
}

// OtelAttemptUsage is one attempt's latest OTel reading.
type OtelAttemptUsage struct {
	AttemptID    int64
	Model        string
	CostUSD      float64
	InputTokens  int64
	OutputTokens int64
	LinesAdded   int64
	LinesRemoved int64
	PullRequests int64
	Commits      int64
	UpdatedAt    float64
}

// OtelAttemptUsageRow reads one attempt's OTel reading, if any — nil, nil
// when the attempt's agent never reported in over OTLP.
func (db *DB) OtelAttemptUsageRow(attemptID int64) (*OtelAttemptUsage, error) {
	var u OtelAttemptUsage
	err := db.QueryRow(`SELECT attempt_id, model, cost_usd, input_tokens, output_tokens,
			lines_added, lines_removed, pull_requests, commits, updated_at
		FROM otel_attempt_usage WHERE attempt_id = ?`, attemptID).Scan(
		&u.AttemptID, &u.Model, &u.CostUSD, &u.InputTokens, &u.OutputTokens,
		&u.LinesAdded, &u.LinesRemoved, &u.PullRequests, &u.Commits, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// OtelAttemptUsageSince lists every attempt's OTel reading updated at or
// after cutoff — internal/outcomes' bulk read for Rebuild, one query instead
// of one per attempt.
func (db *DB) OtelAttemptUsageSince(cutoff float64) (map[int64]*OtelAttemptUsage, error) {
	rows, err := db.Query(`SELECT attempt_id, model, cost_usd, input_tokens, output_tokens,
			lines_added, lines_removed, pull_requests, commits, updated_at
		FROM otel_attempt_usage WHERE updated_at >= ?`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]*OtelAttemptUsage{}
	for rows.Next() {
		var u OtelAttemptUsage
		if err := rows.Scan(&u.AttemptID, &u.Model, &u.CostUSD, &u.InputTokens, &u.OutputTokens,
			&u.LinesAdded, &u.LinesRemoved, &u.PullRequests, &u.Commits, &u.UpdatedAt); err != nil {
			continue
		}
		out[u.AttemptID] = &u
	}
	return out, rows.Err()
}
