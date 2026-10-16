package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Model is one resolved model: the provider that serves it and its metadata.
type Model struct {
	Ref      string // provider/model, for display and events
	ID       string // provider-local id sent in requests
	Provider llm.Provider
	Info     llm.ModelInfo
}

// Hooks lets user hooks observe and veto tool calls. Nil is fine.
type Hooks interface {
	// PreTool runs before a tool call. A non-empty block reason stops the
	// call and is returned to the model as the tool's error.
	PreTool(ctx context.Context, tool string, args json.RawMessage) (block string)
	// PostTool runs after a tool call. Extra text is appended to the
	// result the model sees (e.g. formatter or linter output).
	PostTool(ctx context.Context, tool string, args json.RawMessage, res tools.Result) (extra string)
	// Prompt runs on each user prompt; extra text is added as context, a
	// non-empty block reason refuses the prompt.
	Prompt(ctx context.Context, prompt string) (extra, block string)
	// Stop runs when a turn ends.
	Stop(ctx context.Context, stats TurnStats)
}

// Compactor shrinks a conversation that is close to the context window.
type Compactor interface {
	Compact(ctx context.Context, msgs []llm.Message) (kept []llm.Message, summary string, err error)
}

// Options configures an Agent.
type Options struct {
	Model    Model
	Fallback []Model // tried in order when Model keeps failing

	Tools *tools.Registry
	// Active lists the tool names offered to the model (nil = all).
	Active map[string]bool
	// Env is the template for every tool call (CallID/Mode are filled in).
	Env tools.Env

	// System builds the system prompt. It is called once per turn so it
	// can reflect the current mode; keep its output stable within a turn.
	System func(mode string) string
	Mode   string

	MaxTokens int
	Reasoning string
	Sink      Sink

	MaxSteps     int           // 0 = unlimited (main agent)
	MaxRetries   int           // transient retries per step, default 3
	StallTimeout time.Duration // no stream event for this long = stall, default 120s

	Compactor Compactor
	CompactAt float64 // fraction of the context window, default 0.8; <0 disables

	// Recall returns context to attach to a new user prompt (memories,
	// mentioned files). May be nil.
	Recall func(ctx context.Context, prompt string) string
	Hooks  Hooks

	// Persist is called after every change to the message history so the
	// session store can save it. Rewritten=true means earlier messages
	// changed (compaction) rather than only new ones being appended.
	Persist func(msgs []llm.Message, rewritten bool)

	// ID identifies a subagent in events ("" for the main agent).
	ID string
	// Headers are sent with every model call.
	Headers map[string]string
}

// Agent is a conversation with a model that can call tools. It is safe to
// call Steer, Mode setters and Messages from other goroutines while Run is
// in progress; Run itself must not be called concurrently.
type Agent struct {
	opts Options

	mu       sync.Mutex
	msgs     []llm.Message
	steering []string
	mode     string
	model    Model
	total    llm.Usage
	cost     float64
	lastIn   int // input tokens of the last step (≈ context in use)
	running  bool
	stopTurn bool
	cancel   context.CancelFunc
}

// New creates an agent.
func New(o Options) *Agent {
	if o.MaxRetries == 0 {
		o.MaxRetries = 3
	}
	if o.StallTimeout == 0 {
		o.StallTimeout = 120 * time.Second
	}
	if o.CompactAt == 0 {
		o.CompactAt = 0.8
	}
	if o.Mode == "" {
		o.Mode = "default"
	}
	if o.Sink == nil {
		o.Sink = func(Event) {}
	}
	return &Agent{opts: o, mode: o.Mode, model: o.Model}
}

// SetMessages replaces the history (session resume).
func (a *Agent) SetMessages(m []llm.Message) {
	a.mu.Lock()
	a.msgs = append([]llm.Message(nil), m...)
	a.mu.Unlock()
}

// Messages returns a copy of the history.
func (a *Agent) Messages() []llm.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Message(nil), a.msgs...)
}

// SetMode changes the mode for the next step.
func (a *Agent) SetMode(m string) { a.mu.Lock(); a.mode = m; a.mu.Unlock() }

