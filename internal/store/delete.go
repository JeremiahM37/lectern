package store

import (
	"database/sql"
	"strings"
)

// taskDependents deletes, in order, every row that references a task or one
// of its attempts, then the tasks themselves. %TASKS% is a subquery naming
// the tasks. Every foreign key into tasks or attempts must be listed here
// (TestDeleteCoversEveryForeignKey checks the schema against it); rows kept
// on purpose as history (memory_deliveries, limit_holds, outcome_facts) carry
// no foreign key and are left alone.
var taskDependents = []string{
	`DELETE FROM events WHERE attempt_id IN (SELECT id FROM attempts WHERE task_id IN (%TASKS%))`,
	`DELETE FROM approvals WHERE attempt_id IN (SELECT id FROM attempts WHERE task_id IN (%TASKS%))`,
	`DELETE FROM claims WHERE attempt_id IN (SELECT id FROM attempts WHERE task_id IN (%TASKS%))`,
	`DELETE FROM otel_attempt_usage WHERE attempt_id IN (SELECT id FROM attempts WHERE task_id IN (%TASKS%))`,
	`DELETE FROM memories WHERE created_by_attempt IN (SELECT id FROM attempts WHERE task_id IN (%TASKS%))`,
	`DELETE FROM task_takeovers WHERE task_id IN (%TASKS%)`,
	`DELETE FROM task_messages WHERE task_id IN (%TASKS%)`,
	`DELETE FROM ci_watches WHERE task_id IN (%TASKS%)`,
	`DELETE FROM usage_daily WHERE task_id IN (%TASKS%)`,
	`DELETE FROM attempts WHERE task_id IN (%TASKS%)`,
	// child tasks (agent-filed / reviewer-gate) are orphaned, not cascaded —
	// an agent-filed follow-up may be real work the operator wants to keep
	`UPDATE tasks SET parent_task_id=NULL WHERE parent_task_id IN (%TASKS%)`,
	`DELETE FROM tasks WHERE id IN (%TASKS%)`,
}

// projectDependents are the rest of a project's rows once its tasks are gone.
// Sessions and wraps are unassigned rather than deleted: the tmux session is a
// real thing that outlives this record, and goes back to being unassigned —
// exactly what it was before it was promoted. ? is the project id.
var projectDependents = []string{
	`DELETE FROM memories WHERE project_id=?`,
	`DELETE FROM trigger_events WHERE project_id=?1 OR source_id IN (SELECT id FROM trigger_sources WHERE project_id=?1)`,
	`DELETE FROM trigger_sources WHERE project_id=?`,
	`DELETE FROM eval_results WHERE run_id IN (SELECT r.id FROM eval_runs r JOIN eval_suites s ON s.id=r.suite_id WHERE s.project_id=?1)
		OR case_id IN (SELECT c.id FROM eval_cases c JOIN eval_suites s ON s.id=c.suite_id WHERE s.project_id=?1)`,
	`DELETE FROM eval_runs WHERE suite_id IN (SELECT id FROM eval_suites WHERE project_id=?)`,
	`DELETE FROM eval_cases WHERE suite_id IN (SELECT id FROM eval_suites WHERE project_id=?)`,
	`DELETE FROM eval_suites WHERE project_id=?`,
	`UPDATE sessions SET project_id=NULL WHERE project_id=?`,
	`UPDATE session_wraps SET project_id=NULL WHERE project_id=?`,
	`DELETE FROM projects WHERE id=?`,
}

func deleteTasksTx(tx *sql.Tx, sel string, args ...any) error {
	for _, stmt := range taskDependents {
		// each statement names the subquery once, so it takes the args once
		if _, err := tx.Exec(strings.ReplaceAll(stmt, "%TASKS%", sel), args...); err != nil {
			return err
		}
	}
	return nil
}

// DeleteTasks removes tasks and every row that references them or their
// attempts, all or nothing. Files and worktrees are the caller's.
func (db *DB) DeleteTasks(ids ...int64) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	sel := "SELECT id FROM tasks WHERE id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"
	return db.inTx(func(tx *sql.Tx) error { return deleteTasksTx(tx, sel, args...) })
}

// DeleteProject removes a project, its tasks and everything that references
// either, all or nothing: a failure part-way leaves the project exactly as it
// was. A project's skill materializations must already be cleaned up; if any
// are left, the foreign key fails and nothing is deleted.
func (db *DB) DeleteProject(id int64) error {
	return db.inTx(func(tx *sql.Tx) error {
		if err := deleteTasksTx(tx, "SELECT id FROM tasks WHERE project_id=?", id); err != nil {
			return err
		}
		for _, stmt := range projectDependents {
			if _, err := tx.Exec(stmt, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func (db *DB) inTx(fn func(*sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
