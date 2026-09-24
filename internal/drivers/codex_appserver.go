package drivers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/agents"
	"github.com/JeremiahM37/lectern/v2/internal/broker"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// codexAppServerDriver speaks `codex app-server`'s JSON-RPC-over-stdio
// protocol instead of `codex exec --json`. It gets codex two things exec
// cannot: mid-run steering (turn/steer, the same idea as claude's streaming
// input) and gated approvals (codex has no PreToolUse-equivalent hook, so
// "codex tasks can only run in bypass mode" was true until this driver: the
// app-server protocol routes command/patch approvals to us as ordinary
// JSON-RPC requests, which this driver answers through internal/broker
// exactly like claude's hook.py answers a PreToolUse call).
//
// Protocol facts below were NOT taken on faith from docs prose — they were
// read directly off the running binary:
//
//	codex app-server generate-json-schema --out DIR
//
// (codex-cli 0.155.1, stable/non-experimental subset — the --experimental
// flag pulls in a much larger surface this driver does not use: realtime
// voice, Bedrock, Windows sandbox setup, etc.). That schema is the source for
// every method name, parameter and notification shape used here:
// InitializeParams/-Response, ThreadStartParams/-Response, TurnStartParams/
// -Response, TurnSteerParams, TurnInterruptParams, ExecCommandApprovalParams/
// -Response, ApplyPatchApprovalParams/-Response, ItemStartedNotification,
// ItemCompletedNotification, TurnCompletedNotification,
// ThreadTokenUsageUpdatedNotification, ThreadItem's commandExecution/
// fileChange/agentMessage variants, and ReviewDecision. JSON-RPC framing
// (JSONRPCRequest/-Response/-Notification, one object per line) matches the
// same convention `codex exec --json` already uses elsewhere in this
// codebase (internal/agents/parse.go's normalizeCodex).
//
// Deliberately out of scope: the newer `item/*/requestApproval` methods (this
// driver uses the older, simpler execCommandApproval/applyPatchApproval
// pair), and everything else the schema exposes (threads/projects/plugins/
// realtime/...) that an unattended coding task never touches.
type codexAppServerDriver struct{}

const codexRequestTimeout = 20 * time.Second

