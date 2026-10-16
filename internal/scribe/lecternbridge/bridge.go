// Package lecternbridge connects a Scribe session that runs inside a Lectern
// session to Lectern. It reports what the agent does through the same
// per-session hook endpoints Claude Code's hooks use
// (docs/agent-events.md section 2), and it asks Lectern to decide approvals
// (section 3) so the phone and the web can answer them. The local host and
// Lectern race: the first answer wins and the other side is cancelled.
//
// Lectern injects LECTERN_HOOK_URL (the session's hook base, ending in
// /api/hook/session/<id>) and LECTERN_HOOK_TOKEN into every session it
// launches. Without both, Attach does nothing.
package lecternbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/app"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Environment names Lectern sets for each session (internal/agentevents/env.go).
const (
	EnvHookURL   = "LECTERN_HOOK_URL"
	EnvHookToken = "LECTERN_HOOK_TOKEN"
)

// ErrNoSession means this process is not inside a Lectern session.
var ErrNoSession = errors.New("not inside a Lectern session (LECTERN_HOOK_URL and LECTERN_HOOK_TOKEN are not set)")

// Config is one Lectern session's hook endpoint.
type Config struct {
	// HookURL is the session base, e.g. http://host:9110/api/hook/session/42.
	HookURL string
	Token   string
	// Client is used for every request. Nil means http.DefaultClient.
	Client *http.Client
}

// FromEnv reads the Lectern session environment. ok is false outside a
// Lectern session.
func FromEnv() (Config, bool) {
	c := Config{
		HookURL: strings.TrimRight(strings.TrimSpace(os.Getenv(EnvHookURL)), "/"),
		Token:   strings.TrimSpace(os.Getenv(EnvHookToken)),
	}
	return c, c.HookURL != "" && c.Token != ""
}

// maxApprovalWait caps how long a remote decision is awaited. Lectern holds
// the request for its own approval window (120s by default) and then answers
// with no opinion, so this only guards against a server that never answers.
const maxApprovalWait = 15 * time.Minute

// queueSize bounds fire-and-forget posts. Ordering is kept by one worker.
const queueSize = 512

// Attach connects the app to Lectern. It subscribes to the app's events,
// installs a host that asks Lectern about approvals alongside the local host,
// and returns that host. Call it from an app.OnOpen hook, or before the first
// Run. host is the local host; nil means the app's current host.
func Attach(a *app.App, host tools.Host) (tools.Host, error) {
	cfg, ok := FromEnv()
	if !ok {
		return host, ErrNoSession
	}
	if a == nil {
		return host, errors.New("lecternbridge: nil app")
	}
	if host == nil {
		host = a.Host()
	}
	b := newBridge(cfg)
	a.Subscribe(b.handle)
	wrapped := &approvalHost{b: b, inner: host}
	a.SetHost(wrapped)
	return wrapped, nil
}

// bridge is the Lectern side of one Scribe session.
type bridge struct {
	cfg    Config
	client *http.Client
	queue  chan post

	mu       sync.Mutex
	model    string
	ctxUsed  int
	ctxSize  int
	callArgs map[string]map[string]any // CallID -> tool_input, for PostToolUse
}

// post is one fire-and-forget hook call (or a statusline snapshot).
type post struct {
	path string // event name, or "statusline"
	body map[string]any
}

func newBridge(cfg Config) *bridge {
	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}
	b := &bridge{
		cfg:      cfg,
		client:   client,
		queue:    make(chan post, queueSize),
		callArgs: map[string]map[string]any{},
	}
	go b.worker()
	return b
}

func (b *bridge) worker() {
	for p := range b.queue {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = b.send(ctx, p.path, p.body, false)
		cancel()
	}
}

// enqueue hands a post to the worker. A full queue drops the post rather
// than blocking the agent: a lost progress line is better than a stalled turn.
func (b *bridge) enqueue(path string, body map[string]any) {
	select {
	case b.queue <- post{path: path, body: body}:
	default:
	}
}

// send POSTs body to hook path (an event name or "statusline"). With
// wantAnswer it decodes the JSON reply; otherwise the reply is discarded.
func (b *bridge) send(ctx context.Context, path string, body map[string]any, wantAnswer bool) (map[string]any, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := b.cfg.HookURL + "/" + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.cfg.Token)
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("lectern hook %s: HTTP %d", path, resp.StatusCode)
	}
	if !wantAnswer {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("lectern hook %s: %w", path, err)
	}
	return out, nil
}

// handle is the app event sink. It only translates and enqueues.
func (b *bridge) handle(e agent.Event) {
	if e.AgentID != "" {
		return // subagent activity stays on the Scribe side
	}
	switch e.Kind {
	case agent.EvTurnStart:
		b.mu.Lock()
		b.model = e.Model
		b.mu.Unlock()
		b.enqueue("UserPromptSubmit", map[string]any{
			"hook_event_name": "UserPromptSubmit",
			"prompt":          clip(e.Prompt, 4000),
		})
	case agent.EvToolRun:
		input := toolInput(e.Args)
		b.mu.Lock()
		b.callArgs[e.CallID] = input
		b.mu.Unlock()
		b.enqueue("PreToolUse", map[string]any{
			"hook_event_name": "PreToolUse",
			"tool_name":       e.Tool,
			"tool_input":      input,
		})
	case agent.EvToolResult:
		b.mu.Lock()
		input := b.callArgs[e.CallID]
		delete(b.callArgs, e.CallID)
		b.mu.Unlock()
		if input == nil {
			input = map[string]any{}
		}
		resp := map[string]any{}
		if e.Result != nil {
			resp["output"] = clip(e.Result.Output, 2000)
			resp["is_error"] = e.Result.IsError
		}
		b.enqueue("PostToolUse", map[string]any{
			"hook_event_name": "PostToolUse",
			"tool_name":       e.Tool,
			"tool_input":      input,
			"tool_response":   resp,
		})
	case agent.EvContext:
		b.mu.Lock()
		b.ctxUsed, b.ctxSize = e.ContextUsed, e.ContextWindow
		b.mu.Unlock()
	case agent.EvStepFinish:
		b.statusline(e)
	case agent.EvTurnDone:
		if e.Stats != nil && e.Stats.Error != "" && !e.Stats.Aborted {
			b.enqueue("StopFailure", map[string]any{
				"hook_event_name": "StopFailure",
				"error":           clip(e.Stats.Error, 1000),
			})
			return
		}
		b.enqueue("Stop", map[string]any{
			"hook_event_name":        "Stop",
			"last_assistant_message": clip(e.Text, 8000),
			"stop_hook_active":       false,
		})
	}
}

