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

// CIWatchView is the CI loop's watch of a task's or session's pull request
// (internal/ciloop, docs/ci-loop.md). label is the card text, computed
// server-side so every surface says the same thing.
export interface CIWatchView {
  id: number;
  task_id?: number;
  session_id?: number;
  pr_url: string;
  state: "pending" | "failing" | "passed" | "capped" | "merged" | "closed" | "none" | "error" | "stalled";
  attempts: number;
  max_attempts: number;
  failing: string[];
  detail?: string;
  label: string;
  active: boolean;
  updated_at: number;
}

export interface TaskView extends Task {
  takeover?: Takeover;
  ci?: CIWatchView | null;
  labels: string[];
  project_name: string;
  target_name: string;
  target_host: string;
  target_user: string;
  target_kind: string;
  attempt?: AttemptView;
  attempts: AttemptSummary[];
  // The usage-limit hold that stopped this task, while it still matters
  // (docs/rate-limits.md).
  limit?: LimitHold | null;
}

// LimitHold is one usage-limit stop (internal/limits, docs/rate-limits.md).
export interface LimitHold {
  id: number;
  session_id?: number;
  task_id?: number;
  attempt_id?: number;
  agent: string;
  source: string;
  pattern: string;
  message: string;
  detected_at: number;
  reset_at?: number | null;
  policy: "notify" | "wait" | "handoff";
  state: string;
  due_at?: number | null;
  tries: number;
  nudged_at?: number | null;
  resolved_at?: number | null;
  successor_id?: number | null;
  note?: string;
  // fallback names where "Hand off" goes under the effective policy.
  fallback?: string;
  policy_scope: string;
}