func (codexAppServerDriver) Start(ctx context.Context, ex executor.Executor, spec Spec) (Handle, error) {
	rt := agents.RuntimeDir(spec.Worktree)
	if err := ex.WriteFile(ctx, rt+"/pump.py", []byte(pumpScript)); err != nil {
		return nil, err
	}
	if r, err := ex.Run(ctx, ensureFifoCommand(rt), executor.RunOpts{Timeout: 20}); err != nil {
		return nil, err
	} else if !r.OK() {
		return nil, executor.Errf("could not create the steering fifo: %s", strings.TrimSpace(r.Stderr))
	}
	bin := spec.Bin
	if bin == "" {
		bin = "codex"
	}
	envPrefix, err := agents.EnvPrefix(spec.Env, spec.Sandbox)
	if err != nil {
		return nil, err
	}
	agentCmd := envPrefix + shellq.Quote(bin) + " app-server"
	cmd := streamLaunchCommand(spec.TmuxSession, rt, spec.Worktree, agentCmd)
	r, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 60})
	if err != nil {
		return nil, err
	}
	if !r.OK() {
		return nil, executor.Errf("tmux launch failed: %s", strings.TrimSpace(r.Stderr))
	}

	run := &appServerRun{
		ex: ex, rt: rt, tmux: spec.TmuxSession,
		tail:            tailer{ex: ex, path: rt + "/events.jsonl"},
		interval:        spec.pollInterval(),
		pending:         map[int64]chan rpcResult{},
		broker:          spec.Broker,
		attemptID:       spec.AttemptID,
		approvalTimeout: spec.approvalTimeout(),
		events:          make(chan agents.Event, 64),
		done:            make(chan struct{}),
	}
	loopCtx, cancelLoop := context.WithCancel(context.Background())
	run.cancelLoop = cancelLoop
	go run.pollLoop(loopCtx)

	if _, err := run.request(ctx, "initialize", map[string]any{
		"clientInfo": map[string]any{"name": "lectern", "version": "1"},
	}, spec.handshakeTimeout()); err != nil {
		cancelLoop()
		// A failed handshake usually means the app-server process is already
		// gone (wrong binary, crashed, no such subcommand) — closeCommand's
		// fifo append would then have no reader left to unblock its own
		// open(), and would block for its full RunOpts timeout for nothing.
		// Killing the tmux session directly works regardless of whether
		// anything is still alive to read the fifo.
		ex.Run(ctx, fmt.Sprintf("tmux kill-session -t =%s 2>/dev/null || true", spec.TmuxSession),
			executor.RunOpts{Timeout: 20})
		// Keep codex exec --json as the fallback when app-server is unavailable
		// or fails the handshake, exactly as claude/gemini's tmux-launched
		// exec paths behaved before this driver existed.
		fallback, ferr := execDriver{Agent: "codex"}.Start(ctx, ex, withFallbackPermissions(spec))
		if ferr != nil {
			return nil, fmt.Errorf("codex app-server handshake failed (%w) and exec fallback also failed: %v", err, ferr)
		}
		return withNotice(fallback, fmt.Sprintf(
			"codex app-server unavailable (%s); fell back to codex exec --json in bypass mode "+
				"— gated approvals are not available on this run", err)), nil
	}

	threadRaw, err := run.request(ctx, "thread/start", map[string]any{
		"cwd":            spec.Worktree,
		"approvalPolicy": "on-request",
		"sandbox":        "workspace-write",
	}, codexRequestTimeout)
	if err != nil {
		cancelLoop()
		return nil, fmt.Errorf("codex app-server thread/start: %w", err)
	}
	var threadResp struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	json.Unmarshal(threadRaw, &threadResp)
	run.mu.Lock()
	run.threadID = threadResp.Thread.ID
	run.mu.Unlock()

	turnRaw, err := run.request(ctx, "turn/start", map[string]any{
		"threadId": run.threadID,
		"input":    []map[string]any{{"type": "text", "text": spec.Prompt}},
	}, codexRequestTimeout)
	if err != nil {
		cancelLoop()
		return nil, fmt.Errorf("codex app-server turn/start: %w", err)
	}
	var turnResp struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	json.Unmarshal(turnRaw, &turnResp)
	run.mu.Lock()
	run.turnID = turnResp.Turn.ID
	run.active = true
	run.mu.Unlock()

	go run.watchExit(ctx)
	return run, nil
}

// withFallbackPermissions downgrades a "default" (gated) request to
// bypassPermissions for the exec fallback: codex exec has no approval
// mechanism at all, so running it "gated" would just hang forever waiting for
// a PreToolUse-style prompt that never comes.
func withFallbackPermissions(spec Spec) Spec {
	spec.PermissionMode = "bypassPermissions"
	return spec
}

type rpcResult struct {
	result json.RawMessage
	errMsg string
}

// appServerRun is the live handle for a codex app-server session.
type appServerRun struct {
	ex              executor.Executor
	rt              string
	tmux            string
	tail            tailer
	interval        time.Duration
	broker          *broker.Broker
	attemptID       int64
	approvalTimeout time.Duration
	cancelLoop      context.CancelFunc

	nextID  int64
	pending map[int64]chan rpcResult

	events chan agents.Event
	done   chan struct{}

	mu        sync.Mutex
	threadID  string
	turnID    string
	active    bool
	closed    bool
	canceled  bool
	lastUsage map[string]any
	result    Result
}

func (r *appServerRun) Events() <-chan agents.Event { return r.events }

func (r *appServerRun) Send(ctx context.Context, text string) error {
	r.mu.Lock()
	closed := r.closed
	threadID, turnID, active := r.threadID, r.turnID, r.active
	r.mu.Unlock()
	if closed {
		return fmt.Errorf("run has already ended; dispatch a follow-up attempt instead")
	}
	input := []map[string]any{{"type": "text", "text": text}}
	if active {
		_, err := r.request(ctx, "turn/steer", map[string]any{
			"threadId": threadID, "expectedTurnId": turnID, "input": input,
		}, codexRequestTimeout)
		return err
	}
	raw, err := r.request(ctx, "turn/start", map[string]any{
		"threadId": threadID, "input": input,
	}, codexRequestTimeout)
	if err != nil {
		return err
	}
	var resp struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	json.Unmarshal(raw, &resp)
	r.mu.Lock()
	r.turnID = resp.Turn.ID
	r.active = true
	r.mu.Unlock()
	return nil
}

