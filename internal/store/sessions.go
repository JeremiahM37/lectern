package store

import (
	"database/sql"
	"errors"
)

const sessionCols = `s.id, s.project_id, s.target_id, s.name, s.agent, s.model,
	s.workdir, s.tmux_session, s.status, s.origin, s.pane_hash, s.pane_tail,
	s.context_pct, s.last_activity_at, s.created_at, s.updated_at, s.ended_at, s.worktree_json, s.group_path, s.tracking_identity, s.resume_id, s.native_recovery_cid, s.boot_id, s.archived_at, s.launch_config_json, s.setup_state, s.setup_error, s.setup_cancel_requested,
	s.hook_token, s.permission_mode, s.agent_state, s.state_source, s.state_at, s.hook_seen_at,
	s.context_used_pct, s.context_tokens, s.context_size, s.cost_usd, s.lines_added, s.lines_removed,
	s.rate_5h_pct, s.rate_5h_reset, s.rate_7d_pct, s.rate_7d_reset, s.usage_at, s.codex_thread_id, s.precompact_at,
	s.repo_key, s.repo_toplevel, s.awareness_briefing_hash, s.awareness_briefing_at,
	s.last_prompt_excerpt, s.last_prompt_at, s.otel_active_at, s.end_reason, s.agent_exited_at, s.relaunched_at, s.resume_guess, s.account_id, s.session_backend`

func scanSession(sc interface{ Scan(...any) error }, withJoin bool) (*Session, error) {
	var s Session
	dest := []any{&s.ID, &s.ProjectID, &s.TargetID, &s.Name, &s.Agent, &s.Model,
		&s.Workdir, &s.TmuxSession, &s.Status, &s.Origin, &s.PaneHash, &s.PaneTail,
		&s.ContextPct, &s.LastActivityAt, &s.CreatedAt, &s.UpdatedAt, &s.EndedAt, &s.WorktreeJSON, &s.GroupPath, &s.TrackingIdentity, &s.ResumeID, &s.NativeRecoveryCID, &s.BootID, &s.ArchivedAt, &s.LaunchConfigJSON, &s.SetupState, &s.SetupError, &s.SetupCancelRequested,
		&s.HookToken, &s.PermissionMode, &s.AgentState, &s.StateSource, &s.StateAt, &s.HookSeenAt,
		&s.ContextUsedPct, &s.ContextTokens, &s.ContextSize, &s.CostUSD, &s.LinesAdded, &s.LinesRemoved,
		&s.Rate5hPct, &s.Rate5hReset, &s.Rate7dPct, &s.Rate7dReset, &s.UsageAt, &s.CodexThreadID, &s.PrecompactAt,
		&s.RepoKey, &s.RepoToplevel, &s.AwarenessBriefingHash, &s.AwarenessBriefingAt,
		&s.LastPromptExcerpt, &s.LastPromptAt, &s.OtelActiveAt, &s.EndReason, &s.AgentExitedAt, &s.RelaunchedAt, &s.ResumeGuess, &s.AccountID, &s.SessionBackend}
	if withJoin {
		var projectName sql.NullString
		dest = append(dest, &projectName, &s.TargetName, &s.TargetKind)
		if err := sc.Scan(dest...); err != nil {
			return nil, err
		}
		s.ProjectName = projectName.String
		return &s, nil
	}
	return &s, sc.Scan(dest...)
}

const sessionJoin = `FROM sessions s
	LEFT JOIN projects p ON p.id=s.project_id
	JOIN targets t ON t.id=s.target_id`

