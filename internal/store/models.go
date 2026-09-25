package store

// The row structs below mirror their table columns 1:1, including the raw
// `*_json` text columns. The API serialises them straight to the client, and the
// UI parses the JSON blobs it needs — the same contract the Python service had,
// so dashboards and the MCP server keep working across the rewrite.

// Target is a machine lectern can dispatch onto.
type Target struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	User          string `json:"user"`
	KeyPath       string `json:"key_path"`
	Workroot      string `json:"workroot"`
	MaxConcurrent int    `json:"max_concurrent"`
	Sandbox       int    `json:"sandbox"`
	Status        string `json:"status"`
	InfoJSON      string `json:"info_json"`
	ContextJSON   string `json:"context_json"`
	MemoryDir     string `json:"memory_dir"`
	// CommandPrefix wraps every command run on this target. It exists for hosts
	// whose SSH lands somewhere other than where the work is — a Windows box
	// where the toolchain lives in WSL needs `wsl -e bash -lc`, and without it
	// the target probes as "no tmux, no python3" and nothing can run.
	CommandPrefix string  `json:"command_prefix"`
	CreatedAt     float64 `json:"created_at"`
}

// Project is a repository on a target, plus every policy that governs agents
// dispatched against it.
type Project struct {
	SetupCmd          string `json:"setup_cmd"`
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	TargetID          int64  `json:"target_id"`
	RepoPath          string `json:"repo_path"`
	DefaultBaseBranch string `json:"default_base_branch"`
	WorkrootOverride  string `json:"workroot_override"`
	PolicyJSON        string `json:"policy_json"`
	VerifyCmd         string `json:"verify_cmd"`
	KeepWorktrees     int    `json:"keep_worktrees"`
	ReviewGate        int    `json:"review_gate"`
	EnvJSON           string `json:"env_json"`
	ContextJSON       string `json:"context_json"`
	// MCPJSON is credential-bearing storage and is exposed only through the
	// redacted /projects/{id}/mcp endpoint. Never serialize it in a project row.
	MCPJSON               string `json:"-"`
	StrictMCP             int    `json:"strict_mcp"`
	PermissionsJSON       string `json:"permissions_json"`
	GateMatcher           string `json:"gate_matcher"`
	DefaultAgent          string `json:"default_agent"`
	CapabilityProfile     string `json:"capability_profile"`
	DefaultPermissionMode string `json:"default_permission_mode"`
	// DefaultIsolationJSON is this project's default isolation.Config
	// (json-encoded) for a new session or task launch — see
	// internal/isolation. A launch's explicit override still wins; this is
	// only what a launch gets when it names none.
	DefaultIsolationJSON string `json:"default_isolation_json"`
	SkillSourcesJSON     string `json:"skill_sources_json"`
	MemoryTopic          string `json:"memory_topic"`
	MemoryStatus         string `json:"memory_status"`
	// RepoKey/RepoToplevel are cross-agent awareness's cache (see
	// internal/awareness and schema.go's session_file_edits comment):
	// backfilled from the first session in this project to resolve its own
	// git common dir, so every task attempt of this project can be matched
	// to a peer session without an extra git call. Never serialized: it is
	// an internal join key, not something a settings form edits.
	RepoKey      string  `json:"-"`
	RepoToplevel string  `json:"-"`
	CreatedAt    float64 `json:"created_at"`

	// joined for the projects list — the UI names a project's target inline
	TargetName string `json:"target_name,omitempty"`
	TargetKind string `json:"target_kind,omitempty"`
}

