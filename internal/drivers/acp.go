package drivers

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/agents"
	"github.com/JeremiahM37/lectern/v2/internal/broker"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// acpDriver speaks the Agent Client Protocol (agentclientprotocol.com): the
// same open protocol Zed, Toad, Devin Desktop and OpenHands use to drive any
// compliant coding agent (Zed's own @zed-industries/claude-code-acp and
// @zed-industries/codex-acp adapters, Gemini CLI's --experimental-acp,
// and others) without a per-CLI backend integration. This is what closes
// that gap for Lectern: any agent configured with an `acp: {command, args,
// env}` definition (internal/agents.ACPDefinition, internal/sessions.ACPSpec)
// gets a live timeline, mid-run steering and gated approvals for free — the
// same capabilities codex-appserver already gives codex — with no new
// backend code per agent.
//
// Protocol facts below were NOT taken on faith from the docs site's prose
// (agentclientprotocol.com's own summariser proved lossy when cross-checked —
// it invented methods like "session/list"/"$/cancel_request" that do not
// exist in the shipped SDK). They were read directly off
// @agentclientprotocol/sdk@0.14.1's schema/schema.json and dist/{acp,stream}.js
// — the exact dependency @zed-industries/claude-code-acp@0.16.2 ships with —
// via `npm pack`, not executed against a real model.
//
//   - Framing: newline-delimited JSON-RPC 2.0 over stdio (dist/stream.js's
//     ndJsonStream: `JSON.stringify(message) + "\n"` out, split on "\n" in) —
//     NOT Content-Length/LSP framing. This is exactly the shape the fifo/pump
//     substrate (fifo.go) and tailer (tail.go) already assume for
//     codex-appserver, so this driver reuses both unchanged.
//   - protocolVersion is an int; the SDK's PROTOCOL_VERSION constant is 1.
//   - Agent methods (client→agent, this driver sends): initialize,
//     session/new, session/prompt; notification session/cancel.
//   - Client methods (agent→client, this driver answers): fs/read_text_file,
//     fs/write_text_file, session/request_permission; notification
//     session/update. terminal/* (create/output/wait_for_exit/kill/release)
//     exists in the schema but is declined outright (clientCapabilities.
//     terminal: false in initialize) — see the package doc's "not done" note
//     for why.
//   - initialize params: {protocolVersion:int, clientCapabilities:{fs:
//     {readTextFile,writeTextFile}, terminal:bool}, clientInfo?}. result:
//     {protocolVersion, agentCapabilities, agentInfo?, authMethods}.
//   - session/new params: {cwd (absolute, required), mcpServers (required
//     array, may be empty — this driver never translates Lectern's project
//     MCP declaration for a custom ACP agent, matching the existing "no
//     automatic MCP translation" rule for any non-built-in agent)}. result:
//     {sessionId}.
//   - session/prompt params: {sessionId, prompt:[{type:"text",text}]}.
//     UNLIKE codex's turn/start, this call's RESPONSE is the turn's own
//     completion: {stopReason, usage?}. It does not return until the whole
//     turn ends, so this driver issues it from a dedicated background
//     goroutine (runPrompt) rather than blocking Start/Send on it — a real
//     agent turn can run far longer than any of this codebase's other
//     request timeouts. stopReason values seen in the schema: end_turn,
//     max_tokens, max_turn_requests, refusal, cancelled.
//   - session/update notification: {sessionId, update:{sessionUpdate:
//     "<kind>", ...}}. Kinds this driver maps to a timeline event:
//     agent_message_chunk/agent_thought_chunk ({content:{type,text}}) → text;
//     tool_call/tool_call_update (ToolCall{toolCallId,title,kind,status,
//     content,locations,rawInput}) → tool_use on first sight of a
//     toolCallId, tool_result once status is completed/failed; plan
//     ({entries:[{content,status}]}, field names not independently
//     re-verified — best-effort) → a rendered checklist. Everything else
//     (available_commands_update, current_mode_update, config_option_update,
//     session_info_update, the unstable usage_update) is accepted and
//     ignored — see docs/acp.md.
//   - session/request_permission (agent→client) params: {sessionId,
//     toolCall:ToolCallUpdate, options:[{optionId,name,kind}]}, kind enum
//     allow_once|allow_always|reject_once|reject_always. Routed through
//     internal/broker exactly like codex's execCommandApproval/
//     applyPatchApproval, respecting the attempt's Lectern permission mode:
//     "default" gates every call through the broker; every other mode
//     (acceptEdits, plan, bypassPermissions, "") auto-allows by selecting the
//     allow_always option (falling back to allow_once if the agent did not
//     offer one) — see handlePermissionRequest. The protocol requires the
//     client to answer every pending permission request with
//     {outcome:{outcome:"cancelled"}} once it has sent session/cancel; this
//     driver honours that via the run's own canceled flag.
//   - fs/read_text_file params: {sessionId,path(absolute),line?(1-based),
//     limit?} → {content}. fs/write_text_file: {sessionId,path,content} →
//     {}. Both are confined to the attempt's worktree (resolveInWorktree) —
//     a path outside it, or a relative path, is refused with a JSON-RPC
//     error rather than silently satisfied, since the client capability this
//     driver advertises is scoped to the worktree, not the whole target.
//   - session/cancel is a NOTIFICATION (no id, no response expected):
//     {sessionId}.
//
// Not implemented (see docs/acp.md "limits"): terminal/* (a real ACP agent
// falls back to asking the client for permission-gated shell execution
// through its own tool-call flow instead, same as it would for a client with
// no terminal capability at all); session/load / session resume across a
// fresh process; session/set_mode / session/set_model.
type acpDriver struct{}

