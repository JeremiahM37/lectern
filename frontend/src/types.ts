// API row contracts from internal/store/models.go JSON fields.
// Timestamp values are seconds since epoch; nullable pointers remain nullable.

export interface AttemptView {
  id: number;
  n: number;
  status: string;
  branch: string;
  worktree_path: string;
  tmux_session: string;
  started_at: number | null;
  finished_at: number | null;
  exit_code: number | null;
  result: Record<string, unknown>;
  diff_stat: unknown[];
  verify: Record<string, unknown>;
  // driver names the internal/drivers.Kind running this attempt (e.g.
  // "claude-steer"); the UI uses it to decide whether the steer input
  // applies, rather than guessing from agent + permission_mode.
  driver: string;
}

export interface AttemptSummary {
  id: number;
  n: number;
  status: string;
  model: string;
  exit_code: number | null;
  cost_usd: unknown;
  agent: string;
  permission_mode: string;
  driver: string;
  started_at: number | null;
  finished_at: number | null;
  diff_stat: { path: string; additions?: number; deletions?: number }[];
  verify: Record<string, unknown>;
  input_tokens: unknown;
  output_tokens: unknown;
}

export interface Takeover {
  task_id: number;
  attempt_id: number;
  session_id: number | null;
  status: string;
  error: string;
}

export interface TaskView extends Task {
  takeover?: Takeover;
  labels: string[];
  project_name: string;
  target_name: string;
  target_host: string;
  target_user: string;
  target_kind: string;
  attempt?: AttemptView;
  attempts: AttemptSummary[];
}

export interface InteractiveWorkspace {
  operation_active?: boolean;
  setup_command?: string;
  setup_state?: string;
  setup_output?: string;
  repo: string;
  path: string;
  branch: string;
  base: string;
  commit: string;
  state: string;
  error?: string;
  repositories?: {name:string;project_id?:number;worktree:InteractiveWorkspace}[];
}
export interface SessionView extends Session {
  launch_profile?: string;
  can_restore: boolean;
  workspace?: InteractiveWorkspace;
  idle_seconds: number;
  uptime_seconds: number;
  handoff_in_flight: boolean;
  wraps: number;
}

export interface Target {
  id: number;
  name: string;
  kind: string;
  host: string;
  port: number;
  user: string;
  key_path: string;
  workroot: string;
  max_concurrent: number;
  sandbox: number;
  status: string;
  info_json: string;
  context_json: string;
  memory_dir: string;
  command_prefix: string;
  created_at: number;
}

export interface Project {
  setup_cmd: string;
  id: number;
  name: string;
  target_id: number;
  repo_path: string;
  default_base_branch: string;
  workroot_override: string;
  policy_json: string;
  verify_cmd: string;
  keep_worktrees: number;
  review_gate: number;
  env_json: string;
  context_json: string;
  strict_mcp: number;
  permissions_json: string;
  gate_matcher: string;
  default_agent: string;
  capability_profile: string;
  default_permission_mode: string;
  skill_sources_json: string;
  created_at: number;
  target_name?: string;
  target_kind?: string;
}

export interface Task {
  id: number;
  project_id: number;
  title: string;
  prompt: string;
  status: string;
  priority: number;
  labels_json: string;
  agent: string;
  model: string;
  permission_mode: string;
  base_branch: string;
  parent_task_id: number | null;
  created_by: string;
  created_by_attempt: number | null;
  created_at: number;
  updated_at: number;
}

export interface Attempt {
  id: number;
  task_id: number;
  n: number;
  status: string;
  token: string;
  prompt: string;
  resume_session: string;
  model: string;
  sandbox_vmid: string;
  worktree_path: string;
  branch: string;
  tmux_session: string;
  session_id: string;
  log_offset: number;
  started_at: number | null;
  finished_at: number | null;
  exit_code: number | null;
  result_json: string;
  diff_stat_json: string;
  verify_json: string;
}

export interface Event {
  id: number;
  attempt_id: number;
  seq: number;
  ts: number;
  type: string;
  payload: Record<string, unknown>;
  attempt_n: number;
}

export interface Approval {
  id: number;
  attempt_id: number;
  tool_name: string;
  status: string;
  decided_by: string;
  note: string;
  created_at: number;
  decided_at: number | null;
  input: Record<string, unknown>;
  task_id?: number;
  attempt_n?: number;
  task_title?: string;
}

export interface Memory {
  id: number;
  project_id: number;
  note: string;
  created_by_attempt: number | null;
  created_at: number;
}

export interface Session {
  setup_cancel_requested?: boolean;
  setup_state?: string;
  setup_error?: string;
  archived_at: number | null;
  resume_id?: string;
  group_path: string;
  id: number;
  project_id: number | null;
  target_id: number;
  name: string;
  agent: string;
  model: string;
  workdir: string;
  tmux_session: string;
  status: string;
  origin: string;
  pane_tail: string;
  context_pct: number | null;
  last_activity_at: number | null;
  created_at: number;
  updated_at: number;
  ended_at: number | null;
  project_name?: string;
  target_name?: string;
  target_kind?: string;
}

export interface Wrap {
  id: number;
  session_id: number;
  project_id: number | null;
  summary: string;
  transcript?: string;
  next_session_id: number | null;
  created_at: number;
}

// An open forward: a port on a target's localhost, or a desktop running there.
export interface LiveView {
  id: number;
  kind: "port" | "desktop";
  title: string;
  target_id: number;
  target_name: string;
  session_id: number | null;
  port: number;
  listen_port: number;
  created_at: number;
  expires_at: number;
  connections: number;
  detail?: Record<string, string>;
}

export interface Media {
  id: number;
  session_id: number | null;
  kind: "file" | "link";
  title: string;
  note: string;
  name: string;
  mime: string;
  size: number;
  url: string;
  source: string;
  created_at: number;
  session_name?: string;
}
