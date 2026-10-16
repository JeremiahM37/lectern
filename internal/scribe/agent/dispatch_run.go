package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Step budgets for subagents (spec 03 §3.5 and §3.9). The app wiring passes
// them to the agents it builds.
const (
	ExploreMaxSteps = 12
	CodeMaxSteps    = 18
	WebMaxSteps     = 15
)

// breakerThreshold is the number of retryable provider failures within
// breakerWindow that opens the circuit breaker: the dispatch stops launching
// subagents and aborts the ones running (spec 03 §3.6).
const (
	breakerThreshold = 6
	breakerWindow    = 30 * time.Second
)

// errBreakerTripped is what a subagent that was running when the breaker
// opened reports.
var errBreakerTripped = errors.New("circuit breaker tripped: the provider kept failing, so this agent was stopped")

// defaultRetryWait is the pause before a code task's retry.
const defaultRetryWait = 2 * time.Second

var errBreakerOpen = fmt.Errorf("not started: %d provider failures in this dispatch opened the circuit breaker, so no more subagents are launched", breakerThreshold)

// retryNoteEdited is the focused retry note for a code task that finished
// without editing anything.
const retryNoteEdited = "the previous attempt finished without editing any file"

// board is the multi-agent display state of one dispatch. Each subagent,
// including retries and fix-ups, has an index on it.
type board struct {
	mu     sync.Mutex
	infos  []*AgentInfo
	sink   Sink
	callID string
	newID  func() string
}

// add registers a new subagent and announces it.
func (b *board) add(role, task string) int {
	b.mu.Lock()
	info := &AgentInfo{ID: b.newID(), Task: task, Role: role, Status: "pending"}
	b.infos = append(b.infos, info)
	idx := len(b.infos) - 1
	c := *info
	b.mu.Unlock()
	b.emit(c)
	return idx
}

// update changes subagent i and announces the new state.
func (b *board) update(i int, f func(*AgentInfo)) {
	b.mu.Lock()
	f(b.infos[i])
	c := *b.infos[i]
	b.mu.Unlock()
	b.emit(c)
}

func (b *board) id(i int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.infos[i].ID
}

func (b *board) emit(c AgentInfo) {
	if b.sink != nil {
		b.sink(Event{Kind: EvSubagent, At: time.Now(), Agent: &c, CallID: b.callID})
	}
}

// dispatchRun is the state shared by the tasks of one dispatch call: the
// board, the circuit breaker and the summed subagent usage.
type dispatchRun struct {
	d         *Dispatcher
	env       *tools.Env
	board     *board
	retryWait time.Duration

	// cancel aborts every subagent of the dispatch when the breaker opens.
	cancel context.CancelFunc

	mu       sync.Mutex
	failures int         // retryable provider failures, for the report
	recent   []time.Time // when the failures in the window happened
	open     bool        // the breaker has opened
	total    llm.Usage
	cost     float64
}

func newDispatchRun(d *Dispatcher, env *tools.Env) *dispatchRun {
	wait := d.retryWait
	if wait == 0 {
		wait = defaultRetryWait
	}
	return &dispatchRun{
		d:         d,
		env:       env,
		retryWait: wait,
		board: &board{
			sink:   d.Sink,
			callID: env.CallID,
			newID:  func() string { return fmt.Sprintf("a%d", d.seq.Add(1)) },
		},
	}
}

func (r *dispatchRun) tripped() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.open
}

// recordFailure counts a retryable provider failure. Six within the window
// open the breaker and cancel the dispatch's running subagents.
func (r *dispatchRun) recordFailure() {
	r.mu.Lock()
	r.failures++
	now := time.Now()
	r.recent = append(r.recent, now)
	kept := r.recent[:0]
	for _, t := range r.recent {
		if now.Sub(t) <= breakerWindow {
			kept = append(kept, t)
		}
	}
	r.recent = kept
	trip := !r.open && len(r.recent) >= breakerThreshold
	if trip {
		r.open = true
	}
	cancel := r.cancel
	r.mu.Unlock()
	if trip && cancel != nil {
		cancel()
	}
}

// stopErr is why a task that never got to run was stopped: the breaker, or
// the caller's cancellation.
func (r *dispatchRun) stopErr(ctx context.Context) error {
	if r.tripped() {
		return errBreakerOpen
	}
	return ctx.Err()
}

func (r *dispatchRun) failureCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failures
}

func (r *dispatchRun) usage() llm.Usage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total
}

// costUSD is the summed spend of the subagents, each priced at the model it ran on.
func (r *dispatchRun) costUSD() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cost
}

// taskResult is how one task of a dispatch ended.
type taskResult struct {
	report   string
	err      error
	edited   []string
	read     []string        // files the subagent read
	attempts int             // subagent runs that started
	noDone   bool            // a code task that edited nothing
	notes    []string        // retries and other events, shown under the report
	reg      *tools.Registry // the registry the last code attempt ran with
}

// attempt is one subagent run.
type attempt struct {
	report string
	err    error
	usage  llm.Usage
	cost   float64
	edited []string
	read   []string
	reg    *tools.Registry
}

