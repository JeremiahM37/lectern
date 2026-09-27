package store

import (
	"database/sql"
	"errors"
	"strings"
)

// LimitHold is one time an agent was stopped by its provider's usage limit —
// see schema.go's limit_holds comment and internal/limits for the lifecycle.
type LimitHold struct {
	ID              int64    `json:"id"`
	SessionID       *int64   `json:"session_id,omitempty"`
	TaskID          *int64   `json:"task_id,omitempty"`
	AttemptID       *int64   `json:"attempt_id,omitempty"`
	Agent           string   `json:"agent"`
	Source          string   `json:"source"`
	Pattern         string   `json:"pattern"`
	Message         string   `json:"message"`
	DetectedAt      float64  `json:"detected_at"`
	ResetAt         *float64 `json:"reset_at,omitempty"`
	Policy          string   `json:"policy"`
	State           string   `json:"state"`
	DueAt           *float64 `json:"due_at,omitempty"`
	Tries           int      `json:"tries"`
	NudgedAt        *float64 `json:"nudged_at,omitempty"`
	ResetNotifiedAt *float64 `json:"-"`
	ResolvedAt      *float64 `json:"resolved_at,omitempty"`
	SuccessorID     *int64   `json:"successor_id,omitempty"`
	// AccountFrom and AccountTo are the two ends of an account swap
	// (docs/accounts.md): the login that hit the limit and the one the work
	// moved to. 0 is the CLI's default login; both nil until a swap starts.
	AccountFrom *int64  `json:"account_from,omitempty"`
	AccountTo   *int64  `json:"account_to,omitempty"`
	Note        string  `json:"note,omitempty"`
	CreatedAt   float64 `json:"created_at"`
	UpdatedAt   float64 `json:"updated_at"`
}

// Open reports whether the hold still needs something to happen.
func (h *LimitHold) Open() bool { return h != nil && h.ResolvedAt == nil }

const limitHoldCols = `id, session_id, task_id, attempt_id, agent, source, pattern, message,
	detected_at, reset_at, policy, state, due_at, tries, nudged_at, reset_notified_at,
	resolved_at, successor_id, note, created_at, updated_at, account_from, account_to`

func scanLimitHold(sc interface{ Scan(...any) error }) (*LimitHold, error) {
	var h LimitHold
	err := sc.Scan(&h.ID, &h.SessionID, &h.TaskID, &h.AttemptID, &h.Agent, &h.Source,
		&h.Pattern, &h.Message, &h.DetectedAt, &h.ResetAt, &h.Policy, &h.State, &h.DueAt,
		&h.Tries, &h.NudgedAt, &h.ResetNotifiedAt, &h.ResolvedAt, &h.SuccessorID, &h.Note,
		&h.CreatedAt, &h.UpdatedAt, &h.AccountFrom, &h.AccountTo)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &h, err
}

