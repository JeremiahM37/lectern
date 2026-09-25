package store

import (
	"database/sql"
	"errors"
	"strings"
)

// TriggerSource is one inbound connection a project watches for work to pick
// up on its own: a GitHub repo, a Slack workspace, or a Linear team. See
// internal/triggers for the polling/socket engines and internal/api/triggers.go
// for the HTTP surface.
type TriggerSource struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id"`
	Kind      string `json:"kind"` // github | slack | linear
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	// ConfigJSON is the non-secret, kind-specific shape (repo, label, mention
	// handle, allowed authors, ...) — safe to return from the API, but the
	// API layer parses it into "config" on triggerSourceView instead of
	// exposing the raw string a second time.
	ConfigJSON string `json:"-"`
	// SecretsJSON holds tokens/keys. Never serialize this field raw to the
	// API — internal/api/triggers.go redacts it, the same pattern
	// Project.MCPJSON already uses for MCP server credentials.
	SecretsJSON string   `json:"-"`
	IntervalS   int      `json:"interval_s"`
	CursorJSON  string   `json:"-"`
	Status      string   `json:"status"`
	LastPollAt  *float64 `json:"last_poll_at"`
	LastError   string   `json:"last_error"`
	CreatedAt   float64  `json:"created_at"`
	UpdatedAt   float64  `json:"updated_at"`
}

const triggerSourceCols = `id, project_id, kind, name, enabled, config_json,
	secrets_json, interval_s, cursor_json, status, last_poll_at, last_error,
	created_at, updated_at`

