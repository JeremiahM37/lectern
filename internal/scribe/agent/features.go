package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// Features are the agentFeatures switches (spec 02 §2.2). They are read by
// the dispatcher; config wiring fills them in from the user's settings.
type Features struct {
	// Desloppify runs a cleanup subagent over the files code tasks edited,
	// after the dispatch and before the project check. Off by default.
	Desloppify bool
	// TierRouting lets the app's subagent factory pick the cheap model tier
	// for explore and code tasks. The factory reads the flag. On by default.
	TierRouting bool
	// DispatchCache gives the subagents of one dispatch a shared AgentBus:
	// file reads are cached between them, and their findings and edits are
	// recorded on it. On by default.
	DispatchCache bool
	// TargetFileValidation checks each code task's file targets against the
	// workdir and notes the ones that do not exist. On by default.
	TargetFileValidation bool
	// VerifyEdits runs a read-only reviewer after the project check, when
	// code tasks changed files. Off by default.
	VerifyEdits bool
}

// DefaultFeatures is the feature set when the user has not set one.
func DefaultFeatures() Features {
	return Features{TierRouting: true, DispatchCache: true, TargetFileValidation: true}
}

// DispatchGuard reports whether subagents may be editing the project right
// now. The agent publishes itself under env.Services["dispatch"] so the
// shell and git tools can refuse repository changes meanwhile.
type DispatchGuard interface {
	DispatchActive() bool
}

// Optional hook capabilities. A Hooks value that implements one of these in
// addition to Hooks gets the call; the others need no change.

// compactObserver is implemented by hooks that run before compaction
// (the PreCompact event).
type compactObserver interface {
	PreCompact(ctx context.Context)
}

// stepNoticer is implemented by hooks that add a note to the hints of each
// model step after the first, so news that arrives mid-turn (another tab's
// file claims) reaches the model without waiting for the next prompt.
type stepNoticer interface {
	StepNotice(ctx context.Context, step int) string
}

// notifier is implemented by hooks that receive Notification events, sent
// when a turn ends in an error the user should see.
type notifier interface {
	Notification(ctx context.Context, msg string)
}

// toolInputRewriter is implemented by hooks whose PreToolUse result can
// replace the tool's input (hookSpecificOutput.updatedInput). It takes the
// place of Hooks.PreTool for the call: it returns the input to run with, and
// a non-empty block reason refuses the call.
type toolInputRewriter interface {
	PreToolUpdate(ctx context.Context, tool string, args json.RawMessage) (json.RawMessage, string)
}

// defaultToolTimeout bounds one tool call. A tool that never returns would
// otherwise hold the turn forever.
const defaultToolTimeout = 900 * time.Second

// unboundedTools are not held to the tool timeout: a dispatch runs whole
// subagents, which can take longer than any tool should, and the two
// interactive tools wait on the user.
var unboundedTools = map[string]bool{"dispatch": true, "ask_user": true, "plan": true}

func unbounded(tool string) bool { return unboundedTools[tool] }

// journalCap bounds the compaction log.
const journalCap = 200

// modelLogCap bounds the model log: the newest 500 records are kept, in
// memory only (spec 03 §9.4).
const modelLogCap = 500

// modelErrorTextCap is how many bytes of an error are kept in the model log.
// The full text still goes to the event stream.
const modelErrorTextCap = 500

// modelLogKinds are the events the model log keeps: each model call's
// usage, and the retries, fallbacks and errors that surround it.
var modelLogKinds = map[EventKind]bool{
	EvStepFinish: true, EvRetry: true, EvFallback: true, EvError: true,
}

// Call sources, as recorded in a model-log entry's Meta["source"].
const (
	callSourceMain       = "main"
	callSourceSubagent   = "subagent"
	callSourceCompaction = "compaction"
)

// metaTab is the Meta key for the TUI tab that made a model call.
const metaTab = "tab"

// Call states, as recorded in a model-log entry's Meta["state"].
const (
	callStateOK    = "ok"
	callStateError = "error"
)

// callMeta is the Meta of a model-call record: which agent made the call
// and how it ended. Duration on the same event is the call's latency.
func callMeta(source, state string) map[string]any {
	return map[string]any{"source": source, "state": state}
}

// callSource names who makes this agent's model calls: a subagent when it
// was given an ID, the turn's agent otherwise.
func (a *Agent) callSource() string {
	if a.opts.ID != "" {
		return callSourceSubagent
	}
	return callSourceMain
}

// compactLogKinds are the events the compaction log keeps.
var compactLogKinds = map[EventKind]bool{EvCompactStart: true, EvCompactDone: true}

// estimateTokens approximates the tokens a conversation takes, before the
// provider has reported a real count: about four characters per token.
func estimateHistoryTokens(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		for _, p := range m.Parts {
			n += len(p.Text) + len(p.Args) + len(p.Result) + len(p.Signature)
		}
	}
	return n / 4
}

// withService returns a copy of services with key set to v. The caller's
// map is never written, because the same Env template is shared by calls
// that run in parallel.
func withService(services map[string]any, key string, v any) map[string]any {
	out := make(map[string]any, len(services)+1)
	for k, x := range services {
		out[k] = x
	}
	out[key] = v
	return out
}

// dispatchFailedNotice is the Notification text for a turn that ended in an
// error the user should see.
func dispatchFailedNotice(stats TurnStats) string {
	msg := strings.TrimSpace(stats.Error)
	if msg == "" {
		return "The turn ended without a result."
	}
	return "The turn ended with an error: " + msg
}