const (
	acpProtocolVersion = 1
	// acpRequestTimeout bounds the two quick request/response calls this
	// driver makes (initialize uses spec.handshakeTimeout() instead —
	// separately configurable since a slow-starting `npx` download is a
	// realistic first-run cost session/new never has).
	acpRequestTimeout = 20 * time.Second
	// acpPromptTimeout bounds session/prompt only as an absolute safety net —
	// the real bound is the run's own lifetime (Cancel + the process actually
	// exiting unblocks it via failAllPending; see watchExit), not this
	// number, so it is set deliberately high rather than tuned like an
	// ordinary RPC timeout.
	acpPromptTimeout = 24 * time.Hour
)

func (acpDriver) Start(ctx context.Context, ex executor.Executor, spec Spec) (Handle, error) {
	if strings.TrimSpace(spec.Bin) == "" {
		return nil, fmt.Errorf("acp driver: no command configured (the agent's acp.command is empty)")
	}
	rt := agents.RuntimeDir(spec.Worktree)
	if err := ex.WriteFile(ctx, rt+"/pump.py", []byte(pumpScript)); err != nil {
		return nil, err
	}
	if r, err := ex.Run(ctx, ensureFifoCommand(rt), executor.RunOpts{Timeout: 20}); err != nil {
		return nil, err
	} else if !r.OK() {
		return nil, executor.Errf("could not create the steering fifo: %s", strings.TrimSpace(r.Stderr))
	}
	envPrefix, err := agents.EnvPrefix(spec.Env, spec.Sandbox)
	if err != nil {
		return nil, err
	}
	parts := []string{shellq.Quote(spec.Bin)}
	for _, a := range spec.ACPArgs {
		parts = append(parts, shellq.Quote(a))
	}
	agentCmd := envPrefix + strings.Join(parts, " ")
	cmd := streamLaunchCommand(spec.TmuxSession, rt, spec.Worktree, agentCmd)
	r, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 60})
	if err != nil {
		return nil, err
	}
	if !r.OK() {
		return nil, executor.Errf("tmux launch failed: %s", strings.TrimSpace(r.Stderr))
	}

	run := &acpRun{
		ex: ex, rt: rt, tmux: spec.TmuxSession, worktree: filepath.Clean(spec.Worktree),
		tail:            tailer{ex: ex, path: rt + "/events.jsonl"},
		interval:        spec.pollInterval(),
		pending:         map[int64]chan rpcResult{},
		broker:          spec.Broker,
		attemptID:       spec.AttemptID,
		approvalTimeout: spec.approvalTimeout(),
		permissionMode:  spec.PermissionMode,
		baseCtx:         ctx,
		announced:       map[string]bool{},
		events:          make(chan agents.Event, 64),
		done:            make(chan struct{}),
	}
	loopCtx, cancelLoop := context.WithCancel(context.Background())
	run.cancelLoop = cancelLoop
	go run.pollLoop(loopCtx)

	// A failed handshake usually means the process is already gone (wrong
	// command, npx could not resolve the package, no such --experimental-acp
	// flag) — closeCommand's fifo append would then have no reader left to
	// unblock its own open(), so it would block for its full timeout for
	// nothing. Killing the tmux session directly works regardless of whether
	// anything is still alive to read the fifo. There is deliberately no
	// exec-mode fallback here (unlike codex-appserver): an ACP-configured
	// agent has no other invocation shape to fall back to.
	kill := func() {
		cancelLoop()
		ex.Run(ctx, fmt.Sprintf("tmux kill-session -t =%s 2>/dev/null || true", spec.TmuxSession),
			executor.RunOpts{Timeout: 20})
	}

	if _, err := run.request(ctx, "initialize", map[string]any{
		"protocolVersion": acpProtocolVersion,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": true, "writeTextFile": true},
			"terminal": false,
		},
		"clientInfo": map[string]any{"name": "lectern", "version": "1"},
	}, spec.handshakeTimeout()); err != nil {
		kill()
		return nil, fmt.Errorf("acp initialize handshake failed: %w", err)
	}

	sessRaw, err := run.request(ctx, "session/new", map[string]any{
		"cwd":        spec.Worktree,
		"mcpServers": []any{},
	}, acpRequestTimeout)
	if err != nil {
		kill()
		return nil, fmt.Errorf("acp session/new failed: %w", err)
	}
	var sessResp struct {
		SessionID string `json:"sessionId"`
	}
	json.Unmarshal(sessRaw, &sessResp)
	if sessResp.SessionID == "" {
		kill()
		return nil, fmt.Errorf("acp session/new returned no sessionId")
	}
	run.mu.Lock()
	run.sessionID = sessResp.SessionID
	run.active = true
	run.mu.Unlock()

	go run.runPrompt(ctx, spec.Prompt)
	go run.watchExit(ctx)
	return run, nil
}

