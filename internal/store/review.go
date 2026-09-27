package store

import (
	"database/sql"
	"errors"
	"strings"
)

// ---- review workspace ------------------------------------------------------
//
// The persistent half of docs/review.md: inline review comments, the files a
// reviewer marked viewed, and the line hashes an agent's edit hooks reported.

// ReviewComment is one inline comment on one diff line. Code and the two
// context strings are what the reviewer saw when commenting: the client uses
// them to find the same line again after the agent edits the file, and to
// tell whether a sent comment's code has since changed.
type ReviewComment struct {
	ID            int64    `json:"id"`
	SessionID     int64    `json:"session_id"`
	Repo          string   `json:"repo"`
	File          string   `json:"file"`
	Side          string   `json:"side"`
	Line          int      `json:"line"`
	Code          string   `json:"code"`
	ContextBefore string   `json:"context_before"`
	ContextAfter  string   `json:"context_after"`
	Text          string   `json:"text"`
	Status        string   `json:"status"` // draft | sent | resolved
	Round         int      `json:"round"`
	Author        string   `json:"author"`
	CreatedAt     float64  `json:"created_at"`
	SentAt        *float64 `json:"sent_at"`
	ResolvedAt    *float64 `json:"resolved_at"`
}

// ReviewFileMark records that a reviewer marked one file viewed while its
// diff had this fingerprint. A different fingerprint means the file changed
// since, and the mark no longer applies.
type ReviewFileMark struct {
	Repo        string  `json:"repo"`
	Path        string  `json:"path"`
	Fingerprint string  `json:"fingerprint"`
	At          float64 `json:"at"`
}

const reviewCommentCols = `id, session_id, repo, file, side, line, code, context_before,
	context_after, text, status, round, author, created_at, sent_at, resolved_at`

func scanReviewComment(s interface{ Scan(...any) error }) (*ReviewComment, error) {
	var c ReviewComment
	err := s.Scan(&c.ID, &c.SessionID, &c.Repo, &c.File, &c.Side, &c.Line, &c.Code,
		&c.ContextBefore, &c.ContextAfter, &c.Text, &c.Status, &c.Round, &c.Author,
		&c.CreatedAt, &c.SentAt, &c.ResolvedAt)
	return &c, err
}

