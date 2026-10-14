package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// ---- effect checks --------------------------------------------------------
//
// See schema.go's effect_checks table comment and internal/effects, which
// decides what a row means. This file is only the store half.

const effectCheckCols = `id, session_id, attempt_id, message_hash, created_at,
	results_json, verified, contradicted, unknown, enforced`

func scanEffectCheck(s interface{ Scan(...any) error }) (*EffectCheck, error) {
	var c EffectCheck
	var results string
	err := s.Scan(&c.ID, &c.SessionID, &c.AttemptID, &c.MessageHash, &c.CreatedAt,
		&results, &c.Verified, &c.Contradicted, &c.Unknown, &c.Enforced)
	if err != nil {
		return nil, err
	}
	c.Results = json.RawMessage(nz(results, "[]"))
	return &c, nil
}

// InsertEffectCheck records one evaluated message. CreatedAt defaults to now.
func (db *DB) InsertEffectCheck(c *EffectCheck) (*EffectCheck, error) {
	if c.CreatedAt == 0 {
		c.CreatedAt = Now()
	}
	res, err := db.Exec(`INSERT INTO effect_checks(session_id, attempt_id, message_hash,
		created_at, results_json, verified, contradicted, unknown, enforced)
		VALUES(?,?,?,?,?,?,?,?,?)`,
		c.SessionID, c.AttemptID, c.MessageHash, c.CreatedAt, nz(string(c.Results), "[]"),
		c.Verified, c.Contradicted, c.Unknown, c.Enforced)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return db.EffectCheck(id)
}

// EffectCheck fetches one evaluation by id.
func (db *DB) EffectCheck(id int64) (*EffectCheck, error) {
	row := db.QueryRow(`SELECT `+effectCheckCols+` FROM effect_checks WHERE id=?`, id)
	c, err := scanEffectCheck(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// EffectChecks lists a session's evaluations, newest first.
func (db *DB) EffectChecks(sessionID int64, limit int) ([]*EffectCheck, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.Query(`SELECT `+effectCheckCols+`
		FROM effect_checks WHERE session_id=? ORDER BY id DESC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*EffectCheck{}
	for rows.Next() {
		c, err := scanEffectCheck(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// LatestEffectCheck is the newest evaluation for a session, or (nil, nil) when
// the session has none.
func (db *DB) LatestEffectCheck(sessionID int64) (*EffectCheck, error) {
	rows, err := db.EffectChecks(sessionID, 1)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

// HasEffectCheck reports whether this exact message hash was already evaluated
// for the session. It is the durable half of the dedupe; the watcher also keeps
// an in-memory set for messages still being evaluated.
func (db *DB) HasEffectCheck(sessionID int64, messageHash string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM effect_checks WHERE session_id=? AND message_hash=?`,
		sessionID, messageHash).Scan(&n)
	return n > 0, err
}

// CountEnforcedEffectChecks counts a session's enforcement notices sent at or
// after since. The watcher uses it for the per-hour cap, so the cap survives a
// restart.
func (db *DB) CountEnforcedEffectChecks(sessionID int64, since float64) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM effect_checks
		WHERE session_id=? AND enforced=1 AND created_at>=?`, sessionID, since).Scan(&n)
	return n, err
}

// EffectReliabilityRow is one agent+model group in the reliability summary.
// Claims is verified+contradicted+unknown; Checks counts evaluated messages.
type EffectReliabilityRow struct {
	Agent        string `json:"agent"`
	Model        string `json:"model"`
	Checks       int    `json:"checks"`
	Claims       int    `json:"claims"`
	Verified     int    `json:"verified"`
	Contradicted int    `json:"contradicted"`
	Unknown      int    `json:"unknown"`
}

// EffectReliability groups evaluations created at or after since by the
// session's agent and model. It is the per-agent claim reliability: how often a
// claim the agent made was verified, contradicted, or could not be decided.
func (db *DB) EffectReliability(since float64) ([]EffectReliabilityRow, error) {
	rows, err := db.Query(`SELECT s.agent, s.model, COUNT(*),
			COALESCE(SUM(e.verified+e.contradicted+e.unknown),0),
			COALESCE(SUM(e.verified),0), COALESCE(SUM(e.contradicted),0), COALESCE(SUM(e.unknown),0)
		FROM effect_checks e JOIN sessions s ON s.id = e.session_id
		WHERE e.created_at >= ?
		GROUP BY s.agent, s.model
		ORDER BY s.agent, s.model`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EffectReliabilityRow{}
	for rows.Next() {
		var r EffectReliabilityRow
		if err := rows.Scan(&r.Agent, &r.Model, &r.Checks, &r.Claims, &r.Verified, &r.Contradicted, &r.Unknown); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
