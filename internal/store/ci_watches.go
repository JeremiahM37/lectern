package store

import (
	"database/sql"
	"errors"
)

// DefaultCIMaxAttempts is the fix-request cap a project gets when it has not
// set one (docs/ci-loop.md).
const DefaultCIMaxAttempts = 3

func ciMaxAttempts(n int) int {
	if n <= 0 {
		return DefaultCIMaxAttempts
	}
	return n
}

// CIWatch is one pull request the CI loop is watching — see the ci_watches
// table in schema.go and internal/ciloop.
type CIWatch struct {
	ID           int64   `json:"id"`
	TaskID       *int64  `json:"task_id"`
	SessionID    *int64  `json:"session_id"`
	ProjectID    *int64  `json:"project_id"`
	TargetID     int64   `json:"target_id"`
	Branch       string  `json:"branch"`
	PRURL        string  `json:"pr_url"`
	State        string  `json:"state"`
	Attempts     int     `json:"attempts"`
	MaxAttempts  int     `json:"max_attempts"`
	HeadSHA      string  `json:"head_sha"`
	AskedSHA     string  `json:"asked_sha"`
	FailingJSON  string  `json:"-"`
	Detail       string  `json:"detail"`
	Errors       int     `json:"-"`
	IntervalS    float64 `json:"-"`
	NextPollAt   float64 `json:"next_poll_at"`
	LastChangeAt float64 `json:"last_change_at"`
	CreatedAt    float64 `json:"created_at"`
	UpdatedAt    float64 `json:"updated_at"`
}

const ciWatchCols = `id, task_id, session_id, project_id, target_id, branch, pr_url, state,
	attempts, max_attempts, head_sha, asked_sha, failing_json, detail, errors, interval_s,
	next_poll_at, last_change_at, created_at, updated_at`

func scanCIWatch(s interface{ Scan(...any) error }) (*CIWatch, error) {
	var w CIWatch
	err := s.Scan(&w.ID, &w.TaskID, &w.SessionID, &w.ProjectID, &w.TargetID, &w.Branch,
		&w.PRURL, &w.State, &w.Attempts, &w.MaxAttempts, &w.HeadSHA, &w.AskedSHA,
		&w.FailingJSON, &w.Detail, &w.Errors, &w.IntervalS, &w.NextPollAt,
		&w.LastChangeAt, &w.CreatedAt, &w.UpdatedAt)
	return &w, err
}

// UpsertCIWatch starts watching w.PRURL. A PR already being watched keeps
// its row and counters; one whose watch had finished is re-armed from
// scratch, since a new push to a closed-out PR is a new round.
func (db *DB) UpsertCIWatch(w *CIWatch) (*CIWatch, error) {
	now := Now()
	_, err := db.Exec(`INSERT INTO ci_watches(task_id, session_id, project_id, target_id,
		branch, pr_url, state, max_attempts, next_poll_at, last_change_at, created_at, updated_at)
		VALUES(?,?,?,?,?,?,'pending',?,?,?,?,?)
		ON CONFLICT(pr_url) DO UPDATE SET
		  task_id=excluded.task_id, session_id=excluded.session_id,
		  project_id=excluded.project_id, target_id=excluded.target_id,
		  branch=excluded.branch, max_attempts=excluded.max_attempts,
		  state='pending', attempts=0, head_sha='', asked_sha='', failing_json='[]',
		  detail='', errors=0, interval_s=0, next_poll_at=excluded.next_poll_at,
		  last_change_at=excluded.last_change_at, updated_at=excluded.updated_at
		WHERE ci_watches.state NOT IN ('pending','failing')`,
		w.TaskID, w.SessionID, w.ProjectID, w.TargetID, w.Branch, w.PRURL,
		ciMaxAttempts(w.MaxAttempts), now, now, now, now)
	if err != nil {
		return nil, err
	}
	return db.CIWatchByURL(w.PRURL)
}

// CIWatch fetches one watch by id.
func (db *DB) CIWatch(id int64) (*CIWatch, error) {
	w, err := scanCIWatch(db.QueryRow(`SELECT `+ciWatchCols+` FROM ci_watches WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return w, err
}

// CIWatchByURL fetches the watch for one pull request URL.
func (db *DB) CIWatchByURL(url string) (*CIWatch, error) {
	w, err := scanCIWatch(db.QueryRow(`SELECT `+ciWatchCols+` FROM ci_watches WHERE pr_url=?`, url))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return w, err
}

// LatestCIWatch is the newest watch owned by a task (column "task_id") or a
// session ("session_id"); nil with no error when there is none.
func (db *DB) LatestCIWatch(column string, ownerID int64) (*CIWatch, error) {
	if column != "task_id" && column != "session_id" {
		return nil, errors.New("LatestCIWatch: owner column must be task_id or session_id")
	}
	w, err := scanCIWatch(db.QueryRow(`SELECT `+ciWatchCols+` FROM ci_watches
		WHERE `+column+`=? ORDER BY updated_at DESC, id DESC LIMIT 1`, ownerID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return w, err
}

// DueCIWatches lists active watches whose next poll time has passed.
func (db *DB) DueCIWatches(now float64) ([]*CIWatch, error) {
	rows, err := db.Query(`SELECT `+ciWatchCols+` FROM ci_watches
		WHERE state IN ('pending','failing') AND next_poll_at<=? ORDER BY next_poll_at`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*CIWatch{}
	for rows.Next() {
		w, err := scanCIWatch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
