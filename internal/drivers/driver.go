// Package drivers is the one seam between "an unattended agent run" and
// everything that wants to start one: the scheduler (queued attempts today
// launched by tmux + a polled events.jsonl file, unchanged), the new steering
// endpoint, and — the reason this package exists standalone rather than as
// scheduler-internal helpers — future best-of-N and agent-eval workers that
// want to start a run directly, with no task, board or database involved.
//
// A Driver always talks to its agent through an executor.Executor (Run /
// ReadFile / WriteFile), never a raw os/exec pipe: attempts already run on
// arbitrary targets (local, ssh, pct, sandbox), and a driver that only worked
// locally would be useless for most of them. Two execution shapes exist:
//
//   - execDriver (claude-exec, codex-exec, gemini-exec): exactly today's
//     tmux + redirected events.jsonl/exit_code, wrapping the SAME
//     agents.Launcher.Command and agents.ParseStreamLines the scheduler's own
//     launch/poll already use, so there is one source of truth for the CLI
//     flags and one for the event shapes. Not steerable — the process reads
//     its prompt as an argument and exits after its one turn, same as today.
//
//   - streamRun-based drivers (claude-steer, codex-appserver): the process's
//     stdin is a FIFO kept open by a small pump script for the run's whole
//     lifetime (see fifo.go), so a driver can append a message at any point —
//     mid-run steering — and the process only exits once the driver closes
//     the pump with an explicit end sentinel. claude-steer speaks Claude
//     Code's `--input-format stream-json` protocol; codex-appserver speaks
//     `codex app-server`'s JSON-RPC-over-stdio protocol (see
//     codex_appserver.go for where those message shapes came from).
package drivers

