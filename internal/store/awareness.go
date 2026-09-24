package store

// ---- cross-agent awareness ------------------------------------------------
//
// See schema.go's session_file_edits table comment and internal/awareness,
// which owns the logic that decides when to record an edit, build a
// briefing, or warn about an overlap. This file is just the store half.

// UpsertSessionFileEdit records (or refreshes) the latest edit time for one
// (session, rel_path) pair. ON CONFLICT keeps exactly one row per file per
// session — a file touched five times in a row is one row with a moving
// timestamp, not five.
func (db *DB) UpsertSessionFileEdit(sessionID int64, repoKey, relPath string, at float64) error {
	_, err := db.Exec(`INSERT INTO session_file_edits(session_id, repo_key, rel_path, at)
		VALUES(?,?,?,?)
		ON CONFLICT(session_id, rel_path) DO UPDATE SET repo_key=excluded.repo_key, at=excluded.at`,
		sessionID, repoKey, relPath, at)
	return err
}

func scanSessionFileEdit(s interface{ Scan(...any) error }) (*SessionFileEdit, error) {
	var e SessionFileEdit
	err := s.Scan(&e.ID, &e.SessionID, &e.RepoKey, &e.RelPath, &e.At)
	return &e, err
}

// SessionFileEditsForRepo returns every file edit in a repo (across every
// session that has ever worked in it, live or not — the caller filters to
// live peers) at or after `since`, newest first. Used to build a session's
// peer summary: "who else touched what, recently".
func (db *DB) SessionFileEditsForRepo(repoKey string, since float64) ([]*SessionFileEdit, error) {
	rows, err := db.Query(`SELECT id, session_id, repo_key, rel_path, at
		FROM session_file_edits WHERE repo_key=? AND at>=? ORDER BY at DESC`, repoKey, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SessionFileEdit{}
	for rows.Next() {
		e, err := scanSessionFileEdit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SessionFileEditsFor returns one session's own recent file edits, newest
// first — what a card's "edited N files recently" summary reads.
func (db *DB) SessionFileEditsFor(sessionID int64, since float64) ([]*SessionFileEdit, error) {
	rows, err := db.Query(`SELECT id, session_id, repo_key, rel_path, at
		FROM session_file_edits WHERE session_id=? AND at>=? ORDER BY at DESC`, sessionID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SessionFileEdit{}
	for rows.Next() {
		e, err := scanSessionFileEdit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneSessionFileEdits deletes edit records older than `before` — run
// opportunistically on every write (internal/awareness), matching the
// "plain SQL, homelab scale" philosophy elsewhere in this store rather than
// standing up a separate janitor goroutine for one small table.
func (db *DB) PruneSessionFileEdits(before float64) error {
	_, err := db.Exec(`DELETE FROM session_file_edits WHERE at<?`, before)
	return err
}