// statusline sends a Claude-shaped statusline snapshot. Lectern turns the
// cumulative cost into the session's cost and the context numbers into its
// context gauge, the same as for Claude Code.
func (b *bridge) statusline(e agent.Event) {
	b.mu.Lock()
	model, used, size := b.model, b.ctxUsed, b.ctxSize
	b.mu.Unlock()
	pct := 0
	if size > 0 {
		pct = used * 100 / size
	}
	b.enqueue("statusline", map[string]any{
		"model":          map[string]any{"id": model, "display_name": model},
		"cost":           map[string]any{"total_cost_usd": e.CostUSD},
		"context_window": map[string]any{"total_input_tokens": used, "context_window_size": size, "used_percentage": pct},
	})
}

// answer is the remote side's reply to one approval.
type answer struct {
	decided bool
	dec     tools.Decision
}

// askRemote posts the approval as a PermissionRequest and waits for Lectern.
// It returns decided=false when Lectern has no opinion (not in ask mode, hold
// window passed, the request was cancelled, or Lectern is unreachable).
func (b *bridge) askRemote(ctx context.Context, ap tools.Approval) answer {
	name, input := approvalTool(ap)
	ctx, cancel := context.WithTimeout(ctx, maxApprovalWait)
	defer cancel()
	out, err := b.send(ctx, "PermissionRequest", map[string]any{
		"hook_event_name": "PermissionRequest",
		"tool_name":       name,
		"tool_input":      input,
	}, true)
	if err != nil {
		return answer{}
	}
	hso, _ := out["hookSpecificOutput"].(map[string]any)
	dec, _ := hso["decision"].(map[string]any)
	switch behavior, _ := dec["behavior"].(string); behavior {
	case "allow":
		return answer{decided: true, dec: tools.AllowOnce}
	case "deny":
		return answer{decided: true, dec: tools.Deny}
	}
	return answer{}
}

// approvalHost is the host Scribe uses once attached. It races the local host
// against Lectern.
type approvalHost struct {
	b     *bridge
	inner tools.Host
}

// Approve asks the local host and Lectern at the same time. The first real
// answer wins; the other request is cancelled. A remote answer also
// dismisses the local prompt.
func (h *approvalHost) Approve(ctx context.Context, ap tools.Approval) tools.Decision {
	remoteCtx, cancelRemote := context.WithCancel(ctx)
	defer cancelRemote()
	remote := make(chan answer, 1)
	go func() { remote <- h.b.askRemote(remoteCtx, ap) }()

	if !localAnswers(h.inner) {
		// A headless local host would answer at once with a deny, so wait
		// for Lectern. With no remote answer, the local rule applies.
		if a := <-remote; a.decided {
			return a.dec
		}
		return h.inner.Approve(ctx, ap)
	}

	localCtx, cancelLocal := context.WithCancel(ctx)
	defer cancelLocal()
	local := make(chan tools.Decision, 1)
	go func() { local <- h.inner.Approve(localCtx, ap) }()

	select {
	case d := <-local:
		// The cancelled request leaves Lectern's row pending until Scribe's
		// own PostToolUse for this call (sent when the tool result arrives)
		// expires it. Sending one from here could race the row's creation.
		cancelRemote()
		return d
	case a := <-remote:
		if a.decided {
			cancelLocal()
			return a.dec
		}
		// Lectern had no opinion: the local host decides alone.
		return <-local
	}
}

// Ask is the local host's question. Lectern does not answer questions yet.
func (h *approvalHost) Ask(ctx context.Context, q tools.Question) (string, error) {
	return h.inner.Ask(ctx, q)
}

// Progress is the local host's progress line.
func (h *approvalHost) Progress(callID, text string) { h.inner.Progress(callID, text) }

// localAnswers reports whether the local host can answer a prompt now.
func localAnswers(h tools.Host) bool {
	if i, ok := h.(interface{ Interactive() bool }); ok {
		return i.Interactive()
	}
	return true
}

// approvalTool maps a Scribe approval onto the tool name and input shape
// Lectern already understands (its policy rules and summaries read Bash's
// command and Write's file_path).
func approvalTool(ap tools.Approval) (string, map[string]any) {
	title := ap.Title
	switch ap.Kind {
	case "shell":
		return "Bash", map[string]any{"command": ap.Detail, "description": title}
	case "write_outside", "destructive":
		return ap.Tool, map[string]any{"file_path": ap.Detail, "description": title}
	}
	name := ap.Tool
	if name == "" {
		name = "scribe"
	}
	return name, map[string]any{"detail": ap.Detail, "description": title, "kind": ap.Kind}
}

// toolInput decodes a tool call's arguments into the object Lectern expects.
func toolInput(raw json.RawMessage) map[string]any {
	out := map[string]any{}
	if len(raw) == 0 {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]any{"raw": clip(string(raw), 4000)}
	}
	return out
}

// clip shortens s to about n bytes, cutting only on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