import (
	"context"
	"fmt"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/agents"
	"github.com/JeremiahM37/lectern/v2/internal/broker"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// Driver kinds, persisted on the attempt record (store.Attempt.Driver) so a
// later worker can see and reuse the choice instead of re-deriving it from
// agent name and permission mode.
const (
	KindClaudeExec     = "claude-exec"
	KindClaudeSteer    = "claude-steer"
	KindCodexExec      = "codex-exec"
	KindCodexAppServer = "codex-appserver"
	KindGemini         = "gemini-exec"
	// KindACP is a configured custom agent that speaks the Agent Client
	// Protocol (agentclientprotocol.com) — Zed's claude-code-acp/codex-acp
	// adapters, Gemini CLI's --experimental-acp, or any other ACP agent.
	// Unlike KindGeneric it IS a structured driver (see acp.go): it gets
	// live timeline events, mid-run steering and gated approvals for free,
	// the same way codex-appserver does for codex. Select() never returns
	// this on its own — a configured agent's TaskDefinition.ACP being set is
	// what chooses it, checked by the caller (scheduler.go) before falling
	// back to Select, since Select's (agent, builtin) signature has no way to
	// see a custom agent's definition.
	KindACP = "acp"
	// KindGeneric is a configured custom CLI (agents.TaskDefinition, not
	// Builtin, and with no ACP definition either). The scheduler's existing
	// generic-task tmux path (agents.Launcher.Command's genericTaskCommand
	// branch) keeps handling those attempts directly, unchanged. It is a
	// legal Select() answer so the attempt record is still honest about what
	// ran it.
	KindGeneric = "task-generic"
)

// Select turns "which agent, launched how" into a driver choice. It is the
// one place that decision is made, so the attempt record (queue time), the
// API's permission-mode validation and the scheduler's launch branch cannot
// disagree about what a given (agent, permission_mode) combination means.
//
// steerable and codex-gated approvals are both opt-in through permission_mode
// rather than a new field: "steerable" is a claude-only mode that behaves
// like acceptEdits (no PreToolUse gate) but launches the streaming-input
// driver so mid-run messages are possible; "default" on codex — rejected
// before this package existed, since codex has no PreToolUse-equivalent hook
// — now selects the app-server driver, whose approval requests are routed
// through the same broker claude's hook uses (see codex_appserver.go).
func Select(agent string, builtin bool, permissionMode string) string {
	switch {
	case !builtin:
		return KindGeneric
	case agent == "claude" && permissionMode == "steerable":
		return KindClaudeSteer
	case agent == "claude":
		return KindClaudeExec
	case agent == "codex" && permissionMode == "default":
		return KindCodexAppServer
	case agent == "codex":
		return KindCodexExec
	case agent == "gemini":
		return KindGemini
	default:
		return KindGeneric
	}
}

// Spec is one unattended run's parameters. It deliberately mirrors
// agents.LaunchSpec's fields (same names, same meaning) for the ones an
// execDriver forwards unchanged, plus what only a driver needs: the prompt
// text itself (stageRuntime already writes prompt.md; a standalone caller has
// no stageRuntime, so a driver writes it from here), and the approval wiring
// for codex-appserver.
type Spec struct {
	Agent          string
	Bin            string // resolved binary; empty means the adapter's own default
	Worktree       string
	TmuxSession    string
	PermissionMode string
	Model          string
	ResumeSession  string
	Sandbox        bool
	Env            map[string]string
	SettingsPath   string
	MCPConfig      string
	StrictMCP      bool
	Prompt         string

	// ACPArgs are the fixed command-line arguments for the acp driver's agent
	// process (agents.ACPDefinition.Args — e.g. nothing for
	// "npx -y @zed-industries/claude-code-acp", or ["--experimental-acp"] for
	// gemini). Bin carries the command itself; unused by every other driver.
	ACPArgs []string

	// Driver overrides Select's answer. Run(...) uses it when set; StartFor
	// callers (the scheduler) already know the kind and pass it directly.
	Driver string

	// Broker and AttemptID route codex-appserver's approval requests through
	// the operator's normal decision path. A nil Broker auto-approves —
	// standalone callers (evals, best-of-N) have no board to ask and opt into
	// that explicitly by leaving it unset.
	Broker    *broker.Broker
	AttemptID int64
	// ApprovalTimeout bounds how long an approval waits before auto-denying,
	// separate from the broker's own expiry (which resolves an approval that
	// nobody answers, for the *record*) because a driver with no operator
	// watching it must not hang the run forever. Zero uses a 2-minute default.
	ApprovalTimeout time.Duration
	// HandshakeTimeout bounds codex-appserver's initialize round trip before
	// it gives up and falls back to codex exec --json. Zero uses 10s in
	// production; tests shrink it so a deliberately-failing stub does not
	// stall the suite.
	HandshakeTimeout time.Duration

	// PollInterval paces a driver's background poll loop. Zero uses 1s
	// (matches the scheduler's own default cadence); tests shrink it so a
	// stub-binary run finishes quickly.
	PollInterval time.Duration
}

func (s Spec) pollInterval() time.Duration {
	if s.PollInterval > 0 {
		return s.PollInterval
	}
	return time.Second
}

func (s Spec) approvalTimeout() time.Duration {
	if s.ApprovalTimeout > 0 {
		return s.ApprovalTimeout
	}
	return 2 * time.Minute
}

func (s Spec) handshakeTimeout() time.Duration {
	if s.HandshakeTimeout > 0 {
		return s.HandshakeTimeout
	}
	return 10 * time.Second
}

// Result is what a run leaves behind once it ends.
type Result struct {
	ExitCode      int
	SessionID     string
	CostUSD       *float64
	InputTokens   *int
	OutputTokens  *int
	ContextTokens *int
	Raw           map[string]any
	// Err is set when the run ended abnormally (target unreachable, cancelled,
	// context cancelled) rather than by the process exiting.
	Err string
}

// Handle is one live unattended run.
type Handle interface {
	// Events streams normalised agent.Event records as they are parsed from
	// the target. It closes once the run has ended; drain it (or let Run do
	// so) rather than leaving it unread, or the driver's internal loop can
	// block delivering to a full channel.
	Events() <-chan agents.Event
	// Send delivers a follow-up user message while the run is still live. A
	// non-steerable driver (claude-exec, codex-exec, gemini-exec) always
	// returns an error: those processes already consumed their one prompt as
	// an argument and exit after their single turn.
	Send(ctx context.Context, text string) error
	// Cancel stops the run without waiting for a natural end. Safe to call
	// after the run has already ended.
	Cancel(ctx context.Context) error
	// Wait blocks until the run ends and returns its final result. Safe to
	// call more than once; every caller after the first gets the same result.
	Wait(ctx context.Context) (Result, error)
}

// Driver starts one unattended run on a target.
type Driver interface {
	Start(ctx context.Context, ex executor.Executor, spec Spec) (Handle, error)
}

// StartFor starts the named driver kind directly, bypassing Select. The
// scheduler uses this — it already computed and persisted the kind at queue
// time (see store.Attempt.Driver) and must not have it silently recomputed
// out from under a running attempt if a project's defaults change mid-flight.
func StartFor(kind string, ctx context.Context, ex executor.Executor, spec Spec) (Handle, error) {
	switch kind {
	case KindClaudeSteer:
		return claudeSteerDriver{}.Start(ctx, ex, spec)
	case KindCodexAppServer:
		return codexAppServerDriver{}.Start(ctx, ex, spec)
	case KindACP:
		return acpDriver{}.Start(ctx, ex, spec)
	case KindClaudeExec:
		return execDriver{Agent: "claude"}.Start(ctx, ex, spec)
	case KindCodexExec:
		return execDriver{Agent: "codex"}.Start(ctx, ex, spec)
	case KindGemini:
		return execDriver{Agent: "gemini"}.Start(ctx, ex, spec)
	default:
		return nil, fmt.Errorf("drivers: no driver for kind %q", kind)
	}
}

// Run is the standalone entry point: start a driver against an executor and
// block for its result, with no scheduler, database or board involved. This
// is what a best-of-N or agent-eval worker calls directly.
//
// It drains Events() itself and discards them; a caller that wants the
// timeline should use StartFor (or Select + StartFor) and read Handle.Events
// itself instead of calling Run.
func Run(ctx context.Context, ex executor.Executor, spec Spec) (Result, error) {
	kind := spec.Driver
	if kind == "" {
		kind = Select(spec.Agent, true, spec.PermissionMode)
	}
	run, err := StartFor(kind, ctx, ex, spec)
	if err != nil {
		return Result{}, err
	}
	go func() {
		for range run.Events() {
		}
	}()
	return run.Wait(ctx)
}
