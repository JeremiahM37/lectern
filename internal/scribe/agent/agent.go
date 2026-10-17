package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

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

// postCompactObserver is implemented by hooks that run after a compaction
// replaced the history (the PostCompact event).
type postCompactObserver interface {
	PostCompact(ctx context.Context)
}

// SubagentObserver is implemented by hooks that see each subagent start and
// finish (the SubagentStart and SubagentStop events). errText is empty when
// the subagent finished normally.
type SubagentObserver interface {
	SubagentStart(ctx context.Context, role, task string)
	SubagentStop(ctx context.Context, role, task, errText string)
}

// Compactor shrinks a conversation that is close to the context window.
type Compactor interface {
	Compact(ctx context.Context, msgs []llm.Message) (kept []llm.Message, summary string, err error)
}

// Options configures an Agent.
type Options struct {
	Model    Model
	Fallback []Model // tried in order when Model keeps failing
	// FallbackFor overrides Fallback for one primary model, keyed by its
	// Ref. A model with no entry uses Fallback.
	FallbackFor map[string][]Model

	Tools *tools.Registry
	// Peers, when set, returns the peer findings new since its last call;
	// they are added to the next step's hints (PeerFeed).
	Peers func() string
	// SequentialTools runs every tool call in order. By default consecutive
	// read-only calls run in parallel.
	SequentialTools bool
	// ToolTimeout bounds one tool call; 0 takes 900 s, negative turns the
	// bound off. A timed-out call returns an error to the model.
	ToolTimeout time.Duration
	// Guard is the dispatcher, consulted by DispatchActive. Nil is fine:
	// the agent still reports its own dispatch calls.
	Guard DispatchGuard
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

	MaxSteps   int // 0 = unlimited (main agent)
	MaxRetries int // transient retries per step, default 3

	// Stream watchdog (spec 03 §5.3). FirstTokenTimeout applies until the
	// first text or tool call, InterTokenTimeout after that, and StepTimeout
	// caps one whole model call. Zero takes the default (180s, 120s, 900s).
	// StallTimeout, when set, stands in for the first-token and inter-token
	// thresholds that are not set, as it did before those existed.
	StallTimeout      time.Duration
	FirstTokenTimeout time.Duration
	InterTokenTimeout time.Duration
	StepTimeout       time.Duration
	// MaxStallRetries is how many times a stalled model call is resumed from
	// the text it had produced. Default 3; negative means never resume. Stall
	// retries are immediate and do not use MaxRetries.
	MaxStallRetries int

	// RetryBase is the base delay of transient retries: attempt n waits
	// RetryBase×2^n plus up to half of it. Zero takes 1 s (chat); subagents
	// use 2 s. Clamped to 250 ms–60 s.
	RetryBase time.Duration

	// Budget bounds a subagent's run: step nudges, a tool-less final step and
	// a token limit (spec 03 §3.5). Nil for the main agent.
	Budget *StepBudget

	// Tune edits every model request after it is built, for example to set
	// provider options that depend on the conversation. m is the model the
	// request goes to (the current one, or a fallback). May be nil.
	Tune func(m Model, r *llm.Request)
	// RequestDump sees each model request just before it is sent, with its
	// step number within the turn and the model ref. It must not keep or
	// change the request. Nil is fine; DumpRequests writes them as JSON Lines.
	RequestDump func(step int, modelRef string, req llm.Request)
	// Redact scrubs secrets from a tool result's Output before it enters the
	// history or the events. May be nil.
	Redact func(string) string
	// BeforeTool sees each tool call just before it runs. It must not change
	// the arguments. May be nil.
	BeforeTool func(ctx context.Context, name string, args json.RawMessage)
	// AfterTool sees each tool call's result after it runs and before Redact,
	// and may change it. May be nil.
	AfterTool func(ctx context.Context, name string, args json.RawMessage, res *tools.Result)

	Compactor Compactor
	CompactAt float64 // fraction of the context window, default 0.7; <0 disables
	// CompactReset is the fill below which the compaction latch clears. A
	// compaction sets the latch and nothing compacts again until the fill
	// drops under this, so one oversized step does not compact repeatedly.
	// Default 0.4.
	CompactReset float64

	// Deferred, when set, offers only the core tools plus those the model
	// activated with request_tools. Nil offers every tool (subagents).
	Deferred *Deferred

	// Verify runs the project's quick check (typecheck, else build) when a
	// turn edited files and ended without running one. label names the
	// command ("" = nothing to check), report is its output on failure.
	Verify func(ctx context.Context) (label string, ok bool, report string)

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
	steering []steerItem
	mode     string
	model    Model
	total    llm.Usage
	cost     float64
	lastIn   int // input tokens of the last step (≈ context in use)
	// compactLatched is set by an automatic compaction and cleared when the
	// fill falls below CompactReset (hysteresis).
	compactLatched bool
	// gen counts history replacements (compaction, reset, resume); it is
	// part of tools.Env.Scope so per-context tool memory starts over.
	gen      int
	planExec bool // carrying out a plan approved this turn (plan_exec.go)
	running  bool
	stopTurn bool
	// premature is why the turn is ending before the model finished (set by
	// loop, read by Run for the notice and the Notification hook).
	premature string
	cancel    context.CancelFunc

	// retries counts transient and stall retries in model calls this turn.
	retries int

	editedSinceCheck bool // files changed since the last project/test-like call
	checkRounds      int  // auto-fix rounds used this turn
	autoSeq          int

	// badArgs holds the tool calls of the last successful step whose
	// arguments were not valid JSON, keyed by call id. Their history parts
	// carry "{}" so the transcript stays valid; runTool refuses to run them.
	badArgs map[string]badArg

	// Subagent budget state (budget.go). policy is decided before each step
	// and read by request and execTool; tokenNudged and pendingText carry the
	// token nudge; readStreak counts read-only steps in a row.
	policy      stepPolicy
	tokenNudged bool
	pendingText bool
	readStreak  int

	// latchedMode is the mode the conversation was last told about. A change
	// is latched into the history once (modeNotice).
	latchedMode string
	// skills is the loaded-skills block for the next turn (SetSkills).
	skills string
	// dispatching counts this agent's dispatch tool calls in flight.
	dispatching int
	// waits counts tool calls blocked on an approval or a question. The tool
	// watchdog does not run its clock while any is pending.
	waits atomic.Int32
	// sysChars is the length of the last system prompt, for the context
	// estimate before the first response.
	sysChars int
	// toolChars is the size of the tool definitions in that request.
	toolChars int
	// modelLog and compactLog are the bounded histories behind /model-events
	// and the compaction log.
	modelLog   []Event
	compactLog []Event
}