// Mode returns the current mode.
func (a *Agent) Mode() string { a.mu.Lock(); defer a.mu.Unlock(); return a.mode }

// SetModel switches model for the next step.
func (a *Agent) SetModel(m Model) { a.mu.Lock(); a.model = m; a.mu.Unlock() }

// CurrentModel returns the active model.
func (a *Agent) CurrentModel() Model { a.mu.Lock(); defer a.mu.Unlock(); return a.model }

// SetReasoning changes the reasoning effort.
func (a *Agent) SetReasoning(r string) { a.mu.Lock(); a.opts.Reasoning = r; a.mu.Unlock() }

// Usage returns cumulative token usage and cost.
func (a *Agent) Usage() (llm.Usage, float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.total, a.cost
}

// ContextUsed returns the tokens the last request used and the window.
func (a *Agent) ContextUsed() (int, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastIn, a.model.Info.ContextWindow
}

// Running reports whether a turn is in progress.
func (a *Agent) Running() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.running }

// SetActive replaces the set of tool names offered to the model (nil = all).
// It takes effect on the next tool call, even mid-turn.
func (a *Agent) SetActive(active map[string]bool) {
	a.mu.Lock()
	a.opts.Active = active
	a.mu.Unlock()
}

func (a *Agent) active() map[string]bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.opts.Active
}

// Steer queues a message for the running turn; it is delivered at the
// next step boundary. Returns false when the queue is full (5).
func (a *Agent) Steer(msg string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.steering) >= 5 {
		return false
	}
	a.steering = append(a.steering, msg)
	return true
}

// Queued returns pending steering messages.
func (a *Agent) Queued() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.steering...)
}

// Abort cancels the running turn and clears queued steering.
func (a *Agent) Abort() {
	a.mu.Lock()
	a.steering = nil
	c := a.cancel
	a.mu.Unlock()
	if c != nil {
		c()
	}
}

// EndTurn asks the loop to stop after the current tool calls (used by the
// plan tool, which needs the user to review before anything runs).
func (a *Agent) EndTurn() { a.mu.Lock(); a.stopTurn = true; a.mu.Unlock() }

func (a *Agent) emit(e Event) {
	e.At = time.Now()
	if e.AgentID == "" {
		e.AgentID = a.opts.ID
	}
	a.opts.Sink(e)
}

func (a *Agent) persist(rewritten bool) {
	if a.opts.Persist == nil {
		return
	}
	a.opts.Persist(a.Messages(), rewritten)
}

// Run sends prompt (plus optional images) and runs until the model stops
// calling tools, the turn is aborted, or an error ends it.
func (a *Agent) Run(ctx context.Context, prompt string, images []llm.Part) (TurnStats, error) {
	start := time.Now()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return TurnStats{}, errors.New("agent is already running")
	}
	a.running, a.cancel, a.stopTurn = true, cancel, false
	mode := a.mode
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.running, a.cancel = false, nil
		a.mu.Unlock()
	}()

	stats := TurnStats{}
	a.emit(Event{Kind: EvTurnStart, Prompt: prompt, Model: a.CurrentModel().Ref, Mode: mode})

	text := prompt
	if a.opts.Hooks != nil {
		extra, block := a.opts.Hooks.Prompt(ctx, prompt)
		if block != "" {
			stats.Error = "blocked by hook: " + block
			a.emit(Event{Kind: EvError, Category: llm.CatConfig, Text: stats.Error})
			a.emit(Event{Kind: EvTurnDone, Stats: &stats})
			return stats, errors.New(stats.Error)
		}
		if extra != "" {
			text += "\n\n<system-reminder>\n" + extra + "\n</system-reminder>"
		}
	}
	if a.opts.Recall != nil {
		if r := a.opts.Recall(ctx, prompt); r != "" {
			text = r + "\n\n" + text
		}
	}
	user := llm.Message{Role: llm.RoleUser, Parts: append([]llm.Part{{Type: llm.PartText, Text: text}}, images...)}
	a.mu.Lock()
	a.msgs = append(a.msgs, user)
	a.mu.Unlock()
	a.persist(false)

	final, err := a.loop(ctx, &stats)
	stats.Duration = time.Since(start)
	a.mu.Lock()
	stats.Tokens, stats.CostUSD = a.total, a.cost
	a.mu.Unlock()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			stats.Aborted = true
			err = context.Canceled
		} else {
			stats.Error = err.Error()
		}
	}
	if a.opts.Hooks != nil {
		a.opts.Hooks.Stop(context.WithoutCancel(ctx), stats)
	}
	a.emit(Event{Kind: EvTurnDone, Text: final, Stats: &stats})
	return stats, err
}

