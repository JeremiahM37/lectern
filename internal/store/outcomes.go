package store

// OutcomeFact is one row of outcome_facts — see schema.go's comment for what
// each column means and why the table is a recomputed cache rather than an
// append-only ledger. internal/outcomes owns the derivation; this file is
// just the store half (upsert, range read), the same split usage.go and
// session_checks.go use for their own tables.
type OutcomeFact struct {
	ID          int64
	Scope       string // "attempt" | "session"
	RefID       int64
	Date        string
	TaskID      *int64
	ProjectID   *int64
	Agent       string
	Model       string
	CostUSD     float64
	CostSource  string
	CheckPassed *bool
	Accepted    bool
	LinesKept   *int64
	EvalPass    *bool
	TimeToPassS *float64
	UpdatedAt   float64
}

// UpsertOutcomeFact writes or replaces the one fact row for (scope, ref_id).
// Rebuild calls this once per attempt/session it recomputes; a stale row for
// a ref no longer in the recomputed window simply ages out of every query
// that filters by `date`, so nothing here needs to delete it.
func (db *DB) UpsertOutcomeFact(f *OutcomeFact) error {
	checkPassed := nullableBool(f.CheckPassed)
	evalPass := nullableBool(f.EvalPass)
	_, err := db.Exec(`INSERT INTO outcome_facts(scope, ref_id, date, task_id, project_id,
			agent, model, cost_usd, cost_source, check_passed, accepted, lines_kept,
			eval_pass, time_to_pass_s, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(scope, ref_id) DO UPDATE SET
			date = excluded.date, task_id = excluded.task_id, project_id = excluded.project_id,
			agent = excluded.agent, model = excluded.model, cost_usd = excluded.cost_usd,
			cost_source = excluded.cost_source, check_passed = excluded.check_passed,
			accepted = excluded.accepted, lines_kept = excluded.lines_kept,
			eval_pass = excluded.eval_pass, time_to_pass_s = excluded.time_to_pass_s,
			updated_at = excluded.updated_at`,
		f.Scope, f.RefID, f.Date, f.TaskID, f.ProjectID, f.Agent, f.Model, f.CostUSD,
		f.CostSource, checkPassed, boolToInt(f.Accepted), f.LinesKept, evalPass,
		f.TimeToPassS, f.UpdatedAt)
	return err
}

// OutcomeFactsSince lists every fact dated at or after cutoffDate (a UTC
// yyyy-mm-dd string, inclusive) — the read half of GET /api/outcomes.
func (db *DB) OutcomeFactsSince(cutoffDate string) ([]*OutcomeFact, error) {
	rows, err := db.Query(`SELECT id, scope, ref_id, date, task_id, project_id, agent, model,
			cost_usd, cost_source, check_passed, accepted, lines_kept, eval_pass, time_to_pass_s, updated_at
		FROM outcome_facts WHERE date >= ?`, cutoffDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*OutcomeFact{}
	for rows.Next() {
		f := &OutcomeFact{}
		var checkPassed, evalPass *int
		var accepted int
		if err := rows.Scan(&f.ID, &f.Scope, &f.RefID, &f.Date, &f.TaskID, &f.ProjectID,
			&f.Agent, &f.Model, &f.CostUSD, &f.CostSource, &checkPassed, &accepted,
			&f.LinesKept, &evalPass, &f.TimeToPassS, &f.UpdatedAt); err != nil {
			continue
		}
		f.CheckPassed = intToBool(checkPassed)
		f.EvalPass = intToBool(evalPass)
		f.Accepted = accepted != 0
		out = append(out, f)
	}
	return out, rows.Err()
}

func nullableBool(b *bool) any {
	if b == nil {
		return nil
	}
	return boolToInt(*b)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func intToBool(v *int) *bool {
	if v == nil {
		return nil
	}
	b := *v != 0
	return &b
}