// Task is one unit of work on the board.
type Task struct {
	ID               int64   `json:"id"`
	ProjectID        int64   `json:"project_id"`
	Title            string  `json:"title"`
	Prompt           string  `json:"prompt"`
	Status           string  `json:"status"`
	Priority         int     `json:"priority"`
	LabelsJSON       string  `json:"labels_json"`
	Agent            string  `json:"agent"`
	Model            string  `json:"model"`
	PermissionMode   string  `json:"permission_mode"`
	BaseBranch       string  `json:"base_branch"`
	ParentTaskID     *int64  `json:"parent_task_id"`
	CreatedBy        string  `json:"created_by"`
	CreatedByAttempt *int64  `json:"created_by_attempt"`
	CreatedAt        float64 `json:"created_at"`
	UpdatedAt        float64 `json:"updated_at"`
	// CheckCommand overrides the project's own VerifyCmd for auto-verify on
	// this one task, when set — empty means "use the project's", which is
	// every task that predates this field. It exists for evals: a case's own
	// check_command needs to run instead of the project default, without
	// mutating shared project state for the run's duration.
	CheckCommand string `json:"check_command,omitempty"`
	// SetupCommand, when set, is run for real by the scheduler in the
	// attempt's own worktree, on its target, after the worktree is created
	// and before the agent starts — a non-zero exit fails the attempt and the
	// agent never runs. Empty (every task that predates this field) skips the
	// step entirely. It exists for evals: a case's own setup_command used to
	// be folded into the prompt as an instruction for the agent to run itself;
	// this is the real host-side pre-step instead.
	SetupCommand string `json:"setup_command,omitempty"`
	// SetupTimeoutS bounds how long SetupCommand may run, in seconds. The eval
	// engine sets it to min(case.timeout_s, 600); zero falls back to a 600s
	// default inside the scheduler.
	SetupTimeoutS int `json:"setup_timeout_s,omitempty"`
	// BudgetUSD (docs/budgets.md) is this task's own spend cap, settable at
	// create or dispatch time. Nil/0 means "no per-task cap".
	BudgetUSD *float64 `json:"budget_usd,omitempty"`
}

