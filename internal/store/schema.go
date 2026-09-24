package store

// Schema is the whole database. Plain SQL, WAL, homelab scale, zero magic.
const Schema = `
CREATE TABLE IF NOT EXISTS targets(
  id INTEGER PRIMARY KEY, name TEXT UNIQUE NOT NULL,
  kind TEXT NOT NULL DEFAULT 'ssh',            -- local | ssh | pct | sandbox | mock
  host TEXT DEFAULT '', port INTEGER DEFAULT 22, user TEXT DEFAULT 'root',
  key_path TEXT DEFAULT '', workroot TEXT DEFAULT '',
  max_concurrent INTEGER DEFAULT 4, sandbox INTEGER DEFAULT 0,
  status TEXT DEFAULT 'unknown', info_json TEXT DEFAULT '{}',
  context_json TEXT DEFAULT '[]',              -- control-plane paths staged into every worktree
  memory_dir TEXT DEFAULT '',                  -- opt-in shared Claude Code memory store
  command_prefix TEXT DEFAULT '',              -- wraps every command, e.g. wsl -e bash -lc
  created_at REAL
);
CREATE TABLE IF NOT EXISTS projects(
  id INTEGER PRIMARY KEY, name TEXT NOT NULL,
  target_id INTEGER NOT NULL REFERENCES targets(id),
  repo_path TEXT NOT NULL, default_base_branch TEXT DEFAULT 'main',
  workroot_override TEXT DEFAULT '', policy_json TEXT DEFAULT '{}',
  verify_cmd TEXT DEFAULT '', keep_worktrees INTEGER DEFAULT 0,
  review_gate INTEGER DEFAULT 0, env_json TEXT DEFAULT '{}',
  context_json TEXT DEFAULT '[]',              -- extra staged context, on top of the target's
  mcp_json TEXT DEFAULT '{}',                  -- MCP servers handed to the agent
  strict_mcp INTEGER DEFAULT 0,                -- ignore host MCP config entirely
  permissions_json TEXT DEFAULT '{}',          -- permissions block for .lectern/settings.json
  gate_matcher TEXT DEFAULT '',                -- PreToolUse matcher in gated mode ('' = all tools)
  default_agent TEXT DEFAULT 'claude',         -- agent used by tasks that don't pick one
  capability_profile TEXT DEFAULT 'restricted',
  default_permission_mode TEXT DEFAULT '',
  skill_sources_json TEXT DEFAULT '[]',
  created_at REAL
);
CREATE TABLE IF NOT EXISTS tasks(
  id INTEGER PRIMARY KEY, project_id INTEGER NOT NULL REFERENCES projects(id),
  title TEXT NOT NULL, prompt TEXT DEFAULT '',
  status TEXT NOT NULL DEFAULT 'backlog',
  priority INTEGER DEFAULT 2, labels_json TEXT DEFAULT '[]',
  agent TEXT DEFAULT 'claude', model TEXT DEFAULT '',
  permission_mode TEXT DEFAULT 'acceptEdits', base_branch TEXT DEFAULT '',
  parent_task_id INTEGER, created_by TEXT DEFAULT 'user',
  created_by_attempt INTEGER,
  created_at REAL, updated_at REAL
);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
CREATE TABLE IF NOT EXISTS attempts(
  id INTEGER PRIMARY KEY, task_id INTEGER NOT NULL REFERENCES tasks(id),
  n INTEGER NOT NULL, status TEXT NOT NULL DEFAULT 'queued',
  token TEXT NOT NULL DEFAULT '',
  prompt TEXT DEFAULT '', resume_session TEXT DEFAULT '', model TEXT DEFAULT '',
  sandbox_vmid TEXT DEFAULT '',
  worktree_path TEXT DEFAULT '', branch TEXT DEFAULT '', tmux_session TEXT DEFAULT '',
  session_id TEXT DEFAULT '', log_offset INTEGER DEFAULT 0,
  started_at REAL, finished_at REAL, exit_code INTEGER,
  result_json TEXT DEFAULT '{}', diff_stat_json TEXT DEFAULT '{}',
  verify_json TEXT DEFAULT '{}',
  mcp_json TEXT DEFAULT '{}', strict_mcp INTEGER DEFAULT 0,
  mcp_snapshot INTEGER NOT NULL DEFAULT 0,
  launch_config_json TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_attempts_task ON attempts(task_id);
CREATE TABLE IF NOT EXISTS project_skills(
  id INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES projects(id),
  target_id INTEGER NOT NULL REFERENCES targets(id),
  agent TEXT NOT NULL,
  skill_id TEXT NOT NULL,
  source_id TEXT NOT NULL,
  source_path TEXT NOT NULL,
  entry_name TEXT NOT NULL,
  target_rel TEXT NOT NULL,
  source_digest TEXT NOT NULL DEFAULT '',
  exclude_marker TEXT NOT NULL DEFAULT '',
  created_at REAL NOT NULL,
  UNIQUE(project_id, target_id, agent, skill_id)
);
CREATE INDEX IF NOT EXISTS idx_project_skills_project ON project_skills(project_id, agent);
CREATE TABLE IF NOT EXISTS skill_materializations(
  id INTEGER PRIMARY KEY,
  -- Deliberately no FK cascade: an ownership record must survive a project
  -- edit/delete until its target-local symlink has been cleaned.
  attachment_id INTEGER NOT NULL,
  target_id INTEGER NOT NULL REFERENCES targets(id),
  worktree_path TEXT NOT NULL,
  target_path TEXT NOT NULL,
  source_path TEXT NOT NULL,
  target_rel TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'pending',
  created_at REAL NOT NULL,
  UNIQUE(attachment_id, worktree_path)
);
CREATE INDEX IF NOT EXISTS idx_skill_materializations_path ON skill_materializations(worktree_path);
-- Operator messages survive restarts and are assigned to exactly one turn.
CREATE TABLE IF NOT EXISTS task_messages(
  id INTEGER PRIMARY KEY,
  task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  request_id TEXT NOT NULL,
  text TEXT NOT NULL,
  interrupt INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'pending',
  attempt_id INTEGER,
  error TEXT NOT NULL DEFAULT '',
  created_at REAL NOT NULL,
  UNIQUE(task_id, request_id)
);
CREATE INDEX IF NOT EXISTS idx_task_messages_pending ON task_messages(status, task_id);
CREATE TABLE IF NOT EXISTS events(
  id INTEGER PRIMARY KEY, attempt_id INTEGER NOT NULL REFERENCES attempts(id),
  seq INTEGER NOT NULL, ts REAL, type TEXT NOT NULL, payload_json TEXT DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_events_attempt ON events(attempt_id, seq);
-- attempt_id is nullable (not the historical NOT NULL) so an approval can
-- belong to a session's PermissionRequest hook instead of a task attempt
-- (docs/agent-events.md section 3): exactly one of attempt_id/session_id is
-- set. A database created before this change gets there via
-- migrateApprovalsSessionColumn in migrate_approvals.go, a one-time table
-- rebuild — SQLite has no ALTER COLUMN to drop a NOT NULL constraint, so it
-- cannot be a plain entry in the migrations list below.
CREATE TABLE IF NOT EXISTS approvals(
  id INTEGER PRIMARY KEY, attempt_id INTEGER REFERENCES attempts(id),
  session_id INTEGER REFERENCES sessions(id),
  tool_name TEXT NOT NULL, input_json TEXT DEFAULT '{}',
  status TEXT NOT NULL DEFAULT 'pending',      -- pending|approved|denied|expired
  decided_by TEXT DEFAULT '', note TEXT DEFAULT '',
  created_at REAL, decided_at REAL
);
CREATE INDEX IF NOT EXISTS idx_approvals_status ON approvals(status);
CREATE TABLE IF NOT EXISTS push_subscriptions(
  id INTEGER PRIMARY KEY, endpoint TEXT UNIQUE NOT NULL, keys_json TEXT NOT NULL,
  created_at REAL
);
-- A session is an INTERACTIVE agent you work with, as opposed to a task, which
-- is work you hand off. It outlives any one conversation: the tmux session is
-- the process, the row is the durable record, and a handoff carries the thread
-- across a fresh context.
CREATE TABLE IF NOT EXISTS sessions(
  id INTEGER PRIMARY KEY,
  project_id INTEGER REFERENCES projects(id),
  target_id INTEGER NOT NULL REFERENCES targets(id),
  name TEXT NOT NULL, agent TEXT NOT NULL DEFAULT 'claude', model TEXT DEFAULT '',
  workdir TEXT NOT NULL, tmux_session TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'starting',  -- starting|running|waiting|idle|dead
  origin TEXT NOT NULL DEFAULT 'lectern', -- lectern | discovered
  pane_hash TEXT DEFAULT '', pane_tail TEXT DEFAULT '',
  context_pct INTEGER,                      -- parsed from the agent's own footer
  last_activity_at REAL, created_at REAL, updated_at REAL, ended_at REAL,
  native_recovery_cid TEXT NOT NULL DEFAULT '',
  boot_id TEXT NOT NULL DEFAULT '',
  tracking_identity TEXT NOT NULL DEFAULT '',
  group_path TEXT NOT NULL DEFAULT '',
  worktree_json TEXT NOT NULL DEFAULT '',
  setup_state TEXT NOT NULL DEFAULT '',
  setup_error TEXT NOT NULL DEFAULT '',
  setup_cancel_requested INTEGER NOT NULL DEFAULT 0,
  archived_at REAL,
  archive_text TEXT NOT NULL DEFAULT '',
  resume_id TEXT NOT NULL DEFAULT '',
  launch_config_json TEXT NOT NULL DEFAULT '',
  -- Agent hooks (see internal/agentevents and docs/agent-events.md section 2).
  -- hook_token authenticates POST /api/hook/session/{id}/*; it is never
  -- serialized in the session's own JSON.
  hook_token TEXT NOT NULL DEFAULT '',
  -- permission_mode is this session's resolved launch-time choice —
  -- "bypass" (today's default, --permission-mode bypassPermissions) or
  -- "ask" (no bypass flag; the PermissionRequest hook is registered and
  -- gates every tool call through internal/broker) — docs/agent-events.md
  -- section 3. Empty on a row launched before this column existed; treated
  -- as "bypass" everywhere it is read, matching the unchanged default.
  permission_mode TEXT NOT NULL DEFAULT '',
  -- agent_state is the hook-driven lifecycle signal, distinct from the older
  -- screen-scraped status column: working|waiting_input|waiting_permission|
  -- idle|error|ended. state_source records who wrote it last (hook|screen) so
  -- applyPane knows when it is still allowed to overwrite it (see poll.go).
  agent_state TEXT NOT NULL DEFAULT '',
  state_source TEXT NOT NULL DEFAULT '',
  state_at REAL,
  hook_seen_at REAL,
  -- Usage, latest values only — history lives in usage_daily. Populated from
  -- Claude's statusline JSON (context_window, cost, rate_limits) today; other
  -- drivers fill what they can and leave the rest at their zero value.
  context_tokens INTEGER,
  context_size INTEGER,
  cost_usd REAL,
  lines_added INTEGER,
  lines_removed INTEGER,
  rate_5h_pct INTEGER,
  rate_5h_reset REAL,
  rate_7d_pct INTEGER,
  rate_7d_reset REAL,
  usage_at REAL
);
CREATE INDEX IF NOT EXISTS idx_sessions_status ON sessions(status);
-- One wrap per handoff: what the agent said it was doing, kept so the project
-- survives the context window that produced it.
CREATE TABLE IF NOT EXISTS session_wraps(
  id INTEGER PRIMARY KEY,
  session_id INTEGER NOT NULL REFERENCES sessions(id),
  project_id INTEGER REFERENCES projects(id),
  summary TEXT NOT NULL, transcript TEXT DEFAULT '',
  next_session_id INTEGER, created_at REAL
);
CREATE INDEX IF NOT EXISTS idx_wraps_project ON session_wraps(project_id);
-- Media is what an agent posts back for the operator to look at: a recording of
-- the feature working, a screenshot, a report, a link to the server it started.
-- The bytes live in the media directory under blob; a link has url and no blob.
-- session_id is nullable because a script outside any session may post too.
CREATE TABLE IF NOT EXISTS media(
  id INTEGER PRIMARY KEY,
  session_id INTEGER REFERENCES sessions(id),
  kind TEXT NOT NULL,                       -- file | link
  title TEXT NOT NULL DEFAULT '', note TEXT DEFAULT '',
  name TEXT DEFAULT '', mime TEXT DEFAULT '', size INTEGER DEFAULT 0,
  blob TEXT DEFAULT '', url TEXT DEFAULT '',
  source TEXT DEFAULT '',                   -- mcp | cli
  created_at REAL
);
CREATE INDEX IF NOT EXISTS idx_media_session ON media(session_id);
-- A takeover is durable before the background process is interrupted.
CREATE TABLE IF NOT EXISTS task_takeovers(
  task_id INTEGER PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
  attempt_id INTEGER NOT NULL REFERENCES attempts(id),
  session_id INTEGER REFERENCES sessions(id),
  status TEXT NOT NULL DEFAULT 'pending',
  error TEXT NOT NULL DEFAULT '',
  created_at REAL NOT NULL
);
CREATE TRIGGER IF NOT EXISTS takeover_blocks_attempt BEFORE INSERT ON attempts
WHEN EXISTS(SELECT 1 FROM task_takeovers WHERE task_id=NEW.task_id)
BEGIN SELECT RAISE(ABORT, 'Task is being continued in an interactive session'); END;
CREATE TRIGGER IF NOT EXISTS takeover_blocks_message BEFORE INSERT ON task_messages
WHEN EXISTS(SELECT 1 FROM task_takeovers WHERE task_id=NEW.task_id)
BEGIN SELECT RAISE(ABORT, 'Send messages to the interactive session'); END;
CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY, value TEXT);
CREATE TABLE IF NOT EXISTS launch_profiles(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL COLLATE NOCASE UNIQUE,
  agent TEXT NOT NULL,
  command TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  env_json TEXT NOT NULL DEFAULT '{}'
);
CREATE TABLE IF NOT EXISTS memories(
  id INTEGER PRIMARY KEY, project_id INTEGER NOT NULL REFERENCES projects(id),
  note TEXT NOT NULL, created_by_attempt INTEGER, created_at REAL
);
CREATE INDEX IF NOT EXISTS idx_memories_project ON memories(project_id);
CREATE TABLE IF NOT EXISTS routines(
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  prompt TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  project_ids TEXT NOT NULL DEFAULT '[]',
  agent TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  permission_mode TEXT NOT NULL DEFAULT '',
  schedule TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  dispatch INTEGER NOT NULL DEFAULT 1,
  last_run_at REAL,
  next_run_at REAL,
  created_at REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_routines_due ON routines(enabled, next_run_at);
CREATE TABLE IF NOT EXISTS workspace_operations(
 id INTEGER PRIMARY KEY,
 session_id INTEGER NOT NULL REFERENCES sessions(id),
 plan_json TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'running',
 error TEXT NOT NULL DEFAULT '',
 cancel_requested INTEGER NOT NULL DEFAULT 0,
 created_at REAL NOT NULL,
 updated_at REAL NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_workspace_active ON workspace_operations(session_id)
 WHERE state IN ('running','recovering');
-- usage_daily is the history a session's/task's latest usage columns do not
-- keep. Each row accumulates DELTAS between successive statusline totals
-- (never the raw cumulative number, which would double-count every sample),
-- one row per (date, session_id|task_id, agent, model). session_id and
-- task_id are nullable so the same table can later hold task usage; today
-- only the session ingest path (internal/agentevents) writes it.
CREATE TABLE IF NOT EXISTS usage_daily(
  id INTEGER PRIMARY KEY,
  date TEXT NOT NULL,
  session_id INTEGER REFERENCES sessions(id),
  task_id INTEGER REFERENCES tasks(id),
  agent TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  cost_usd REAL NOT NULL DEFAULT 0,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_daily_session ON usage_daily(date, session_id, agent, model)
 WHERE session_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_daily_task ON usage_daily(date, task_id, agent, model)
 WHERE task_id IS NOT NULL;
`

