// Package tui is Scribe's full-screen terminal interface (bubbletea).
// It talks to the rest of Scribe only through Backend, so it can be driven
// by a fake in tests and by internal/scribe/app in production.
package tui

import (
	"context"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// ProviderStatus is one row of the provider list.
type ProviderStatus struct {
	ID, Name string
	HasKey   bool
	Local    bool // runs locally, no key needed (ollama, lmstudio)
	Icon     string
	// KeySource is "env:NAME", "store" or "" when no key resolves.
	KeySource string
	// DefaultModel is the model the provider suggests; the wizard preselects it.
	DefaultModel string
}

// SessionInfo is one saved session.
type SessionInfo struct {
	ID, Title, Model, Workdir string
	Updated                   time.Time
	Turns                     int
	CostUSD                   float64
	Preview                   string // first user message
}

// CheckpointInfo is one turn that changed files.
type CheckpointInfo struct {
	Turn    int
	Prompt  string
	Files   []string
	Created time.Time
	Undone  bool
}

// MemoryItem is one stored memory.
type MemoryItem struct {
	ID, Scope, Category, Title, Body string
	Pinned                           bool
	Updated                          time.Time
}

// ServerStatus is an MCP server or language server.
type ServerStatus struct {
	Name, Kind, State, Error string // Kind: mcp | lsp; State: ready | starting | failed | stopped
	Tools                    int
}

// MapStats summarises the code map.
type MapStats struct {
	Files, Symbols, Edges int
	Languages             map[string]int
	Building              bool
	Progress              float64 // 0..1 while building
}

// GitStatus is shown in the header.
type GitStatus struct {
	Branch        string
	Dirty         bool
	Ahead, Behind int
	Changed       []string
}

// Backend is everything the TUI needs from Scribe. All methods may be
// called from the bubbletea goroutine; long ones take a context and are
// run inside tea.Cmds by the TUI.
type Backend interface {
	// Events delivers agent events (all agents, including subagents) and
	// is never closed while the TUI runs.
	Events() <-chan agent.Event
	// Prompts delivers approval and question requests from tools; the TUI
	// answers through the request's Reply channel.
	Prompts() <-chan Prompt

	Submit(prompt string, images []llm.Part) // starts a turn (async); events follow
	Steer(msg string) bool                   // queue a message for the running turn
	Abort()                                  // stop the running turn
	Running() bool

	Workdir() string
	Version() string
	SessionID() string
	SessionTitle() string
	Transcript() []agent.Event // events of the current session, for re-rendering on resume

	Model() string // provider/model
	SetModel(ref string) error
	Models(ctx context.Context, provider string) ([]llm.ModelInfo, error)
	Providers() []ProviderStatus
	SetKey(provider, key string) error
	Mode() string
	SetMode(mode string)
	Reasoning() string
	SetReasoning(level string)

	Usage() (llm.Usage, float64) // cumulative tokens, USD
	Context() (used, window int) // tokens in the last request, model window
	Compact(ctx context.Context) error

	NewSession() error
	Sessions() []SessionInfo
	Resume(id string) error
	Fork() (string, error)
	Rename(title string) error
	Export(format string) (path string, err error) // "markdown" | "json"

	Checkpoints() []CheckpointInfo
	Undo() (CheckpointInfo, error)
	Redo() (CheckpointInfo, error)
	RestoreTo(turn int) error
	TurnDiff(turn int) string

	Memories(query string) []MemoryItem
	SaveMemory(m MemoryItem) error
	DeleteMemory(id string) error

	Git() GitStatus
	GitDiff() string
	GitCommit(message string) error
	SuggestCommitMessage(ctx context.Context) (string, error)

	Servers() []ServerStatus
	RestartServer(name string) error
	// MCPServers lists every configured MCP server with the definition the
	// editor shows, including disabled and untrusted ones.
	MCPServers() []MCPServerSpec
	// SaveMCPServer writes spec to its scope's config and applies it live.
	// was is the definition being edited, or nil for a new server.
	SaveMCPServer(spec MCPServerSpec, was *MCPServerSpec) error
	RemoveMCPServer(name, scope string) error
	MapStats() MapStats
	RebuildMap()
	Jobs() []Job
	KillJob(id string) error
	Skills() []string
	// InstallSkill installs from a local directory or git URL (optionally
	// "#subdir") into scope "project" or "global". It returns the skill name.
	InstallSkill(src, scope string) (string, error)
	// ActiveSkills lists the skills loaded into context for this project.
	ActiveSkills() []string
	// SetSkillActive loads a skill into context, or unloads it, from the next request.
	SetSkillActive(name string, on bool) error
	Tools() []ToolInfo
	SetToolEnabled(name string, on bool)

	Config() map[string]any // current settings as a flat map for the settings screen
	SetConfig(key string, value any, project bool) error
	Theme() string
	SetTheme(name string)

	// Shell runs a user `!command` in the workdir and returns its output.
	Shell(ctx context.Context, command string) (string, int, error)
	// FileCandidates returns workdir-relative paths matching a fuzzy query
	// for @-mentions.
	FileCandidates(query string, limit int) []string

	Close() error
}

// Job is a background shell job.
type Job struct {
	ID, Command, Status string
	Started             time.Time
	ExitCode            int
	Tail                []string
}

// ToolInfo is a tool for the tools screen.
type ToolInfo struct {
	Name, Description string
	Enabled, ReadOnly bool
	Source            string // builtin | mcp:<server>
}

// Prompt is an approval or question the TUI must answer. Exactly one of
// Approval / Question is set. The TUI sends exactly one value on Reply.
type Prompt struct {
	Approval *tools.Approval
	Question *tools.Question
	Reply    chan PromptReply
}

// PromptReply answers a Prompt.
type PromptReply struct {
	Decision tools.Decision // for approvals
	Answer   string         // for questions
}

// Optional capabilities. A Backend that has one of these implements it; the
// command or picker that needs it checks by type assertion and says so when
// it is missing, so the Backend interface itself stays as it was.

// MarkdownExporter copies the session as Markdown without writing a file.
type MarkdownExporter interface {
	ExportMarkdown() (string, error)
}

// GitRepoOps runs the repository commands behind /git init, /git branch and
// /git pull. Output is git's own text.
type GitRepoOps interface {
	GitInit() (string, error)
	GitBranch(name string) error
	GitPull() (string, error)
}

// StagingCommitter commits with an explicit choice about staging: stageAll
// adds every change first, otherwise only what is already staged is committed.
type StagingCommitter interface {
	GitCommitWith(message string, stageAll bool) error
}

// ContextClearer unloads what is injected into context. target is "skills",
// "memory", or "" for everything.
type ContextClearer interface {
	ClearContext(target string) error
}

// ConfigMover saves a setting into one scope and removes it from the other,
// so it lives in exactly one place after the move.
type ConfigMover interface {
	MoveConfig(key string, value any, toProject, fromProject bool) error
}
