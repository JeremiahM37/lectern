package store

import (
	"database/sql"
	"errors"
)

// ---- eval suites --------------------------------------------------------------

const evalSuiteCols = `id, name, project_id, description, created_at`

func scanEvalSuite(s interface{ Scan(...any) error }) (*EvalSuite, error) {
	var e EvalSuite
	err := s.Scan(&e.ID, &e.Name, &e.ProjectID, &e.Description, &e.CreatedAt)
	return &e, err
}

func (db *DB) EvalSuite(id int64) (*EvalSuite, error) {
	e, err := scanEvalSuite(db.QueryRow(`SELECT `+evalSuiteCols+` FROM eval_suites WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return e, err
}

// EvalSuites lists suites, optionally scoped to one project (projectID<=0 for all).
func (db *DB) EvalSuites(projectID int64) ([]*EvalSuite, error) {
	q := `SELECT ` + evalSuiteCols + ` FROM eval_suites`
	args := []any{}
	if projectID > 0 {
		q += ` WHERE project_id=?`
		args = append(args, projectID)
	}
	q += ` ORDER BY id DESC`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*EvalSuite{}
	for rows.Next() {
		e, err := scanEvalSuite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (db *DB) InsertEvalSuite(e *EvalSuite) (*EvalSuite, error) {
	if e.CreatedAt == 0 {
		e.CreatedAt = Now()
	}
	res, err := db.Exec(`INSERT INTO eval_suites(name, project_id, description, created_at)
		VALUES(?,?,?,?)`, e.Name, e.ProjectID, e.Description, e.CreatedAt)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return db.EvalSuite(id)
}

func (db *DB) DeleteEvalSuite(id int64) error {
	_, err := db.Exec(`DELETE FROM eval_suites WHERE id=?`, id)
	return err
}

// ---- eval cases -----------------------------------------------------------

const evalCaseCols = `id, suite_id, name, prompt, base_ref, check_command, timeout_s, setup_command,
	is_replay, source_pr_number, reference_diff`

func scanEvalCase(s interface{ Scan(...any) error }) (*EvalCase, error) {
	var c EvalCase
	var isReplay int
	err := s.Scan(&c.ID, &c.SuiteID, &c.Name, &c.Prompt, &c.BaseRef, &c.CheckCommand,
		&c.TimeoutS, &c.SetupCommand, &isReplay, &c.SourcePRNumber, &c.ReferenceDiff)
	c.IsReplay = isReplay != 0
	return &c, err
}

func (db *DB) EvalCase(id int64) (*EvalCase, error) {
	c, err := scanEvalCase(db.QueryRow(`SELECT `+evalCaseCols+` FROM eval_cases WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

func (db *DB) EvalCases(suiteID int64) ([]*EvalCase, error) {
	rows, err := db.Query(`SELECT `+evalCaseCols+` FROM eval_cases WHERE suite_id=? ORDER BY id`, suiteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*EvalCase{}
	for rows.Next() {
		c, err := scanEvalCase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (db *DB) InsertEvalCase(c *EvalCase) (*EvalCase, error) {
	if c.TimeoutS <= 0 {
		c.TimeoutS = 900
	}
	isReplay := 0
	if c.IsReplay {
		isReplay = 1
	}
	res, err := db.Exec(`INSERT INTO eval_cases(suite_id, name, prompt, base_ref, check_command,
		timeout_s, setup_command, is_replay, source_pr_number, reference_diff) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		c.SuiteID, c.Name, c.Prompt, c.BaseRef, c.CheckCommand, c.TimeoutS, c.SetupCommand,
		isReplay, c.SourcePRNumber, c.ReferenceDiff)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return db.EvalCase(id)
}

func (db *DB) DeleteEvalCase(id int64) error {
	_, err := db.Exec(`DELETE FROM eval_cases WHERE id=?`, id)
	return err
}

// ---- eval runs --------------------------------------------------------------

const evalRunCols = `id, suite_id, created_at, status, variants_json, repeats, notes, with_judge`

func scanEvalRun(s interface{ Scan(...any) error }) (*EvalRun, error) {
	var r EvalRun
	var withJudge int
	err := s.Scan(&r.ID, &r.SuiteID, &r.CreatedAt, &r.Status, &r.VariantsJSON, &r.Repeats, &r.Notes, &withJudge)
	r.WithJudge = withJudge != 0
	return &r, err
}

func (db *DB) EvalRun(id int64) (*EvalRun, error) {
	r, err := scanEvalRun(db.QueryRow(`SELECT `+evalRunCols+` FROM eval_runs WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

func (db *DB) EvalRuns(suiteID int64) ([]*EvalRun, error) {
	rows, err := db.Query(`SELECT `+evalRunCols+` FROM eval_runs WHERE suite_id=? ORDER BY id DESC`, suiteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*EvalRun{}
	for rows.Next() {
		r, err := scanEvalRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (db *DB) InsertEvalRun(r *EvalRun) (*EvalRun, error) {
	if r.CreatedAt == 0 {
		r.CreatedAt = Now()
	}
	if r.Repeats <= 0 {
		r.Repeats = 1
	}
	withJudge := 0
	if r.WithJudge {
		withJudge = 1
	}
	res, err := db.Exec(`INSERT INTO eval_runs(suite_id, created_at, status, variants_json, repeats, notes, with_judge)
		VALUES(?,?,?,?,?,?,?)`, r.SuiteID, r.CreatedAt, nz(r.Status, "queued"), nz(r.VariantsJSON, "[]"),
		r.Repeats, r.Notes, withJudge)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return db.EvalRun(id)
}

// ---- eval results -----------------------------------------------------------

const evalResultCols = `id, run_id, case_id, variant_idx, repeat_idx, task_id, attempt_id, status,
	duration_s, cost_usd, input_tokens, output_tokens, diff_files, diff_lines, check_rc, check_output_tail,
	similarity_files, similarity_lines, size_ratio, judge_status, judge_match, judge_reason`

func scanEvalResult(s interface{ Scan(...any) error }) (*EvalResult, error) {
	var r EvalResult
	err := s.Scan(&r.ID, &r.RunID, &r.CaseID, &r.VariantIdx, &r.RepeatIdx, &r.TaskID, &r.AttemptID,
		&r.Status, &r.DurationS, &r.CostUSD, &r.InputTokens, &r.OutputTokens, &r.DiffFiles,
		&r.DiffLines, &r.CheckRC, &r.CheckOutputTail,
		&r.SimilarityFiles, &r.SimilarityLines, &r.SizeRatio, &r.JudgeStatus, &r.JudgeMatch, &r.JudgeReason)
	return &r, err
}

func (db *DB) EvalResult(id int64) (*EvalResult, error) {
	r, err := scanEvalResult(db.QueryRow(`SELECT `+evalResultCols+` FROM eval_results WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// EvalResultByTaskID finds the cell one dispatched task belongs to — used by
// scheduler.applyEvalJudgeVerdict to find its way back from a headless
// judge task's ParentTaskID (the cell's own task id) to the eval_results row
// its verdict gets recorded on.
func (db *DB) EvalResultByTaskID(taskID int64) (*EvalResult, error) {
	r, err := scanEvalResult(db.QueryRow(`SELECT `+evalResultCols+` FROM eval_results WHERE task_id=?`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// EvalResults lists a run's cells, ordered so a matrix render can group by
// case then variant then repeat without re-sorting client-side.
func (db *DB) EvalResults(runID int64) ([]*EvalResult, error) {
	rows, err := db.Query(`SELECT `+evalResultCols+` FROM eval_results
		WHERE run_id=? ORDER BY case_id, variant_idx, repeat_idx`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*EvalResult{}
	for rows.Next() {
		r, err := scanEvalResult(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (db *DB) InsertEvalResult(r *EvalResult) (*EvalResult, error) {
	res, err := db.Exec(`INSERT INTO eval_results(run_id, case_id, variant_idx, repeat_idx,
		task_id, attempt_id, status, duration_s, cost_usd, input_tokens, output_tokens,
		diff_files, diff_lines, check_rc, check_output_tail) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.RunID, r.CaseID, r.VariantIdx, r.RepeatIdx, r.TaskID, r.AttemptID, nz(r.Status, "queued"),
		r.DurationS, r.CostUSD, r.InputTokens, r.OutputTokens, r.DiffFiles, r.DiffLines,
		r.CheckRC, r.CheckOutputTail)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return db.EvalResult(id)
}
