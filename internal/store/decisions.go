package store

import (
	"database/sql"
	"encoding/json"
)

// ---- decisions ------------------------------------------------------------
//
// See schema.go's decisions table comment and internal/decisionlog, which
// decides what a row means. This file is only the store half.

// Decision is one recorded decision. Pointer fields are NULL in the table
// when there is no value: a reply has no session when none was matched, an
// interrupt has no waited_since. Detail is JSON of lengths, names and counts.
type Decision struct {
	ID             int64           `json:"id"`
	At             float64         `json:"at"`
	Kind           string          `json:"kind"`
	Decision       string          `json:"decision"`
	Via            string          `json:"via"`
	SessionID      *int64          `json:"session_id"`
	AttemptID      *int64          `json:"attempt_id"`
	ProjectID      *int64          `json:"project_id"`
	Agent          string          `json:"agent"`
	WaitedSince    *float64        `json:"waited_since"`
	LatencyS       *float64        `json:"latency_s"`
	ActiveSessions int             `json:"active_sessions"`
	Pending        int             `json:"pending"`
	Detail         json.RawMessage `json:"detail"`
}

const decisionCols = `id, at, kind, decision, via, session_id, attempt_id, project_id,
	agent, waited_since, latency_s, active_sessions, pending, detail_json`

func scanDecision(s interface{ Scan(...any) error }) (*Decision, error) {
	var d Decision
	var sid, aid, pid sql.NullInt64
	var waited, latency sql.NullFloat64
	var detail string
	err := s.Scan(&d.ID, &d.At, &d.Kind, &d.Decision, &d.Via, &sid, &aid, &pid,
		&d.Agent, &waited, &latency, &d.ActiveSessions, &d.Pending, &detail)
	if err != nil {
		return nil, err
	}
	if sid.Valid {
		d.SessionID = &sid.Int64
	}
	if aid.Valid {
		d.AttemptID = &aid.Int64
	}
	if pid.Valid {
		d.ProjectID = &pid.Int64
	}
	if waited.Valid {
		d.WaitedSince = &waited.Float64
	}
	if latency.Valid {
		d.LatencyS = &latency.Float64
	}
	d.Detail = json.RawMessage(nz(detail, "{}"))
	return &d, nil
}

// nullID turns a zero id into SQL NULL, so "no session" is stored as NULL and
// not as a 0 that would look like a real foreign key.
func nullID(v *int64) any {
	if v == nil || *v == 0 {
		return nil
	}
	return *v
}

// nullFloat turns a nil pointer into SQL NULL.
func nullFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

// InsertDecision records one decision. At defaults to now.
func (db *DB) InsertDecision(d *Decision) (int64, error) {
	if d.At == 0 {
		d.At = Now()
	}
	res, err := db.Exec(`INSERT INTO decisions(at, kind, decision, via, session_id, attempt_id,
		project_id, agent, waited_since, latency_s, active_sessions, pending, detail_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.At, d.Kind, d.Decision, d.Via, nullID(d.SessionID), nullID(d.AttemptID), nullID(d.ProjectID),
		d.Agent, nullFloat(d.WaitedSince), nullFloat(d.LatencyS), d.ActiveSessions, d.Pending,
		nz(string(d.Detail), "{}"))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Decisions lists decisions at or after since, newest first. An empty kind
// matches every kind; limit <= 0 returns all of them.
func (db *DB) Decisions(since float64, kind string, limit int) ([]*Decision, error) {
	if limit <= 0 {
		limit = -1 // SQLite: a negative LIMIT means no limit
	}
	rows, err := db.Query(`SELECT `+decisionCols+` FROM decisions
		WHERE at >= ? AND (? = '' OR kind = ?)
		ORDER BY at DESC, id DESC LIMIT ?`, since, kind, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Decision{}
	for rows.Next() {
		d, err := scanDecision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
