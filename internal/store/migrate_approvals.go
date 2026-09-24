package store

import "database/sql"

// migrateApprovalsSessionColumn rebuilds the approvals table, once, so an
// approval can belong to a session's PermissionRequest hook instead of only
// a task attempt (docs/agent-events.md section 3: "extend it so an approval
// can belong to a session instead of an attempt, schema append-only").
//
// This cannot be a plain entry in the `migrations` []string list the way
// every other column addition is: attempt_id was declared NOT NULL, and
// SQLite has no "ALTER TABLE ... ALTER COLUMN" to drop a NOT NULL constraint
// short of recreating the table. The rebuild itself stays additive from the
// caller's point of view — every existing row keeps its id, and the new
// session_id column is NULL for all of them — so it satisfies the
// "append-only" intent even though the mechanism differs from ADD COLUMN.
//
// Guarded by checking sqlite_master for the OLD not-null shape rather than
// column presence: a fresh database's schema.go CREATE TABLE already has the
// nullable attempt_id and the session_id column, so this must recognise
// "nothing to do" for a brand-new db too, not just a rebuilt one.
func migrateApprovalsSessionColumn(db *sql.DB) error {
	notNull, err := approvalsAttemptIDNotNull(db)
	if err != nil {
		return err
	}
	if !notNull {
		return nil
	}
	// A rebuild, not a copy-and-swap: foreign_keys cannot be toggled inside a
	// transaction (SQLite silently ignores the pragma there), so it is
	// turned off, the whole rebuild runs as one statement batch, and it is
	// turned back on before this function returns control to Open's caller.
	// Nothing in the rebuilt data actually violates the FKs (attempt_id
	// values keep referencing the same, untouched attempts rows; the new
	// session_id column is NULL throughout), but disabling it removes any
	// doubt about DROP/RENAME ordering under enforcement.
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer db.Exec(`PRAGMA foreign_keys=ON`)
	_, err = db.Exec(`
BEGIN;
CREATE TABLE approvals_new(
  id INTEGER PRIMARY KEY, attempt_id INTEGER REFERENCES attempts(id),
  session_id INTEGER REFERENCES sessions(id),
  tool_name TEXT NOT NULL, input_json TEXT DEFAULT '{}',
  status TEXT NOT NULL DEFAULT 'pending',
  decided_by TEXT DEFAULT '', note TEXT DEFAULT '',
  created_at REAL, decided_at REAL
);
INSERT INTO approvals_new(id, attempt_id, tool_name, input_json, status, decided_by, note, created_at, decided_at)
  SELECT id, attempt_id, tool_name, input_json, status, decided_by, note, created_at, decided_at FROM approvals;
DROP TABLE approvals;
ALTER TABLE approvals_new RENAME TO approvals;
CREATE INDEX IF NOT EXISTS idx_approvals_status ON approvals(status);
COMMIT;
`)
	return err
}

// approvalsAttemptIDNotNull reports whether the live approvals table still
// has the old NOT NULL attempt_id column. False (including "table does not
// exist yet", which Schema's CREATE TABLE IF NOT EXISTS handles before this
// runs) means there is nothing for this migration to do.
func approvalsAttemptIDNotNull(db *sql.DB) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(approvals)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == "attempt_id" {
			return notnull != 0, nil
		}
	}
	return false, rows.Err()
}