// acpRun is the live handle for one ACP session. Its process, like
// claude-steer's and codex-appserver's, outlives a single turn: Send queues
// or starts another session/prompt on the SAME session, and only Cancel (or
// the process dying on its own) ends the run.
type acpRun struct {
	ex              executor.Executor
	rt              string
	tmux            string
	worktree        string
	tail            tailer
	interval        time.Duration
	broker          *broker.Broker
	attemptID       int64
	approvalTimeout time.Duration
	permissionMode  string
	// baseCtx is Start's own ctx, reused for the long-lived background
	// goroutines (runPrompt, watchExit) — matching codexAppServerDriver's
	// watchExit, which does the same. It is NOT a per-call timeout; a
	// session/prompt call is unblocked by a real response, Cancel ending the
	// process (failAllPending), or this context ending, whichever comes
	// first.
	baseCtx    context.Context
	cancelLoop context.CancelFunc

	nextID  int64
	pending map[int64]chan rpcResult

	events chan agents.Event
	done   chan struct{}

	mu        sync.Mutex
	sessionID string
	active    bool
	queue     []string
	announced map[string]bool
	closed    bool
	canceled  bool
	result    Result
}

func (r *acpRun) Events() <-chan agents.Event { return r.events }

// Send steers the run: if a turn is currently in flight, the message is
// queued and becomes the next session/prompt once the current one's
// {stopReason,...} response arrives (runPrompt's own loop), exactly the "a
// follow-up session/prompt after the current turn ends, or queue" behaviour
// the protocol's request/response session/prompt shape requires — unlike
// codex's turn/steer or claude's streaming-input fifo, ACP has no way to
// fold a message into an ALREADY-in-flight prompt call.
func (r *acpRun) Send(ctx context.Context, text string) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return fmt.Errorf("run has already ended; dispatch a follow-up attempt instead")
	}
	if r.active {
		r.queue = append(r.queue, text)
		r.mu.Unlock()
		return nil
	}
	r.active = true
	r.mu.Unlock()
	go r.runPrompt(r.baseCtx, text)
	return nil
}

