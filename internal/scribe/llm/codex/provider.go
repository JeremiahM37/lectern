package codex

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// defaultTimeout bounds one codex call. A turn that runs with tools can take
// minutes, so this is generous, but a hung CLI must not hold a chat forever.
const defaultTimeout = 10 * time.Minute

// execTerminalGrace is how long a closing error waits for a consumer that is
// still reading. After that the send is dropped, so an abandoned channel
// cannot hold the goroutine.
const execTerminalGrace = 2 * time.Second

// fallbackModelIDs is used when no ListModels is given. Those names are the
// ones this plan is known to offer; the live list comes from app-server.
var fallbackModelIDs = []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}

// Options configures a Provider. The zero value runs codex from PATH with a
// 10 minute limit per call and the working directory of the process.
type Options struct {
	Binary     string                                             // "" -> DefaultBinary
	Cwd        func() string                                      // nil -> os.Getwd
	ListModels func(ctx context.Context) ([]llm.ModelInfo, error) // nil -> built-in fallback list
	Timeout    time.Duration                                      // 0 -> 10 min per call
}

// Provider runs model calls through the codex CLI. It keeps nothing between
// calls: each Stream starts one `codex exec`, and the CLI holds the login.
type Provider struct {
	opts Options
}

// New returns a Provider for o.
func New(o Options) *Provider { return &Provider{opts: o} }

// ID returns ProviderID.
func (p *Provider) ID() string { return ProviderID }

// Models returns the models the plan offers. The subscription has no per-token
// price, so the prices are zero and CatalogPriced says a zero means free.
func (p *Provider) Models(ctx context.Context) ([]llm.ModelInfo, error) {
	if p.opts.ListModels != nil {
		return p.opts.ListModels(ctx)
	}
	out := make([]llm.ModelInfo, 0, len(fallbackModelIDs))
	for _, id := range fallbackModelIDs {
		out = append(out, llm.ModelInfo{ID: id, Name: id, Tools: true, CatalogPriced: true})
	}
	return out, nil
}

// Stream starts one model call and returns its events. The channel always ends
// with exactly one EvFinish or EvError, then closes. The call runs in a
// goroutine, so a failure to start is an EvError too, not a second error path.
func (p *Provider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	ch := make(chan llm.Event, 32)
	go func() {
		defer close(ch)
		evs, err := p.run(ctx, req)
		if err != nil {
			sendTerminal(ch, err)
			return
		}
		for _, ev := range evs {
			select {
			case ch <- ev:
			case <-ctx.Done():
				sendTerminal(ch, ctx.Err())
				return
			}
		}
	}()
	return ch, nil
}

// sendTerminal delivers the closing error, waiting briefly so a consumer that
// is still reading gets it even though ctx was cancelled.
func sendTerminal(ch chan<- llm.Event, err error) {
	select {
	case ch <- llm.Event{Type: llm.EvError, Err: err}:
	case <-time.After(execTerminalGrace):
	}
}

// run performs one codex exec and returns the events it produced. Cancelling
// ctx, or passing the timeout, kills the codex process group.
func (p *Provider) run(ctx context.Context, req llm.Request) ([]llm.Event, error) {
	cwd, err := p.workDir()
	if err != nil {
		return nil, fmt.Errorf("codex: working directory: %w", err)
	}
	schemaPath, cleanup, err := writeOutputSchema()
	if err != nil {
		return nil, fmt.Errorf("codex: write output schema: %w", err)
	}
	defer cleanup()

	limit := p.timeout()
	runCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	out, err := runExec(runCtx, p.binary(), execArgs(req.Model, cwd, schemaPath), buildPrompt(req))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return nil, errNotInstalled
		}
		return nil, fmt.Errorf("codex: start: %w", err)
	}
	if runCtx.Err() != nil {
		return nil, fmt.Errorf("codex did not answer within %s", limit)
	}
	return eventsFor(parseExecOutput(out.stdout), out.waitErr, out.stderr)
}

// eventsFor builds the events of a finished run. A run that did not end with
// an answer is an error, whatever the exit code was, since a turn can fail
// with exit 0 and a clean exit can still leave no answer.
func eventsFor(res execResult, waitErr error, stderr string) ([]llm.Event, error) {
	if res.turnFailed == "" && res.hasFinal && waitErr == nil {
		return replyEvents(res), nil
	}
	msg := firstNonEmpty(res.turnFailed, res.lastError, stderr)
	if msg == "" && waitErr != nil {
		msg = waitErr.Error()
	}
	if msg == "" {
		msg = "codex finished without an answer"
	}
	return nil, failure(msg)
}

// replyEvents turns a successful run into events. Reasoning comes first, as it
// did in the stream. The answer is then either the schema object, with text and
// tool calls, or plain text when the model did not follow the schema.
func replyEvents(res execResult) []llm.Event {
	var evs []llm.Event
	for _, t := range res.reasoning {
		evs = append(evs, llm.Event{Type: llm.EvReasoning, Text: t})
	}
	reply, structured := parseReply(res.finalText)
	if !structured {
		if res.finalText != "" {
			evs = append(evs, llm.Event{Type: llm.EvText, Text: res.finalText})
		}
		return append(evs, llm.Event{Type: llm.EvFinish, FinishReason: llm.FinishStop, Usage: res.usage.toLLM()})
	}
	if reply.Text != "" {
		evs = append(evs, llm.Event{Type: llm.EvText, Text: reply.Text})
	}
	// Each response gets its own id prefix, the same as the ollama adapter. The
	// agent and the TUI key tool rows by id, so ids must not repeat across calls.
	prefix := newExecCallPrefix()
	for i, c := range reply.ToolCalls {
		id := fmt.Sprintf("call_%s_%d", prefix, i)
		evs = append(evs,
			llm.Event{Type: llm.EvToolCallStart, ToolCallID: id, ToolName: c.ToolName},
			llm.Event{Type: llm.EvToolCallEnd, ToolCallID: id, ToolName: c.ToolName, Args: llm.ToolArgs(c.Input)},
		)
	}
	reason := llm.FinishStop
	if len(reply.ToolCalls) > 0 {
		reason = llm.FinishToolCalls
	}
	return append(evs, llm.Event{Type: llm.EvFinish, FinishReason: reason, Usage: res.usage.toLLM()})
}

// newExecCallPrefix returns a short random tag for one response's call ids.
func newExecCallPrefix() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func (p *Provider) binary() string {
	if p.opts.Binary != "" {
		return p.opts.Binary
	}
	return DefaultBinary
}

func (p *Provider) timeout() time.Duration {
	if p.opts.Timeout > 0 {
		return p.opts.Timeout
	}
	return defaultTimeout
}

func (p *Provider) workDir() (string, error) {
	if p.opts.Cwd != nil {
		if d := p.opts.Cwd(); d != "" {
			return d, nil
		}
	}
	return os.Getwd()
}
