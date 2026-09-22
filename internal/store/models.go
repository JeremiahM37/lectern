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
	MCPJSON               string  `json:"-"`
	StrictMCP             int     `json:"strict_mcp"`
	PermissionsJSON       string  `json:"permissions_json"`
	GateMatcher           string  `json:"gate_matcher"`
	DefaultAgent          string  `json:"default_agent"`
	CapabilityProfile     string  `json:"capability_profile"`
	DefaultPermissionMode string  `json:"default_permission_mode"`
	SkillSourcesJSON      string  `json:"skill_sources_json"`
	MemoryTopic           string  `json:"memory_topic"`
	MemoryStatus          string  `json:"memory_status"`
	CreatedAt             float64 `json:"created_at"`

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

// Approval is one tool call a gated agent is blocked on.
type Approval struct {
	ID        int64    `json:"id"`
	AttemptID int64    `json:"attempt_id"`
	ToolName  string   `json:"tool_name"`
	InputJSON string   `json:"-"`
	Status    string   `json:"status"`
	DecidedBy string   `json:"decided_by"`
	Note      string   `json:"note"`
	CreatedAt float64  `json:"created_at"`
	DecidedAt *float64 `json:"decided_at"`

	Input     map[string]any `json:"input"`
	TaskID    int64          `json:"task_id,omitempty"`
	AttemptN  int            `json:"attempt_n,omitempty"`
	TaskTitle string         `json:"task_title,omitempty"`
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

	// joined for the UI, which groups sessions by project and names their host
	ProjectName string `json:"project_name,omitempty"`
	TargetName  string `json:"target_name,omitempty"`
	TargetKind  string `json:"target_kind,omitempty"`
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