// migrations are additive: they bring a database created by an older build up to
// the current schema. Each is expected to fail with "duplicate column" once the
// column exists, which is not an error.
var migrations = []string{
	"ALTER TABLE sessions ADD COLUMN setup_cancel_requested INTEGER NOT NULL DEFAULT 0",
	"ALTER TABLE sessions ADD COLUMN setup_state TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE sessions ADD COLUMN setup_error TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE sessions ADD COLUMN launch_config_json TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE sessions ADD COLUMN archived_at REAL",
	"ALTER TABLE sessions ADD COLUMN archive_text TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE sessions ADD COLUMN resume_id TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE sessions ADD COLUMN tracking_identity TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE sessions ADD COLUMN native_recovery_cid TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE sessions ADD COLUMN boot_id TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE sessions ADD COLUMN group_path TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE sessions ADD COLUMN worktree_json TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE targets ADD COLUMN context_json TEXT DEFAULT '[]'",
	"ALTER TABLE targets ADD COLUMN memory_dir TEXT DEFAULT ''",
	"ALTER TABLE targets ADD COLUMN command_prefix TEXT DEFAULT ''",
	"ALTER TABLE projects ADD COLUMN verify_cmd TEXT DEFAULT ''",
	"ALTER TABLE projects ADD COLUMN keep_worktrees INTEGER DEFAULT 0",
	"ALTER TABLE projects ADD COLUMN review_gate INTEGER DEFAULT 0",
	"ALTER TABLE projects ADD COLUMN env_json TEXT DEFAULT '{}'",
	"ALTER TABLE projects ADD COLUMN context_json TEXT DEFAULT '[]'",
	"ALTER TABLE projects ADD COLUMN mcp_json TEXT DEFAULT '{}'",
	"ALTER TABLE projects ADD COLUMN strict_mcp INTEGER DEFAULT 0",
	"ALTER TABLE projects ADD COLUMN permissions_json TEXT DEFAULT '{}'",
	"ALTER TABLE projects ADD COLUMN gate_matcher TEXT DEFAULT ''",
	"ALTER TABLE projects ADD COLUMN setup_cmd TEXT DEFAULT ''",
	"ALTER TABLE projects ADD COLUMN default_agent TEXT DEFAULT 'claude'",
	"ALTER TABLE projects ADD COLUMN capability_profile TEXT DEFAULT 'restricted'",
	"ALTER TABLE projects ADD COLUMN default_permission_mode TEXT DEFAULT ''",
	"ALTER TABLE projects ADD COLUMN skill_sources_json TEXT DEFAULT '[]'",
	"ALTER TABLE projects ADD COLUMN memory_topic TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE projects ADD COLUMN memory_status TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE skill_materializations ADD COLUMN state TEXT NOT NULL DEFAULT 'pending'",
	"ALTER TABLE tasks ADD COLUMN parent_task_id INTEGER",
	"ALTER TABLE tasks ADD COLUMN created_by TEXT DEFAULT 'user'",
	"ALTER TABLE tasks ADD COLUMN created_by_attempt INTEGER",
	"ALTER TABLE attempts ADD COLUMN model TEXT DEFAULT ''",
	"ALTER TABLE attempts ADD COLUMN sandbox_vmid TEXT DEFAULT ''",
	"ALTER TABLE attempts ADD COLUMN verify_json TEXT DEFAULT '{}'",
	"ALTER TABLE attempts ADD COLUMN mcp_json TEXT DEFAULT '{}'",
	"ALTER TABLE attempts ADD COLUMN strict_mcp INTEGER DEFAULT 0",
	"ALTER TABLE attempts ADD COLUMN mcp_snapshot INTEGER NOT NULL DEFAULT 0",
	"ALTER TABLE attempts ADD COLUMN launch_config_json TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE sessions ADD COLUMN hook_token TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE sessions ADD COLUMN agent_state TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE sessions ADD COLUMN state_source TEXT NOT NULL DEFAULT ''",
	"ALTER TABLE sessions ADD COLUMN state_at REAL",
	"ALTER TABLE sessions ADD COLUMN hook_seen_at REAL",
	"ALTER TABLE sessions ADD COLUMN context_tokens INTEGER",
	"ALTER TABLE sessions ADD COLUMN context_size INTEGER",
	"ALTER TABLE sessions ADD COLUMN cost_usd REAL",
	"ALTER TABLE sessions ADD COLUMN lines_added INTEGER",
	"ALTER TABLE sessions ADD COLUMN lines_removed INTEGER",
	"ALTER TABLE sessions ADD COLUMN rate_5h_pct INTEGER",
	"ALTER TABLE sessions ADD COLUMN rate_5h_reset REAL",
	"ALTER TABLE sessions ADD COLUMN rate_7d_pct INTEGER",
	"ALTER TABLE sessions ADD COLUMN rate_7d_reset REAL",
	"ALTER TABLE sessions ADD COLUMN usage_at REAL",
	"ALTER TABLE sessions ADD COLUMN permission_mode TEXT NOT NULL DEFAULT ''",
}
