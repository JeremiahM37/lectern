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

	// cancels stops one running subagent (CancelAgent); stopped records the
	// ids the user cancelled, so a subagent that has not started yet is not run.
	cancels map[string]context.CancelFunc
	stopped map[string]bool
	// waits cancels the wait of a task blocked on its depends_on tasks, so
	// /cancel frees it without waiting for those tasks to finish.
	waits map[string]context.CancelFunc
}

// setWait records the cancel of task id's dependency wait.
func (b *board) setWait(id string, cancel context.CancelFunc) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.waits == nil {
		b.waits = map[string]context.CancelFunc{}
	}
	b.waits[id] = cancel
}

// clearWait forgets task id's dependency wait once it has ended.
func (b *board) clearWait(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.waits, id)
}

// errCancelledByUser is the error of a subagent stopped with /cancel.
var errCancelledByUser = errors.New("cancelled by the user")

// errDispatchCancelled marks a subagent stopped because an agent of the same
// dispatch called cancel_dispatch.
var errDispatchCancelled = errors.New("cancelled: an agent of this dispatch called cancel_dispatch")

// register records the cancel function of a subagent that is about to run.
// It reports false when the user already cancelled it.
func (b *board) register(id string, cancel context.CancelFunc) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped[id] {
		return false
	}
	if b.cancels == nil {
		b.cancels = map[string]context.CancelFunc{}
	}
	b.cancels[id] = cancel
	return true
}

// unregister forgets a subagent that has finished.
func (b *board) unregister(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.cancels, id)
}

// wasStopped reports whether the user cancelled subagent id.
func (b *board) wasStopped(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stopped[id]
}

// cancelAgent stops subagent id of this dispatch. A running one is cancelled
// through its context and marks itself cancelled when it returns; a pending one
// is marked cancelled now and is skipped when it would start.
func (b *board) cancelAgent(id string) error {
	b.mu.Lock()
	idx := -1
	for i, info := range b.infos {
		if info.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		b.mu.Unlock()
		return fmt.Errorf("no subagent %s in the newest dispatch", id)
	}
	status := b.infos[idx].Status
	if status != "running" && status != "pending" {
		b.mu.Unlock()
		return fmt.Errorf("subagent %s has already %s", id, status)
	}
	if b.stopped == nil {
		b.stopped = map[string]bool{}
	}
	b.stopped[id] = true
	cancel := b.cancels[id]
	if cancel == nil {
		cancel = b.waits[id]
	}
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if status == "pending" {
		b.update(idx, func(a *AgentInfo) { a.Status, a.Error = "cancelled", errCancelledByUser.Error() })
	}
	return nil
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
	// bus is shared by the subagents of this dispatch: their findings and,
	// when the dispatch_cache feature is on, their reads.
	bus *AgentBus
	// locks is the per-path edit lock the subagents' edit tools take
	// (editlock_w40.go); it is shared by every subagent of the dispatch.
	locks *editLocks

	// cancel aborts every subagent of the dispatch when the breaker opens.
	cancel context.CancelFunc

	// cache counts the dispatch cache's outcomes for the Cache section.
	cache cacheTally

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
		bus:       NewAgentBus(),
		locks: newEditLocks(func(msg string) {
			if d.Sink != nil {
				d.Sink(Event{Kind: EvError, At: time.Now(), Category: "edit_lock", Text: msg})
			}
		}),
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
	if r.bus.Aborted() {
		return errDispatchCancelled
	}
	return ctx.Err()
}

// mapStop turns a context cancellation into the reason the dispatch stopped:
// the breaker, or a cancel_dispatch call. Other errors pass through.
func (r *dispatchRun) mapStop(err error) error {
	if !errors.Is(err, context.Canceled) {
		return err
	}
	if r.tripped() {
		return errBreakerTripped
	}
	if r.bus.Aborted() {
		return errDispatchCancelled
	}
	return err
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
	attempts int             // provider attempts: each run, plus its in-run retries
	noDone   bool            // a code task that edited nothing
	notes    []string        // retries and other events, shown under the report
	reg      *tools.Registry // the registry the last code attempt ran with
}

// attempt is one subagent run.
type attempt struct {
	report   string
	err      error
	usage    llm.Usage
	cost     float64
	edited   []string
	read     []string
	reg      *tools.Registry
	attempts int // 1 when the subagent ran, plus its transient and stall retries
}

