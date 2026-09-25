package store

// ---- claim board ------------------------------------------------------------
//
// See schema.go's claims table comment and internal/claims, which owns the
// logic that decides scope validity, overlap detection and briefing/warning
// text. This file is just the store half — plain SQL, like session_file_edits
// in awareness.go.

const claimCols = `id, repo_key, scope_kind, scope, holder, holder_kind,
	session_id, attempt_id, agent, intent, auto, ttl_seconds, created_at, expires_at, released_at`

func scanClaim(sc interface{ Scan(...any) error }) (*Claim, error) {
	var c Claim
	var auto int
	if err := sc.Scan(&c.ID, &c.RepoKey, &c.ScopeKind, &c.Scope, &c.Holder, &c.HolderKind,
		&c.SessionID, &c.AttemptID, &c.Agent, &c.Intent, &auto, &c.TTLSeconds,
		&c.CreatedAt, &c.ExpiresAt, &c.ReleasedAt); err != nil {
		return nil, err
	}
	c.Auto = auto != 0
	return &c, nil
}

// InsertClaim creates a new claim. CreatedAt/ExpiresAt are expected to already
// be set by the caller (internal/claims computes them against its own TTL
// rules); Now() is not implicitly used here so a test can control both.
func (db *DB) InsertClaim(c *Claim) (*Claim, error) {
	res, err := db.Exec(`INSERT INTO claims(repo_key, scope_kind, scope, holder, holder_kind,
		session_id, attempt_id, agent, intent, auto, ttl_seconds, created_at, expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.RepoKey, c.ScopeKind, c.Scope, c.Holder, c.HolderKind,
		c.SessionID, c.AttemptID, c.Agent, c.Intent, boolInt(c.Auto), c.TTLSeconds, c.CreatedAt, c.ExpiresAt)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return db.Claim(id)
}

// Claim fetches one claim by id.
func (db *DB) Claim(id int64) (*Claim, error) {
	c, err := scanClaim(db.QueryRow(`SELECT `+claimCols+` FROM claims WHERE id=?`, id))
	if err != nil {
		return nil, ErrNotFound
	}
	return c, nil
}

func queryClaims(db *DB, q string, args ...any) ([]*Claim, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Claim{}
	for rows.Next() {
		c, err := scanClaim(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ActiveClaimsForRepo returns every un-released, unexpired claim in a
// repository, oldest first — what a briefing/warning/panel reads.
func (db *DB) ActiveClaimsForRepo(repoKey string, now float64) ([]*Claim, error) {
	return queryClaims(db, `SELECT `+claimCols+` FROM claims
		WHERE repo_key=? AND released_at IS NULL AND expires_at>? ORDER BY created_at ASC`, repoKey, now)
}

// ActiveClaims returns every un-released, unexpired claim across every
// repository — the board-wide list a human's Claims panel starts from before
// filtering by repo.
func (db *DB) ActiveClaims(now float64) ([]*Claim, error) {
	return queryClaims(db, `SELECT `+claimCols+` FROM claims
		WHERE released_at IS NULL AND expires_at>? ORDER BY created_at ASC`, now)
}

// ActiveClaimsForSession returns a session's own active claims.
func (db *DB) ActiveClaimsForSession(sessionID int64, now float64) ([]*Claim, error) {
	return queryClaims(db, `SELECT `+claimCols+` FROM claims
		WHERE session_id=? AND released_at IS NULL AND expires_at>? ORDER BY created_at ASC`, sessionID, now)
}

// ActiveClaimsForAttempt returns a task attempt's own active claims.
func (db *DB) ActiveClaimsForAttempt(attemptID int64, now float64) ([]*Claim, error) {
	return queryClaims(db, `SELECT `+claimCols+` FROM claims
		WHERE attempt_id=? AND released_at IS NULL AND expires_at>? ORDER BY created_at ASC`, attemptID, now)
}

// ReleaseClaim marks one claim released, unless it already was.
func (db *DB) ReleaseClaim(id int64, at float64) error {
	_, err := db.Exec(`UPDATE claims SET released_at=? WHERE id=? AND released_at IS NULL`, at, id)
	return err
}

// ReleaseClaimsForSession releases every active claim a session holds — a
// session ending (docs/claims.md) or an explicit "release all" call.
func (db *DB) ReleaseClaimsForSession(sessionID int64, at float64) (int64, error) {
	res, err := db.Exec(`UPDATE claims SET released_at=? WHERE session_id=? AND released_at IS NULL`, at, sessionID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ReleaseClaimsForAttempt releases every active claim a task attempt holds.
func (db *DB) ReleaseClaimsForAttempt(attemptID int64, at float64) (int64, error) {
	res, err := db.Exec(`UPDATE claims SET released_at=? WHERE attempt_id=? AND released_at IS NULL`, at, attemptID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RenewClaim extends one claim's expiry — the TTL is "renewed by activity"
// clause in docs/claims.md.
func (db *DB) RenewClaim(id int64, expiresAt float64) error {
	_, err := db.Exec(`UPDATE claims SET expires_at=? WHERE id=? AND released_at IS NULL`, expiresAt, id)
	return err
}

// RenewClaimsForSession extends every active claim a session holds by that
// claim's own ttl_seconds — called on hook activity (PreToolUse/PostToolUse/
// UserPromptSubmit) so a working session's claims never lapse mid-task.
func (db *DB) RenewClaimsForSession(sessionID int64, now float64) (int64, error) {
	res, err := db.Exec(`UPDATE claims SET expires_at=?+ttl_seconds
		WHERE session_id=? AND released_at IS NULL`, now, sessionID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ExpireClaims releases every active claim whose expires_at has passed — the
// TTL-lapse half of auto-release. Returns how many were released, so a sweep
// can log something worth reading.
func (db *DB) ExpireClaims(now float64) (int64, error) {
	res, err := db.Exec(`UPDATE claims SET released_at=? WHERE released_at IS NULL AND expires_at<?`, now, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ReleaseFinishedAttemptClaims releases every claim held by an attempt whose
// task attempt is no longer queued/running — the "auto-release ... when a
// task attempt finishes" clause in docs/claims.md.
func (db *DB) ReleaseFinishedAttemptClaims(now float64) (int64, error) {
	res, err := db.Exec(`UPDATE claims SET released_at=? WHERE released_at IS NULL
		AND attempt_id IS NOT NULL
		AND attempt_id IN (SELECT id FROM attempts WHERE status NOT IN ('queued','running'))`, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ReleaseDeadSessionClaims releases every claim held by a session that has
// ended (agent_state='ended', the SessionEnd hook's mapping) or gone dead
// (the older screen-scraped status column) — a belt-and-braces sweep for the
// case the immediate SessionEnd-hook release (internal/api's hook handler)
// never fires, e.g. a tmux session killed outright.
func (db *DB) ReleaseDeadSessionClaims(now float64) (int64, error) {
	res, err := db.Exec(`UPDATE claims SET released_at=? WHERE released_at IS NULL
		AND session_id IS NOT NULL
		AND session_id IN (SELECT id FROM sessions WHERE agent_state='ended' OR status='dead' OR ended_at IS NOT NULL)`, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
