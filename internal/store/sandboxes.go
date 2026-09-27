package store

import (
	"database/sql"
	"errors"
)

// Sandbox is one sandbox Lectern made (docs/sandboxes.md), kept so its
// lifecycle — suspend, resume, destroy — can be driven from the UI and so a
// kept sandbox is never forgotten.
type Sandbox struct {
	ID          int64    `json:"id"`
	TargetID    int64    `json:"target_id"`
	Provider    string   `json:"provider"`
	ExtID       string   `json:"ext_id"`
	AttemptID   *int64   `json:"attempt_id"`
	Status      string   `json:"status"`
	Note        string   `json:"note"`
	CreatedAt   float64  `json:"created_at"`
	UpdatedAt   float64  `json:"updated_at"`
	DestroyedAt *float64 `json:"destroyed_at"`
}

const sandboxCols = `id, target_id, provider, ext_id, attempt_id, status, note, created_at, updated_at, destroyed_at`

func scanSandbox(s interface{ Scan(...any) error }) (*Sandbox, error) {
	var b Sandbox
	err := s.Scan(&b.ID, &b.TargetID, &b.Provider, &b.ExtID, &b.AttemptID, &b.Status, &b.Note,
		&b.CreatedAt, &b.UpdatedAt, &b.DestroyedAt)
	return &b, err
}

// InsertSandbox records a sandbox that now exists.
func (db *DB) InsertSandbox(b *Sandbox) (*Sandbox, error) {
	now := Now()
	res, err := db.Exec(`INSERT INTO sandboxes(target_id, provider, ext_id, attempt_id, status, note, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?)`, b.TargetID, b.Provider, b.ExtID, b.AttemptID, nz(b.Status, "running"), b.Note, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return db.Sandbox(id)
}

// Sandbox fetches one sandbox record.
func (db *DB) Sandbox(id int64) (*Sandbox, error) {
	b, err := scanSandbox(db.QueryRow(`SELECT `+sandboxCols+` FROM sandboxes WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return b, err
}

// SandboxForAttempt is the live sandbox an attempt runs in, if any.
func (db *DB) SandboxForAttempt(attemptID int64) (*Sandbox, error) {
	b, err := scanSandbox(db.QueryRow(`SELECT `+sandboxCols+` FROM sandboxes
		WHERE attempt_id=? AND destroyed_at IS NULL ORDER BY id DESC LIMIT 1`, attemptID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return b, err
}

// Sandboxes lists sandboxes, live ones first, newest first.
func (db *DB) Sandboxes(includeDestroyed bool) ([]*Sandbox, error) {
	q := `SELECT ` + sandboxCols + ` FROM sandboxes`
	if !includeDestroyed {
		q += ` WHERE destroyed_at IS NULL`
	}
	rows, err := db.Query(q + ` ORDER BY destroyed_at IS NOT NULL, id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Sandbox{}
	for rows.Next() {
		b, err := scanSandbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// SetSandboxStatus moves a sandbox to a new state; "destroyed" also stamps it.
func (db *DB) SetSandboxStatus(id int64, status string) error {
	if status == "destroyed" {
		_, err := db.Exec(`UPDATE sandboxes SET status=?, updated_at=?, destroyed_at=? WHERE id=?`, status, Now(), Now(), id)
		return err
	}
	_, err := db.Exec(`UPDATE sandboxes SET status=?, updated_at=? WHERE id=?`, status, Now(), id)
	return err
}

// SetAttemptEnv stores a dispatch's own environment for one attempt.
func (db *DB) SetAttemptEnv(attemptID int64, envJSON string) error {
	_, err := db.Exec(`INSERT INTO attempt_env(attempt_id, env_json) VALUES(?,?)
		ON CONFLICT(attempt_id) DO UPDATE SET env_json=excluded.env_json`, attemptID, envJSON)
	return err
}

// AttemptEnv is the environment a dispatch gave an attempt ("{}" when none).
func (db *DB) AttemptEnv(attemptID int64) string {
	var raw string
	if err := db.QueryRow(`SELECT env_json FROM attempt_env WHERE attempt_id=?`, attemptID).Scan(&raw); err != nil {
		return "{}"
	}
	return raw
}