// attempt runs one fresh subagent on task and reports how it ended. Its
// status and progress go to the board under index idx.
func (r *dispatchRun) attempt(ctx context.Context, idx int, role, task string) attempt {
	b := r.board
	id := b.id(idx)
	sub := r.d.New(role, id, func(e Event) {
		switch e.Kind {
		case EvStepFinish:
			b.update(idx, func(a *AgentInfo) { a.Step = e.Step; a.Tokens = e.Total })
		case EvToolRun:
			b.update(idx, func(a *AgentInfo) { a.Last = e.Tool + " " + argHint(e.Args) })
		case EvFallback:
			b.update(idx, func(a *AgentInfo) { a.Model = e.Model })
		}
		if r.env.Host != nil && e.Kind == EvToolRun {
			r.env.Host.Progress(r.env.CallID, fmt.Sprintf("[%s] %s %s", id, e.Tool, argHint(e.Args)))
		}
	})
	if sub == nil {
		return attempt{err: fmt.Errorf("could not start a %s agent", role)}
	}
	b.update(idx, func(a *AgentInfo) { a.Status, a.Error, a.Model = "running", "", sub.CurrentModel().Ref })
	stats, err := sub.Run(ctx, task, nil)
	b.update(idx, func(a *AgentInfo) {
		a.Tokens = stats.Tokens
		a.Step = stats.Steps
		if err != nil {
			a.Status, a.Error = "error", err.Error()
		} else {
			a.Status = "done"
		}
	})
	return attempt{
		report: lastAssistantText(sub.Messages()),
		err:    err,
		usage:  stats.Tokens,
		cost:   stats.CostUSD,
		edited: stats.FilesEdited,
		read:   stats.FilesRead,
		reg:    sub.opts.Tools,
	}
}

// launch runs one attempt unless the circuit breaker is open, and counts
// its provider failure toward the breaker.
func (r *dispatchRun) launch(ctx context.Context, idx int, role, task string) attempt {
	if r.tripped() {
		r.board.update(idx, func(a *AgentInfo) { a.Status, a.Error = "error", errBreakerOpen.Error() })
		return attempt{err: errBreakerOpen}
	}
	res := r.attempt(ctx, idx, role, task)
	r.mu.Lock()
	r.total.Add(res.usage)
	r.cost += res.cost
	r.mu.Unlock()
	if retryableFailure(ctx, res.err) {
		r.recordFailure()
	}
	return res
}

// runTask runs one task and marks a code task that edited nothing as
// no-done.
func (r *dispatchRun) runTask(ctx context.Context, idx int, role, task string) taskResult {
	res := r.runTaskOnce(ctx, idx, role, task)
	if role == "code" && res.err == nil && len(res.edited) == 0 {
		res.noDone = true
	}
	return res
}

// runTaskOnce runs one task. A code task gets one retry, with a note saying why,
// when it finished without editing any file or stopped on a retryable
// provider error. Explore and web tasks are not retried. A code task that
// still edited nothing is marked no-done.
func (r *dispatchRun) runTaskOnce(ctx context.Context, idx int, role, task string) taskResult {
	first := r.launch(ctx, idx, role, task)
	res := taskResult{report: first.report, err: first.err, edited: first.edited, read: first.read, reg: first.reg}
	res.attempts = startedAttempts(first.err)
	if r.tripped() && errors.Is(res.err, context.Canceled) {
		res.err = errBreakerTripped
	}
	if role != "code" || ctx.Err() != nil || r.tripped() {
		return res
	}
	var why string
	switch {
	case retryableFailure(ctx, first.err):
		why = "the previous attempt stopped on a provider error (" + first.err.Error() + ")"
	case first.err == nil && len(first.edited) == 0:
		why = retryNoteEdited
	default:
		return res
	}
	select {
	case <-time.After(r.retryWait):
	case <-ctx.Done():
		return res
	}
	note := task + "\n\nNote: " + why + ". Make the edits the task asks for now. If the change is already in place, say so and give the file:line that shows it."
	if len(first.read) > 0 {
		note += "\nYou already read these files, so do not read them again: " + strings.Join(first.read, ", ") + "."
	}
	second := r.launch(ctx, idx, role, note)
	if errors.Is(second.err, errBreakerOpen) {
		res.notes = append(res.notes, "retry skipped: "+errBreakerOpen.Error())
		return res
	}
	res.attempts += startedAttempts(second.err)
	res.notes = append(res.notes, "retried once: "+why)
	res.report, res.err = second.report, second.err
	res.edited = sortedUnique(append(append([]string(nil), first.edited...), second.edited...))
	res.read = sortedUnique(append(append([]string(nil), first.read...), second.read...))
	if second.reg != nil {
		res.reg = second.reg
	}
	if r.tripped() && errors.Is(res.err, context.Canceled) {
		res.err = errBreakerTripped
	}
	return res
}

// startedAttempts is 1 when a subagent ran, 0 when the breaker kept it from
// starting.
func startedAttempts(err error) int {
	if errors.Is(err, errBreakerOpen) {
		return 0
	}
	return 1
}

// providerFailure reports whether err came from the model provider (an HTTP
// error or a stream/network failure). Cancellation by the caller, a tripped
// breaker and max-steps stops do not count.
func providerFailure(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil || errors.Is(err, errBreakerOpen) {
		return false
	}
	var ae *llm.APIError
	if errors.As(err, &ae) {
		return true
	}
	_, retry := llm.Classify(err)
	return retry
}

// retryableFailure is a provider failure the provider itself marks as
// transient (rate limit, overload, network, stream).
func retryableFailure(ctx context.Context, err error) bool {
	if !providerFailure(ctx, err) {
		return false
	}
	_, retry := llm.Classify(err)
	return retry
}

// lastAssistantText returns the final non-empty assistant text.
func lastAssistantText(msgs []llm.Message) string {
	for k := len(msgs) - 1; k >= 0; k-- {
		if msgs[k].Role == llm.RoleAssistant && strings.TrimSpace(msgs[k].Text()) != "" {
			return msgs[k].Text()
		}
	}
	return ""
}
