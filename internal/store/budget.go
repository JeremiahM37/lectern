package store

import (
	"sort"
	"time"
)

// SpendSince sums cost across usage_daily (interactive sessions) and finished
// task attempts from startUnix (inclusive) through now, optionally narrowed
// to one agent ("" = every agent). The session half is day-granular — like
// GET /api/usage before it, usage_daily has no per-row timestamp, only a
// date bucket, so a startUnix that falls mid-day is rounded down to that
// day's start.
func (db *DB) SpendSince(startUnix float64, agent string) (float64, error) {
	startDate := time.Unix(int64(startUnix), 0).UTC().Format("2006-01-02")
	var total float64

	sessQ := `SELECT COALESCE(SUM(cost_usd),0) FROM usage_daily WHERE session_id IS NOT NULL AND date >= ?`
	args := []any{startDate}
	if agent != "" {
		sessQ += " AND agent = ?"
		args = append(args, agent)
	}
	var sessTotal float64
	if err := db.QueryRow(sessQ, args...).Scan(&sessTotal); err != nil {
		return 0, err
	}
	total += sessTotal

	taskQ := `SELECT a.result_json FROM attempts a JOIN tasks t ON t.id = a.task_id
		WHERE a.finished_at IS NOT NULL AND a.finished_at >= ? AND a.result_json != '{}'`
	targs := []any{startUnix}
	if agent != "" {
		taskQ += " AND t.agent = ?"
		targs = append(targs, agent)
	}
	rows, err := db.Query(taskQ, targs...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var resultJSON string
		if rows.Scan(&resultJSON) != nil {
			continue
		}
		cost, _ := UnjObj(resultJSON)["cost_usd"].(float64)
		total += cost
	}
	return total, rows.Err()
}

// SpendByAgent buckets the same two sources (usage_daily sessions, finished
// task attempts) by agent, for per-agent budgets and the Usage page's bars.
func (db *DB) SpendByAgent(startUnix float64) (map[string]float64, error) {
	startDate := time.Unix(int64(startUnix), 0).UTC().Format("2006-01-02")
	out := map[string]float64{}

	rows, err := db.Query(`SELECT agent, COALESCE(SUM(cost_usd),0) FROM usage_daily
		WHERE session_id IS NOT NULL AND date >= ? GROUP BY agent`, startDate)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var agent string
		var cost float64
		if rows.Scan(&agent, &cost) == nil {
			out[agent] += cost
		}
	}
	rows.Close()

	rows, err = db.Query(`SELECT t.agent, a.result_json FROM attempts a JOIN tasks t ON t.id = a.task_id
		WHERE a.finished_at IS NOT NULL AND a.finished_at >= ? AND a.result_json != '{}'`, startUnix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var agent, resultJSON string
		if rows.Scan(&agent, &resultJSON) != nil {
			continue
		}
		cost, _ := UnjObj(resultJSON)["cost_usd"].(float64)
		out[agent] += cost
	}
	return out, rows.Err()
}