// InsertReviewComment stores a new draft comment.
func (db *DB) InsertReviewComment(c *ReviewComment) (*ReviewComment, error) {
	res, err := db.Exec(`INSERT INTO review_comments(session_id, repo, file, side, line, code,
		context_before, context_after, text, status, round, author, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.SessionID, c.Repo, c.File, nz(c.Side, "new"), c.Line, c.Code, c.ContextBefore,
		c.ContextAfter, c.Text, nz(c.Status, "draft"), c.Round, c.Author, Now())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return db.ReviewComment(id)
}

// ReviewComment fetches one comment.
func (db *DB) ReviewComment(id int64) (*ReviewComment, error) {
	c, err := scanReviewComment(db.QueryRow(`SELECT `+reviewCommentCols+` FROM review_comments WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// ReviewComments lists a session's comments, oldest first.
func (db *DB) ReviewComments(sessionID int64) ([]*ReviewComment, error) {
	rows, err := db.Query(`SELECT `+reviewCommentCols+` FROM review_comments
		WHERE session_id=? ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ReviewComment{}
	for rows.Next() {
		c, err := scanReviewComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkReviewCommentsSent moves the given comments to status "sent" in one
// round, all or nothing.
func (db *DB) MarkReviewCommentsSent(ids []int64, round int) error {
	if len(ids) == 0 {
		return nil
	}
	now := Now()
	return db.inTx(func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.Exec(`UPDATE review_comments SET status='sent', round=?, sent_at=?,
				resolved_at=NULL WHERE id=?`, round, now, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteReviewComment removes one comment.
func (db *DB) DeleteReviewComment(id int64) error {
	_, err := db.Exec(`DELETE FROM review_comments WHERE id=?`, id)
	return err
}

// ReviewFileMarks lists the files a session's reviewer marked viewed.
func (db *DB) ReviewFileMarks(sessionID int64) ([]ReviewFileMark, error) {
	rows, err := db.Query(`SELECT repo, path, fingerprint, at FROM review_file_marks
		WHERE session_id=? ORDER BY path`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReviewFileMark{}
	for rows.Next() {
		var m ReviewFileMark
		if err := rows.Scan(&m.Repo, &m.Path, &m.Fingerprint, &m.At); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetReviewFileMark marks a file viewed at fingerprint, or clears the mark
// when fingerprint is empty.
func (db *DB) SetReviewFileMark(sessionID int64, repo, path, fingerprint string) error {
	if fingerprint == "" {
		_, err := db.Exec(`DELETE FROM review_file_marks WHERE session_id=? AND repo=? AND path=?`,
			sessionID, repo, path)
		return err
	}
	_, err := db.Exec(`INSERT INTO review_file_marks(session_id, repo, path, fingerprint, at)
		VALUES(?,?,?,?,?) ON CONFLICT(session_id, repo, path)
		DO UPDATE SET fingerprint=excluded.fingerprint, at=excluded.at`,
		sessionID, repo, path, fingerprint, Now())
	return err
}

// AddAgentLineMarks records hashes of lines an agent wrote into path (an
// absolute path on the session's target).
func (db *DB) AddAgentLineMarks(sessionID int64, path string, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	now := Now()
	return db.inTx(func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(`INSERT INTO agent_line_marks(session_id, path, line_hash, at)
			VALUES(?,?,?,?) ON CONFLICT(session_id, path, line_hash) DO UPDATE SET at=excluded.at`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, h := range hashes {
			if _, err := stmt.Exec(sessionID, path, h, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// AgentLineMarks returns every recorded line hash for the given absolute
// paths, keyed by path.
func (db *DB) AgentLineMarks(sessionID int64, paths []string) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	if len(paths) == 0 {
		return out, nil
	}
	args := []any{sessionID}
	for _, p := range paths {
		args = append(args, p)
	}
	rows, err := db.Query(`SELECT path, line_hash FROM agent_line_marks WHERE session_id=? AND path IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(paths)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p, h string
		if err := rows.Scan(&p, &h); err != nil {
			return nil, err
		}
		if out[p] == nil {
			out[p] = map[string]bool{}
		}
		out[p][h] = true
	}
	return out, rows.Err()
}

// CountAgentLineMarks is how many line hashes a session has recorded at all —
// zero means its agent never reported an edit, so uncommitted lines cannot be
// attributed either way.
func (db *DB) CountAgentLineMarks(sessionID int64) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM agent_line_marks WHERE session_id=?`, sessionID).Scan(&n)
	return n, err
}

// DeleteAgentLineMarks drops the given hashes recorded for one path.
func (db *DB) DeleteAgentLineMarks(sessionID int64, path string, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	return db.inTx(func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(`DELETE FROM agent_line_marks WHERE session_id=? AND path=? AND line_hash=?`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, h := range hashes {
			if _, err := stmt.Exec(sessionID, path, h); err != nil {
				return err
			}
		}
		return nil
	})
}

// AgentLineMarkPaths lists the paths a session has marks for below dir (an
// absolute directory, without a trailing slash).
func (db *DB) AgentLineMarkPaths(sessionID int64, dir string) ([]string, error) {
	prefix := strings.TrimSuffix(dir, "/") + "/"
	rows, err := db.Query(`SELECT DISTINCT path FROM agent_line_marks WHERE session_id=? AND substr(path, 1, ?)=?`,
		sessionID, len(prefix), prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteAgentLineMarksForPaths drops every mark a session holds for these
// paths.
func (db *DB) DeleteAgentLineMarksForPaths(sessionID int64, paths []string) error {
	return db.inTx(func(tx *sql.Tx) error {
		for _, p := range paths {
			if _, err := tx.Exec(`DELETE FROM agent_line_marks WHERE session_id=? AND path=?`, sessionID, p); err != nil {
				return err
			}
		}
		return nil
	})
}

// PruneEndedAgentLineMarks drops the marks of sessions that ended before
// cutoff (epoch seconds); attribution is only ever asked of a live diff.
func (db *DB) PruneEndedAgentLineMarks(cutoff float64) (int64, error) {
	res, err := db.Exec(`DELETE FROM agent_line_marks WHERE session_id IN
		(SELECT id FROM sessions WHERE ended_at IS NOT NULL AND ended_at < ?)`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