func (r *appServerRun) Cancel(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.canceled = true
	threadID, turnID, active := r.threadID, r.turnID, r.active
	r.mu.Unlock()
	if active && threadID != "" && turnID != "" {
		_, _ = r.request(ctx, "turn/interrupt", map[string]any{
			"threadId": threadID, "turnId": turnID}, 5*time.Second)
	}
	_, err := r.ex.Run(ctx, closeCommand(r.rt), executor.RunOpts{Timeout: 20})
	return err
}

func (r *appServerRun) Wait(ctx context.Context) (Result, error) {
	select {
	case <-r.done:
		return r.result, nil
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

// request sends one JSON-RPC request and blocks for its response. pollLoop
// (already running) is what actually delivers the response — this only
// registers the wait and writes the request line.
func (r *appServerRun) request(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
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

// pollLoop tails events.jsonl for JSON-RPC traffic and dispatches every line:
// our own requests' responses, the server's approval requests, and
// notifications (item/turn/usage updates).
func (r *appServerRun) pollLoop(ctx context.Context) {
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

func (r *appServerRun) handleLine(line string) {
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
		r.handleApprovalRequest(msg.ID, msg.Method, msg.Params)
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

// handleApprovalRequest routes a codex command/patch approval through the
// same operator decision path claude's PreToolUse hook uses: create a
// pending broker.Approval, wait for a human (or expiry) to decide, translate
// the decision into a codex ReviewDecision, and answer the request. Runs in
// its own goroutine so a slow decision does not block the poll loop from
// delivering other events meanwhile.
func (r *appServerRun) handleApprovalRequest(rawID json.RawMessage, method string, params json.RawMessage) {
	go func() {
		toolName, toolInput := approvalToolShape(method, params)
		decision := "denied"
		note := "no approval broker configured for this run"
		if r.broker != nil {
			id, err := r.broker.Create(r.attemptID, toolName, toolInput, false)
			if err == nil {
				row := r.broker.Wait(context.Background(), id, r.approvalTimeout)
				if row != nil && row.Status == "approved" {
					decision = "approved"
				}
				if row != nil {
					note = row.Note
				} else {
					note = "approval could not be recorded"
				}
			} else {
				note = err.Error()
			}
		}
		result := map[string]any{"decision": "approved"}
		if decision != "approved" {
			result = map[string]any{"decision": map[string]any{"denied": map[string]any{"rejection": note}}}
		}
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(rawID), "result": result})
		r.ex.Run(context.Background(), appendCommand(r.rt, string(raw)), executor.RunOpts{Timeout: 20})
	}()
}

// approvalToolShape turns an execCommandApproval/applyPatchApproval request
// into the same (toolName, toolInput) shape the broker already renders for a
// claude PreToolUse call, so the approvals UI needs no codex-specific case.
func approvalToolShape(method string, params json.RawMessage) (string, map[string]any) {
	switch method {
	case "execCommandApproval":
		var p struct {
			Command []string `json:"command"`
			Cwd     string   `json:"cwd"`
			Reason  string   `json:"reason"`
		}
		json.Unmarshal(params, &p)
		return "Bash", map[string]any{"command": strings.Join(p.Command, " "), "cwd": p.Cwd, "reason": p.Reason}
	case "applyPatchApproval":
		var p struct {
			FileChanges map[string]any `json:"fileChanges"`
			Reason      string         `json:"reason"`
		}
		json.Unmarshal(params, &p)
		paths := make([]string, 0, len(p.FileChanges))
		for path := range p.FileChanges {
			paths = append(paths, path)
		}
		return "Edit", map[string]any{"file_path": strings.Join(paths, ", "), "reason": p.Reason}
	default:
		return method, map[string]any{}
	}
}

func (r *appServerRun) handleNotification(method string, params json.RawMessage) {
	switch method {
	case "item/started", "item/completed":
		r.handleItem(method == "item/started", params)
	case "turn/completed":
		r.handleTurnCompleted(params)
	case "thread/tokenUsage/updated":
		var p struct {
			TokenUsage struct {
				Total map[string]any `json:"total"`
			} `json:"tokenUsage"`
		}
		json.Unmarshal(params, &p)
		r.mu.Lock()
		r.lastUsage = p.TokenUsage.Total
		r.mu.Unlock()
	}
}

func (r *appServerRun) handleItem(started bool, params json.RawMessage) {
	var p struct {
		Item map[string]any `json:"item"`
	}
	if json.Unmarshal(params, &p) != nil || p.Item == nil {
		return
	}
	item := p.Item
	id, _ := item["id"].(string)
	switch itemType, _ := item["type"].(string); itemType {
	case "commandExecution":
		if started {
			cmd, _ := item["command"].(string)
			r.emit(agents.Event{Type: "tool_use", Payload: map[string]any{
				"id": id, "name": "Bash", "input": map[string]any{"command": cmd}}})
			return
		}
		out, _ := item["aggregatedOutput"].(string)
		exitCode, _ := item["exitCode"].(float64)
		r.emit(agents.Event{Type: "tool_result", Payload: map[string]any{
			"tool_use_id": id, "content": clipStr(out, 2000), "is_error": exitCode != 0}})
	case "fileChange":
		changes, _ := item["changes"].([]any)
		paths := make([]string, 0, len(changes))
		for _, c := range changes {
			if cm, ok := c.(map[string]any); ok {
				if p, ok := cm["path"].(string); ok {
					paths = append(paths, p)
				}
			}
		}
		files := strings.Join(paths, ", ")
		if started {
			r.emit(agents.Event{Type: "tool_use", Payload: map[string]any{
				"id": id, "name": "Edit", "input": map[string]any{"file_path": files}}})
			return
		}
		status, _ := item["status"].(string)
		r.emit(agents.Event{Type: "tool_result", Payload: map[string]any{
			"tool_use_id": id, "content": "changed: " + files, "is_error": status == "failed"}})
	case "agentMessage":
		if started {
			return
		}
		if text, _ := item["text"].(string); strings.TrimSpace(text) != "" {
			r.emit(agents.Event{Type: "text", Payload: map[string]any{"text": text}})
		}
	}
}

func (r *appServerRun) handleTurnCompleted(params json.RawMessage) {
	var p struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
			DurationMs *int64 `json:"durationMs"`
		} `json:"turn"`
	}
	json.Unmarshal(params, &p)
	r.mu.Lock()
	if r.turnID == p.Turn.ID {
		r.active = false
	}
	usage := r.lastUsage
	r.mu.Unlock()

	payload := map[string]any{
		"subtype": "success", "cost_usd": nil, "num_turns": nil,
		"duration_ms": p.Turn.DurationMs, "result": "", "session_id": p.Turn.ID,
	}
	if p.Turn.Status == "failed" {
		payload["subtype"] = "error"
		if p.Turn.Error != nil {
			payload["result"] = p.Turn.Error.Message
		}
	}
	if usage != nil {
		out := map[string]any{}
		if v, ok := usage["inputTokens"]; ok {
			out["input_tokens"] = v
		}
		if v, ok := usage["outputTokens"]; ok {
			out["output_tokens"] = v
		}
		payload["usage"] = out
		payload["tokens"] = usage["outputTokens"]
	}
	r.emit(agents.Event{Type: "result", Payload: payload})
}

func (r *appServerRun) emit(ev agents.Event) {
	select {
	case r.events <- ev:
	default:
		// a driver run that nobody is draining must never deadlock the poll
		// loop; Run(...) always drains, and the scheduler's consumer goroutine
		// does too, so this only bites a caller that ignored Events entirely.
	}
}

// watchExit is the run's own finalisation watcher: the app-server process
// only exits once its stdin (the pump) is closed by Cancel, so — unlike
// execDriver — reaching "done" is always an explicit close, not the process
// finishing a single turn on its own.
func (r *appServerRun) watchExit(ctx context.Context) {
	defer close(r.done)
	defer close(r.events)
	exitPath := r.rt + "/exit_code"
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.cancelLoop()
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
		rc := -1
		fmt.Sscanf(trimmed, "%d", &rc)
		r.mu.Lock()
		r.closed = true
		canceled := r.canceled
		threadID := r.threadID
		r.mu.Unlock()
		errMsg := ""
		if canceled {
			errMsg = "cancelled"
		}
		r.result = Result{ExitCode: rc, SessionID: threadID, Err: errMsg}
		return
	}
}

func clipStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