export interface LimitPolicy {
  mode: "notify" | "wait" | "handoff";
  fallback_agent?: string;
  fallback_model?: string;
  fallback_profile_id?: number;
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
// SessionCheckSummary is the badge-sized view of a session's most recent
// check (internal/checks) — status/finished_at/command, no output.
export interface SessionCheckSummary {
  status: "running" | "passed" | "failed" | "error" | "skipped";
  finished_at: number | null;
  command: string;
}

// SessionCheck is one full run of a session's check command, as returned by
// GET /api/sessions/{id}/checks.
export interface SessionCheck extends SessionCheckSummary {
  id: number;
  session_id: number;
  exit_code: number | null;
  output_tail: string;
  started_at: number;
  reason: "stop" | "screen" | "manual" | string;
}

export interface SessionView extends Session {
  launch_profile?: string;
  can_restore: boolean;
  workspace?: InteractiveWorkspace;
  idle_seconds: number;
  uptime_seconds: number;
  handoff_in_flight: boolean;
  wraps: number;
  last_check?: SessionCheckSummary | null;
  ci?: CIWatchView | null;
  // AwarenessOverlap is the card chip's data (docs/agent-events.md
  // "Cross-agent awareness" point 6) — see AwarenessOverlapChip.tsx.
  awareness_overlap?: { session_id: number; name: string; files: string[] } | null;
  // Set while the session's machine is not answering the status poll.
  target_reach?: { unreachable: boolean; since?: number; error?: string } | null;
  // Isolation is this session's actual running sandbox tier — the card's
  // isolation badge (see internal/isolation and docs/isolation.md). Absent
  // or mode:"" means unsandboxed, which is every session predating this.
  isolation?: IsolationConfig;
  // The open usage-limit hold, when the agent is stopped by its limit.
  limit?: LimitHold | null;
}

// IsolationConfig mirrors internal/isolation.Config: a session or task's
// per-agent-process sandbox choice. mode:"" (or the field absent) is "none"
// — today's unsandboxed default. See docs/isolation.md.
export interface IsolationConfig {
  mode?: "" | "bwrap" | "docker";
  network?: "" | "allow" | "deny";
  docker_image?: string;
  allow_hosts?: string[];
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
  // default_isolation_json is a raw internal/isolation.Config JSON string
  // (same pattern as env_json/context_json above) — this project's default
  // sandbox tier for a new session or task launch. "{}" means none.
  default_isolation_json: string;
  skill_sources_json: string;
  // CI-aware PR loop opt-in and fix-request cap (docs/ci-loop.md).
  ci_loop?: number;
  ci_max_attempts?: number;
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
  // budget_usd (docs/budgets.md): this task's own spend cap, settable at
  // create/dispatch/patch. Absent/0 means no cap.
  budget_usd?: number | null;
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

// An approval belongs to exactly one of a task attempt (attempt_id/task_id)
// or an interactive session (session_id/session_name) —
// docs/agent-events.md section 3.
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
  session_id?: number;
  session_name?: string;
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
  // Lectern can list this session's saved conversations (Claude, Codex, and
  // catalog agents that say where they keep sessions).
  saved_conversations?: boolean;
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
  end_reason?: string;
  // Set while the agent has exited but its terminal is open at a shell.
  agent_exited_at?: number | null;
  // When restart recovery last relaunched this session.
  relaunched_at?: number | null;
  project_name?: string;
  target_name?: string;
  target_kind?: string;
  // Usage (see internal/store/models.go and docs/agent-events.md's usage
  // section). context_used_pct is deliberately a SEPARATE field from the
  // older context_pct above: context_pct is "percent left until
  // auto-compact" (low is bad); context_used_pct is "percent of the context
  // window already used", from the agent's own statusline/rollout (high is
  // bad) — see SessionCard's contextBadge for the one place that renders it.
  context_used_pct?: number | null;
  context_tokens?: number | null;
  context_size?: number | null;
  cost_usd?: number | null;
  lines_added?: number | null;
  lines_removed?: number | null;
  rate_5h_pct?: number | null;
  rate_5h_reset?: number | null;
  rate_7d_pct?: number | null;
  rate_7d_reset?: number | null;
  usage_at?: number | null;
  precompact_at?: number | null;
  // permission_mode is "bypass" | "ask" (docs/agent-events.md section 3).
  // Absent on a row from before this field existed, which every reader
  // treats the same as "bypass".
  permission_mode?: string;
  // Cross-agent awareness (docs/agent-events.md "Cross-agent awareness").
  // repo_key is an opaque "<target id>:<git common dir>" string, the same
  // for every worktree of one repository — used client-side only to GROUP
  // live sessions for the "possible duplicate work" notice, never shown
  // directly. Empty/absent means "not resolved yet" or not a git checkout.
  repo_key?: string;
  last_prompt_excerpt?: string;
  last_prompt_at?: number | null;
}

// Cross-agent awareness (docs/agent-events.md "Cross-agent awareness"):
// GET /api/sessions/{id}/peers, GET /api/peers, and the `active_work` MCP
// tool all return this shape — see internal/awareness/peers.go.
export interface PeerFile {
  rel_path: string;
  at: number;
  age_seconds: number;
}

export interface Peer {
  kind: "session" | "attempt";
  session_id?: number;
  attempt_id?: number;
  task_id?: number;
  name: string;
  agent: string;
  branch?: string;
  agent_state?: string;
  last_prompt?: string;
  same_workdir: boolean;
  workdir?: string;
  files: PeerFile[];
}

export interface SessionPeers {
  peers: Peer[];
  self_files: PeerFile[];
}

// GET /api/awareness/duplicate-prompts.
export interface DuplicatePromptPair {
  session_a_id: number;
  session_a: string;
  session_b_id: number;
  session_b: string;
  score: number;
}

// Claim board (docs/claims.md): a shared, vendor-neutral record of who is
// doing what in a repository. GET/POST/DELETE /api/claims, POST
// /api/claims/{id}/extend, GET /api/claims/topic-overlap.
export interface Claim {
  id: number;
  repo_key: string;
  scope_kind: "task" | "paths" | "topic";
  // scope is the task id, a JSON-encoded array of path globs (scope_kind
  // "paths"), or free topic text — parse with parseClaimPaths for "paths".
  scope: string;
  holder: string;
  holder_kind: "session" | "attempt" | "human" | "";
  session_id?: number;
  attempt_id?: number;
  agent?: string;
  intent: string;
  auto: boolean;
  created_at: number;
  expires_at: number;
  released_at?: number | null;
}

// GET /api/usage?days=N — see internal/api/usage.go.
export interface UsageDayBucket {
  date: string;
  cost_usd: number;
  input_tokens: number;
  output_tokens: number;
}
export interface UsageSplit {
  key: string;
  agent?: string;
  model?: string;
  project_id?: number | null;
  project_name?: string;
  cost_usd: number;
  input_tokens: number;
  output_tokens: number;
  estimated_usd?: number;
}
export interface UsageTopSession {
  id: number;
  name: string;
  agent: string;
  model: string;
  cost_usd: number;
}
export interface UsageTopTask {
  id: number;
  title: string;
  cost_usd: number;
}
export interface UsageRateWindow {
  used_percentage: number;
  resets_at: number;
}
export interface UsageQuota {
  five_hour: UsageRateWindow;
  seven_day: UsageRateWindow;
  at: number;
  stale: boolean;
  empty: boolean;
}
export interface UsageReport {
  days: number;
  generated_at: number;
  daily: UsageDayBucket[];
  by_agent_model: UsageSplit[];
  by_project: UsageSplit[];
  today_usd: number;
  week_usd: number;
  top_sessions: UsageTopSession[];
  top_tasks: UsageTopTask[];
  quota: UsageQuota;
  budgets: BudgetStatus;
}

// GET/PUT /api/budgets — see docs/budgets.md.
export interface BudgetLimit {
  daily_usd: number;
  weekly_usd: number;
  mode: "warn" | "stop";
}
export interface BudgetConfig {
  overall: BudgetLimit;
  per_agent: Record<string, BudgetLimit>;
  thresholds: number[];
  quota_thresholds: number[];
  anomaly_enabled: boolean;
  anomaly_multiplier: number;
}
export interface BudgetPeriodStatus {
  cap_usd: number;
  spent_usd: number;
  percent: number;
  blocked: boolean;
}
export interface BudgetLimitStatus {
  label: string;
  mode: "warn" | "stop";
  daily?: BudgetPeriodStatus | null;
  weekly?: BudgetPeriodStatus | null;
}
export interface BudgetStatus {
  config: BudgetConfig;
  overall: BudgetLimitStatus;
  per_agent: Record<string, BudgetLimitStatus>;
  any_blocked: boolean;
}

// GET /api/outcomes?days=N&group=agent|model|project — see docs/outcomes.md.
export interface OutcomeRow {
  key: string;
  label: string;
  attempts: number;
  sessions: number;
  cost_usd: number;
  checked: number;
  passed: number;
  accepted: number;
  lines_kept: number;
  eval_total: number;
  eval_passed: number;
  pull_requests?: number;
  commits?: number;
  partial: boolean;
  estimated: boolean;
  cost_per_pass?: number;
  cost_per_accepted?: number;
  cost_per_100_lines?: number;
  passes_per_10usd?: number;
  median_time_to_pass_s?: number;
}
export interface OutcomesReport {
  days: number;
  group: "agent" | "model" | "project";
  generated_at: number;
  rows: OutcomeRow[];
}

// GET/PUT /api/model-prices — see docs/outcomes.md "Estimates".
export interface ModelPrice {
  input_per_1m: number;
  output_per_1m: number;
  cached_input_per_1m?: number;
}
export interface ModelPriceConfig {
  prices: Record<string, ModelPrice>;
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

// One action a toast can offer, such as Undo right after closing a session.
export interface NoticeAction {
  label: string;
  run(): void | Promise<void>;
}
