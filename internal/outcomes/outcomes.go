package outcomes

import (
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Rebuild recomputes outcome_facts for every finished attempt and every
// checked session touched within the last `days` days, and upserts the
// result. It is a pure derivation over attempts/tasks/session_checks/
// eval_results/otel_attempt_usage — see schema.go's outcome_facts comment —
// so calling it twice in a row for the same window is idempotent and safe to
// do on every GET /api/outcomes request (homelab scale: hundreds to low
// thousands of rows, not the kind of table that needs a background job).
func Rebuild(db *store.DB, days int) error {
	if days <= 0 {
		days = 30
	}
	now := store.Now()
	cutoff := now - float64(days)*86400

	if err := rebuildAttempts(db, cutoff); err != nil {
		return err
	}
	return rebuildSessions(db, cutoff)
}

func rebuildAttempts(db *store.DB, cutoff float64) error {
	rows, err := db.Query(`SELECT a.id, a.task_id, a.n, a.agent, a.model, a.started_at,
			a.finished_at, a.result_json, a.verify_json, a.diff_stat_json,
			t.agent, t.model, t.project_id, t.status,
			(SELECT MAX(n) FROM attempts WHERE task_id = a.task_id)
		FROM attempts a JOIN tasks t ON t.id = a.task_id
		WHERE a.finished_at IS NOT NULL AND a.finished_at >= ?`, cutoff)
	if err != nil {
		return err
	}
	type attemptRow struct {
		id, taskID                 int64
		n                          int
		attemptAgent, attemptModel string
		startedAt, finishedAt      *float64
		resultJSON, verifyJSON     string
		diffStatJSON               string
		taskAgent, taskModel       string
		projectID                  int64
		taskStatus                 string
		maxN                       int
	}
	var attemptRows []attemptRow
	for rows.Next() {
		var r attemptRow
		if err := rows.Scan(&r.id, &r.taskID, &r.n, &r.attemptAgent, &r.attemptModel,
			&r.startedAt, &r.finishedAt, &r.resultJSON, &r.verifyJSON, &r.diffStatJSON,
			&r.taskAgent, &r.taskModel, &r.projectID, &r.taskStatus, &r.maxN); err != nil {
			rows.Close()
			return err
		}
		attemptRows = append(attemptRows, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// A cutoff of 0 loads the whole table: otel_attempt_usage only ever holds
	// one small row per attempt that has run OTel at all, so there is no
	// separate "since" filter worth adding on top of the outer attempts scan.
	otelByAttempt, err := db.OtelAttemptUsageSince(0)
	if err != nil {
		return err
	}
	evalPassByAttempt, err := evalPassMap(db)
	if err != nil {
		return err
	}
	prices := LoadPrices(db)

	for _, r := range attemptRows {
		agent := firstNonEmpty(r.attemptAgent, r.taskAgent, "claude")
		model := firstNonEmpty(r.attemptModel, r.taskModel)
		date := time.Unix(int64(*r.finishedAt), 0).UTC().Format("2006-01-02")

		var cost float64
		var inTok, outTok int64
		costSource := ""
		if otel, ok := otelByAttempt[r.id]; ok {
			cost, inTok, outTok = otel.CostUSD, otel.InputTokens, otel.OutputTokens
			if otel.Model != "" {
				model = otel.Model
			}
			costSource = "otel"
		} else {
			result := store.UnjObj(r.resultJSON)
			rc, ri, ro := resultUsage(result)
			cost, inTok, outTok = rc, ri, ro
			// Only a real dollar figure counts as "result_json" — an agent
			// that reported tokens but no cost_usd at all (Codex-style) must
			// still be eligible for the price-table estimate below, not get
			// stuck reporting a silent $0.
			if cost > 0 {
				costSource = "result_json"
			}
		}
		if costSource == "" && (inTok > 0 || outTok > 0) {
			if est, ok := prices.Estimate(model, inTok, outTok); ok {
				cost, costSource = est, "estimated"
			}
		}

		checkPassed := verifyPassed(r.verifyJSON)
		accepted := r.taskStatus == "done" && r.n == r.maxN

		var linesKept *int64
		if accepted {
			n := diffStatLines(r.diffStatJSON)
			linesKept = &n
		}

		var evalPass *bool
		if v, ok := evalPassByAttempt[r.id]; ok {
			ev := v
			evalPass = &ev
		}

		var timeToPass *float64
		if checkPassed != nil && *checkPassed && r.startedAt != nil {
			d := *r.finishedAt - *r.startedAt
			timeToPass = &d
		}

		pid := r.projectID
		tid := r.taskID
		fact := &store.OutcomeFact{
			Scope: "attempt", RefID: r.id, Date: date, TaskID: &tid, ProjectID: &pid,
			Agent: agent, Model: model, CostUSD: cost, CostSource: costSource,
			CheckPassed: checkPassed, Accepted: accepted, LinesKept: linesKept,
			EvalPass: evalPass, TimeToPassS: timeToPass, UpdatedAt: store.Now(),
		}
		if err := db.UpsertOutcomeFact(fact); err != nil {
			return err
		}
	}
	return nil
}

func rebuildSessions(db *store.DB, cutoff float64) error {
	rows, err := db.Query(`SELECT s.id, s.agent, s.model, s.project_id, s.cost_usd,
			s.created_at, s.updated_at, s.otel_active_at,
			c.status, c.finished_at
		FROM sessions s
		JOIN (
			SELECT session_id, status, finished_at,
				ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY id DESC) rn
			FROM session_checks
		) c ON c.session_id = s.id AND c.rn = 1
		WHERE s.updated_at >= ?`, cutoff)
	if err != nil {
		return err
	}
	// Collected into a slice and the Rows cursor closed BEFORE calling
	// UpsertOutcomeFact below: db.SetMaxOpenConns(1) (store.Open) means this
	// *sql.DB has exactly one connection, so an Exec while a Query's Rows
	// from the same connection is still open would block forever waiting for
	// a connection this same goroutine is holding open — the same reason
	// rebuildAttempts above already reads fully before writing.
	type sessionRow struct {
		id                          int64
		agent, model, checkStatus   string
		projectID                   *int64
		costUSD                     *float64
		createdAt, updatedAt        float64
		otelActiveAt, checkFinished *float64
	}
	var sessionRows []sessionRow
	for rows.Next() {
		var r sessionRow
		if err := rows.Scan(&r.id, &r.agent, &r.model, &r.projectID, &r.costUSD, &r.createdAt, &r.updatedAt,
			&r.otelActiveAt, &r.checkStatus, &r.checkFinished); err != nil {
			rows.Close()
			return err
		}
		sessionRows = append(sessionRows, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, r := range sessionRows {
		id, agent, model, projectID := r.id, r.agent, r.model, r.projectID
		costUSD, createdAt, updatedAt := r.costUSD, r.createdAt, r.updatedAt
		otelActiveAt, checkStatus, checkFinishedAt := r.otelActiveAt, r.checkStatus, r.checkFinished
		date := time.Unix(int64(updatedAt), 0).UTC().Format("2006-01-02")
		cost := 0.0
		costSource := ""
		if costUSD != nil {
			cost = *costUSD
			if otelActiveAt != nil {
				costSource = "otel"
			} else {
				costSource = "statusline"
			}
		}
		var checkPassed *bool
		var timeToPass *float64
		switch checkStatus {
		case "passed":
			t := true
			checkPassed = &t
			if checkFinishedAt != nil {
				d := *checkFinishedAt - createdAt
				timeToPass = &d
			}
		case "failed", "error":
			f := false
			checkPassed = &f
		}
		fact := &store.OutcomeFact{
			Scope: "session", RefID: id, Date: date, ProjectID: projectID,
			Agent: agent, Model: model, CostUSD: cost, CostSource: costSource,
			CheckPassed: checkPassed, TimeToPassS: timeToPass, UpdatedAt: store.Now(),
		}
		if err := db.UpsertOutcomeFact(fact); err != nil {
			return err
		}
	}
	return nil
}

// evalPassMap loads every eval_results row that names an attempt, latest
// result per attempt winning — a re-run of the same case/variant against the
// same attempt id cannot happen (a new eval run cuts a new attempt), so "the
// row that exists" is unambiguous; this just tolerates more than one
// defensively.
func evalPassMap(db *store.DB) (map[int64]bool, error) {
	rows, err := db.Query(`SELECT attempt_id, status FROM eval_results WHERE attempt_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var attemptID int64
		var status string
		if err := rows.Scan(&attemptID, &status); err != nil {
			continue
		}
		out[attemptID] = status == "passed"
	}
	return out, rows.Err()
}

// resultUsage mirrors internal/api's resultUsageTokens (usage.go) — the same
// small parse duplicated here rather than shared, since internal/api already
// imports internal/outcomes for GET /api/outcomes and the reverse import
// would be a cycle.
func resultUsage(result map[string]any) (cost float64, input, output int64) {
	cost, _ = result["cost_usd"].(float64)
	if usage, ok := result["usage"].(map[string]any); ok {
		in, _ := usage["input_tokens"].(float64)
		cc, _ := usage["cache_creation_input_tokens"].(float64)
		cr, _ := usage["cache_read_input_tokens"].(float64)
		out, _ := usage["output_tokens"].(float64)
		return cost, int64(in + cc + cr), int64(out)
	}
	if ct, ok := result["context_tokens"].(float64); ok {
		out, _ := result["output_tokens"].(float64)
		return cost, int64(ct), int64(out)
	}
	return cost, 0, 0
}

// verifyPassed reads attempts.verify_json's {"rc":N} shape (internal/checks.
// RunForTask's own comment: "callers must not change that shape"). nil means
// no check ever ran for this attempt.
func verifyPassed(verifyJSON string) *bool {
	v := store.UnjObj(verifyJSON)
	rc, ok := v["rc"].(float64)
	if !ok {
		return nil
	}
	passed := rc == 0
	return &passed
}

// diffStatLines sums |additions|+|deletions| across every file in an
// attempt's diff_stat_json (store.UnjList's generic []any shape).
func diffStatLines(diffStatJSON string) int64 {
	var total int64
	for _, item := range store.UnjList(diffStatJSON) {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if a, ok := m["additions"].(float64); ok {
			total += int64(a)
		}
		if d, ok := m["deletions"].(float64); ok {
			total += int64(d)
		}
	}
	return total
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