// attempt runs one fresh subagent on task and reports how it ended. Its
// status and progress go to the board under index idx.
func (r *dispatchRun) attempt(ctx context.Context, idx int, role string, purpose Purpose, task string) attempt {
	b := r.board
	id := b.id(idx)
	sctx, scancel := context.WithCancel(ctx)
	defer scancel()
	if !b.register(id, scancel) {
		b.update(idx, func(a *AgentInfo) { a.Status, a.Error = "cancelled", errCancelledByUser.Error() })
		return attempt{err: errCancelledByUser}
	}
	defer b.unregister(id)
	spec := SubagentSpec{
		Role: role, Purpose: purpose, ID: id,
		MaxRetries: r.d.MaxRetries, MaxStallRetries: r.d.MaxStallRetries,
		Bus: r.bus, ReadCache: r.d.features().DispatchCache,
	}
	sub := r.d.New(spec, func(e Event) {
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
	// Every edit this subagent makes takes the path's lock and is recorded
	// as its edit (spec 03 section 3.7). Applied here so it covers every
	// launch path: tasks, retries, cleanup, the fix pass and the reviewer.
	guardEdits(sub.opts.Tools, role, id, r.bus, r.locks)
	b.update(idx, func(a *AgentInfo) { a.Status, a.Error, a.Model = "running", "", sub.CurrentModel().Ref })
	stats, err := sub.Run(sctx, task, nil)
	stopped := b.wasStopped(id)
	// A dispatch cancelled through the bus stops every agent, and the one
	// that asked for it is not an error.
	dispatchStop := !stopped && err != nil && r.bus.Aborted()
	b.update(idx, func(a *AgentInfo) {
		a.Tokens = stats.Tokens
		a.Step = stats.Steps
		switch {
		case stopped:
			a.Status, a.Error = "cancelled", errCancelledByUser.Error()
		case dispatchStop:
			a.Status, a.Error = "cancelled", errDispatchCancelled.Error()
		case err != nil:
			a.Status, a.Error = "error", err.Error()
		default:
			a.Status = "done"
		}
	})
	if stopped {
		err = errCancelledByUser
	}
	if dispatchStop {
		err = errDispatchCancelled
	}
	report := SubagentReport(sub, err)
	if err != nil && report == "" {
		report = salvageReport(r.bus.Findings(), id, stats.FilesRead, stats.FilesEdited)
	}
	return attempt{
		report:   report,
		err:      err,
		usage:    stats.Tokens,
		cost:     stats.CostUSD,
		edited:   stats.FilesEdited,
		read:     stats.FilesRead,
		reg:      sub.opts.Tools,
		attempts: 1 + stats.Retries,
	}
}

// salvageReport is what a subagent that stopped with an error and wrote no
// answer still hands back: the findings it posted. The files it read and
// edited are listed by the task report itself. It is "" when the subagent
// did no work to salvage (spec 03 §3.6).
func salvageReport(findings []Finding, id string, read, edited []string) string {
	var mine []string
	for _, f := range findings {
		if f.Agent == id {
			mine = append(mine, formatFinding(f))
		}
	}
	switch {
	case len(mine) > 0:
		return "Agent failed but produced partial results. Findings it posted before it stopped:\n" + strings.Join(mine, "\n")
	case len(read) > 0 || len(edited) > 0:
		return "Agent failed but produced partial results: the files it read and edited are listed below."
	}
	return ""
}

// launch runs one attempt unless the circuit breaker is open, and counts
// its provider failure toward the breaker.
func (r *dispatchRun) launch(ctx context.Context, idx int, role string, purpose Purpose, task string) attempt {
	if r.tripped() {
		r.board.update(idx, func(a *AgentInfo) { a.Status, a.Error = "error", errBreakerOpen.Error() })
		return attempt{err: errBreakerOpen}
	}
	hooks := r.d.Hooks
	if hooks != nil {
		hooks.SubagentStart(ctx, role, task)
	}
	res := r.attempt(ctx, idx, role, purpose, task)
	if hooks != nil {
		errText := ""
		if res.err != nil {
			errText = res.err.Error()
		}
		hooks.SubagentStop(ctx, role, task, errText)
	}
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
func (r *dispatchRun) runTask(ctx context.Context, idx int, role string, purpose Purpose, task string) taskResult {
	res := r.runTaskOnce(ctx, idx, role, purpose, task)
	// A subagent that stopped after writing something returns it, marked as
	// partial in its report, instead of failing the task (spec 03 §3.6).
	if res.err != nil && SalvageSucceeds(res.err, res.report) {
		res.notes = append(res.notes, "stopped early: "+res.err.Error())
		res.err = nil
	}
	if role == "code" && res.err == nil && len(res.edited) == 0 {
		res.noDone = true
	}
	return res
}

// runTaskOnce runs one task. A code task gets one retry, with a note saying why,
// when it finished without editing any file or stopped on a retryable
// provider error. Explore and web tasks are not retried. A code task that
// still edited nothing is marked no-done.
func (r *dispatchRun) runTaskOnce(ctx context.Context, idx int, role string, purpose Purpose, task string) taskResult {
	first := r.launch(ctx, idx, role, purpose, task)
	res := taskResult{report: first.report, err: first.err, edited: first.edited, read: first.read, reg: first.reg}
	res.attempts = first.attempts
	res.err = r.mapStop(res.err)
	if role != "code" || ctx.Err() != nil || r.tripped() || r.bus.Aborted() {
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
	second := r.launch(ctx, idx, role, purpose, note)
	if errors.Is(second.err, errBreakerOpen) {
		res.notes = append(res.notes, "retry skipped: "+errBreakerOpen.Error())
		return res
	}
	res.attempts += second.attempts
	res.notes = append(res.notes, "retried once: "+why)
	res.report, res.err = second.report, second.err
	res.edited = sortedUnique(append(append([]string(nil), first.edited...), second.edited...))
	res.read = sortedUnique(append(append([]string(nil), first.read...), second.read...))
	if second.reg != nil {
		res.reg = second.reg
	}
	res.err = r.mapStop(res.err)
	return res
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

// awaitDeps blocks task idx until its dependencies deps have finished. It
// returns nil when they all succeeded. Otherwise it returns why the task
// will not run: a failed dependency, a /cancel of this task, the caller's
// or the breaker's stop, or a wait longer than wait.
func (r *dispatchRun) awaitDeps(ctx context.Context, idx int, deps []int, done []chan struct{}, results []taskResult, ids []string, wait time.Duration) error {
	if len(deps) == 0 {
		return nil
	}
	id := r.board.id(idx)
	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	r.board.setWait(id, cancel)
	defer r.board.clearWait(id)
	for _, dep := range deps {
		select {
		case <-done[dep]:
		case <-wctx.Done():
			switch {
			case ctx.Err() != nil:
				return r.stopErr(ctx)
			case r.board.wasStopped(id):
				return errCancelledByUser
			default:
				return fmt.Errorf("gave up after %s waiting for %s", wait.Round(time.Second), ids[dep])
			}
		}
		if results[dep].err != nil {
			return fmt.Errorf("dependency %s failed", ids[dep])
		}
	}
	return nil
}