// loop runs model steps until done. It returns the final answer text.
func (a *Agent) loop(ctx context.Context, stats *TurnStats) (string, error) {
	continues := 0
	var final string
	for step := 1; ; step++ {
		if err := ctx.Err(); err != nil {
			return final, err
		}
		if a.opts.MaxSteps > 0 && step > a.opts.MaxSteps {
			err := fmt.Errorf("max steps reached (%d)", a.opts.MaxSteps)
			a.emit(Event{Kind: EvError, Category: llm.CatOther, Text: err.Error()})
			return final, err
		}
		if err := a.maybeCompact(ctx); err != nil && ctx.Err() != nil {
			return final, ctx.Err()
		}
		a.emit(Event{Kind: EvStepStart, Step: step})
		msg, finish, err := a.stepWithRetry(ctx, step)
		if err != nil {
			return final, err
		}
		stats.Steps = step
		a.mu.Lock()
		a.msgs = append(a.msgs, msg)
		a.mu.Unlock()
		a.persist(false)

		calls := msg.ToolCalls()
		if len(calls) == 0 {
			final = msg.Text()
			if finish == llm.FinishLength && continues < 2 {
				continues++
				a.appendUser("<system-reminder>Your last response was cut off by the output limit. Continue exactly where you stopped.</system-reminder>")
				continue
			}
			if q := a.drainSteering(); q != "" {
				a.appendUser(q)
				continue
			}
			return final, nil
		}
		results := a.runTools(ctx, calls, stats)
		a.mu.Lock()
		a.msgs = append(a.msgs, llm.Message{Role: llm.RoleTool, Parts: results})
		stop := a.stopTurn
		a.mu.Unlock()
		var hints []string
		if h := a.loopHint(); h != "" {
			hints = append(hints, h)
		}
		if q := a.drainSteering(); q != "" {
			hints = append(hints, q)
		}
		if len(hints) > 0 {
			a.appendUser(strings.Join(hints, "\n\n"))
		} else {
			a.persist(false)
		}
		if ctx.Err() != nil {
			return final, ctx.Err()
		}
		if stop {
			return msg.Text(), nil
		}
	}
}

func (a *Agent) appendUser(text string) {
	a.mu.Lock()
	a.msgs = append(a.msgs, llm.UserText(text))
	a.mu.Unlock()
	a.persist(false)
}

func (a *Agent) drainSteering() string {
	a.mu.Lock()
	q := a.steering
	a.steering = nil
	a.mu.Unlock()
	if len(q) == 0 {
		return ""
	}
	for _, s := range q {
		a.emit(Event{Kind: EvSteering, Text: s})
	}
	return "<steering>\nThe user sent this while you were working. Finish the tool call you are in, then act on it:\n\n" +
		strings.Join(q, "\n\n") + "\n</steering>"
}

// loopHint detects the model repeating the same tool call.
func (a *Agent) loopHint() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	seen := map[string]int{}
	n := 0
	for i := len(a.msgs) - 1; i >= 0 && n < 16; i, n = i-1, n+1 {
		for _, c := range a.msgs[i].ToolCalls() {
			seen[c.ToolName+string(c.Args)]++
		}
	}
	for _, v := range seen {
		if v >= 3 {
			return "<system-reminder>You have made the same tool call three times with identical arguments. It will not give a different answer. Change approach: read a different file, search for something else, or tell the user what is blocking you.</system-reminder>"
		}
	}
	return ""
}