// Cancel asks the current turn to stop (session/cancel — a notification,
// not a request) and then closes the run's stdin so the process exits on its
// own, exactly like claude-steer/codex-appserver's Cancel. Any
// session/request_permission still pending when canceled=true is answered
// {outcome:{outcome:"cancelled"}} per the protocol's own requirement (see
// handlePermissionRequest); any session/prompt still waiting for a response
// is unblocked once the process actually exits (watchExit → failAllPending).
func (r *acpRun) Cancel(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.canceled = true
	sessionID := r.sessionID
	r.mu.Unlock()
	if sessionID != "" {
		raw, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": sessionID},
		})
		r.ex.Run(ctx, appendCommand(r.rt, string(raw)), executor.RunOpts{Timeout: 20})
	}
	_, err := r.ex.Run(ctx, closeCommand(r.rt), executor.RunOpts{Timeout: 20})
	return err
}

func (r *acpRun) Wait(ctx context.Context) (Result, error) {
	select {
	case <-r.done:
		return r.result, nil
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

func (r *acpRun) currentSessionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessionID
}

// request sends one JSON-RPC request and blocks for its response, exactly
// like appServerRun.request (codex_appserver.go) — duplicated rather than
// shared because the two run types have no common base and the logic is
// small; pollLoop (already running) is what actually delivers the response.
func (r *acpRun) request(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	id := atomic.AddInt64(&r.nextID, 1)
	ch := make(chan rpcResult, 1)
	r.mu.Lock()
	r.pending[id] = ch
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.pending, id)
		r.mu.Unlock()
	}()
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	res, err := r.ex.Run(ctx, appendCommand(r.rt, string(raw)), executor.RunOpts{Timeout: 20})
	if err != nil {
		return nil, err
	}
	if !res.OK() {
		return nil, fmt.Errorf("could not send %s: %s", method, strings.TrimSpace(res.Stderr))
	}
	select {
	case rr := <-ch:
		if rr.errMsg != "" {
			return nil, fmt.Errorf("%s: %s", method, rr.errMsg)
		}
		return rr.result, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("%s: timed out waiting for a response", method)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// runPrompt owns one session/prompt call and, since ACP has no way to fold a
// message into an in-flight one, everything Send queued while it was
// running: on completion it keeps looping over the queue instead of
// returning, so a steered run never needs a second driver-level goroutine
// racing this one.
func (r *acpRun) runPrompt(ctx context.Context, text string) {
	for {
		raw, err := r.request(ctx, "session/prompt", map[string]any{
			"sessionId": r.currentSessionID(),
			"prompt":    []map[string]any{{"type": "text", "text": text}},
		}, acpPromptTimeout)
		if err != nil {
			r.emit(agents.Event{Type: "result", Payload: map[string]any{
				"subtype": "error", "result": err.Error(), "session_id": r.currentSessionID(),
			}})
		} else {
			var resp struct {
				StopReason string         `json:"stopReason"`
				Usage      map[string]any `json:"usage"`
			}
			json.Unmarshal(raw, &resp)
			r.mu.Lock()
			weCanceled := r.canceled
			r.mu.Unlock()
			payload := map[string]any{
				"subtype": acpResultSubtype(resp.StopReason, weCanceled), "result": "",
				"session_id": r.currentSessionID(), "stop_reason": resp.StopReason,
			}
			if resp.Usage != nil {
				out := map[string]any{}
				if v, ok := numField(resp.Usage, "input_tokens"); ok {
					out["input_tokens"] = v
				}
				if v, ok := numField(resp.Usage, "output_tokens"); ok {
					out["output_tokens"] = v
				}
				if len(out) > 0 {
					payload["usage"] = out
				}
			}
			r.emit(agents.Event{Type: "result", Payload: payload})
		}
		r.mu.Lock()
		if len(r.queue) > 0 {
			text = r.queue[0]
			r.queue = r.queue[1:]
			r.mu.Unlock()
			continue
		}
		r.active = false
		r.mu.Unlock()
		return
	}
}

func acpResultSubtype(stopReason string, weCanceled bool) string {
	switch {
	case stopReason == "" || stopReason == "end_turn":
		return "success"
	case stopReason == "cancelled" && weCanceled:
		return "success"
	default: // max_tokens, max_turn_requests, refusal, or an unrequested cancellation
		return "error"
	}
}

// pollLoop tails events.jsonl for JSON-RPC traffic, exactly like
// appServerRun.pollLoop.
func (r *acpRun) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		lines, err := r.tail.lines(ctx)
		if err != nil || lines == "" {
			continue
		}
		for _, line := range strings.Split(strings.TrimRight(lines, "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				r.handleLine(line)
			}
		}
	}
}