// Attempt is a single agent run against a task. Retries, follow-ups and the
// parallel B side of an A/B dispatch are all attempts.
type Attempt struct {
	ID            int64    `json:"id"`
	TaskID        int64    `json:"task_id"`
	N             int      `json:"n"`
	Status        string   `json:"status"`
	Token         string   `json:"token"`
	Prompt        string   `json:"prompt"`
	ResumeSession string   `json:"resume_session"`
	Model         string   `json:"model"`
	SandboxVMID   string   `json:"sandbox_vmid"`
	WorktreePath  string   `json:"worktree_path"`
	Branch        string   `json:"branch"`
	TmuxSession   string   `json:"tmux_session"`
	SessionID     string   `json:"session_id"`
	LogOffset     int64    `json:"log_offset"`
	StartedAt     *float64 `json:"started_at"`
	FinishedAt    *float64 `json:"finished_at"`
	ExitCode      *int     `json:"exit_code"`
	ResultJSON    string   `json:"result_json"`
	DiffStatJSON  string   `json:"diff_stat_json"`
	VerifyJSON    string   `json:"verify_json"`
	// MCPJSON and StrictMCP snapshot the project launch policy for takeover;
	// they are internal because MCP declarations can contain credentials.
	MCPJSON          string `json:"-"`
	StrictMCP        int    `json:"-"`
	MCPSnapshot      int    `json:"-"`
	LaunchConfigJSON string `json:"-"`
	// Driver is the internal/drivers.Kind chosen for this attempt at queue
	// time (e.g. "claude-exec", "claude-steer", "codex-appserver"), so later
	// workers and the UI can see and reuse the choice without re-deriving it
	// from the agent name and permission mode.
	Driver string `json:"driver"`
	// Agent and PermissionMode let a Best-of-N variant pick its own agent or
	// gating instead of inheriting the task's; empty means "use the task's"
	// (see scheduler.effAgent/effPermissionMode), which is what every
	// pre-existing single-attempt task and model-only A/B dispatch already is.
	Agent          string `json:"agent,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
	// LiveCostUSD (docs/budgets.md) is this attempt's own cumulative cost so
	// far, updated by the scheduler as it parses streamed 'result' events —
	// see the column's own comment in store/schema.go for why this exists
	// separately from ResultJSON, which only ever lands once, at finish.
	LiveCostUSD float64 `json:"live_cost_usd,omitempty"`
}

// Event is one normalised line of an agent's output stream.
type Event struct {
	ID          int64   `json:"id"`
	AttemptID   int64   `json:"attempt_id"`
	Seq         int64   `json:"seq"`
	TS          float64 `json:"ts"`
	Type        string  `json:"type"`
	PayloadJSON string  `json:"-"`

	// filled by the API layer
	Payload  map[string]any `json:"payload"`
	AttemptN int            `json:"attempt_n"`
}

// Approval is one tool call a gated agent is blocked on. It belongs to
// exactly one of a task attempt (AttemptID) or an interactive session
// (SessionID) — docs/agent-events.md section 3. Both id fields keep the
// existing "0 means absent" convention TaskID already used, so nothing that
// already reads AttemptID as a plain int64 needs to change: the zero value
// only ever shows up on a session-scoped row, and the DB column underneath
// it is genuinely NULL there (see migrate_approvals.go) so foreign-key
// enforcement never sees a false attempt id 0.
type Approval struct {
	ID        int64    `json:"id"`
	AttemptID int64    `json:"attempt_id,omitempty"`
	SessionID int64    `json:"session_id,omitempty"`
	ToolName  string   `json:"tool_name"`
	InputJSON string   `json:"-"`
	Status    string   `json:"status"`
	DecidedBy string   `json:"decided_by"`
	Note      string   `json:"note"`
	CreatedAt float64  `json:"created_at"`
	DecidedAt *float64 `json:"decided_at"`

	Input       map[string]any `json:"input"`
	TaskID      int64          `json:"task_id,omitempty"`
	AttemptN    int            `json:"attempt_n,omitempty"`
	TaskTitle   string         `json:"task_title,omitempty"`
	SessionName string         `json:"session_name,omitempty"`
}

// Memory is a durable note an agent left for the agents that come after it.
type Memory struct {
	ID               int64   `json:"id"`
	ProjectID        int64   `json:"project_id"`
	Note             string  `json:"note"`
	CreatedByAttempt *int64  `json:"created_by_attempt"`
	CreatedAt        float64 `json:"created_at"`
}

// Session is an interactive agent the operator works WITH, as opposed to a task,
// which is work they hand OFF. Its tmux session is the process; this row is the
// durable record that outlives it.
type Session struct {
	SetupCancelRequested bool     `json:"setup_cancel_requested,omitempty"`
	SetupState           string   `json:"setup_state,omitempty"`
	SetupError           string   `json:"setup_error,omitempty"`
	LaunchConfigJSON     string   `json:"-"`
	ArchivedAt           *float64 `json:"archived_at"`
	ResumeID             string   `json:"resume_id,omitempty"`
	NativeRecoveryCID    string   `json:"-"`
	BootID               string   `json:"-"`
	TrackingIdentity     string   `json:"-"`
	GroupPath            string   `json:"group_path"`
	WorktreeJSON         string   `json:"-"`
	ID                   int64    `json:"id"`
	ProjectID            *int64   `json:"project_id"`
	TargetID             int64    `json:"target_id"`
	Name                 string   `json:"name"`
	Agent                string   `json:"agent"`
	Model                string   `json:"model"`
	Workdir              string   `json:"workdir"`
	TmuxSession          string   `json:"tmux_session"`
	Status               string   `json:"status"`
	Origin               string   `json:"origin"`
	PaneHash             string   `json:"-"`
	PaneTail             string   `json:"pane_tail"`
	ContextPct           *int     `json:"context_pct"`
	LastActivityAt       *float64 `json:"last_activity_at"`
	CreatedAt            float64  `json:"created_at"`
	UpdatedAt            float64  `json:"updated_at"`
	EndedAt              *float64 `json:"ended_at"`

	// HookToken authenticates POST /api/hook/session/{id}/* (see
	// internal/agentevents). Never serialized: it is a bearer secret handed to
	// the target process via LECTERN_HOOK_TOKEN, not something the API repeats
	// back to a browser.
	HookToken string `json:"-"`
	// PermissionMode is this session's resolved launch-time choice, "bypass"
	// or "ask" (docs/agent-events.md section 3); empty on a row launched
	// before this column existed, which every reader treats as "bypass".
	PermissionMode string `json:"permission_mode,omitempty"`
	// AgentState/StateSource/StateAt/HookSeenAt are the hook-driven lifecycle
	// signal described in docs/agent-events.md section 2, kept alongside the
	// older screen-scraped Status. StateSource says who wrote AgentState last
	// (hook|screen); HookSeenAt is when a hook last reached this session at
	// all, which is what poll.go's applyPane checks before screen-scraping is
	// allowed to overwrite AgentState again.
	AgentState  string   `json:"agent_state,omitempty"`
	StateSource string   `json:"state_source,omitempty"`
	StateAt     *float64 `json:"state_at,omitempty"`
	HookSeenAt  *float64 `json:"hook_seen_at,omitempty"`

	// Usage — latest values only, from the agent's own statusline/rollout.
	// History lives in usage_daily (see store/usage.go).
	// ContextUsedPct is the agent's "percent of context window used" —
	// distinct from the older, screen-scraped ContextPct above ("percent
	// left until auto-compact"); see the schema comment on context_used_pct.
	ContextUsedPct *int     `json:"context_used_pct,omitempty"`
	ContextTokens  *int     `json:"context_tokens,omitempty"`
	ContextSize    *int     `json:"context_size,omitempty"`
	CostUSD        *float64 `json:"cost_usd,omitempty"`
	LinesAdded     *int     `json:"lines_added,omitempty"`
	LinesRemoved   *int     `json:"lines_removed,omitempty"`
	Rate5hPct      *int     `json:"rate_5h_pct,omitempty"`
	Rate5hReset    *float64 `json:"rate_5h_reset,omitempty"`
	Rate7dPct      *int     `json:"rate_7d_pct,omitempty"`
	Rate7dReset    *float64 `json:"rate_7d_reset,omitempty"`
	UsageAt        *float64 `json:"usage_at,omitempty"`
	// CodexThreadID is the codex rollout session id (see schema.go). Never
	// serialized: it is an internal handle for the rollout reader, not
	// something a card needs to render.
	CodexThreadID string `json:"-"`
	// PrecompactAt is the last PreCompact hook time, for a brief
	// "compacting" card warning (see schema.go comment).
	PrecompactAt *float64 `json:"precompact_at,omitempty"`

	// RepoKey/RepoToplevel/AwarenessBriefingHash/AwarenessBriefingAt are
	// cross-agent awareness's state (internal/awareness, docs/agent-events.md
	// "Cross-agent awareness"). RepoKey IS serialized (unlike the others,
	// which are dedup bookkeeping/paths nobody needs) because the frontend
	// groups live sessions by it client-side to render the "possible
	// duplicate work" notice without a second board-wide endpoint; it is an
	// opaque "<target id>:<git common dir>" string, not a credential.
	RepoKey               string   `json:"repo_key,omitempty"`
	RepoToplevel          string   `json:"-"`
	AwarenessBriefingHash string   `json:"-"`
	AwarenessBriefingAt   *float64 `json:"-"`
	// LastPromptExcerpt/LastPromptAt are the latest UserPromptSubmit prompt,
	// clipped to ~200 chars — "what is this session working on" for a peer
	// summary (internal/awareness).
	LastPromptExcerpt string   `json:"last_prompt_excerpt,omitempty"`
	LastPromptAt      *float64 `json:"last_prompt_at,omitempty"`
	// OtelActiveAt (docs/outcomes.md) is when this session's Claude Code OTLP
	// exporter last reported in. Non-nil is the precedence signal: once set,
	// IngestStatusline stops booking its own usage_daily deltas for this
	// session (the exact OTel numbers own that job from here on).
	OtelActiveAt *float64 `json:"otel_active_at,omitempty"`

	// joined for the UI, which groups sessions by project and names their host
	ProjectName string `json:"project_name,omitempty"`
	TargetName  string `json:"target_name,omitempty"`
	TargetKind  string `json:"target_kind,omitempty"`
}

// SessionFileEdit is the latest edit of one file by one session — see
// internal/awareness and schema.go's session_file_edits comment.
type SessionFileEdit struct {
	ID        int64   `json:"id"`
	SessionID int64   `json:"session_id"`
	RepoKey   string  `json:"-"`
	RelPath   string  `json:"rel_path"`
	At        float64 `json:"at"`
}

// SessionCheck is one run of a session's check command — see internal/checks
// and schema.go's session_checks table doc comment.
type SessionCheck struct {
	ID          int64    `json:"id"`
	SessionID   int64    `json:"session_id"`
	Fingerprint string   `json:"-"`
	Command     string   `json:"command"`
	Status      string   `json:"status"`
	ExitCode    *int     `json:"exit_code"`
	OutputTail  string   `json:"output_tail"`
	StartedAt   float64  `json:"started_at"`
	FinishedAt  *float64 `json:"finished_at"`
	Reason      string   `json:"reason"`
}

// EvalSuite is a named set of agent test cases against one project.
type EvalSuite struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	ProjectID   int64   `json:"project_id"`
	Description string  `json:"description"`
	CreatedAt   float64 `json:"created_at"`
}

// EvalCase is one scenario in a suite: a prompt run from base_ref and graded
// by check_command (falling back to the project's own VerifyCmd when empty).
type EvalCase struct {
	ID           int64  `json:"id"`
	SuiteID      int64  `json:"suite_id"`
	Name         string `json:"name"`
	Prompt       string `json:"prompt"`
	BaseRef      string `json:"base_ref"`
	CheckCommand string `json:"check_command"`
	TimeoutS     int    `json:"timeout_s"`
	SetupCommand string `json:"setup_command"`
}

// EvalVariant is one agent/model/permission combination a run scores every
// case against — the same shape a Best-of-N dispatch variant takes.
type EvalVariant struct {
	Agent          string `json:"agent"`
	Model          string `json:"model"`
	LaunchProfile  string `json:"launch_profile,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
}

