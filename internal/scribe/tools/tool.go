// Package tools holds the tools Scribe's agent can call. Each tool is a
// value implementing Tool; the agent loop looks them up in a Registry.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// Result is what a tool returns to the model (Output) and to the UI
// (Display and Meta). Output is always plain text the model reads.
type Result struct {
	Output  string `json:"output"`
	IsError bool   `json:"is_error,omitempty"`
	// Tag is a short machine code for an error, so the UI and hooks can
	// branch without parsing Output. Empty when there is nothing to branch
	// on. Known values: "forbidden", "not found", "concurrent modification",
	// "stale_undo", "contested".
	Tag string `json:"tag,omitempty"`
	// Summary is a one-line human summary for tool rows, e.g. "42 lines",
	// "3 matches in 2 files", "+5 -2". Optional.
	Summary string `json:"summary,omitempty"`
	// Diff is a unified diff for edit/write tools, rendered by the UI.
	Diff string `json:"diff,omitempty"`
	// Files lists paths (relative to the workdir) the call created,
	// changed or deleted. Used for checkpoints, "files edited" and the
	// changed-files bar.
	Files []string `json:"files,omitempty"`
	// Meta is free-form structured data for the UI.
	Meta map[string]any `json:"meta,omitempty"`
}

// Errorf builds an error Result.
func Errorf(format string, a ...any) Result {
	return Result{Output: fmt.Sprintf(format, a...), IsError: true}
}

// Approval is a question to the user before a risky action.
type Approval struct {
	Tool   string // tool name
	Kind   string // "shell", "write_outside", "destructive", "web", "mcp"
	Title  string // short line, e.g. "Run shell command"
	Detail string // the command / path / URL
	Danger bool   // destructive: render red, default to Deny
}

// Decision is the user's answer to an Approval.
type Decision int

const (
	Deny Decision = iota
	AllowOnce
	AllowSession // allow this kind of action for the rest of the session
)

// Question is an ask_user prompt.
type Question struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
	// AllowFreeText lets the user type an answer instead of picking.
	AllowFreeText bool `json:"allow_free_text,omitempty"`
	// Kind is "" for a plain question or "plan_review" when the agent is
	// asking the user to approve a plan (the UI shows the plan view).
	Kind string `json:"kind,omitempty"`
	// Body is extra content to show with the question (the plan markdown).
	Body string `json:"body,omitempty"`
}

// Host is what the agent's surroundings (TUI, headless runner, Lectern
// session) provide to tools. Every method must be safe to call from any
// goroutine. Implementations for headless use may answer without a user.
type Host interface {
	Approve(ctx context.Context, a Approval) Decision
	Ask(ctx context.Context, q Question) (string, error)
	// Progress streams partial output of a long-running call (shell
	// output lines, subagent steps) to the UI. May be a no-op.
	Progress(callID, text string)
}

// FileTracker records file contents before the agent changes them, so a
// turn can be undone (checkpoints) and edits can be refused on files the
// model has not read or that changed since it read them.
type FileTracker interface {
	// BeforeWrite snapshots abs before it is modified (or notes that it
	// did not exist). Idempotent within a checkpoint.
	BeforeWrite(abs string)
	// MarkRead records that the model has seen abs at its current mtime.
	MarkRead(abs string)
	// StaleSinceRead reports whether abs changed on disk since MarkRead
	// (false when it was never read).
	StaleSinceRead(abs string) bool
	// WasRead reports whether the model has read abs this session.
	WasRead(abs string) bool
}

// Env is the per-call environment. Optional services are nil when not
// available; tools must check.
type Env struct {
	Workdir string // absolute project root
	CallID  string // the tool call id
	Host    Host
	Files   FileTracker
	// Services is a bag of optional subsystems keyed by name ("repomap",
	// "lsp", "memory", "skills", "plan", "tasks", "dispatch", "mcp"). Each
	// package that provides one documents its key and type.
	Services map[string]any
	// Mode is the agent mode ("default", "plan", "auto", ...). Tools that
	// self-approve in auto mode read it.
	Mode string
	// Scope identifies the conversation context the call belongs to: one
	// agent's history, renewed whenever that history is compacted, reset or
	// replaced. Tools that remember what the model has already seen (the read
	// tool's "unchanged" notices) key that memory by Scope.
	Scope string
	// Claims reports the files other tabs in this process have claimed. Nil
	// when the tab has no cross-tab registry (tests, headless runs).
	Claims ClaimSource
}