func (r *acpRun) handleLine(line string) {
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		r.emit(agents.Event{Type: "raw", Payload: map[string]any{"line": clipStr(line, 2000)}})
		return
	}
	hasID := len(msg.ID) > 0 && string(msg.ID) != "null"
	switch {
	case msg.Method != "" && hasID:
		r.handleAgentRequest(msg.ID, msg.Method, msg.Params)
	case msg.Method != "":
		r.handleNotification(msg.Method, msg.Params)
	case hasID:
		var id int64
		if json.Unmarshal(msg.ID, &id) != nil {
			return
		}
		r.mu.Lock()
		ch, ok := r.pending[id]
		r.mu.Unlock()
		if !ok {
			return
		}
		if msg.Error != nil {
			ch <- rpcResult{errMsg: msg.Error.Message}
		} else {
			ch <- rpcResult{result: msg.Result}
		}
	}
}

// handleAgentRequest answers a JSON-RPC request the AGENT sent us — the
// "client methods" half of ACP (fs/*, session/request_permission). Every
// other client method this driver did not advertise (terminal/*) gets a
// clean protocol error instead of silently hanging a compliant agent that
// nonetheless probes for it.
func (r *acpRun) handleAgentRequest(rawID json.RawMessage, method string, params json.RawMessage) {
	switch method {
	case "session/request_permission":
		go r.handlePermissionRequest(rawID, params)
	case "fs/read_text_file":
		go r.handleFsRead(rawID, params)
	case "fs/write_text_file":
		go r.handleFsWrite(rawID, params)
	default:
		r.respondError(rawID, -32601, "method not found: "+method)
	}
}

func (r *acpRun) respondResult(id json.RawMessage, result any) {
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
	r.ex.Run(context.Background(), appendCommand(r.rt, string(raw)), executor.RunOpts{Timeout: 20})
}

func (r *acpRun) respondError(id json.RawMessage, code int, msg string) {
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id),
		"error": map[string]any{"code": code, "message": msg}})
	r.ex.Run(context.Background(), appendCommand(r.rt, string(raw)), executor.RunOpts{Timeout: 20})
}

// resolveInWorktree confines an fs/read_text_file or fs/write_text_file call
// to the attempt's worktree. The client capability this driver advertises in
// initialize is meant to be scoped to the worktree an agent was launched
// against, not the whole target — executor.ReadFile/WriteFile have no such
// notion themselves, so a path outside it (an absolute path elsewhere, or a
// "../.." escape) must be refused here, before either ever sees it.
func (r *acpRun) resolveInWorktree(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path %q must be absolute", path)
	}
	clean := filepath.Clean(path)
	if clean == r.worktree {
		return clean, nil
	}
	rel, err := filepath.Rel(r.worktree, clean)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the attempt's worktree", path)
	}
	return clean, nil
}

