// Package agent is Scribe's agent loop: it builds each model request,
// streams the response, runs tool calls, and reports everything it does as
// Events. The TUI, the headless runner and Lectern's session bridge are all
// consumers of the same Event stream.
package agent

import (
	"encoding/json"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// EventKind discriminates Event.
type EventKind string

const (
	EvTurnStart     EventKind = "turn_start"     // Prompt, Model, Mode
	EvStepStart     EventKind = "step_start"     // Step (1-based)
	EvText          EventKind = "text"           // Text delta
	EvReasoning     EventKind = "reasoning"      // Text delta
	EvToolStart     EventKind = "tool_start"     // CallID, Tool (args still streaming)
	EvToolArgs      EventKind = "tool_args"      // CallID, Text = partial JSON delta
	EvToolRun       EventKind = "tool_run"       // CallID, Tool, Args (execution begins)
	EvToolProgress  EventKind = "tool_progress"  // CallID, Text (streamed output)
	EvToolResult    EventKind = "tool_result"    // CallID, Tool, Result, Duration
	EvStepFinish    EventKind = "step_finish"    // Step, Usage (this step), Total, CostUSD (cumulative)
	EvRetry         EventKind = "retry"          // Attempt, MaxAttempts, Category, Text (reason), Delay
	EvFallback      EventKind = "fallback"       // Model (new model ref), Text (reason)
	EvWarning       EventKind = "warning"        // Text
	EvError         EventKind = "error"          // Category, Text
	EvSteering      EventKind = "steering"       // Text (queued user message consumed mid-turn)
	EvCompactStart  EventKind = "compact_start"  // Text (reason)
	EvCompactDone   EventKind = "compact_done"   // Text (summary), Tokens before/after in Meta
	EvPlan          EventKind = "plan"           // Plan (proposed or updated)
	EvTasks         EventKind = "tasks"          // Tasks (task_list changed)
	EvSubagent      EventKind = "subagent"       // Agent (id, task, status, model, step, tokens)
	EvContext       EventKind = "context"        // ContextUsed, ContextWindow (after each step)
	EvTurnDone      EventKind = "turn_done"      // Stats; Text = final answer
)

// Event is one thing the agent did. Only the fields for its Kind are set.
type Event struct {
	Kind EventKind `json:"kind"`
	At   time.Time `json:"at"`

	// AgentID is "" for the main agent, else the subagent id.
	AgentID string `json:"agent_id,omitempty"`

	Text     string          `json:"text,omitempty"`
	Prompt   string          `json:"prompt,omitempty"`
	Model    string          `json:"model,omitempty"` // provider/model
	Mode     string          `json:"mode,omitempty"`
	Step     int             `json:"step,omitempty"`
	CallID   string          `json:"call_id,omitempty"`
	Tool     string          `json:"tool,omitempty"`
	Args     json.RawMessage `json:"args,omitempty"`
	Result   *tools.Result   `json:"result,omitempty"`
	Duration time.Duration   `json:"duration,omitempty"`

	Usage   llm.Usage `json:"usage,omitempty"`
	Total   llm.Usage `json:"total,omitempty"`
	CostUSD float64   `json:"cost_usd,omitempty"`

	Attempt     int               `json:"attempt,omitempty"`
	MaxAttempts int               `json:"max_attempts,omitempty"`
	Category    llm.ErrorCategory `json:"category,omitempty"`
	Delay       time.Duration     `json:"delay,omitempty"`

	ContextUsed   int `json:"context_used,omitempty"`
	ContextWindow int `json:"context_window,omitempty"`

	Plan  *Plan      `json:"plan,omitempty"`
	Tasks []Task     `json:"tasks,omitempty"`
	Agent *AgentInfo `json:"agent,omitempty"`
	Stats *TurnStats `json:"stats,omitempty"`

	Meta map[string]any `json:"meta,omitempty"`
}

// Plan is a proposed implementation plan (plan mode) and its progress.
type Plan struct {
	Title   string     `json:"title"`
	Summary string     `json:"summary,omitempty"`
	Steps   []PlanStep `json:"steps"`
	// Body is the full markdown of the plan as written by the model.
	Body string `json:"body,omitempty"`
	// Path is where the plan was saved (.scribe/plans/<slug>.md).
	Path string `json:"path,omitempty"`
}

// PlanStep is one step of a Plan.
type PlanStep struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Status string `json:"status"` // pending | active | done | skipped
	Files  []string `json:"files,omitempty"`
}

// Task is one task_list entry.
type Task struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Status string `json:"status"` // pending | in_progress | done | cancelled
}

// AgentInfo describes a subagent for the multi-agent display.
type AgentInfo struct {
	ID     string    `json:"id"`
	Task   string    `json:"task"`
	Role   string    `json:"role"`   // explore | code | web
	Status string    `json:"status"` // pending | running | done | error
	Model  string    `json:"model"`
	Step   int       `json:"step"`
	Tokens llm.Usage `json:"tokens"`
	Last   string    `json:"last,omitempty"` // last tool/action line
	Error  string    `json:"error,omitempty"`
}

// TurnStats summarises a finished turn.
type TurnStats struct {
	Steps       int           `json:"steps"`
	Tokens      llm.Usage     `json:"tokens"`
	CostUSD     float64       `json:"cost_usd"`
	ToolCalls   []string      `json:"tool_calls"`
	FilesEdited []string      `json:"files_edited"`
	Duration    time.Duration `json:"duration"`
	Error       string        `json:"error,omitempty"`
	Aborted     bool          `json:"aborted,omitempty"`
}

// Sink receives events. Implementations must not block for long; the TUI
// forwards into a buffered channel.
type Sink func(Event)