// badArg is a tool call whose arguments the provider sent as invalid JSON.
type badArg struct {
	raw    string // the arguments as the provider sent them
	reason string // the JSON parser's complaint
}

// maxAutoFixRounds bounds how many times a failed automatic check is fed
// back to the model in one turn.
const maxAutoFixRounds = 2

// Defaults for the stream watchdog and resume (spec 03 §5.3, §10).
const (
	defaultFirstTokenTimeout = 180 * time.Second
	defaultInterTokenTimeout = 120 * time.Second
	defaultStepTimeout       = 900 * time.Second
	defaultMaxStallRetries   = 3
	// forceResolveTimeout bounds how long a stalled stream is drained after
	// it was cancelled, in case the provider does not close its channel.
	forceResolveTimeout = 5 * time.Second
	// defaultRetryBase is the transient retry base for the chat (spec 03 §10).
	defaultRetryBase = time.Second
	// maxFallbackCycles is how many times the fallback chain may restart
	// from the primary model before the step fails.
	maxFallbackCycles = 3
	// resumeText is sent after a stalled answer's partial text, so the model
	// continues it instead of starting over.
	resumeText = "Continue exactly where you stopped."
)

// New creates an agent.
func New(o Options) *Agent {
	if o.MaxRetries == 0 {
		o.MaxRetries = 3
	}
	if o.MaxStallRetries == 0 {
		o.MaxStallRetries = defaultMaxStallRetries
	}
	if o.StallTimeout > 0 {
		if o.FirstTokenTimeout == 0 {
			o.FirstTokenTimeout = o.StallTimeout
		}
		if o.InterTokenTimeout == 0 {
			o.InterTokenTimeout = o.StallTimeout
		}
	}
	if o.FirstTokenTimeout == 0 {
		o.FirstTokenTimeout = defaultFirstTokenTimeout
	}
	if o.InterTokenTimeout == 0 {
		o.InterTokenTimeout = defaultInterTokenTimeout
	}
	if o.StepTimeout == 0 {
		o.StepTimeout = defaultStepTimeout
	}
	if o.RetryBase == 0 {
		o.RetryBase = defaultRetryBase
	}
	o.RetryBase = min(max(o.RetryBase, 250*time.Millisecond), time.Minute)
	if o.CompactAt == 0 {
		o.CompactAt = 0.7
	}
	if o.CompactReset == 0 {
		o.CompactReset = 0.4
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
	a.gen++
	a.mu.Unlock()
}

// scope is the tools.Env.Scope for calls made now.
func (a *Agent) scope() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return fmt.Sprintf("%p#%d", a, a.gen)
}

// Messages returns a copy of the history.
func (a *Agent) Messages() []llm.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Message(nil), a.msgs...)
}

// SetMode changes the mode for the next step.
// turnModeNow is the mode the tools and prompt use. It is the live mode: the
// plan tool's Execute lifts the restrictions within the same turn, so freezing
// the mode per turn (spec 03 §2.6) would need that path changed first.
func (a *Agent) turnModeNow() string { return a.Mode() }

// currentPolicy is the step policy in force.
func (a *Agent) currentPolicy() stepPolicy { a.mu.Lock(); defer a.mu.Unlock(); return a.policy }

// Reasoning is the reasoning effort set for the agent's model calls.
func (a *Agent) Reasoning() string { a.mu.Lock(); defer a.mu.Unlock(); return a.opts.Reasoning }

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
	used, _ := a.ContextEstimate()
	a.mu.Lock()
	defer a.mu.Unlock()
	return used, a.model.Info.ContextWindow
}

// ContextEstimate is the context in use: the last response's count, or,
// before any response has reported one, an estimate from the characters in
// the system prompt and history. estimated says which it is.
func (a *Agent) ContextEstimate() (used int, estimated bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lastIn > 0 {
		return a.lastIn, false
	}
	return a.sysChars/4 + estimateHistoryTokens(a.msgs), len(a.msgs) > 0
}

// DispatchActive reports whether subagents may be editing the project now:
// this agent has a dispatch call in flight, or the guard reports one.
// It implements DispatchGuard, which the shell and git tools consult.
func (a *Agent) DispatchActive() bool {
	a.mu.Lock()
	own := a.dispatching > 0
	a.mu.Unlock()
	return own || (a.opts.Guard != nil && a.opts.Guard.DispatchActive())
}

// fallbackFor is the fallback chain for a primary model: its own list when
// the config gave one, else the shared Fallback.
func (a *Agent) fallbackFor(ref string) []Model {
	if fb, ok := a.opts.FallbackFor[ref]; ok {
		return fb
	}
	return a.opts.Fallback
}

// minCompactMessages is the history length below which automatic compaction
// does not run: summarising a handful of messages saves nothing.
const minCompactMessages = 6

// ModelEvents returns the model log: each model call's usage, and the
// retries, fallbacks and errors around it, oldest first.
func (a *Agent) ModelEvents() []Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Event(nil), a.modelLog...)
}

// CompactionLog returns the compactions that started, finished or were
// refused, oldest first.
func (a *Agent) CompactionLog() []Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Event(nil), a.compactLog...)
}

// record appends e to the logs it belongs in.
func (a *Agent) record(e Event) {
	modelKind := modelLogKinds[e.Kind]
	compactKind := compactLogKinds[e.Kind] ||
		(e.Kind == EvWarning && strings.HasPrefix(e.Text, "compaction failed"))
	if !modelKind && !compactKind {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if modelKind {
		a.modelLog = appendEventCapped(a.modelLog, e)
	}
	if compactKind {
		a.compactLog = appendEventCapped(a.compactLog, e)
	}
}

func appendEventCapped(s []Event, e Event) []Event {
	s = append(s, e)
	if over := len(s) - journalCap; over > 0 {
		s = append([]Event(nil), s[over:]...)
	}
	return s
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

// offeredNames returns the tool names sent to the model (nil = all).
func (a *Agent) offeredNames() map[string]bool {
	base := a.active()
	if a.opts.Deferred == nil {
		return base
	}
	return a.opts.Deferred.offered(base)
}

// steerItem is one queued steering message and the images sent with it.
type steerItem struct {
	text   string
	images []llm.Part
}

// maxSteering is how many steering messages may wait for a step boundary.
const maxSteering = 5

// Steer queues a message for the running turn; it is delivered at the
// next step boundary. Returns false when the queue is full (5).
func (a *Agent) Steer(msg string) bool {
	return a.SteerWithImages(msg, nil)
}

// SteerWithImages queues a message with images (PartImage parts; other parts
// are dropped). The images are delivered with the message, in the same user
// turn, at the next step boundary.
func (a *Agent) SteerWithImages(msg string, images []llm.Part) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.steering) >= maxSteering {
		return false
	}
	var imgs []llm.Part
	for _, p := range images {
		if p.Type == llm.PartImage && len(p.Data) > 0 {
			imgs = append(imgs, p)
		}
	}
	a.steering = append(a.steering, steerItem{text: msg, images: imgs})
	return true
}