func (r *acpRun) handleFsRead(rawID json.RawMessage, params json.RawMessage) {
	var p struct {
		Path  string `json:"path"`
		Line  *int   `json:"line"`
		Limit *int   `json:"limit"`
	}
	json.Unmarshal(params, &p)
	clean, err := r.resolveInWorktree(p.Path)
	if err != nil {
		r.respondError(rawID, -32602, err.Error())
		return
	}
	data, err := r.ex.ReadFile(context.Background(), clean, 0)
	if err != nil {
		r.respondError(rawID, -32603, err.Error())
		return
	}
	content := string(data)
	if p.Line != nil || p.Limit != nil {
		content = sliceLines(content, p.Line, p.Limit)
	}
	r.respondResult(rawID, map[string]any{"content": content})
}

func (r *acpRun) handleFsWrite(rawID json.RawMessage, params json.RawMessage) {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	json.Unmarshal(params, &p)
	clean, err := r.resolveInWorktree(p.Path)
	if err != nil {
		r.respondError(rawID, -32602, err.Error())
		return
	}
	if err := r.ex.WriteFile(context.Background(), clean, []byte(p.Content)); err != nil {
		r.respondError(rawID, -32603, err.Error())
		return
	}
	r.respondResult(rawID, map[string]any{})
}

// sliceLines applies fs/read_text_file's optional 1-based `line`/`limit`
// window over an already-fully-read file (executor.ReadFile has no line
// concept of its own).
func sliceLines(content string, line, limit *int) string {
	lines := strings.Split(content, "\n")
	start := 0
	if line != nil && *line > 1 {
		start = *line - 1
	}
	if start >= len(lines) {
		return ""
	}
	end := len(lines)
	if limit != nil && *limit >= 0 && start+*limit < end {
		end = start + *limit
	}
	return strings.Join(lines[start:end], "\n")
}

// acpPermOption is one session/request_permission option.
type acpPermOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// pickAllowAlways is the auto-allow answer for every Lectern permission mode
// except "default" — GAP requirement: "bypass mode auto-allows with the
// allow always option".
func pickAllowAlways(options []acpPermOption) string {
	for _, o := range options {
		if o.Kind == "allow_always" {
			return o.OptionID
		}
	}
	for _, o := range options {
		if o.Kind == "allow_once" {
			return o.OptionID
		}
	}
	return ""
}

// pickByOutcome maps a broker decision onto whichever matching option the
// agent actually offered — an agent is not required to offer every kind.
func pickByOutcome(options []acpPermOption, approved bool) string {
	if approved {
		for _, o := range options {
			if o.Kind == "allow_once" {
				return o.OptionID
			}
		}
		for _, o := range options {
			if o.Kind == "allow_always" {
				return o.OptionID
			}
		}
		return ""
	}
	for _, o := range options {
		if o.Kind == "reject_once" {
			return o.OptionID
		}
	}
	for _, o := range options {
		if o.Kind == "reject_always" {
			return o.OptionID
		}
	}
	return ""
}