func (db *DB) limitHolds(where string, args ...any) ([]*LimitHold, error) {
	rows, err := db.Query(`SELECT `+limitHoldCols+` FROM limit_holds WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*LimitHold{}
	for rows.Next() {
		h, err := scanLimitHold(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// LimitHold fetches one hold by id.
func (db *DB) LimitHold(id int64) (*LimitHold, error) {
	return scanLimitHold(db.QueryRow(`SELECT `+limitHoldCols+` FROM limit_holds WHERE id=?`, id))
}

// OpenLimitHoldForSession is the session's unresolved hold, or ErrNotFound.
func (db *DB) OpenLimitHoldForSession(sessionID int64) (*LimitHold, error) {
	return scanLimitHold(db.QueryRow(`SELECT `+limitHoldCols+` FROM limit_holds
		WHERE session_id=? AND resolved_at IS NULL`, sessionID))
}

// OpenLimitHoldForAttempt is the attempt's unresolved hold, or ErrNotFound.
func (db *DB) OpenLimitHoldForAttempt(attemptID int64) (*LimitHold, error) {
	return scanLimitHold(db.QueryRow(`SELECT `+limitHoldCols+` FROM limit_holds
		WHERE attempt_id=? AND resolved_at IS NULL`, attemptID))
}

// LatestLimitHoldForSession is the session's newest hold, open or not.
func (db *DB) LatestLimitHoldForSession(sessionID int64) (*LimitHold, error) {
	return scanLimitHold(db.QueryRow(`SELECT `+limitHoldCols+` FROM limit_holds
		WHERE session_id=? ORDER BY id DESC LIMIT 1`, sessionID))
}

// LatestLimitHoldForTask is the task's newest hold, open or not, for its card.
func (db *DB) LatestLimitHoldForTask(taskID int64) (*LimitHold, error) {
	return scanLimitHold(db.QueryRow(`SELECT `+limitHoldCols+` FROM limit_holds
		WHERE task_id=? ORDER BY id DESC LIMIT 1`, taskID))
}

// OpenLimitHolds lists every unresolved hold, oldest first.
func (db *DB) OpenLimitHolds() ([]*LimitHold, error) {
	return db.limitHolds(`resolved_at IS NULL ORDER BY id`)
}

// RecentLimitHolds lists the newest holds, open or resolved.
func (db *DB) RecentLimitHolds(limit int) ([]*LimitHold, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	return db.limitHolds(`1=1 ORDER BY id DESC LIMIT ?`, limit)
}

// InsertLimitHold records a new hold. At most one hold per session or attempt
// may be open (a partial unique index enforces it), so a detection that races
// another one returns the hold that won instead of an error.
func (db *DB) InsertLimitHold(h *LimitHold) (*LimitHold, bool, error) {
	now := Now()
	res, err := db.Exec(`INSERT INTO limit_holds(session_id, task_id, attempt_id, agent, source,
		pattern, message, detected_at, reset_at, policy, state, due_at, note, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		h.SessionID, h.TaskID, h.AttemptID, h.Agent, h.Source, h.Pattern, h.Message,
		nzFloat(h.DetectedAt, now), h.ResetAt, nz(h.Policy, "notify"), nz(h.State, "waiting"),
		h.DueAt, h.Note, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			var existing *LimitHold
			var ferr error
			if h.SessionID != nil {
				existing, ferr = db.OpenLimitHoldForSession(*h.SessionID)
			} else if h.AttemptID != nil {
				existing, ferr = db.OpenLimitHoldForAttempt(*h.AttemptID)
			} else {
				ferr = err
			}
			return existing, false, ferr
		}
		return nil, false, err
	}
	id, _ := res.LastInsertId()
	fresh, err := db.LimitHold(id)
	return fresh, true, err
}

// TransitionLimitHold is the compare-and-swap every automatic action goes
// through: it applies fields only if the hold is still open and still in
// fromState, and reports whether this caller won. Two Lectern processes, or
// one process before and after a restart, can therefore never both act on the
// same hold state.
func (db *DB) TransitionLimitHold(id int64, fromState string, fields map[string]any) (bool, error) {
	if len(fields) == 0 {
		return false, nil
	}
	fields["updated_at"] = Now()
	sets := make([]string, 0, len(fields))
	args := make([]any, 0, len(fields)+2)
	for k, v := range fields {
		sets = append(sets, k+"=?")
		args = append(args, v)
	}
	args = append(args, id, fromState)
	res, err := db.Exec(`UPDATE limit_holds SET `+strings.Join(sets, ", ")+
		` WHERE id=? AND state=? AND resolved_at IS NULL`, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// LimitPolicyJSON is the stored policy for one scope, or "" when none is set.
func (db *DB) LimitPolicyJSON(scope string, scopeID int64) string {
	var raw string
	if err := db.QueryRow(`SELECT policy_json FROM limit_policies WHERE scope=? AND scope_id=?`,
		scope, scopeID).Scan(&raw); err != nil {
		return ""
	}
	return raw
}

// SetLimitPolicyJSON stores (or, with an empty value, clears) one scope's policy.
func (db *DB) SetLimitPolicyJSON(scope string, scopeID int64, raw string) error {
	if strings.TrimSpace(raw) == "" {
		_, err := db.Exec(`DELETE FROM limit_policies WHERE scope=? AND scope_id=?`, scope, scopeID)
		return err
	}
	_, err := db.Exec(`INSERT INTO limit_policies(scope, scope_id, policy_json, updated_at)
		VALUES(?,?,?,?) ON CONFLICT(scope, scope_id) DO UPDATE SET
		policy_json=excluded.policy_json, updated_at=excluded.updated_at`, scope, scopeID, raw, Now())
	return err
}

func nzFloat(v, def float64) float64 {
	if v == 0 {
		return def
	}
	return v
}