// generation reports how many times the history has been replaced.
func (a *Agent) generation() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.gen
}

// Queued returns pending steering messages.
func (a *Agent) Queued() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.steering))
	for _, s := range a.steering {
		out = append(out, s.text)
	}
	return out
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
	a.record(e)
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
	a.premature = ""
	a.checkRounds = 0
	a.retries = 0
	mode := a.mode
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.running, a.cancel, a.planExec = false, nil, false
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
	if notice := modeNotice(a.latchedMode, mode); notice != "" {
		a.msgs = append(a.msgs, llm.UserText(notice))
	}
	a.latchedMode = mode
	a.latchSkillsLocked()
	a.msgs = append(a.msgs, user)
	a.mu.Unlock()
	a.persist(false)

	final, err := a.loop(ctx, &stats)
	stats.Duration = time.Since(start)
	a.mu.Lock()
	stats.Tokens, stats.CostUSD = a.total, a.cost
	stats.Retries = a.retries
	premature := a.premature
	a.mu.Unlock()
	if premature != "" && err == nil {
		a.emit(Event{Kind: EvNotice, Text: prematureNotice(premature)})
	}
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
		if n, ok := a.opts.Hooks.(notifier); ok {
			switch {
			case stats.Error != "" && !stats.Aborted:
				n.Notification(context.WithoutCancel(ctx), dispatchFailedNotice(stats))
			case premature != "" && err == nil:
				n.Notification(context.WithoutCancel(ctx), prematureNotice(premature))
			}
		}
	}
	a.emit(Event{Kind: EvTurnDone, Text: final, Stats: &stats})
	return stats, err
}