// handlePermissionRequest routes session/request_permission through the
// same operator decision path claude's PreToolUse hook and codex's
// execCommandApproval/applyPatchApproval use — see this file's package doc
// comment for the exact permission-mode mapping.
func (r *acpRun) handlePermissionRequest(rawID json.RawMessage, params json.RawMessage) {
	var p struct {
		ToolCall json.RawMessage `json:"toolCall"`
		Options  []acpPermOption `json:"options"`
	}
	json.Unmarshal(params, &p)
	r.mu.Lock()
	canceled := r.canceled
	gated := r.permissionMode == "default"
	r.mu.Unlock()
	if canceled {
		r.respondResult(rawID, map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
		return
	}
	var chosen string
	if !gated || r.broker == nil {
		chosen = pickAllowAlways(p.Options)
	} else {
		toolName, toolInput := acpToolShape(p.ToolCall)
		approved := false
		id, err := r.broker.Create(r.attemptID, toolName, toolInput, false)
		if err == nil {
			row := r.broker.Wait(context.Background(), id, r.approvalTimeout)
			approved = row != nil && row.Status == "approved"
		}
		chosen = pickByOutcome(p.Options, approved)
	}
	if chosen == "" {
		r.respondResult(rawID, map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
		return
	}
	r.respondResult(rawID, map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": chosen}})
}

// acpToolUpdate is the common shape of ACP's ToolCall/ToolCallUpdate —
// shared by session/update's tool_call(_update) and
// session/request_permission's toolCall summary.
type acpToolUpdate struct {
	ToolCallID string `json:"toolCallId"`
	Title      string `json:"title,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Status     string `json:"status,omitempty"`
	Locations  []struct {
		Path string `json:"path"`
	} `json:"locations,omitempty"`
	Content []map[string]any `json:"content,omitempty"`
}

var acpToolKindNames = map[string]string{
	"execute": "Bash", "edit": "Edit", "delete": "Edit", "move": "Edit",
	"read": "Read", "search": "Read", "fetch": "Fetch",
}

// acpToolName renders an ACP ToolKind as the same tool-name vocabulary the
// approvals UI and broker summaries already use for claude/codex
// (Bash/Edit/Read/...), so it needs no ACP-specific case.
func acpToolName(kind string) string {
	if name, ok := acpToolKindNames[kind]; ok {
		return name
	}
	return "Tool"
}

func acpToolInput(tc acpToolUpdate) map[string]any {
	input := map[string]any{}
	if tc.Title != "" {
		input["title"] = tc.Title
	}
	if len(tc.Locations) > 0 {
		paths := make([]string, 0, len(tc.Locations))
		for _, l := range tc.Locations {
			if l.Path != "" {
				paths = append(paths, l.Path)
			}
		}
		if len(paths) > 0 {
			input["file_path"] = strings.Join(paths, ", ")
		}
	}
	return input
}

func acpToolOutput(tc acpToolUpdate) string {
	parts := make([]string, 0, len(tc.Content))
	for _, c := range tc.Content {
		switch c["type"] {
		case "content":
			if inner, ok := c["content"].(map[string]any); ok {
				if text, ok := inner["text"].(string); ok && text != "" {
					parts = append(parts, text)
				}
			}
		case "diff":
			if path, ok := c["path"].(string); ok {
				parts = append(parts, "diff: "+path)
			}
		case "terminal":
			parts = append(parts, "[terminal output]")
		}
	}
	if len(parts) == 0 {
		return tc.Title
	}
	return strings.Join(parts, "\n")
}

func acpToolShape(raw json.RawMessage) (string, map[string]any) {
	var tc acpToolUpdate
	json.Unmarshal(raw, &tc)
	return acpToolName(tc.Kind), acpToolInput(tc)
}

// handleNotification dispatches session/update — the only notification this
// driver's agent side sends; session/cancel is one THIS driver sends, never
// receives.
func (r *acpRun) handleNotification(method string, params json.RawMessage) {
	if method == "session/update" {
		r.handleUpdate(params)
	}
}

func (r *acpRun) handleUpdate(params json.RawMessage) {
	var p struct {
		Update json.RawMessage `json:"update"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	var kind struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if json.Unmarshal(p.Update, &kind) != nil {
		return
	}
	switch kind.SessionUpdate {
	case "agent_message_chunk", "agent_thought_chunk":
		var c struct {
			Content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		json.Unmarshal(p.Update, &c)
		if strings.TrimSpace(c.Content.Text) != "" {
			r.emit(agents.Event{Type: "text", Payload: map[string]any{"text": c.Content.Text}})
		}
	case "tool_call", "tool_call_update":
		r.handleToolEvent(p.Update)
	case "plan":
		r.handlePlan(p.Update)
	}
	// available_commands_update, current_mode_update, config_option_update,
	// session_info_update, usage_update: not surfaced in the timeline yet —
	// see docs/acp.md's limits section.
}

// handleToolEvent unifies tool_call (the first announcement) and
// tool_call_update (every later partial/terminal update) — both carry the
// same ToolCall shape and are told apart only by which fields are actually
// present, so tracking "have we announced this toolCallId yet" is simpler
// and more robust than trusting the notification kind alone.
func (r *acpRun) handleToolEvent(raw json.RawMessage) {
	var tc acpToolUpdate
	if json.Unmarshal(raw, &tc) != nil || tc.ToolCallID == "" {
		return
	}
	r.mu.Lock()
	seen := r.announced[tc.ToolCallID]
	if !seen {
		r.announced[tc.ToolCallID] = true
	}
	r.mu.Unlock()
	if !seen {
		r.emit(agents.Event{Type: "tool_use", Payload: map[string]any{
			"id": tc.ToolCallID, "name": acpToolName(tc.Kind), "input": acpToolInput(tc),
		}})
	}
	if tc.Status == "completed" || tc.Status == "failed" {
		r.emit(agents.Event{Type: "tool_result", Payload: map[string]any{
			"tool_use_id": tc.ToolCallID, "content": clipStr(acpToolOutput(tc), 2000), "is_error": tc.Status == "failed",
		}})
	}
}

// handlePlan renders a plan update as a checklist. The exact field names for
// a plan entry were not independently re-verified against the SDK schema
// (the research pass ran out of budget before pinning them down) — this is
// deliberately lenient (a shape mismatch just means no plan text is emitted,
// never an error) rather than asserted as protocol fact elsewhere in this
// file. See docs/acp.md.
func (r *acpRun) handlePlan(raw json.RawMessage) {
	var p struct {
		Entries []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"entries"`
	}
	if json.Unmarshal(raw, &p) != nil || len(p.Entries) == 0 {
		return
	}
	lines := make([]string, 0, len(p.Entries))
	for _, e := range p.Entries {
		mark := " "
		if e.Status == "completed" {
			mark = "x"
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s", mark, e.Content))
	}
	r.emit(agents.Event{Type: "text", Payload: map[string]any{"text": "Plan:\n" + strings.Join(lines, "\n")}})
}

func (r *acpRun) emit(ev agents.Event) {
	select {
	case r.events <- ev:
	default:
		// a driver run that nobody is draining must never deadlock the poll
		// loop; Run(...) always drains, and the scheduler's consumer goroutine
		// does too, so this only bites a caller that ignored Events entirely.
	}
}

// failAllPending unblocks every in-flight request() call — critically
// including a session/prompt that may have been waiting since Start — when
// the run is ending and nothing will ever answer them. Without this, Cancel
// on a run with an active turn would leave runPrompt's goroutine blocked
// until acpPromptTimeout (24h) even though the process it was talking to is
// already gone.
func (r *acpRun) failAllPending(msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, ch := range r.pending {
		select {
		case ch <- rpcResult{errMsg: msg}:
		default:
		}
		delete(r.pending, id)
	}
}

// watchExit mirrors appServerRun.watchExit: the process only exits once its
// stdin (the pump) is closed by Cancel, so reaching "done" is always an
// explicit close, not a single turn finishing on its own.
func (r *acpRun) watchExit(ctx context.Context) {
	defer close(r.done)
	defer close(r.events)
	exitPath := r.rt + "/exit_code"
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.cancelLoop()
			r.failAllPending("run context ended")
			r.result = Result{ExitCode: -1, Err: ctx.Err().Error()}
			return
		case <-ticker.C:
		}
		exitRaw, err := r.ex.ReadFile(ctx, exitPath, 0)
		if err != nil {
			continue
		}
		trimmed := strings.TrimSpace(string(exitRaw))
		if trimmed == "" {
			continue
		}
		// let the poll loop deliver whatever is left, then stop it
		time.Sleep(r.interval)
		r.cancelLoop()
		r.failAllPending("the acp agent process exited")
		rc := -1
		fmt.Sscanf(trimmed, "%d", &rc)
		r.mu.Lock()
		r.closed = true
		canceled := r.canceled
		sessionID := r.sessionID
		r.mu.Unlock()
		errMsg := ""
		if canceled {
			errMsg = "cancelled"
		}
		r.result = Result{ExitCode: rc, SessionID: sessionID, Err: errMsg}
		return
	}
}