func scanTriggerSource(sc interface{ Scan(...any) error }) (*TriggerSource, error) {
	var s TriggerSource
	var enabled int
	if err := sc.Scan(&s.ID, &s.ProjectID, &s.Kind, &s.Name, &enabled, &s.ConfigJSON,
		&s.SecretsJSON, &s.IntervalS, &s.CursorJSON, &s.Status, &s.LastPollAt,
		&s.LastError, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	s.Enabled = enabled == 1
	return &s, nil
}

// TriggerSources lists every configured source for one project, newest first.
func (db *DB) TriggerSources(projectID int64) ([]*TriggerSource, error) {
	rows, err := db.Query(`SELECT `+triggerSourceCols+`
		FROM trigger_sources WHERE project_id=? ORDER BY id DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TriggerSource{}
	for rows.Next() {
		s, err := scanTriggerSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// AllTriggerSources lists every configured source across every project — what
// the poller ticks over.
func (db *DB) AllTriggerSources() ([]*TriggerSource, error) {
	rows, err := db.Query(`SELECT ` + triggerSourceCols + ` FROM trigger_sources ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TriggerSource{}
	for rows.Next() {
		s, err := scanTriggerSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// TriggerSource fetches one source by id.
func (db *DB) TriggerSource(id int64) (*TriggerSource, error) {
	s, err := scanTriggerSource(db.QueryRow(`SELECT `+triggerSourceCols+`
		FROM trigger_sources WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return s, err
}

// InsertTriggerSource saves a new source.
func (db *DB) InsertTriggerSource(s *TriggerSource) (*TriggerSource, error) {
	now := Now()
	s.CreatedAt, s.UpdatedAt = now, now
	res, err := db.Exec(`INSERT INTO trigger_sources
		(project_id, kind, name, enabled, config_json, secrets_json, interval_s,
		 cursor_json, status, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		s.ProjectID, s.Kind, s.Name, boolInt(s.Enabled), nz(s.ConfigJSON, "{}"),
		nz(s.SecretsJSON, "{}"), s.IntervalS, nz(s.CursorJSON, "{}"),
		nz(s.Status, "unconfigured"), s.CreatedAt, s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return db.TriggerSource(id)
}

// DeleteTriggerSource removes a source and its event history.
func (db *DB) DeleteTriggerSource(id int64) error {
	if _, err := db.Exec(`DELETE FROM trigger_events WHERE source_id=?`, id); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM trigger_sources WHERE id=?`, id)
	return err
}

// RecordPoll updates a source's polling state after one poll attempt: the
// cursor it should resume from, whether it succeeded, and why if not.
func (db *DB) RecordPoll(id int64, cursorJSON, status, lastError string) error {
	return db.Update("trigger_sources", id, map[string]any{
		"cursor_json": nz(cursorJSON, "{}"), "status": status, "last_error": lastError,
		"last_poll_at": Now(), "updated_at": Now(),
	})
}

// TriggerEvent is one inbound event a source saw — matched (and possibly a
// task) or not (and why). This is the ledger dedup reads and the audit trail
// the settings UI shows as "recent events".
type TriggerEvent struct {
	ID             int64   `json:"id"`
	SourceID       int64   `json:"source_id"`
	ProjectID      int64   `json:"project_id"`
	ExternalID     string  `json:"external_id"`
	Kind           string  `json:"kind"`
	Author         string  `json:"author"`
	Summary        string  `json:"summary"`
	Action         string  `json:"action"` // task_created | skipped | ignored
	Reason         string  `json:"reason"`
	TaskID         *int64  `json:"task_id"`
	PostbackStatus string  `json:"postback_status"`
	PostbackNote   string  `json:"postback_note"`
	RawJSON        string  `json:"-"`
	CreatedAt      float64 `json:"created_at"`
}

const triggerEventCols = `id, source_id, project_id, external_id, kind, author,
	summary, action, reason, task_id, postback_status, postback_note, raw_json,
	created_at`

func scanTriggerEvent(sc interface{ Scan(...any) error }) (*TriggerEvent, error) {
	var e TriggerEvent
	if err := sc.Scan(&e.ID, &e.SourceID, &e.ProjectID, &e.ExternalID, &e.Kind,
		&e.Author, &e.Summary, &e.Action, &e.Reason, &e.TaskID, &e.PostbackStatus,
		&e.PostbackNote, &e.RawJSON, &e.CreatedAt); err != nil {
		return nil, err
	}
	return &e, nil
}

// ErrDuplicateEvent is returned by InsertTriggerEvent when this source has
// already recorded this external id — the whole point of the ledger: an
// event is never acted on twice.
var ErrDuplicateEvent = errors.New("event already recorded")

// InsertTriggerEvent records one seen event. A second insert of the same
// (source_id, external_id) pair returns ErrDuplicateEvent instead of a row —
// dedup enforced by the database, not by a caller remembering to check first.
func (db *DB) InsertTriggerEvent(e *TriggerEvent) (*TriggerEvent, error) {
	e.CreatedAt = Now()
	res, err := db.Exec(`INSERT INTO trigger_events
		(source_id, project_id, external_id, kind, author, summary, action,
		 reason, task_id, postback_status, postback_note, raw_json, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.SourceID, e.ProjectID, e.ExternalID, e.Kind, e.Author, e.Summary,
		e.Action, e.Reason, e.TaskID, e.PostbackStatus, e.PostbackNote,
		nz(e.RawJSON, "{}"), e.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, ErrDuplicateEvent
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	ev, err := scanTriggerEvent(db.QueryRow(`SELECT `+triggerEventCols+`
		FROM trigger_events WHERE id=?`, id))
	return ev, err
}

// ReserveTriggerEvent claims (source_id, external_id) before any decision is
// made about it, so a redelivery is refused before it can ever cause a
// second CreateTask call — not just recorded after the fact. Returns the
// reserved row's id, or ErrDuplicateEvent if another call already claimed
// it. Callers finish the row with FinalizeTriggerEvent once they know the
// outcome.
func (db *DB) ReserveTriggerEvent(sourceID, projectID int64, externalID, kind, author, summary, rawJSON string) (int64, error) {
	now := Now()
	res, err := db.Exec(`INSERT INTO trigger_events
		(source_id, project_id, external_id, kind, author, summary, action,
		 reason, postback_status, postback_note, raw_json, created_at)
		VALUES(?,?,?,?,?,?,'pending','','','',?,?)`,
		sourceID, projectID, externalID, kind, author, summary, nz(rawJSON, "{}"), now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return 0, ErrDuplicateEvent
		}
		return 0, err
	}
	return res.LastInsertId()
}

// FinalizeTriggerEvent records the outcome of a reserved event: what
// happened (action/reason) and, when a task was created, its id.
func (db *DB) FinalizeTriggerEvent(id int64, action, reason string, taskID *int64) error {
	return db.Update("trigger_events", id, map[string]any{
		"action": action, "reason": reason, "task_id": taskID,
	})
}

// TriggerEvent fetches one event by id.
func (db *DB) TriggerEvent(id int64) (*TriggerEvent, error) {
	e, err := scanTriggerEvent(db.QueryRow(`SELECT `+triggerEventCols+`
		FROM trigger_events WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return e, err
}

// RecentTriggerEvents lists a project's last N events across all its sources,
// newest first — what the settings UI shows next to each source.
func (db *DB) RecentTriggerEvents(projectID int64, limit int) ([]*TriggerEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := db.Query(`SELECT `+triggerEventCols+`
		FROM trigger_events WHERE project_id=? ORDER BY id DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TriggerEvent{}
	for rows.Next() {
		e, err := scanTriggerEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EventsInWindow counts this source's task_created events since `since` — the
// per-source rate limit's own counter.
func (db *DB) EventsInWindow(sourceID int64, since float64) (int, error) {
	return db.Count("trigger_events",
		"source_id=? AND action='task_created' AND created_at>=?", sourceID, since)
}

// PendingPostbackEvents lists events that created a task which has since left
// queued/running, but nothing has been posted back to the source yet.
func (db *DB) PendingPostbackEvents() ([]*TriggerEvent, error) {
	rows, err := db.Query(`SELECT ` + triggerEventCols + ` FROM trigger_events
		WHERE task_id IS NOT NULL AND postback_status=''
		AND task_id IN (SELECT id FROM tasks WHERE status IN ('review','done','failed','cancelled'))
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TriggerEvent{}
	for rows.Next() {
		e, err := scanTriggerEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkPostback records the result of posting a trigger-created task's outcome
// back to its source (a PR link, a comment reply, or why it could not send).
func (db *DB) MarkPostback(id int64, status, note string) error {
	return db.Update("trigger_events", id, map[string]any{
		"postback_status": status, "postback_note": note,
	})
}