// loop runs model steps until done. It returns the final answer text.
func (a *Agent) loop(ctx context.Context, stats *TurnStats) (string, error) {
	continues := 0
	silent := 0 // reminders sent for a reply that ended silently after tools
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
		gen := a.generation()
		if err := a.maybeCompact(ctx); err != nil && ctx.Err() != nil {
			return final, ctx.Err()
		}
		// Steering that arrived while the history was being compacted goes in
		// now, after the summary, rather than waiting for the next tool step.
		if step > 1 && a.generation() != gen {
			if q, imgs := a.drainSteering(); q != "" {
				a.appendSteering(q, imgs)
			}
		}
		a.setPolicy(step)
		a.emit(Event{Kind: EvStepStart, Step: step})
		if used, estimated := a.ContextEstimate(); estimated {
			a.emit(Event{Kind: EvContext, ContextUsed: used, ContextWindow: a.CurrentModel().Info.ContextWindow, Estimated: true})
		}
		msg, finish, err := a.stepWithRetry(ctx, step)
		if err != nil {
			// An aborted step keeps the answer text it had produced.
			if ctx.Err() != nil && len(msg.Parts) > 0 {
				a.mu.Lock()
				a.msgs = append(a.msgs, msg)
				a.mu.Unlock()
				a.persist(false)
			}
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
			if finish == llm.FinishLength {
				a.setPremature("the reply was cut off by the output limit three times in a row")
			}
			if q, imgs := a.drainSteering(); q != "" {
				a.appendSteering(q, imgs)
				continue
			}
			// Some models stop with an empty reply right after tool results,
			// leaving the work half done and the user with no answer. Ask
			// once to carry on or to answer.
			if strings.TrimSpace(final) == "" && silent < 1 && ctx.Err() == nil && a.afterToolResults() {
				silent++
				a.appendUser("<system-reminder>You ended your turn with no reply after the tool results. If the task is not finished, continue it now. If it is finished, give the user your final answer.</system-reminder>")
				continue
			}
			if a.autoCheck(ctx) {
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
		budgetHint, stopNow := a.budgetHints(calls, step)
		if budgetHint != "" {
			hints = append(hints, budgetHint)
		}
		if a.opts.Peers != nil {
			if fresh := a.opts.Peers(); fresh != "" {
				hints = append(hints, fresh)
			}
		}
		if sn, ok := a.opts.Hooks.(stepNoticer); ok {
			if note := sn.StepNotice(ctx, step+1); note != "" {
				hints = append(hints, note)
			}
		}
		q, imgs := a.drainSteering()
		if q != "" {
			hints = append(hints, q)
		}
		if len(hints) > 0 {
			a.appendSteering(strings.Join(hints, "\n\n"), imgs)
		} else {
			a.persist(false)
		}
		if ctx.Err() != nil {
			return final, ctx.Err()
		}
		if stop {
			return msg.Text(), nil
		}
		if stopNow {
			a.setPremature("the context budget was used up before the task finished")
			return msg.Text(), nil
		}
	}
}

// setPremature records why the turn is ending early. The first reason wins.
func (a *Agent) setPremature(why string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.premature == "" {
		a.premature = why
	}
}

// prematureNotice is the text shown and sent to Notification hooks when a
// turn stops before the model finished on its own.
func prematureNotice(why string) string {
	return "Stopped early: " + why + ". The answer may be incomplete; ask to continue."
}

// setPolicy records the step policy for step before the model is called.
func (a *Agent) setPolicy(step int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.policy = policyFor(a.opts.Budget, a.opts.MaxSteps, step, a.pendingText)
	a.pendingText = false
}

// budgetHints collects the subagent budget hints due after step: the
// read-only streak, the wrap-up for the next step, and the token nudge. stop
// is true when the token limit has ended the run. It returns nothing for the
// main agent.
func (a *Agent) budgetHints(calls []llm.Part, step int) (hint string, stop bool) {
	b := a.opts.Budget
	if b == nil {
		return "", false
	}
	var hints []string
	allRead := true
	for _, c := range calls {
		if !a.readOnly(c) {
			allRead = false
			break
		}
	}
	a.mu.Lock()
	if allRead {
		a.readStreak++
	} else {
		a.readStreak = 0
	}
	streak := a.readStreak
	a.mu.Unlock()
	if streak == readOnlyStreakHint {
		hints = append(hints, readStreakHint(b))
	}
	if h := wrapUpHint(b, a.opts.MaxSteps, step+1); h != "" {
		hints = append(hints, h)
	}
	h, stop := a.tokenCheck()
	if h != "" {
		hints = append(hints, h)
	}
	return strings.Join(hints, "\n"), stop
}

// autoCheck runs Verify once the turn has edited files without a check after
// them. A failure is fed back as a reminder so the model can fix it; it
// returns true when a reminder was queued and the loop should go on.
func (a *Agent) autoCheck(ctx context.Context) bool {
	a.mu.Lock()
	if !a.editedSinceCheck || a.opts.Verify == nil || ctx.Err() != nil {
		a.mu.Unlock()
		return false
	}
	a.editedSinceCheck = false
	a.autoSeq++
	id := fmt.Sprintf("auto_check_%d", a.autoSeq)
	a.mu.Unlock()

	label, ok, report := a.opts.Verify(ctx)
	if ctx.Err() != nil || label == "" {
		return false
	}
	a.emit(Event{Kind: EvToolRun, CallID: id, Tool: "auto_check", Args: json.RawMessage(fmt.Sprintf(`{"command":%q}`, label))})
	if ok {
		r := tools.Result{Output: "passed", Summary: "passed"}
		a.emit(Event{Kind: EvToolResult, CallID: id, Tool: "auto_check", Args: json.RawMessage(fmt.Sprintf(`{"command":%q}`, label)), Result: &r})
		return false
	}
	r := tools.Result{Output: report, IsError: true, Summary: "failed"}
	a.emit(Event{Kind: EvToolResult, CallID: id, Tool: "auto_check", Args: json.RawMessage(fmt.Sprintf(`{"command":%q}`, label)), Result: &r})
	a.mu.Lock()
	if a.checkRounds >= maxAutoFixRounds {
		a.mu.Unlock()
		a.emit(Event{Kind: EvWarning, Text: fmt.Sprintf("%s still fails after %d automatic fix rounds; reporting it instead", label, maxAutoFixRounds)})
		return false
	}
	a.checkRounds++
	a.mu.Unlock()
	a.appendUser("<system-reminder>The automatic check `" + label + "` failed after your edits. Fix the errors below, then finish. Output:\n\n" + report + "\n</system-reminder>")
	return true
}

// setEdited records whether files changed since the last check.
func (a *Agent) setEdited(v bool) {
	a.mu.Lock()
	a.editedSinceCheck = v
	a.mu.Unlock()
}

// isCheckCall reports whether a call runs the project's checks: the project
// tool, or a shell command that runs tests, builds or linters.
func isCheckCall(c llm.Part) bool {
	switch c.ToolName {
	case "project":
		return true
	case "shell":
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(c.Args, &in) != nil {
			return false
		}
		cmd := strings.ToLower(in.Command)
		for _, k := range []string{"test", "build", "vet", "lint", "tsc", "pytest", "cargo"} {
			if strings.Contains(cmd, k) {
				return true
			}
		}
	}
	return false
}

func (a *Agent) appendUser(text string) {
	a.mu.Lock()
	a.msgs = append(a.msgs, llm.UserText(text))
	a.mu.Unlock()
	a.persist(false)
}

// drainSteering takes every queued steering message as one <steering> block,
// with the images sent along with them. It returns "" when nothing is queued.
func (a *Agent) drainSteering() (string, []llm.Part) {
	a.mu.Lock()
	q := a.steering
	a.steering = nil
	a.mu.Unlock()
	if len(q) == 0 {
		return "", nil
	}
	texts := make([]string, 0, len(q))
	var images []llm.Part
	for _, s := range q {
		a.emit(Event{Kind: EvSteering, Text: s.text, Meta: map[string]any{"images": len(s.images)}})
		texts = append(texts, s.text)
		images = append(images, s.images...)
	}
	block := "<steering>\nThe user sent this while you were working. Finish the tool call you are in, then act on it:\n\n" +
		strings.Join(texts, "\n\n") + "\n</steering>"
	return block, images
}

// appendSteering puts a drained steering block, and its images, into the
// history. It joins a user message that is already last, so two user turns
// are not left in a row.
func (a *Agent) appendSteering(text string, images []llm.Part) {
	a.mu.Lock()
	parts := append([]llm.Part{{Type: llm.PartText, Text: text}}, images...)
	if n := len(a.msgs); n > 0 && a.msgs[n-1].Role == llm.RoleUser {
		a.msgs[n-1].Parts = append(a.msgs[n-1].Parts, parts...)
	} else {
		a.msgs = append(a.msgs, llm.Message{Role: llm.RoleUser, Parts: parts})
	}
	a.mu.Unlock()
	a.persist(false)
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

// request builds the next model call. extra messages are appended to the
// history for this call only.
func (a *Agent) request(extra ...llm.Message) llm.Request {
	// System may call back into the agent (CurrentModel), so it must run
	// without the lock held.
	sys := ""
	if a.opts.System != nil {
		sys = a.opts.System(a.turnModeNow())
	}
	offered := a.offeredNames() // takes the lock itself, so call it first
	specs := a.opts.Tools.Specs(offered)
	a.mu.Lock()
	a.sysChars = len(sys)
	msgs := append(append([]llm.Message(nil), a.msgs...), extra...)
	req := llm.Request{
		Model:       a.model.ID,
		System:      sys,
		Messages:    msgs,
		Tools:       a.policy.filterTools(specs),
		MaxTokens:   a.opts.MaxTokens,
		Reasoning:   a.opts.Reasoning,
		CacheSystem: true,
		Headers:     a.opts.Headers,
	}
	a.toolChars = 0
	for _, t := range req.Tools {
		a.toolChars += len(t.Name) + len(t.Description) + len(t.Schema)
	}
	m := a.model
	a.mu.Unlock()
	if a.opts.Tune != nil {
		a.opts.Tune(m, &req)
	}
	return req
}

// ContextParts estimates what fills the context, in tokens, from the last
// request: the system prompt, the tool definitions and the conversation.
// They are character estimates (about four per token), so they need not add
// up to the count the provider reports.
type ContextParts struct {
	System, Tools, Conversation int
}

// ContextParts returns the estimate. It is zero before the first request.
func (a *Agent) ContextParts() ContextParts {
	a.mu.Lock()
	defer a.mu.Unlock()
	return ContextParts{System: a.sysChars / 4, Tools: a.toolChars / 4, Conversation: estimateHistoryTokens(a.msgs)}
}

// stepWithRetry runs one model call, retrying transient failures and then
// walking the fallback chain. When the chain is exhausted it goes back to
// the primary model, up to maxFallbackCycles times (spec 03 §5.1).
func (a *Agent) stepWithRetry(ctx context.Context, step int) (llm.Message, llm.FinishReason, error) {
	primaryNow := a.CurrentModel()
	chain := append([]Model{primaryNow}, a.fallbackFor(primaryNow.Ref)...)
	primary := chain[0]
	var lastErr error
	cycles := 0
	for i := 0; ; i++ {
		if i >= len(chain) {
			// The chain is spent. Go back to the primary, or give up.
			if len(chain) == 1 {
				break
			}
			cycles++
			if cycles > maxFallbackCycles {
				lastErr = fmt.Errorf("exhausted %d fallback cycles: %w", maxFallbackCycles, lastErr)
				break
			}
			a.emit(Event{Kind: EvFallback, Model: primary.Ref, Text: fmt.Sprintf("All fallbacks exhausted, retrying primary (cycle %d/%d): %s", cycles, maxFallbackCycles, errText(lastErr))})
			a.SetModel(primary)
			i = 0
		}
		m := chain[i]
		if i > 0 {
			if m.Ref == primary.Ref {
				continue
			}
			a.emit(Event{Kind: EvFallback, Model: m.Ref, Text: errText(lastErr)})
			a.SetModel(m)
		}
		// resume is the text a stalled attempt produced. Later attempts
		// continue it. Stall resumes are immediate and have their own budget.
		var resume string
		stalls, attempt := 0, 0
		for {
			msg, finish, err := a.step(ctx, step, resume)
			if err == nil {
				return msg, finish, nil
			}
			if ctx.Err() != nil {
				return msg, "", ctx.Err()
			}
			lastErr = err
			var st *stallError
			if errors.As(err, &st) {
				resume = st.partial
				stalls++
				limit := max(a.opts.MaxStallRetries, 0)
				if stalls > limit {
					lastErr = fmt.Errorf("%s after %d retries; giving up. You can resend your message", st.describe(), limit)
					break
				}
				a.countRetry()
				a.emit(Event{Kind: EvRetry, Attempt: stalls, MaxAttempts: limit, Category: llm.CatStream,
					Text: fmt.Sprintf("%s. Auto-retrying (%d/%d)…", st.notice(), stalls, limit)})
				continue
			}
			cat, retry := llm.Classify(err)
			status := statusOf(err)
			if !retry {
				a.emit(Event{Kind: EvError, Category: cat, Text: err.Error(), Status: status})
				return llm.Message{}, "", err
			}
			if attempt == a.opts.MaxRetries {
				break
			}
			d := backoffFrom(a.opts.RetryBase, attempt, err)
			attempt++
			a.countRetry()
			a.emit(Event{Kind: EvRetry, Attempt: attempt, MaxAttempts: a.opts.MaxRetries, Category: cat, Text: err.Error(), Delay: d, Status: status})
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return llm.Message{}, "", ctx.Err()
			}
		}
	}
	cat, _ := llm.Classify(lastErr)
	a.emit(Event{Kind: EvError, Category: cat, Text: errText(lastErr), Status: statusOf(lastErr)})
	return llm.Message{}, "", lastErr
}

// countRetry records one in-run retry of a model call.
func (a *Agent) countRetry() {
	a.mu.Lock()
	a.retries++
	a.mu.Unlock()
}

// statusOf is the HTTP status of a provider error, or 0 when there is none.
func statusOf(err error) int {
	var ae *llm.APIError
	if errors.As(err, &ae) {
		return ae.StatusCode
	}
	return 0
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// backoff is the pause before retry n (n counts from 0) with the chat's base.
func backoff(attempt int, err error) time.Duration {
	return backoffFrom(defaultRetryBase, attempt, err)
}

// backoffFrom is base×2^n plus jitter of up to half the base, or the
// server's Retry-After when it gave one.
func backoffFrom(base time.Duration, attempt int, err error) time.Duration {
	var ae *llm.APIError
	// An explicit Retry-After: 0 means retry immediately.
	if errors.As(err, &ae) && ae.RetryAfterSet && ae.RetryAfter < 2*time.Minute {
		return ae.RetryAfter
	}
	d := base << attempt
	return d + time.Duration(rand.Int64N(int64(base/2)+1))
}

// stallError is a model call that went silent. partial is the answer text
// it had produced, which the next attempt continues. It is empty when the
// attempt had already completed a tool call, since that cannot be resumed
// as text alone.
type stallError struct {
	partial string
	after   time.Duration // the threshold that tripped
	total   bool          // true when StepTimeout tripped, not the idle timer
}

// notice is the user-facing line for a stall, in the chat's wording.
func (e *stallError) notice() string {
	if e.total {
		return e.describe()
	}
	wait := e.after.Round(time.Second).String()
	if e.after < time.Second {
		wait = e.after.Round(time.Millisecond).String()
	}
	return "Connection stalled — no activity for " + wait
}

func (e *stallError) describe() string {
	if e.total {
		return fmt.Sprintf("stream stalled: the model call ran past %s", e.after.Round(time.Millisecond))
	}
	return fmt.Sprintf("stream stalled: no activity for %s", e.after.Round(time.Millisecond))
}

func (e *stallError) Error() string { return e.describe() }

// errContentFilter ends a step whose reply the provider's safety filter cut
// short. It is not retried and the partial reply is not kept.
var errContentFilter = errors.New("the provider's content filter stopped the reply, so it was not used")

// step streams one model response and returns the assistant message. When
// resume is set, the model is asked to continue that text, and the returned
// message starts with it.
func (a *Agent) step(ctx context.Context, step int, resume string) (llm.Message, llm.FinishReason, error) {
	if resume == "" {
		// A mode change made during the turn (plan approved, for example) is
		// latched before the model sees the next request.
		a.latchMode()
	}
	var extra []llm.Message
	if resume != "" {
		extra = []llm.Message{llm.AssistantText(resume), llm.UserText(resumeText)}
	}
	started := time.Now()
	req := a.request(extra...)
	m := a.CurrentModel()
	if a.opts.RequestDump != nil {
		a.opts.RequestDump(step, m.Ref, req)
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := m.Provider.Stream(sctx, req)
	if err != nil {
		return llm.Message{}, "", err
	}
	var (
		parts      []llm.Part
		text       strings.Builder
		reason     strings.Builder
		sig        string
		finish     llm.FinishReason
		usage      llm.Usage
		calls      = map[string]int{} // call id → index in parts
		bad        = map[string]badArg{}
		stalled    bool
		hasContent bool // text or a tool call has arrived
		stall      *stallError
	)
	text.WriteString(resume)
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
	// idle is the threshold for the next event: first-token until there is
	// content, inter-token after that. Every stream part resets it.
	idle := func() time.Duration {
		if hasContent {
			return a.opts.InterTokenTimeout
		}
		return a.opts.FirstTokenTimeout
	}
	timer := time.NewTimer(idle())
	defer timer.Stop()
	capTimer := time.NewTimer(a.opts.StepTimeout)
	defer capTimer.Stop()
	for done := false; !done; {
		select {
		case <-timer.C:
			stalled = true
			stall = &stallError{after: idle()}
			cancel()
			drainAfterStall(ch)
			done = true
		case <-capTimer.C:
			stalled = true
			stall = &stallError{after: a.opts.StepTimeout, total: true}
			cancel()
			drainAfterStall(ch)
			done = true
		case ev, ok := <-ch:
			if !ok {
				done = true
				break
			}
			switch ev.Type {
			case llm.EvText:
				hasContent = true
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
				hasContent = true
				flushText()
				flushReason()
				a.emit(Event{Kind: EvToolStart, CallID: ev.ToolCallID, Tool: ev.ToolName})
			case llm.EvToolCallDelta:
				a.emit(Event{Kind: EvToolArgs, CallID: ev.ToolCallID, Text: ev.ArgsDelta})
			case llm.EvToolCallEnd:
				flushText()
				flushReason()
				id := ev.ToolCallID
				if id == "" {
					id = fmt.Sprintf("call_%d_%d", step, len(parts))
				}
				// Empty arguments mean "no arguments". Arguments that are
				// not JSON are kept aside, and the call is answered with an
				// error instead of being run.
				args := json.RawMessage("{}")
				if raw, malformed := llm.UnparsedArgs(ev.Args); malformed {
					// The adapter could not parse the arguments and passed
					// the raw text as a JSON string (see llm.ToolArgs).
					bad[id] = badArg{raw: raw, reason: argsProblem(raw)}
				} else if raw := bytes.TrimSpace(ev.Args); len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
					if json.Valid(raw) && raw[0] == '{' {
						args = raw
					} else {
						bad[id] = badArg{raw: string(raw), reason: argsProblem(string(raw))}
					}
				}
				calls[id] = len(parts)
				parts = append(parts, llm.Part{Type: llm.PartToolCall, ToolCallID: id, ToolName: ev.ToolName, Args: args})
				hasContent = true
			case llm.EvFinish:
				finish, usage = ev.FinishReason, ev.Usage
			case llm.EvError:
				flushText()
				flushReason()
				if ctx.Err() != nil {
					return textOnly(parts), "", ctx.Err()
				}
				return llm.Message{}, "", ev.Err
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idle())
		}
	}
	if stalled {
		if ctx.Err() != nil {
			return llm.Message{}, "", ctx.Err()
		}
		flushText()
		// A finished tool call cannot be resumed as text alone, so an
		// attempt that already produced one restarts instead.
		var partial strings.Builder
		hasCall := false
		for _, p := range parts {
			switch p.Type {
			case llm.PartText:
				partial.WriteString(p.Text)
			case llm.PartToolCall:
				hasCall = true
			}
		}
		if !hasCall {
			stall.partial = partial.String()
		}
		return llm.Message{}, "", stall
	}
	if ctx.Err() != nil {
		// Abort keeps the answer text produced so far (spec 03 §2.3).
		flushText()
		return textOnly(parts), "", ctx.Err()
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
	// Some providers (Ollama) report only the prompt tokens they had to
	// evaluate, not those served from their cache, so the reported count can
	// be a small part of what was sent. Take the larger of it and an estimate
	// of the history, so the context bar and auto-compaction stay honest.
	reported := usage.InputTokens + usage.CacheReadTokens + usage.CacheWriteTokens + usage.OutputTokens
	a.lastIn = max(reported, a.sysChars/4+estimateHistoryTokens(a.msgs)+usage.OutputTokens)
	total, cost, used, window := a.total, a.cost, a.lastIn, a.model.Info.ContextWindow
	a.badArgs = bad
	a.mu.Unlock()
	a.emit(Event{Kind: EvStepFinish, Step: step, Usage: usage, Total: total, CostUSD: cost,
		Model: m.Ref, Duration: time.Since(started)})
	a.emit(Event{Kind: EvContext, ContextUsed: used, ContextWindow: window})
	if finish == llm.FinishFilter {
		return llm.Message{}, "", errContentFilter
	}
	return llm.Message{Role: llm.RoleAssistant, Parts: parts}, finish, nil
}

// textOnly keeps the text parts of a partial reply. Tool calls cannot be
// kept without their results, so they are dropped.
func textOnly(parts []llm.Part) llm.Message {
	var keep []llm.Part
	for _, p := range parts {
		if p.Type == llm.PartText && strings.TrimSpace(p.Text) != "" {
			keep = append(keep, p)
		}
	}
	if len(keep) == 0 {
		return llm.Message{}
	}
	return llm.Message{Role: llm.RoleAssistant, Parts: keep}
}

// argsProblem says why raw tool arguments cannot be used, in the parser's
// words when there is one.
func argsProblem(raw string) string {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return err.Error()
	}
	return "the arguments are not a JSON object"
}

// drainAfterStall empties a cancelled stream so the provider can finish,
// but gives up after forceResolveTimeout in case it never closes.
func drainAfterStall(ch <-chan llm.Event) {
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(forceResolveTimeout):
	}
}

// fragment returns the first n bytes of s, cut back to a whole rune.
func fragment(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// runTools executes tool calls: consecutive read-only calls run in
// parallel, everything else in order.
func (a *Agent) runTools(ctx context.Context, calls []llm.Part, stats *TurnStats) []llm.Part {
	out := make([]llm.Part, len(calls))
	i := 0
	for i < len(calls) {
		j := i
		for !a.opts.SequentialTools && j < len(calls) && a.readOnly(calls[j]) {
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
		if c.ToolName == "read" && !out[k].IsError {
			stats.FilesRead = appendUnique(stats.FilesRead, readPaths(c.Args)...)
		}
		if isCheckCall(c) {
			a.setEdited(false)
		}
		if !out[k].IsError {
			if files := a.lastFiles(c.ToolCallID); len(files) > 0 {
				stats.FilesEdited = appendUnique(stats.FilesEdited, files...)
				a.setEdited(true)
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
	ctx = WithCallID(ctx, call.ToolCallID)
	start := time.Now()
	a.emit(Event{Kind: EvToolRun, CallID: call.ToolCallID, Tool: call.ToolName, Args: call.Args})
	if call.ToolName == "dispatch" {
		a.mu.Lock()
		a.dispatching++
		a.mu.Unlock()
		defer func() {
			a.mu.Lock()
			a.dispatching--
			a.mu.Unlock()
		}()
	}
	if a.opts.BeforeTool != nil {
		a.opts.BeforeTool(ctx, call.ToolName, call.Args)
	}
	res := a.runToolCall(ctx, call)
	if a.opts.AfterTool != nil {
		a.opts.AfterTool(ctx, call.ToolName, call.Args, &res)
	}
	a.addSubagentUsage(res.Meta)
	if a.opts.Redact != nil {
		res.Output = a.opts.Redact(res.Output)
	}
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

// toolFailureObserver is implemented by hooks that see failed calls
// (PostToolUseFailure). Hooks.PostTool reports the failures that reached the
// tool; this reports the ones refused before it ran.
type toolFailureObserver interface {
	PostToolFailure(ctx context.Context, tool string, args json.RawMessage, res tools.Result) string
}

// runToolCall runs one call and does what the hooks and the model need after
// it. A call that failed before it reached the tool (unknown, blocked, not
// available for this step) still fires PostToolUseFailure, once. A note the
// user gave when approving the call is put at the top of the result, so the
// model knows what it was allowed to do.
func (a *Agent) runToolCall(ctx context.Context, call llm.Part) tools.Result {
	ctx, notes := tools.WithCallNotes(ctx)
	var posted bool
	res := a.execToolPosted(ctx, call, &posted)
	if res.IsError && !posted && a.opts.Hooks != nil {
		if f, ok := a.opts.Hooks.(toolFailureObserver); ok {
			if extra := f.PostToolFailure(ctx, call.ToolName, call.Args, res); extra != "" {
				res.Output += "\n\n" + extra
			}
		}
	}
	if n := notes(); n != "" {
		res.Output = n + "\n" + res.Output
	}
	return res
}

// callIDKey carries the provider's id for the tool call being run.
type callIDKey struct{}

// WithCallID returns ctx carrying the id of the tool call about to run, so the
// tool hooks can report which call they are about (tool_use_id).
func WithCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, callIDKey{}, id)
}

// CallID is the tool call id in ctx, or "".
func CallID(ctx context.Context) string {
	id, _ := ctx.Value(callIDKey{}).(string)
	return id
}

// execTool runs the call's checks and the tool, with its hooks.
func (a *Agent) execTool(ctx context.Context, call llm.Part) tools.Result {
	var posted bool
	return a.execToolPosted(ctx, call, &posted)
}

// execToolPosted is execTool. posted is set once the PostToolUse hooks have
// run for the call, so runToolCall knows which failures were not reported.
func (a *Agent) execToolPosted(ctx context.Context, call llm.Part, posted *bool) (res tools.Result) {
	defer func() {
		if r := recover(); r != nil {
			res = tools.Errorf("internal error in %s: %v", call.ToolName, r)
		}
	}()
	t, ok := a.opts.Tools.Get(call.ToolName)
	offered := a.offeredNames()
	if !ok || (offered != nil && !offered[call.ToolName]) {
		if ok && a.opts.Deferred != nil && (a.active() == nil || a.active()[call.ToolName]) {
			return tools.Errorf("tool %q is deferred and not loaded. Call request_tools with [%q] first, then call it.", call.ToolName, call.ToolName)
		}
		return tools.Errorf("unknown tool %q. Available tools: %s", call.ToolName, strings.Join(a.toolNames(), ", "))
	}
	if bad, ok := a.takeBadArg(call.ToolCallID); ok {
		return tools.Errorf("the arguments for %s are not valid JSON (%s). The raw input starts with %q. The call was not run. Resend it with valid JSON arguments.",
			call.ToolName, bad.reason, fragment(bad.raw, 200))
	}
	if pol := a.currentPolicy(); !pol.allows(call.ToolName) {
		return tools.Errorf("%s is not available for this step. The budget for this agent is nearly used up, so reply in text with what you have.", call.ToolName)
	}
	if a.executingPlan() && !PlanExecutionAllowed(call.ToolName) {
		return tools.Errorf("%s is not used while carrying out the approved plan: the plan already says what to change. Continue with its steps (read, edit, run checks); if the plan turns out to be wrong, say so and ask with ask_user.", call.ToolName)
	}
	mode := a.turnModeNow()
	if RestrictedModes[mode] && !tools.IsReadOnlyCall(t, call.Args) {
		return tools.Errorf("mode_restricted: %s mode is read-only, so %s cannot run. %s", mode, call.ToolName, restrictedNext(mode))
	}
	args := call.Args
	if a.opts.Hooks != nil {
		if rw, ok := a.opts.Hooks.(toolInputRewriter); ok {
			var block string
			if args, block = rw.PreToolUpdate(ctx, call.ToolName, args); block != "" {
				return tools.Errorf("blocked by a hook: %s", block)
			}
		} else if block := a.opts.Hooks.PreTool(ctx, call.ToolName, args); block != "" {
			return tools.Errorf("blocked by a hook: %s", block)
		}
	}
	env := a.opts.Env
	env.CallID = call.ToolCallID
	env.Mode = mode
	env.Scope = a.scope()
	env.Services = withService(env.Services, "dispatch", a)
	if env.Host != nil {
		env.Host = waitHost{Host: env.Host, waits: &a.waits}
	}
	res = a.runWithTimeout(ctx, call.ToolName, t, &env, args)
	*posted = true
	if a.opts.Hooks != nil {
		if extra := a.opts.Hooks.PostTool(ctx, call.ToolName, args, res); extra != "" {
			res.Output += "\n\n" + extra
		}
	}
	return res
}

// runWithTimeout runs one tool under the tool timeout. A tool that ignores
// its context and never returns is abandoned after the timeout, so the turn
// can go on; the model gets an error in its place. Time the tool spends
// waiting on a person (an approval or a question) does not count toward the
// timeout, because the watchdog cannot tell a slow tool from a patient user.
func (a *Agent) runWithTimeout(ctx context.Context, name string, t tools.Tool, env *tools.Env, args json.RawMessage) tools.Result {
	limit := a.opts.ToolTimeout
	if limit < 0 || unbounded(name) {
		return t.Run(ctx, env, args)
	}
	if limit == 0 {
		limit = defaultToolTimeout
	}
	tctx, cancelTool := context.WithCancel(ctx)
	defer cancelTool()
	done := make(chan tools.Result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- tools.Errorf("internal error in %s: %v", name, r)
			}
		}()
		done <- t.Run(tctx, env, args)
	}()
	expired, stopClock := watchClock(limit, &a.waits)
	defer stopClock()
	select {
	case res := <-done:
		return res
	case <-expired:
	case <-ctx.Done():
	}
	cancelTool()
	if ctx.Err() != nil {
		return tools.Errorf("%s was stopped before it finished.", name)
	}
	// Give a tool that honours its context a moment to report what it did.
	select {
	case res := <-done:
		return res
	case <-time.After(2 * time.Second):
	}
	return tools.Errorf("%s did not finish within %s and was abandoned. Try a narrower request, or do the work in smaller steps.", name, limit.Round(time.Second))
}

// takeBadArg reports whether a call's arguments were invalid JSON, and
// forgets it so it is refused once.
func (a *Agent) takeBadArg(id string) (badArg, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, ok := a.badArgs[id]
	delete(a.badArgs, id)
	return b, ok
}

// subagentUsage reads the tokens a subagent spent, as dispatch reports them
// in a tool result's Meta.
func subagentUsage(meta map[string]any) (llm.Usage, bool) {
	switch u := meta["subagent_usage"].(type) {
	case llm.Usage:
		return u, true
	case *llm.Usage:
		if u != nil {
			return *u, true
		}
	}
	return llm.Usage{}, false
}

// addSubagentUsage adds a subagent's tokens to the turn and session totals.
// Its cost comes from Meta["subagent_cost_usd"] when the dispatcher reports
// one. Otherwise it is estimated at the main model's prices, because a
// subagent may run a different model.
func (a *Agent) addSubagentUsage(meta map[string]any) {
	u, ok := subagentUsage(meta)
	if !ok {
		return
	}
	cost, known := meta["subagent_cost_usd"].(float64)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.total.Add(u)
	if known {
		a.cost += cost
	} else {
		a.cost += a.model.Info.Cost(u)
	}
}

func (a *Agent) toolNames() []string {
	var n []string
	active := a.offeredNames()
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
	used, window, at, n := a.lastIn, a.model.Info.ContextWindow, a.opts.CompactAt, len(a.msgs)
	reset := a.opts.CompactReset
	if window > 0 && float64(used) < reset*float64(window) {
		a.compactLatched = false
	}
	latched := a.compactLatched
	a.mu.Unlock()
	if a.opts.Compactor == nil || at < 0 || window <= 0 || float64(used) < at*float64(window) || n < minCompactMessages || latched {
		return nil
	}
	a.mu.Lock()
	a.compactLatched = true
	a.mu.Unlock()
	return a.Compact(ctx, fmt.Sprintf("context %d%% full", used*100/window))
}

// Compact summarises older history now.
func (a *Agent) Compact(ctx context.Context, reason string) error {
	if a.opts.Compactor == nil {
		return errors.New("compaction is not configured")
	}
	if h, ok := a.opts.Hooks.(compactObserver); ok {
		h.PreCompact(context.WithoutCancel(ctx))
	}
	model := a.CurrentModel().Ref // the compaction log shows which model did the work
	a.emit(Event{Kind: EvCompactStart, Text: reason, Meta: map[string]any{"model": model}})
	before := a.Messages()
	kept, summary, err := a.opts.Compactor.Compact(ctx, before)
	if err != nil {
		// compact_start must always be closed, or the UI keeps its
		// "compacting" spinner; skipped tells it nothing was replaced.
		done := Event{Kind: EvCompactDone, Meta: map[string]any{"skipped": true, "model": model, "messages_before": len(before), "messages_after": len(before)}}
		if errors.Is(err, ErrNotEnoughHistory) {
			// Nothing is wrong: there is simply too little to summarise yet.
			a.emit(done)
			a.emit(Event{Kind: EvNotice, Text: "Nothing to compact yet: the history is too short."})
			return err
		}
		a.emit(done)
		a.emit(Event{Kind: EvWarning, Text: "compaction failed: " + err.Error()})
		return err
	}
	a.mu.Lock()
	a.msgs = kept
	a.lastIn = 0
	a.gen++
	a.mu.Unlock()
	a.persist(true)
	if h, ok := a.opts.Hooks.(postCompactObserver); ok {
		h.PostCompact(context.WithoutCancel(ctx))
	}
	a.emit(Event{Kind: EvCompactDone, Text: summary, Meta: map[string]any{"model": model, "messages_before": len(before), "messages_after": len(kept)}})
	// The context bar should drop now, not at the next model call.
	used, _ := a.ContextEstimate()
	a.emit(Event{Kind: EvContext, ContextUsed: used, ContextWindow: a.CurrentModel().Info.ContextWindow, Estimated: true})
	return nil
}

// OfferedForTest reports which tool names the next request would carry.
// It exists for tests in other packages that check deferred loading.
func (a *Agent) OfferedForTest() map[string]bool {
	names := a.offeredNames()
	if names == nil {
		names = map[string]bool{}
		for _, n := range a.opts.Tools.Names() {
			names[n] = true
		}
	}
	return names
}

// restrictedNext is what a refused call in a read-only mode should do next.
// Only plan mode ends in the plan tool; telling an architect or socratic
// session to "call the plan tool" sent users to the wrong mode.
func restrictedNext(mode string) string {
	switch mode {
	case "plan":
		return "Investigate with read-only tools, then call the plan tool with your plan."
	case "architect":
		return "Investigate with read-only tools, then answer with the design: boundaries, the recommendation, alternatives and risks."
	case "socratic":
		return "Investigate with read-only tools, then present the decisions that matter as options and let the user choose."
	case "challenge":
		return "Investigate with read-only tools, then give your strongest objections and better alternatives."
	}
	return "Investigate with read-only tools, then present your findings."
}

// afterToolResults reports whether the message before the newest assistant
// message holds tool results, i.e. the model replied to tool output.
func (a *Agent) afterToolResults() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := len(a.msgs)
	return n >= 2 && a.msgs[n-2].Role == llm.RoleTool
}