// EvalRun is one execution of a suite: cases x variants x repeats.
type EvalRun struct {
	ID           int64   `json:"id"`
	SuiteID      int64   `json:"suite_id"`
	CreatedAt    float64 `json:"created_at"`
	Status       string  `json:"status"`
	VariantsJSON string  `json:"-"`
	Repeats      int     `json:"repeats"`
	Notes        string  `json:"notes"`
}

// EvalResult is one cell of a run's matrix: one case, one variant, one repeat.
type EvalResult struct {
	ID              int64    `json:"id"`
	RunID           int64    `json:"run_id"`
	CaseID          int64    `json:"case_id"`
	VariantIdx      int      `json:"variant_idx"`
	RepeatIdx       int      `json:"repeat_idx"`
	TaskID          *int64   `json:"task_id"`
	AttemptID       *int64   `json:"attempt_id"`
	Status          string   `json:"status"`
	DurationS       *float64 `json:"duration_s"`
	CostUSD         *float64 `json:"cost_usd"`
	InputTokens     *int     `json:"input_tokens"`
	OutputTokens    *int     `json:"output_tokens"`
	DiffFiles       *int     `json:"diff_files"`
	DiffLines       *int     `json:"diff_lines"`
	CheckRC         *int     `json:"check_rc"`
	CheckOutputTail string   `json:"check_output_tail"`
}

// Wrap is one session handoff: the summary an agent wrote for its successor.
type Wrap struct {
	ID            int64   `json:"id"`
	SessionID     int64   `json:"session_id"`
	ProjectID     *int64  `json:"project_id"`
	Summary       string  `json:"summary"`
	Transcript    string  `json:"transcript,omitempty"`
	NextSessionID *int64  `json:"next_session_id"`
	CreatedAt     float64 `json:"created_at"`
}
