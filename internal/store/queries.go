package store

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ---- targets ----------------------------------------------------------------

const targetCols = `id, name, kind, host, port, user, key_path, workroot,
	max_concurrent, sandbox, status, info_json, context_json, memory_dir,
	command_prefix, created_at`

func scanTarget(s interface{ Scan(...any) error }) (*Target, error) {
	var t Target
	err := s.Scan(&t.ID, &t.Name, &t.Kind, &t.Host, &t.Port, &t.User, &t.KeyPath,
		&t.Workroot, &t.MaxConcurrent, &t.Sandbox, &t.Status, &t.InfoJSON,
		&t.ContextJSON, &t.MemoryDir, &t.CommandPrefix, &t.CreatedAt)
	return &t, err
}

// Targets lists every target, name-ordered — the order the UI renders.
func (db *DB) Targets() ([]*Target, error) {
	rows, err := db.Query(`SELECT ` + targetCols + ` FROM targets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Target{}
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Target fetches one target by id.
func (db *DB) Target(id int64) (*Target, error) {
	t, err := scanTarget(db.QueryRow(`SELECT `+targetCols+` FROM targets WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// TargetByName fetches one target by its unique name.
func (db *DB) TargetByName(name string) (*Target, error) {
	t, err := scanTarget(db.QueryRow(`SELECT `+targetCols+` FROM targets WHERE name=?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// InsertTarget writes a new target and returns it with its assigned id.
func (db *DB) InsertTarget(t *Target) (*Target, error) {
	res, err := db.Exec(`INSERT INTO targets(name, kind, host, port, user, key_path,
		workroot, max_concurrent, sandbox, status, info_json, context_json, memory_dir,
		command_prefix, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.Name, t.Kind, t.Host, t.Port, t.User, t.KeyPath, t.Workroot,
		t.MaxConcurrent, t.Sandbox, nz(t.Status, "unknown"), nz(t.InfoJSON, "{}"),
		nz(t.ContextJSON, "[]"), t.MemoryDir, t.CommandPrefix, Now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return db.Target(id)
}

// ---- projects ---------------------------------------------------------------

const projectCols = `p.id, p.name, p.target_id, p.repo_path, p.default_base_branch,
	p.workroot_override, p.policy_json, p.verify_cmd, p.keep_worktrees, p.review_gate,
	p.env_json, p.context_json, p.mcp_json, p.strict_mcp, p.permissions_json,
	p.setup_cmd, p.gate_matcher, p.default_agent, p.capability_profile, p.default_permission_mode,
	p.skill_sources_json, p.memory_topic, p.memory_status,
	p.created_at`

func scanProject(s interface{ Scan(...any) error }, withTarget bool) (*Project, error) {
	var p Project
	dest := []any{&p.ID, &p.Name, &p.TargetID, &p.RepoPath, &p.DefaultBaseBranch,
		&p.WorkrootOverride, &p.PolicyJSON, &p.VerifyCmd, &p.KeepWorktrees,
		&p.ReviewGate, &p.EnvJSON, &p.ContextJSON, &p.MCPJSON, &p.StrictMCP,
		&p.PermissionsJSON, &p.SetupCmd, &p.GateMatcher, &p.DefaultAgent, &p.CapabilityProfile,
		&p.DefaultPermissionMode, &p.SkillSourcesJSON, &p.MemoryTopic, &p.MemoryStatus, &p.CreatedAt}
	if withTarget {
		dest = append(dest, &p.TargetName, &p.TargetKind)
	}
	return &p, s.Scan(dest...)
}

// Projects lists projects joined with their target's name and kind.
func (db *DB) Projects() ([]*Project, error) {
	rows, err := db.Query(`SELECT ` + projectCols + `, t.name, t.kind FROM projects p
		JOIN targets t ON t.id=p.target_id ORDER BY p.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Project{}
	for rows.Next() {
		p, err := scanProject(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Project fetches one project by id.
func (db *DB) Project(id int64) (*Project, error) {
	p, err := scanProject(db.QueryRow(`SELECT `+projectCols+` FROM projects p WHERE p.id=?`, id), false)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// ProjectForTask resolves the project a task belongs to.
func (db *DB) ProjectForTask(taskID int64) (*Project, error) {
	p, err := scanProject(db.QueryRow(`SELECT `+projectCols+` FROM projects p
		JOIN tasks t ON t.project_id=p.id WHERE t.id=?`, taskID), false)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// ProjectForAttempt resolves the project an attempt is running for.
func (db *DB) ProjectForAttempt(attemptID int64) (*Project, error) {
	p, err := scanProject(db.QueryRow(`SELECT `+projectCols+` FROM projects p
		JOIN tasks t ON t.project_id=p.id JOIN attempts a ON a.task_id=t.id
		WHERE a.id=?`, attemptID), false)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// InsertProject writes a new project and returns it with its assigned id.
func (db *DB) InsertProject(p *Project) (*Project, error) {
	if p.MemoryTopic == "" {
		identity := make([]byte, 16)
		if _, err := rand.Read(identity); err != nil {
			return nil, err
		}
		p.MemoryTopic = fmt.Sprintf("lectern-%x", identity)
		p.MemoryStatus = "pending"
	}
	res, err := db.Exec(`INSERT INTO projects(name, target_id, repo_path,
		default_base_branch, workroot_override, policy_json, verify_cmd, keep_worktrees,
		review_gate, env_json, context_json, mcp_json, strict_mcp, permissions_json,
		setup_cmd, gate_matcher, default_agent, capability_profile, default_permission_mode,
		skill_sources_json, memory_topic, memory_status, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.Name, p.TargetID, p.RepoPath, nz(p.DefaultBaseBranch, "main"),
		p.WorkrootOverride, nz(p.PolicyJSON, "{}"), p.VerifyCmd, p.KeepWorktrees,
		p.ReviewGate, nz(p.EnvJSON, "{}"), nz(p.ContextJSON, "[]"), nz(p.MCPJSON, "{}"),
		p.StrictMCP, nz(p.PermissionsJSON, "{}"), p.SetupCmd, p.GateMatcher,
		nz(p.DefaultAgent, "claude"), nz(p.CapabilityProfile, "restricted"),
		p.DefaultPermissionMode, nz(p.SkillSourcesJSON, "[]"), p.MemoryTopic, p.MemoryStatus, Now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return db.Project(id)
}

// ---- tasks ------------------------------------------------------------------

const taskCols = `id, project_id, title, prompt, status, priority, labels_json,
	agent, model, permission_mode, base_branch, parent_task_id, created_by,
	created_by_attempt, created_at, updated_at, check_command`

func scanTask(s interface{ Scan(...any) error }) (*Task, error) {
	var t Task
	err := s.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Prompt, &t.Status, &t.Priority,
		&t.LabelsJSON, &t.Agent, &t.Model, &t.PermissionMode, &t.BaseBranch,
		&t.ParentTaskID, &t.CreatedBy, &t.CreatedByAttempt, &t.CreatedAt, &t.UpdatedAt,
		&t.CheckCommand)
	return &t, err
}

// Task fetches one task by id.
func (db *DB) Task(id int64) (*Task, error) {
	t, err := scanTask(db.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// TaskFilter narrows the board query. A zero value lists everything.
type TaskFilter struct {
	Status    string
	ProjectID int64
}

// Tasks lists the board in render order: priority first, then most recent.
func (db *DB) Tasks(f TaskFilter) ([]*Task, error) {
	q := `SELECT ` + taskCols + ` FROM tasks`
	var conds []string
	var args []any
	if f.Status != "" {
		conds = append(conds, "status=?")
		args = append(args, f.Status)
	}
	if f.ProjectID != 0 {
		conds = append(conds, "project_id=?")
		args = append(args, f.ProjectID)
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY priority DESC, updated_at DESC"
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TasksWhere runs an arbitrary predicate against the tasks table. Used by the
// scheduler for the handful of queries that don't fit TaskFilter.
func (db *DB) TasksWhere(where string, args ...any) ([]*Task, error) {
	rows, err := db.Query(`SELECT `+taskCols+` FROM tasks WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// InsertTask writes a new task and returns it with its assigned id.
func (db *DB) InsertTask(t *Task) (*Task, error) {
	now := Now()
	res, err := db.Exec(`INSERT INTO tasks(project_id, title, prompt, status, priority,
		labels_json, agent, model, permission_mode, base_branch, parent_task_id,
		created_by, created_by_attempt, created_at, updated_at, check_command)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ProjectID, t.Title, t.Prompt, nz(t.Status, "backlog"), t.Priority,
		nz(t.LabelsJSON, "[]"), nz(t.Agent, "claude"), t.Model,
		nz(t.PermissionMode, "acceptEdits"), t.BaseBranch, t.ParentTaskID,
		nz(t.CreatedBy, "user"), t.CreatedByAttempt, now, now, t.CheckCommand)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return db.Task(id)
}

// ---- attempts ---------------------------------------------------------------

const attemptCols = `id, task_id, n, status, token, prompt, resume_session, model,
	sandbox_vmid, worktree_path, branch, tmux_session, session_id, log_offset,
	started_at, finished_at, exit_code, result_json, diff_stat_json, verify_json,
	mcp_json, strict_mcp, mcp_snapshot, launch_config_json, driver, agent, permission_mode`

func scanAttempt(s interface{ Scan(...any) error }) (*Attempt, error) {
	var a Attempt
	err := s.Scan(&a.ID, &a.TaskID, &a.N, &a.Status, &a.Token, &a.Prompt,
		&a.ResumeSession, &a.Model, &a.SandboxVMID, &a.WorktreePath, &a.Branch,
		&a.TmuxSession, &a.SessionID, &a.LogOffset, &a.StartedAt, &a.FinishedAt,
		&a.ExitCode, &a.ResultJSON, &a.DiffStatJSON, &a.VerifyJSON, &a.MCPJSON,
		&a.StrictMCP, &a.MCPSnapshot, &a.LaunchConfigJSON, &a.Driver, &a.Agent, &a.PermissionMode)
	return &a, err
}

// Attempt fetches one attempt by id.
func (db *DB) Attempt(id int64) (*Attempt, error) {
	a, err := scanAttempt(db.QueryRow(`SELECT `+attemptCols+` FROM attempts WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// AttemptByToken resolves the per-attempt hook token an agent presents.
func (db *DB) AttemptByToken(token string) (*Attempt, error) {
	a, err := scanAttempt(db.QueryRow(`SELECT `+attemptCols+` FROM attempts WHERE token=?`, token))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// AttemptsWhere runs an arbitrary predicate against the attempts table.
func (db *DB) AttemptsWhere(where string, args ...any) ([]*Attempt, error) {
	q := `SELECT ` + attemptCols + ` FROM attempts`
	if where != "" {
		q += " WHERE " + where
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Attempt{}
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// OneAttemptWhere returns the first matching attempt, or ErrNotFound.
func (db *DB) OneAttemptWhere(where string, args ...any) (*Attempt, error) {
	list, err := db.AttemptsWhere(where+" LIMIT 1", args...)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return list[0], nil
}

// LatestAttempt is the highest-numbered attempt of a task.
func (db *DB) LatestAttempt(taskID int64) (*Attempt, error) {
	return db.OneAttemptWhere("task_id=? ORDER BY n DESC", taskID)
}

// TaskAttempts lists a task's attempts oldest first.
func (db *DB) TaskAttempts(taskID int64) ([]*Attempt, error) {
	return db.AttemptsWhere("task_id=? ORDER BY n", taskID)
}

// InsertAttempt writes a new attempt and returns it with its assigned id.
func (db *DB) InsertAttempt(a *Attempt) (*Attempt, error) {
	res, err := db.Exec(`INSERT INTO attempts(task_id, n, status, token, prompt,
		resume_session, model, sandbox_vmid, worktree_path, branch, tmux_session,
		session_id, log_offset, started_at, finished_at, exit_code, result_json,
		diff_stat_json, verify_json, mcp_json, strict_mcp, mcp_snapshot, launch_config_json,
		driver, agent, permission_mode) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.TaskID, a.N, nz(a.Status, "queued"), a.Token, a.Prompt, a.ResumeSession,
		a.Model, a.SandboxVMID, a.WorktreePath, a.Branch, a.TmuxSession, a.SessionID,
		a.LogOffset, a.StartedAt, a.FinishedAt, a.ExitCode, nz(a.ResultJSON, "{}"),
		nz(a.DiffStatJSON, "{}"), nz(a.VerifyJSON, "{}"), nz(a.MCPJSON, "{}"), a.StrictMCP, a.MCPSnapshot, a.LaunchConfigJSON,
		a.Driver, a.Agent, a.PermissionMode)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return db.Attempt(id)
}

// ---- events -----------------------------------------------------------------

// InsertEvent appends one normalised agent event.
func (db *DB) InsertEvent(attemptID int64, seq int64, typ string, payloadJSON string) error {
	_, err := db.Exec(`INSERT INTO events(attempt_id, seq, ts, type, payload_json)
		VALUES(?,?,?,?,?)`, attemptID, seq, Now(), typ, payloadJSON)
	return err
}

// MaxEventSeq is the highest sequence number stored for an attempt.
func (db *DB) MaxEventSeq(attemptID int64) (int64, error) {
	var m sql.NullInt64
	err := db.QueryRow(`SELECT MAX(seq) FROM events WHERE attempt_id=?`, attemptID).Scan(&m)
	return m.Int64, err
}

// TaskEvents returns a task's timeline, optionally one attempt's slice of it and
// optionally only what follows a sequence number (SSE catch-up).
func (db *DB) TaskEvents(taskID int64, afterSeq int64, attemptN *int) ([]*Event, error) {
	q := `SELECT e.id, e.attempt_id, e.seq, e.ts, e.type, e.payload_json, a.n
		FROM events e JOIN attempts a ON a.id=e.attempt_id WHERE a.task_id=?`
	args := []any{taskID}
	if attemptN != nil {
		q += " AND a.n=?"
		args = append(args, *attemptN)
	}
	if afterSeq > 0 {
		q += " AND e.seq>?"
		args = append(args, afterSeq)
	}
	q += " ORDER BY a.n, e.seq"
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.AttemptID, &e.Seq, &e.TS, &e.Type,
			&e.PayloadJSON, &e.AttemptN); err != nil {
			return nil, err
		}
		e.Payload = UnjObj(e.PayloadJSON)
		out = append(out, &e)
	}
	return out, rows.Err()
}

// ---- approvals --------------------------------------------------------------

const approvalCols = `a.id, a.attempt_id, a.session_id, a.tool_name, a.input_json, a.status,
	a.decided_by, a.note, a.created_at, a.decided_at`

// scanApproval reads one approvalCols(+joins) row. attempt_id/session_id and
// everything joined through them are nullable now that an approval can be
// task- or session-scoped (docs/agent-events.md section 3), so they land in
// sql.Null* first and only populate the plain-int/string Approval fields
// (which keep their historical "0/"" means absent" convention) when valid.
func scanApproval(scan func(...any) error) (*Approval, error) {
	var ap Approval
	var attemptID, sessionID, taskID, attemptN sql.NullInt64
	var taskTitle, sessionName sql.NullString
	err := scan(&ap.ID, &attemptID, &sessionID, &ap.ToolName, &ap.InputJSON, &ap.Status,
		&ap.DecidedBy, &ap.Note, &ap.CreatedAt, &ap.DecidedAt,
		&taskID, &attemptN, &taskTitle, &sessionName)
	if err != nil {
		return nil, err
	}
	ap.AttemptID, ap.SessionID = attemptID.Int64, sessionID.Int64
	ap.TaskID, ap.AttemptN = taskID.Int64, int(attemptN.Int64)
	ap.TaskTitle, ap.SessionName = taskTitle.String, sessionName.String
	ap.Input = UnjObj(ap.InputJSON)
	return &ap, nil
}

// Approval fetches one approval by id, joined with its owning task or
// session — whichever of attempt_id/session_id is set (LEFT JOIN: the other
// side's columns come back NULL, per scanApproval).
func (db *DB) Approval(id int64) (*Approval, error) {
	row := db.QueryRow(`SELECT `+approvalCols+`, at.task_id, at.n, t.title, s.name
		FROM approvals a
		LEFT JOIN attempts at ON at.id=a.attempt_id
		LEFT JOIN tasks t ON t.id=at.task_id
		LEFT JOIN sessions s ON s.id=a.session_id
		WHERE a.id=?`, id)
	ap, err := scanApproval(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return ap, nil
}

// ApprovalsByStatus lists approvals in one state, newest first.
func (db *DB) ApprovalsByStatus(status string) ([]*Approval, error) {
	rows, err := db.Query(`SELECT `+approvalCols+`, at.task_id, at.n, t.title, s.name
		FROM approvals a
		LEFT JOIN attempts at ON at.id=a.attempt_id
		LEFT JOIN tasks t ON t.id=at.task_id
		LEFT JOIN sessions s ON s.id=a.session_id
		WHERE a.status=? ORDER BY a.created_at DESC LIMIT 200`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Approval{}
	for rows.Next() {
		ap, err := scanApproval(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, ap)
	}
	return out, rows.Err()
}

// InsertApproval records a tool call awaiting a decision, scoped to a task
// attempt.
func (db *DB) InsertApproval(attemptID int64, tool, inputJSON string) (int64, error) {
	res, err := db.Exec(`INSERT INTO approvals(attempt_id, tool_name, input_json,
		status, created_at) VALUES(?,?,?,'pending',?)`, attemptID, tool, inputJSON, Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// InsertSessionApproval records a tool call awaiting a decision, scoped to an
// interactive session's PermissionRequest hook rather than a task attempt
// (docs/agent-events.md section 3). attempt_id is left NULL — see the
// package doc on Approval for why that is safe under foreign-key
// enforcement, unlike a 0 sentinel would be.
func (db *DB) InsertSessionApproval(sessionID int64, tool, inputJSON string) (int64, error) {
	res, err := db.Exec(`INSERT INTO approvals(session_id, tool_name, input_json,
		status, created_at) VALUES(?,?,?,'pending',?)`, sessionID, tool, inputJSON, Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ---- memories ---------------------------------------------------------------

// ProjectNotes lists a project's agent-written notes, newest first.
func (db *DB) ProjectNotes(projectID int64, limit int) ([]*Memory, error) {
	rows, err := db.Query(`SELECT id, project_id, note, created_by_attempt, created_at
		FROM memories WHERE project_id=? ORDER BY id DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Memory{}
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.ProjectID, &m.Note, &m.CreatedByAttempt,
			&m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

// InsertNote records a durable project note left by an agent.
func (db *DB) InsertNote(projectID int64, note string, attemptID *int64) (int64, error) {
	res, err := db.Exec(`INSERT INTO memories(project_id, note, created_by_attempt,
		created_at) VALUES(?,?,?,?)`, projectID, note, attemptID, Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ---- settings ---------------------------------------------------------------

// Setting reads one settings row, returning "" when unset.
func (db *DB) Setting(key string) string {
	var v string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v); err != nil {
		return ""
	}
	return v
}

// SetSetting upserts one settings row.
func (db *DB) SetSetting(key, value string) error {
	_, err := db.Exec(`INSERT INTO settings(key, value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// ---- generic helpers --------------------------------------------------------

// Count runs a COUNT(*) with the given WHERE clause.
func (db *DB) Count(table, where string, args ...any) (int, error) {
	q := "SELECT COUNT(*) FROM " + table
	if where != "" {
		q += " WHERE " + where
	}
	var n int
	err := db.QueryRow(q, args...).Scan(&n)
	return n, err
}

// Update sets the given columns on one row by id. Column names come only from
// call sites in this repo, never from request data.
func (db *DB) Update(table string, id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	sets := make([]string, 0, len(fields))
	args := make([]any, 0, len(fields)+1)
	for _, k := range sortedKeys(fields) {
		sets = append(sets, k+"=?")
		args = append(args, fields[k])
	}
	args = append(args, id)
	_, err := db.Exec(fmt.Sprintf("UPDATE %s SET %s WHERE id=?", table,
		strings.Join(sets, ", ")), args...)
	return err
}

// Exists reports whether any row matches.
func (db *DB) Exists(table, where string, args ...any) bool {
	n, err := db.Count(table, where, args...)
	return err == nil && n > 0
}

func nz(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// deterministic UPDATE column order keeps queries cache-friendly and diffs
	// of logged SQL stable
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
