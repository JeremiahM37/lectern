package store

// UpsertUsageDelta accumulates one incremental slice of usage into today's
// usage_daily bucket for a session. Callers pass DELTAS, already computed
// against whatever baseline they track (see internal/agentevents), never the
// agent's raw cumulative totals — this function has no way to tell a repeat
// call from a genuinely new one, so double-counting is the caller's to avoid.
// A no-op delta is skipped so an idle session's repeated statusline pings
// don't grow the table for nothing.
func (db *DB) UpsertUsageDelta(date string, sessionID int64, agent, model string, costUSD float64, inputTokens, outputTokens int) error {
	if costUSD == 0 && inputTokens == 0 && outputTokens == 0 {
		return nil
	}
	_, err := db.Exec(`INSERT INTO usage_daily(date, session_id, agent, model, cost_usd, input_tokens, output_tokens)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(date, session_id, agent, model) WHERE session_id IS NOT NULL DO UPDATE SET
			cost_usd = cost_usd + excluded.cost_usd,
			input_tokens = input_tokens + excluded.input_tokens,
			output_tokens = output_tokens + excluded.output_tokens`,
		date, sessionID, agent, model, costUSD, inputTokens, outputTokens)
	return err
}

// UsageDailySessionTotals sums every usage_daily row ever booked for a
// session (across all dates), so a delta source with no cumulative field of
// its own — Claude's statusline reports request-footprint, not a running
// total; see agentevents.IngestStatusline — has a persistent baseline to
// diff against without needing its own tracking column.
func (db *DB) UsageDailySessionTotals(sessionID int64) (inputTokens, outputTokens int, err error) {
	err = db.QueryRow(`SELECT COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0)
		FROM usage_daily WHERE session_id=?`, sessionID).Scan(&inputTokens, &outputTokens)
	return
}
