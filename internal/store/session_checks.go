package store

import (
	"database/sql"
	"errors"
)

// ---- session checks -----------------------------------------------------
//
// See schema.go's session_checks table comment and internal/checks, which
// owns the logic that decides when to run one. This file is just the store
// half: insert, and the two read shapes the API and the runner need.

const sessionCheckCols = `id, session_id, fingerprint, command, status,
	exit_code, output_tail, started_at, finished_at, reason`

func scanSessionCheck(s interface{ Scan(...any) error }) (*SessionCheck, error) {
	var c SessionCheck
	err := s.Scan(&c.ID, &c.SessionID, &c.Fingerprint, &c.Command, &c.Status,
		&c.ExitCode, &c.OutputTail, &c.StartedAt, &c.FinishedAt, &c.Reason)
	return &c, err
}

// InsertSessionCheck records a new check row, normally in status "running" —
// the runner fills in the result with Update("session_checks", id, ...) once
// the command finishes, the same generic-Update pattern every other table uses.
func (db *DB) InsertSessionCheck(c *SessionCheck) (*SessionCheck, error) {
	res, err := db.Exec(`INSERT INTO session_checks(session_id, fingerprint,
		command, status, exit_code, output_tail, started_at, finished_at, reason)
		VALUES(?,?,?,?,?,?,?,?,?)`,
		c.SessionID, c.Fingerprint, c.Command, nz(c.Status, "running"),
		c.ExitCode, c.OutputTail, c.StartedAt, c.FinishedAt, c.Reason)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return db.SessionCheck(id)
}

// SessionCheck fetches one check by id.
func (db *DB) SessionCheck(id int64) (*SessionCheck, error) {
	row := db.QueryRow(`SELECT `+sessionCheckCols+` FROM session_checks WHERE id=?`, id)
	c, err := scanSessionCheck(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// SessionChecks lists a session's checks, newest first.
func (db *DB) SessionChecks(sessionID int64, limit int) ([]*SessionCheck, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.Query(`SELECT `+sessionCheckCols+`
		FROM session_checks WHERE session_id=? ORDER BY id DESC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SessionCheck{}
	for rows.Next() {
		c, err := scanSessionCheck(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// LatestSessionCheck is the most recent check for a session, of any status —
// what the fingerprint-skip compares against and what a session's card badge
// shows. Returns (nil, nil) when the session has never been checked.
func (db *DB) LatestSessionCheck(sessionID int64) (*SessionCheck, error) {
	rows, err := db.SessionChecks(sessionID, 1)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}