func (a *Agent) request() llm.Request {
	// System may call back into the agent (CurrentModel), so it must run
	// without the lock held.
	sys := ""
	if a.opts.System != nil {
		sys = a.opts.System(a.Mode())
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return llm.Request{
		Model:       a.model.ID,
		System:      sys,
		Messages:    append([]llm.Message(nil), a.msgs...),
		Tools:       a.opts.Tools.Specs(a.opts.Active),
		MaxTokens:   a.opts.MaxTokens,
		Reasoning:   a.opts.Reasoning,
		CacheSystem: true,
		Headers:     a.opts.Headers,
	}
}

// stepWithRetry runs one model call, retrying transient failures and then
// walking the fallback chain.
func (a *Agent) stepWithRetry(ctx context.Context, step int) (llm.Message, llm.FinishReason, error) {
	models := append([]Model{a.CurrentModel()}, a.opts.Fallback...)
	var lastErr error
	for mi, m := range models {
		if mi > 0 {
			if m.Ref == models[0].Ref {
				continue
			}
			a.emit(Event{Kind: EvFallback, Model: m.Ref, Text: errText(lastErr)})
			a.SetModel(m)
		}
		for attempt := 0; attempt <= a.opts.MaxRetries; attempt++ {
			msg, finish, err := a.step(ctx, step)
			if err == nil {
				return msg, finish, nil
			}
			if ctx.Err() != nil {
				return llm.Message{}, "", ctx.Err()
			}
			lastErr = err
			cat, retry := llm.Classify(err)
			if !retry {
				a.emit(Event{Kind: EvError, Category: cat, Text: err.Error()})
				return llm.Message{}, "", err
			}
			if attempt == a.opts.MaxRetries {
				break
			}
			d := backoff(attempt, err)
			a.emit(Event{Kind: EvRetry, Attempt: attempt + 1, MaxAttempts: a.opts.MaxRetries, Category: cat, Text: err.Error(), Delay: d})
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return llm.Message{}, "", ctx.Err()
			}
		}
	}
	cat, _ := llm.Classify(lastErr)
	a.emit(Event{Kind: EvError, Category: cat, Text: errText(lastErr)})
	return llm.Message{}, "", lastErr
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func backoff(attempt int, err error) time.Duration {
	var ae *llm.APIError
	if errors.As(err, &ae) && ae.RetryAfter > 0 && ae.RetryAfter < 2*time.Minute {
		return ae.RetryAfter
	}
	base := time.Duration(1<<attempt) * 2 * time.Second
	return base + time.Duration(rand.Int64N(int64(base/2)))
}

var errStall = errors.New("stream stalled: no data from the model")

// step streams one model response and returns the assistant message.
func (a *Agent) step(ctx context.Context, step int) (llm.Message, llm.FinishReason, error) {
	req := a.request()
	m := a.CurrentModel()
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := m.Provider.Stream(sctx, req)
	if err != nil {
		return llm.Message{}, "", err
	}
	var (
		parts   []llm.Part
		text    strings.Builder
		reason  strings.Builder
		sig     string
		finish  llm.FinishReason
		usage   llm.Usage
		calls   = map[string]int{} // call id → index in parts
		stalled bool
	)
	flushText := func() {
		if text.Len() > 0 {
			parts = append(parts, llm.Part{Type: llm.PartText, Text: text.String()})
			text.Reset()
		}
	}
	flushReason := func() {
		if reason.Len() > 0 {
			parts = append(parts, llm.Part{Type: llm.PartReasoning, Text: reason.String(), Signature: sig})
			reason.Reset()
			sig = ""
		}
	}
	timer := time.NewTimer(a.opts.StallTimeout)
	defer timer.Stop()
	for done := false; !done; {
		select {
		case <-timer.C:
			stalled = true
			cancel()
			// drain until the provider closes
			for range ch {
			}
			done = true
		case ev, ok := <-ch:
			if !ok {
				done = true
				break
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(a.opts.StallTimeout)
			switch ev.Type {
			case llm.EvText:
				flushReason()
				text.WriteString(ev.Text)
				a.emit(Event{Kind: EvText, Text: ev.Text, Step: step})
			case llm.EvReasoning:
				flushText()
				reason.WriteString(ev.Text)
				a.emit(Event{Kind: EvReasoning, Text: ev.Text, Step: step})
			case llm.EvReasoningSig:
				sig += ev.Text
			case llm.EvToolCallStart:
				flushText()
				flushReason()
				a.emit(Event{Kind: EvToolStart, CallID: ev.ToolCallID, Tool: ev.ToolName})
			case llm.EvToolCallDelta:
				a.emit(Event{Kind: EvToolArgs, CallID: ev.ToolCallID, Text: ev.ArgsDelta})
			case llm.EvToolCallEnd:
				flushText()
				flushReason()
				args := ev.Args
				if !json.Valid(args) || len(args) == 0 {
					args = json.RawMessage("{}")
				}
				id := ev.ToolCallID
				if id == "" {
					id = fmt.Sprintf("call_%d_%d", step, len(parts))
				}
				calls[id] = len(parts)
				parts = append(parts, llm.Part{Type: llm.PartToolCall, ToolCallID: id, ToolName: ev.ToolName, Args: args})
			case llm.EvFinish:
				finish, usage = ev.FinishReason, ev.Usage
			case llm.EvError:
				flushText()
				flushReason()
				if ctx.Err() != nil {
					return llm.Message{}, "", ctx.Err()
				}
				return llm.Message{}, "", ev.Err
			}
		}
	}
	if stalled {
		if ctx.Err() != nil {
			return llm.Message{}, "", ctx.Err()
		}
		return llm.Message{}, "", errStall
	}
	if ctx.Err() != nil {
		return llm.Message{}, "", ctx.Err()
	}
	flushReason()
	flushText()
	if finish == llm.FinishError {
		return llm.Message{}, "", errors.New("the model ended the response with an error")
	}
	a.mu.Lock()
	a.total.Add(usage)
	stepCost := m.Info.Cost(usage)
	a.cost += stepCost
	a.lastIn = usage.InputTokens + usage.CacheReadTokens + usage.CacheWriteTokens + usage.OutputTokens
	total, cost, used, window := a.total, a.cost, a.lastIn, a.model.Info.ContextWindow
	a.mu.Unlock()
	a.emit(Event{Kind: EvStepFinish, Step: step, Usage: usage, Total: total, CostUSD: cost})
	a.emit(Event{Kind: EvContext, ContextUsed: used, ContextWindow: window})
	return llm.Message{Role: llm.RoleAssistant, Parts: parts}, finish, nil
}

// runTools executes tool calls: consecutive read-only calls run in
// parallel, everything else in order.
func (a *Agent) runTools(ctx context.Context, calls []llm.Part, stats *TurnStats) []llm.Part {
	out := make([]llm.Part, len(calls))
	i := 0
	for i < len(calls) {
		j := i
		for j < len(calls) && a.readOnly(calls[j]) {
			j++
		}
		if j > i+1 {
			var wg sync.WaitGroup
			for k := i; k < j; k++ {
				wg.Add(1)
				go func(k int) {
					defer wg.Done()
					out[k] = a.runTool(ctx, calls[k])
				}(k)
			}
			wg.Wait()
			i = j
			continue
		}
		out[i] = a.runTool(ctx, calls[i])
		i++
	}
	for k, c := range calls {
		stats.ToolCalls = append(stats.ToolCalls, c.ToolName)
		if !out[k].IsError {
			if files := a.lastFiles(c.ToolCallID); len(files) > 0 {
				stats.FilesEdited = appendUnique(stats.FilesEdited, files...)
			}
		}
	}
	return out
}

func appendUnique(s []string, v ...string) []string {
	for _, x := range v {
		found := false
		for _, y := range s {
			if x == y {
				found = true
				break
			}
		}
		if !found {
			s = append(s, x)
		}
	}
	return s
}

// filesByCall remembers Result.Files per call for stats.
var filesByCall sync.Map

func (a *Agent) lastFiles(callID string) []string {
	v, ok := filesByCall.LoadAndDelete(a.opts.ID + "/" + callID)
	if !ok {
		return nil
	}
	return v.([]string)
}

func (a *Agent) readOnly(call llm.Part) bool {
	t, ok := a.opts.Tools.Get(call.ToolName)
	return ok && tools.IsReadOnlyCall(t, call.Args)
}

// RestrictedModes only allow read-only tools (plus the planning tools,
// which are read-only by declaration).
var RestrictedModes = map[string]bool{"plan": true, "architect": true, "socratic": true, "challenge": true}

func (a *Agent) runTool(ctx context.Context, call llm.Part) llm.Part {
	start := time.Now()
	a.emit(Event{Kind: EvToolRun, CallID: call.ToolCallID, Tool: call.ToolName, Args: call.Args})
	res := a.execTool(ctx, call)
	if len(res.Files) > 0 {
		filesByCall.Store(a.opts.ID+"/"+call.ToolCallID, res.Files)
	}
	r := res
	a.emit(Event{Kind: EvToolResult, CallID: call.ToolCallID, Tool: call.ToolName, Args: call.Args, Result: &r, Duration: time.Since(start)})
	out := res.Output
	if out == "" {
		if res.IsError {
			out = "(error with no message)"
		} else {
			out = "(no output)"
		}
	}
	return llm.Part{Type: llm.PartToolResult, ToolCallID: call.ToolCallID, ToolName: call.ToolName, Result: out, IsError: res.IsError}
}

func (a *Agent) execTool(ctx context.Context, call llm.Part) (res tools.Result) {
	defer func() {
		if r := recover(); r != nil {
			res = tools.Errorf("internal error in %s: %v", call.ToolName, r)
		}
	}()
	t, ok := a.opts.Tools.Get(call.ToolName)
	if active := a.active(); !ok || (active != nil && !active[call.ToolName]) {
		return tools.Errorf("unknown tool %q. Available tools: %s", call.ToolName, strings.Join(a.toolNames(), ", "))
	}
	mode := a.Mode()
	if RestrictedModes[mode] && !tools.IsReadOnlyCall(t, call.Args) {
		return tools.Errorf("mode_restricted: %s mode is read-only, so %s cannot run. Investigate with read-only tools, then present your findings (in plan mode, call the plan tool).", mode, call.ToolName)
	}
	if a.opts.Hooks != nil {
		if block := a.opts.Hooks.PreTool(ctx, call.ToolName, call.Args); block != "" {
			return tools.Errorf("blocked by a hook: %s", block)
		}
	}
	env := a.opts.Env
	env.CallID = call.ToolCallID
	env.Mode = mode
	res = t.Run(ctx, &env, call.Args)
	if a.opts.Hooks != nil {
		if extra := a.opts.Hooks.PostTool(ctx, call.ToolName, call.Args, res); extra != "" {
			res.Output += "\n\n" + extra
		}
	}
	return res
}

func (a *Agent) toolNames() []string {
	var n []string
	active := a.active()
	for _, name := range a.opts.Tools.Names() {
		if active == nil || active[name] {
			n = append(n, name)
		}
	}
	sort.Strings(n)
	return n
}

// maybeCompact compacts when the last request filled the context window
// past CompactAt.
func (a *Agent) maybeCompact(ctx context.Context) error {
	a.mu.Lock()
	used, window, at := a.lastIn, a.model.Info.ContextWindow, a.opts.CompactAt
	a.mu.Unlock()
	if a.opts.Compactor == nil || at < 0 || window <= 0 || float64(used) < at*float64(window) {
		return nil
	}
	return a.Compact(ctx, fmt.Sprintf("context %d%% full", used*100/window))
}

// Compact summarises older history now.
func (a *Agent) Compact(ctx context.Context, reason string) error {
	if a.opts.Compactor == nil {
		return errors.New("compaction is not configured")
	}
	a.emit(Event{Kind: EvCompactStart, Text: reason})
	before := a.Messages()
	kept, summary, err := a.opts.Compactor.Compact(ctx, before)
	if err != nil {
		a.emit(Event{Kind: EvWarning, Text: "compaction failed: " + err.Error()})
		return err
	}
	a.mu.Lock()
	a.msgs = kept
	a.lastIn = 0
	a.mu.Unlock()
	a.persist(true)
	a.emit(Event{Kind: EvCompactDone, Text: summary, Meta: map[string]any{"messages_before": len(before), "messages_after": len(kept)}})
	return nil
}
