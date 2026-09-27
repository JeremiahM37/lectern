package store

import (
	"database/sql"
	"errors"
)

// Account is one registered login of an agent CLI on a target — see
// schema.go's agent_accounts comment and docs/accounts.md. Dir is where its
// config and credentials live on the target; it never leaves the server: the
// API, relay clients and the chat connector only ever see the label.
type Account struct {
	ID           int64    `json:"id"`
	TargetID     int64    `json:"target_id"`
	Agent        string   `json:"agent"`
	Label        string   `json:"label"`
	Dir          string   `json:"-"`
	Position     int      `json:"position"`
	LimitedAt    *float64 `json:"limited_at,omitempty"`
	LimitedUntil *float64 `json:"limited_until,omitempty"`
	CreatedAt    float64  `json:"created_at"`
	UpdatedAt    float64  `json:"updated_at"`
}

// Default reports whether this row stands for the CLI's own login (no config
// directory override).
func (a *Account) Default() bool { return a != nil && a.Dir == "" }

const accountCols = `id, target_id, agent, label, dir, position, limited_at, limited_until, created_at, updated_at`

func scanAccount(sc interface{ Scan(...any) error }) (*Account, error) {
	var a Account
	err := sc.Scan(&a.ID, &a.TargetID, &a.Agent, &a.Label, &a.Dir, &a.Position,
		&a.LimitedAt, &a.LimitedUntil, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

func (db *DB) accounts(where string, args ...any) ([]*Account, error) {
	rows, err := db.Query(`SELECT `+accountCols+` FROM agent_accounts WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Account{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Account fetches one account by id.
func (db *DB) Account(id int64) (*Account, error) {
	return scanAccount(db.QueryRow(`SELECT `+accountCols+` FROM agent_accounts WHERE id=?`, id))
}

// Accounts lists every account, in rotation order per target and agent.
func (db *DB) Accounts() ([]*Account, error) {
	return db.accounts(`1=1 ORDER BY target_id, agent, position, id`)
}

// AccountsFor lists one CLI's accounts on one target in rotation order.
func (db *DB) AccountsFor(targetID int64, agent string) ([]*Account, error) {
	return db.accounts(`target_id=? AND agent=? ORDER BY position, id`, targetID, agent)
}

// DefaultAccountFor is the row standing for the CLI's own login on a target,
// or ErrNotFound when none is registered.
func (db *DB) DefaultAccountFor(targetID int64, agent string) (*Account, error) {
	return scanAccount(db.QueryRow(`SELECT `+accountCols+` FROM agent_accounts
		WHERE target_id=? AND agent=? AND dir='' ORDER BY id LIMIT 1`, targetID, agent))
}

// InsertAccount records a new account at the end of its rotation.
func (db *DB) InsertAccount(a *Account) (*Account, error) {
	now := Now()
	var pos int
	db.QueryRow(`SELECT COALESCE(MAX(position)+1, 0) FROM agent_accounts WHERE target_id=? AND agent=?`,
		a.TargetID, a.Agent).Scan(&pos)
	res, err := db.Exec(`INSERT INTO agent_accounts(target_id, agent, label, dir, position, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?)`, a.TargetID, a.Agent, a.Label, a.Dir, pos, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return db.Account(id)
}

// DeleteAccount removes an account's record. Sessions that ran under it keep
// their account_id; they read as "unknown account" afterwards.
func (db *DB) DeleteAccount(id int64) error {
	_, err := db.Exec(`DELETE FROM agent_accounts WHERE id=?`, id)
	return err
}

// MarkAccountLimited records that an account hit its usage limit, with the
// reset when one is known (nil keeps "limited, reset unknown").
func (db *DB) MarkAccountLimited(id int64, at float64, until *float64) error {
	_, err := db.Exec(`UPDATE agent_accounts SET limited_at=?, limited_until=?, updated_at=? WHERE id=?`,
		at, until, Now(), id)
	return err
}

// ClearAccountLimit forgets an account's limit (it was seen working again).
func (db *DB) ClearAccountLimit(id int64) error {
	_, err := db.Exec(`UPDATE agent_accounts SET limited_at=NULL, limited_until=NULL, updated_at=? WHERE id=?`,
		Now(), id)
	return err
}

// LatestAccountUsage is the most recent statusline or rollout usage reading
// from any session that ran under the account (accountID 0 with dflt: the
// CLI's own login), or nil when none has reported one.
func (db *DB) LatestAccountUsage(targetID int64, agent string, accountID int64, dflt bool) (*Session, error) {
	where := `s.target_id=? AND s.agent=? AND s.usage_at IS NOT NULL AND `
	args := []any{targetID, agent}
	if dflt {
		where += `(s.account_id IS NULL OR s.account_id=?)`
	} else {
		where += `s.account_id=?`
	}
	args = append(args, accountID)
	s, err := scanSession(db.QueryRow(`SELECT `+sessionCols+`, p.name, t.name, t.kind `+sessionJoin+
		` WHERE `+where+` ORDER BY s.usage_at DESC LIMIT 1`, args...), true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}