// ClaimSource lists live file claims held by other tabs. Paths are absolute.
type ClaimSource interface {
	// OtherClaims maps each file another tab claimed to that tab's label.
	OtherClaims() map[string]string
}

// Service returns Services[key] as T.
func Service[T any](e *Env, key string) (T, bool) {
	var zero T
	if e == nil || e.Services == nil {
		return zero, false
	}
	v, ok := e.Services[key].(T)
	if !ok {
		return zero, false
	}
	return v, true
}

// Tool is one callable tool.
type Tool interface {
	Spec() llm.ToolSpec
	// ReadOnly reports whether the tool never changes files or runs
	// commands. Restricted modes (plan, architect, ...) only allow these.
	ReadOnly() bool
	Run(ctx context.Context, env *Env, args json.RawMessage) Result
}

// CallReadOnly is implemented by tools whose read-only-ness depends on the
// arguments (git status vs git commit). The agent's mode gate and its
// parallel scheduler prefer it over Tool.ReadOnly.
type CallReadOnly interface {
	ReadOnlyCall(args json.RawMessage) bool
}

// IsReadOnlyCall reports whether this particular call of t only reads.
func IsReadOnlyCall(t Tool, args json.RawMessage) bool {
	if c, ok := t.(CallReadOnly); ok {
		return c.ReadOnlyCall(args)
	}
	return t.ReadOnly()
}

// Registry is a name-indexed set of tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{tools: map[string]Tool{}} }

// Register adds t, replacing any tool with the same name.
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := t.Spec().Name
	if _, ok := r.tools[name]; !ok {
		r.order = append(r.order, name)
	}
	r.tools[name] = t
}

// Unregister removes the tool called name. It reports whether one was
// registered. Specs and Names no longer include it, so the next model
// request does not offer it.
func (r *Registry) Unregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[name]; !ok {
		return false
	}
	delete(r.tools, name)
	for i, n := range r.order {
		if n == name {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	return true
}

// Get returns the tool named name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Names returns tool names in registration order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.order...)
}

// Specs returns specs in registration order, restricted to names when
// names is non-nil.
func (r *Registry) Specs(names map[string]bool) []llm.ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []llm.ToolSpec
	for _, n := range r.order {
		if names != nil && !names[n] {
			continue
		}
		out = append(out, r.tools[n].Spec())
	}
	return out
}

// SortedNames is Names sorted alphabetically.
func (r *Registry) SortedNames() []string {
	n := r.Names()
	sort.Strings(n)
	return n
}

// Schema is a helper to build a JSON Schema object for tool specs:
//
//	Schema(map[string]any{"path": Str("file path")}, "path")
func Schema(props map[string]any, required ...string) json.RawMessage {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	b, _ := json.Marshal(s)
	return b
}

// Str, Int, Bool, Arr and Enum build property schemas.
func Str(desc string) map[string]any  { return map[string]any{"type": "string", "description": desc} }
func Int(desc string) map[string]any  { return map[string]any{"type": "integer", "description": desc} }
func Bool(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
func Arr(desc string, items map[string]any) map[string]any {
	return map[string]any{"type": "array", "description": desc, "items": items}
}
func Enum(desc string, vals ...string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": vals}
}

// Decode unmarshals args into v, tolerating an empty payload.
func Decode(args json.RawMessage, v any) error {
	if len(args) == 0 || string(args) == "null" {
		return nil
	}
	if err := json.Unmarshal(args, v); err != nil {
		// Say what to do next: a model that only hears "invalid" tends to
		// give up on the tool and write its output as prose instead.
		return fmt.Errorf("invalid arguments: %w. Match the tool's schema and call it again", err)
	}
	return nil
}