// MarkBudgetAlertSent records scope/period/threshold as notified, returning
// true only when THIS call is the one that recorded it — the UNIQUE index
// backing it is the actual dedup mechanism, so two concurrent callers (or a
// restart mid-period re-running the same check) can never double-send.
func (db *DB) MarkBudgetAlertSent(scopeKey, periodKey string, threshold int) (bool, error) {
	res, err := db.Exec(`INSERT OR IGNORE INTO budget_alerts_sent(scope_key, period_key, threshold, sent_at)
		VALUES(?,?,?,?)`, scopeKey, periodKey, threshold, Now())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// PruneBudgetAlerts drops dedup rows older than the given cutoff — they exist
// only to block a resend within their own now-passed period, so nothing reads
// them again once that period is long gone.
func (db *DB) PruneBudgetAlerts(beforeUnix float64) error {
	_, err := db.Exec(`DELETE FROM budget_alerts_sent WHERE sent_at < ?`, beforeUnix)
	return err
}

// HourlyRateSample is one finished unit of work's average cost per hour of
// wall time it ran — the raw material for a cost-anomaly baseline.
type HourlyRateSample struct {
	Kind string // "session" | "task"
	ID   int64
	Rate float64 // USD per hour
}

// FinishedHourlyRates collects a per-hour spend rate for every session and
// task attempt that finished within [startUnix, endUnix) and ran at least
// minSeconds — too short a run makes an hourly rate meaningless (a five
// second attempt that cost $0.01 is not "$7.20/hour").
func (db *DB) FinishedHourlyRates(startUnix, endUnix float64, minSeconds float64) ([]HourlyRateSample, error) {
	var out []HourlyRateSample

	rows, err := db.Query(`SELECT id, created_at, ended_at, cost_usd FROM sessions
		WHERE ended_at IS NOT NULL AND ended_at >= ? AND ended_at < ? AND cost_usd IS NOT NULL AND cost_usd > 0
		AND created_at IS NOT NULL`, startUnix, endUnix)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var createdAt, endedAt float64
		var cost float64
		if rows.Scan(&id, &createdAt, &endedAt, &cost) != nil {
			continue
		}
		secs := endedAt - createdAt
		if secs < minSeconds {
			continue
		}
		out = append(out, HourlyRateSample{Kind: "session", ID: id, Rate: cost / (secs / 3600)})
	}
	rows.Close()

	rows, err = db.Query(`SELECT id, started_at, finished_at, result_json FROM attempts
		WHERE finished_at IS NOT NULL AND finished_at >= ? AND finished_at < ? AND started_at IS NOT NULL
		AND result_json != '{}'`, startUnix, endUnix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var startedAt, finishedAt float64
		var resultJSON string
		if rows.Scan(&id, &startedAt, &finishedAt, &resultJSON) != nil {
			continue
		}
		cost, _ := UnjObj(resultJSON)["cost_usd"].(float64)
		if cost <= 0 {
			continue
		}
		secs := finishedAt - startedAt
		if secs < minSeconds {
			continue
		}
		out = append(out, HourlyRateSample{Kind: "task", ID: id, Rate: cost / (secs / 3600)})
	}
	return out, rows.Err()
}

// Median returns the middle value of a sorted copy of rates, 0 for an empty
// slice.
func Median(rates []float64) float64 {
	if len(rates) == 0 {
		return 0
	}
	cp := append([]float64(nil), rates...)
	sort.Float64s(cp)
	mid := len(cp) / 2
	if len(cp)%2 == 1 {
		return cp[mid]
	}
	return (cp[mid-1] + cp[mid]) / 2
}

// ActiveRate is one currently-running session or task attempt's live spend
// rate — this run's own cost divided by its own elapsed hours so far.
type ActiveRate struct {
	Kind    string
	ID      int64
	Name    string
	Elapsed float64 // seconds
	Rate    float64 // USD per hour
}

// ActiveHourlyRates computes a live $/hour for every live session and
// running task attempt that has spent something and run at least minSeconds
// — the anomaly check's other half (FinishedHourlyRates supplies the
// baseline this compares against).
func (db *DB) ActiveHourlyRates(now, minSeconds float64) ([]ActiveRate, error) {
	var out []ActiveRate

	rows, err := db.Query(`SELECT id, name, created_at, cost_usd FROM sessions
		WHERE ended_at IS NULL AND cost_usd IS NOT NULL AND cost_usd > 0 AND created_at IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var name string
		var createdAt, cost float64
		if rows.Scan(&id, &name, &createdAt, &cost) != nil {
			continue
		}
		secs := now - createdAt
		if secs < minSeconds {
			continue
		}
		out = append(out, ActiveRate{Kind: "session", ID: id, Name: name, Elapsed: secs, Rate: cost / (secs / 3600)})
	}
	rows.Close()

	rows, err = db.Query(`SELECT a.id, t.title, a.started_at, a.live_cost_usd FROM attempts a
		JOIN tasks t ON t.id = a.task_id
		WHERE a.status = 'running' AND a.started_at IS NOT NULL AND a.live_cost_usd > 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var title string
		var startedAt, cost float64
		if rows.Scan(&id, &title, &startedAt, &cost) != nil {
			continue
		}
		secs := now - startedAt
		if secs < minSeconds {
			continue
		}
		out = append(out, ActiveRate{Kind: "task", ID: id, Name: title, Elapsed: secs, Rate: cost / (secs / 3600)})
	}
	return out, rows.Err()
}