// Sessions lists sessions newest first, joined with their project and target.
// `includeEnded` brings back the ones whose process is gone — the record is the
// point, so a dead session is still worth showing until it is dismissed.
func (db *DB) Sessions(includeEnded bool) ([]*Session, error) {
	q := `SELECT ` + sessionCols + `, p.name, t.name, t.kind ` + sessionJoin
	if !includeEnded {
		q += ` WHERE s.ended_at IS NULL`
	}
	q += ` ORDER BY s.created_at DESC`
	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Session{}
	for rows.Next() {
		s, err := scanSession(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Session fetches one session by id.
func (db *DB) Session(id int64) (*Session, error) {
	s, err := scanSession(db.QueryRow(`SELECT `+sessionCols+`, p.name, t.name, t.kind `+
		sessionJoin+` WHERE s.id=?`, id), true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return s, err
}

// LiveSessions are the ones the poller still has to watch.
func (db *DB) LiveSessions() ([]*Session, error) {
	rows, err := db.Query(`SELECT ` + sessionCols + ` FROM sessions s WHERE s.ended_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Session{}
	for rows.Next() {
		s, err := scanSession(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RecentClosedSessions returns the newest ended, non-archived interactive
// records. Ordering by ended_at preserves the operator's actual recent history;
// session IDs are allocation order and can be misleading after long uptime.
func (db *DB) RecentClosedSessions(limit int) ([]*Session, error) {
	if limit < 1 {
		return []*Session{}, nil
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := db.Query(`SELECT `+sessionCols+`, p.name, t.name, t.kind `+
		sessionJoin+` WHERE s.ended_at IS NOT NULL AND s.archived_at IS NULL
		ORDER BY s.ended_at DESC, s.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Session{}
	for rows.Next() {
		s, err := scanSession(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RestorableSessions returns every record Restore can offer, newest first:
// ended ones (archived included) except launches that never started, and live
// rows a restart left interrupted. A row from before end_reason existed that
// never showed a screen or bound a conversation is a launch that never
// started, and is left out too.
func (db *DB) RestorableSessions(limit int) ([]*Session, error) {
	if limit < 1 {
		return []*Session{}, nil
	}
	rows, err := db.Query(`SELECT `+sessionCols+`, p.name, t.name, t.kind `+
		sessionJoin+` WHERE (s.ended_at IS NOT NULL AND s.end_reason<>'failed' AND s.setup_state<>'failed'
			AND NOT (s.end_reason='' AND s.archived_at IS NULL AND s.pane_hash='' AND s.resume_id='' AND s.native_recovery_cid=''))
		OR (s.ended_at IS NULL AND s.status='interrupted')
		ORDER BY COALESCE(s.ended_at, s.updated_at) DESC, s.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Session{}
	for rows.Next() {
		s, err := scanSession(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ConversationRef is a session's bound native conversation, for telling which
// closed records a later session already continued.
type ConversationRef struct {
	ID, TargetID int64
	Agent        string
	CIDs         []string
}

// ConversationRefs lists every session with a bound native conversation.
func (db *DB) ConversationRefs() ([]ConversationRef, error) {
	rows, err := db.Query(`SELECT id, target_id, agent, resume_id, native_recovery_cid FROM sessions
		WHERE resume_id<>'' OR native_recovery_cid<>''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConversationRef{}
	for rows.Next() {
		var ref ConversationRef
		var resume, native string
		if err := rows.Scan(&ref.ID, &ref.TargetID, &ref.Agent, &resume, &native); err != nil {
			return nil, err
		}
		for _, cid := range []string{native, resume} {
			if cid != "" {
				ref.CIDs = append(ref.CIDs, cid)
			}
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// RelaunchedSessions lists sessions restart recovery relaunched after since,
// newest first.
func (db *DB) RelaunchedSessions(since float64) ([]*Session, error) {
	rows, err := db.Query(`SELECT `+sessionCols+`, p.name, t.name, t.kind `+
		sessionJoin+` WHERE s.relaunched_at IS NOT NULL AND s.relaunched_at > ?
		ORDER BY s.relaunched_at DESC, s.id DESC LIMIT 100`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Session{}
	for rows.Next() {
		s, err := scanSession(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ReopenedAs maps each record Restore replaced to the session it started.
func (db *DB) ReopenedAs() (map[int64]int64, error) {
	rows, err := db.Query(`SELECT id, reopened_as FROM sessions WHERE reopened_as IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var id, next int64
		if err := rows.Scan(&id, &next); err != nil {
			return nil, err
		}
		out[id] = next
	}
	return out, rows.Err()
}

// SessionByTmux finds a live session by its tmux name on a target. Discovery
// uses it to tell "already adopted" from "new to us".
func (db *DB) SessionByTmux(targetID int64, tmuxName string) (*Session, error) {
	s, err := scanSession(db.QueryRow(`SELECT `+sessionCols+` FROM sessions s
		WHERE s.target_id=? AND s.tmux_session=? AND s.ended_at IS NULL`,
		targetID, tmuxName), false)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return s, err
}

// InsertSession writes a new session row.
func (db *DB) InsertSession(s *Session) (*Session, error) {
	now := Now()
	res, err := db.Exec(`INSERT INTO sessions(project_id, target_id, name, agent, model,
		workdir, tmux_session, status, origin, last_activity_at, created_at, updated_at, group_path, resume_id, native_recovery_cid, boot_id, session_backend)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.ProjectID, s.TargetID, s.Name, nz(s.Agent, "claude"), s.Model, s.Workdir,
		s.TmuxSession, nz(s.Status, "starting"), nz(s.Origin, "lectern"), now, now, now, s.GroupPath, s.ResumeID, s.NativeRecoveryCID, s.BootID, s.SessionBackend)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return db.Session(id)
}

// Wraps lists a project's handoffs, newest first — the thread of a long project
// across every context that has worked on it.
func (db *DB) Wraps(projectID int64, limit int) ([]*Wrap, error) {
	rows, err := db.Query(`SELECT id, session_id, project_id, summary, next_session_id,
		created_at FROM session_wraps WHERE project_id=? ORDER BY id DESC LIMIT ?`,
		projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Wrap{}
	for rows.Next() {
		var w Wrap
		if err := rows.Scan(&w.ID, &w.SessionID, &w.ProjectID, &w.Summary,
			&w.NextSessionID, &w.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &w)
	}
	return out, rows.Err()
}

// SessionWraps lists the handoffs a single session produced.
func (db *DB) SessionWraps(sessionID int64) ([]*Wrap, error) {
	rows, err := db.Query(`SELECT id, session_id, project_id, summary, next_session_id,
		created_at FROM session_wraps WHERE session_id=? ORDER BY id DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Wrap{}
	for rows.Next() {
		var w Wrap
		if err := rows.Scan(&w.ID, &w.SessionID, &w.ProjectID, &w.Summary,
			&w.NextSessionID, &w.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &w)
	}
	return out, rows.Err()
}

// InsertWrap records a handoff summary.
func (db *DB) InsertWrap(w *Wrap) (int64, error) {
	res, err := db.Exec(`INSERT INTO session_wraps(session_id, project_id, summary,
		transcript, next_session_id, created_at) VALUES(?,?,?,?,?,?)`,
		w.SessionID, w.ProjectID, w.Summary, w.Transcript, w.NextSessionID, Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// WrapPredecessor returns the newest wrap that started this session, so a
// successor can point back at the conversation that handed the work over.
func (db *DB) WrapPredecessor(sessionID int64) (*Wrap, error) {
	row := db.QueryRow(`SELECT id, session_id, project_id, summary, next_session_id,
		created_at FROM session_wraps WHERE next_session_id=? ORDER BY id DESC LIMIT 1`, sessionID)
	var w Wrap
	err := row.Scan(&w.ID, &w.SessionID, &w.ProjectID, &w.Summary, &w.NextSessionID, &w.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// ScratchSession is the little the scratch sweep needs from a session row: who
// claims a directory, whether they still do, and whether a conversation was
// ever recorded there.
type ScratchSession struct {
	ID        int64
	TargetID  int64
	Name      string
	Agent     string
	Workdir   string
	ProjectID *int64
	EndedAt   *float64
	LastSeen  float64
	ResumeID  string
	NativeCID string
}

// ScratchSessions lists every session, ended ones included: an ended session is
// what makes a directory's history knowable.
func (db *DB) ScratchSessions() ([]*ScratchSession, error) {
	rows, err := db.Query(`SELECT id,target_id,name,agent,workdir,project_id,ended_at,
 MAX(COALESCE(ended_at,0),COALESCE(updated_at,0),COALESCE(last_activity_at,0),COALESCE(created_at,0)),
 COALESCE(resume_id,''),COALESCE(native_recovery_cid,'') FROM sessions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ScratchSession{}
	for rows.Next() {
		s := new(ScratchSession)
		if err := rows.Scan(&s.ID, &s.TargetID, &s.Name, &s.Agent, &s.Workdir, &s.ProjectID, &s.EndedAt,
			&s.LastSeen, &s.ResumeID, &s.NativeCID); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
