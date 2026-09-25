package store

// ---- memory delivery log --------------------------------------------------
//
// See schema.go's memory_deliveries table comment and docs/memory-visibility.md,
// which owns the explanation. This file is just the store half: write one
// delivery, read a run's deliveries back newest first.

// MemoryDelivery is one time lectern injected project memory into a session or
// a task attempt. SessionID and TaskID/AttemptID are pointers because a row is
// exactly one of the two and the other is genuinely absent.
type MemoryDelivery struct {
	ID        int64   `json:"id"`
	SessionID *int64  `json:"session_id"`
	TaskID    *int64  `json:"task_id"`
	AttemptID *int64  `json:"attempt_id"`
	At        float64 `json:"at"`
	Mode      string  `json:"mode"`
	// Bytes is the byte length of the context block that was injected — the
	// same bound Automatic applies, not a model token count.
	Bytes int `json:"bytes"`
	// ItemsJSON is the provider's own list of what went out. Kept as text here
	// and parsed at the API boundary so a malformed blob can only degrade the
	// Memory section, never the read that would have shown it.
	ItemsJSON string `json:"-"`
}

// InsertMemoryDelivery records one delivery and returns the stored row.
func (db *DB) InsertMemoryDelivery(d MemoryDelivery) (*MemoryDelivery, error) {
	if d.At == 0 {
		d.At = Now()
	}
	if d.ItemsJSON == "" {
		d.ItemsJSON = "[]"
	}
	res, err := db.Exec(`INSERT INTO memory_deliveries(session_id, task_id, attempt_id, at, mode, bytes, items_json)
		VALUES(?,?,?,?,?,?,?)`,
		d.SessionID, d.TaskID, d.AttemptID, d.At, d.Mode, d.Bytes, d.ItemsJSON)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	d.ID = id
	return &d, nil
}

// MemoryDeliveriesForSession lists what a session's agent was given, newest
// first. A limit of zero or less means no limit.
func (db *DB) MemoryDeliveriesForSession(sessionID int64, limit int) ([]*MemoryDelivery, error) {
	return db.memoryDeliveries(`WHERE session_id=?`, []any{sessionID}, limit)
}

// MemoryDeliveriesForTask lists what every attempt of one task was given,
// newest first. The attempt each delivery went to is on the row, so an
// operator looking at a re-dispatched task can still tell them apart.
func (db *DB) MemoryDeliveriesForTask(taskID int64, limit int) ([]*MemoryDelivery, error) {
	return db.memoryDeliveries(`WHERE task_id=?`, []any{taskID}, limit)
}

func (db *DB) memoryDeliveries(where string, args []any, limit int) ([]*MemoryDelivery, error) {
	query := `SELECT id, session_id, task_id, attempt_id, at, mode, bytes, items_json
		FROM memory_deliveries ` + where + ` ORDER BY at DESC, id DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*MemoryDelivery{}
	for rows.Next() {
		var row MemoryDelivery
		if err := rows.Scan(&row.ID, &row.SessionID, &row.TaskID, &row.AttemptID,
			&row.At, &row.Mode, &row.Bytes, &row.ItemsJSON); err != nil {
			return nil, err
		}
		out = append(out, &row)
	}
	return out, rows.Err()
}
