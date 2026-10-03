package mods

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
)

// fnKey marks a function that left its runtime: a Button's onPress travels
// through other mods and Go as {"$fn": "plugin/mod#n"} and is called back in
// the runtime that made it.
const fnKey = "$fn"

// maxFns bounds the functions a runtime keeps for tokens it handed out. Every
// render makes new ones; the oldest go first.
const maxFns = 4096

var promiseType = reflect.TypeOf((*goja.Promise)(nil))

var (
	errTimeout = errors.New("took longer than its time limit")
	errNoMatch = errors.New("matcher did not match")
	errClosed  = errors.New("mod was unloaded")
)

type handler struct {
	matcher goja.Value // nil matches everything
	fn      goja.Callable
}

// runtime is one mod's sandbox and the loop that owns it.
type runtime struct {
	host      *Host
	mod       Mod
	vm        *goja.Runtime
	jobs      chan func()
	quit      chan struct{}
	closeOnce sync.Once
	busy      atomic.Bool

	// Owned by the loop goroutine.
	handlers []handler
	dollar   *goja.Object
	runs     map[string]goja.Callable
	fns      map[int]goja.Value
	nextFn   int

	mu       sync.Mutex
	events   []string // events[i] is handlers[i]'s event
	status   string
	commands []Command
	failures []time.Time
	paused   bool
	lastErr  string
	logs     []string
	state    map[string]any
}

func startRuntime(h *Host, m Mod) (*runtime, error) {
	r := &runtime{host: h, mod: m, vm: goja.New(), jobs: make(chan func(), 256), quit: make(chan struct{}),
		runs: map[string]goja.Callable{}, fns: map[int]goja.Value{}}
	r.vm.SetMaxCallStackSize(512)
	go r.loop()
	errc := make(chan error, 1)
	timer := time.AfterFunc(handlerTimeout, func() { r.vm.Interrupt(errTimeout) })
	defer timer.Stop()
	if !r.do(func() { errc <- r.boot() }) {
		return nil, errClosed
	}
	if err := <-errc; err != nil {
		r.close()
		return nil, err
	}
	return r, nil
}

// boot evaluates the script and calls register(on, options).
func (r *runtime) boot() error {
	r.dollar = r.makeDollar()
	console := r.vm.NewObject()
	_ = console.Set("log", r.logFunc())
	_ = console.Set("error", r.logFunc())
	_ = r.vm.Set("console", console)
	if _, err := r.vm.RunScript(r.mod.Key()+".js", r.mod.Script); err != nil {
		return fmt.Errorf("could not start: %w", jsErr(err))
	}
	register, ok := goja.AssertFunction(r.vm.Get("register"))
	if !ok {
		return errors.New("defines no register function")
	}
	on := r.vm.ToValue(func(c goja.FunctionCall) goja.Value {
		r.on(c)
		return goja.Undefined()
	})
	if _, err := register(goja.Undefined(), on, r.vm.NewObject()); err != nil {
		return fmt.Errorf("register failed: %w", jsErr(err))
	}
	return nil
}

func (r *runtime) loop() {
	for {
		select {
		case f := <-r.jobs:
			r.runJob(f)
		case <-r.quit:
			return
		}
	}
}

func (r *runtime) runJob(f func()) {
	// An interrupt aimed at a job that already finished must not hit this one.
	r.vm.ClearInterrupt()
	r.busy.Store(true)
	defer func() {
		r.busy.Store(false)
		if p := recover(); p != nil {
			r.recordFailure(fmt.Errorf("crashed: %v", p))
		}
	}()
	f()
}

// do queues f on the loop; false when the mod is unloaded. It must not be
// called from the loop itself.
func (r *runtime) do(f func()) bool {
	select {
	case <-r.quit:
		return false
	default:
	}
	select {
	case r.jobs <- f:
		return true
	case <-r.quit:
		return false
	}
}

func (r *runtime) close() {
	r.closeOnce.Do(func() {
		close(r.quit)
		r.vm.Interrupt(errClosed)
	})
}

func (r *runtime) name() string { return nameOf(r.mod) }

// on is the on(event, [matcher], handler) given to register.
func (r *runtime) on(c goja.FunctionCall) {
	event := c.Argument(0).String()
	fnArg, matcher := c.Argument(1), goja.Value(nil)
	if len(c.Arguments) >= 3 {
		matcher, fnArg = c.Argument(1), c.Argument(2)
		if goja.IsUndefined(matcher) || goja.IsNull(matcher) {
			matcher = nil
		} else if obj, ok := matcher.(*goja.Object); ok && obj.ClassName() != "RegExp" {
			panic(r.vm.NewTypeError("on(): the matcher must be a string or a RegExp"))
		} else if !ok {
			matcher = r.vm.ToValue(matcher.String())
		}
	}
	fn, ok := goja.AssertFunction(fnArg)
	if !ok {
		panic(r.vm.NewTypeError("on(): the handler must be a function"))
	}
	r.handlers = append(r.handlers, handler{matcher: matcher, fn: fn})
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *runtime) handlerIndexes(event string) []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paused {
		return nil
	}
	var out []int
	for i, e := range r.events {
		if e == event {
			out = append(out, i)
		}
	}
	return out
}

func (r *runtime) isPaused() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.paused
}

// subject is what a matcher is compared with for event.
func subject(event string, e map[string]any) (string, bool) {
	field := map[string]string{"ui.render": "component", "command.run": "id", "server.event": "type"}[event]
	if field == "" {
		return "", false
	}
	s, _ := e[field].(string)
	return s, true
}

func (r *runtime) matches(matcher goja.Value, s string) bool {
	obj, ok := matcher.(*goja.Object)
	if !ok {
		return matcher.String() == s
	}
	test, ok := goja.AssertFunction(obj.Get("test"))
	if !ok {
		return false
	}
	_ = obj.Set("lastIndex", 0)
	res, err := test(obj, r.vm.ToValue(s))
	return err == nil && res.ToBoolean()
}

// step is one call into the runtime: a handler, a command or a button press.
// Its time limit counts only the mod's own time; while the handler waits for
// next(e) the clock is stopped, since that time belongs to the mods after it.
type step struct {
	r    *runtime
	done chan struct{}

	mu        sync.Mutex
	finished  bool
	val       any
	err       error
	remaining time.Duration
	started   time.Time
	timer     *time.Timer

	next        func(map[string]any) (any, error)
	nextStarted bool
	nextDone    chan struct{}
	nextVal     any
	nextErr     error
}

func newStep(r *runtime, next func(map[string]any) (any, error)) *step {
	return &step{r: r, done: make(chan struct{}), next: next}
}

func (s *step) finish(v any, err error) {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	s.finished, s.val, s.err = true, v, err
	if s.timer != nil {
		s.timer.Stop()
	}
	s.mu.Unlock()
	close(s.done)
}

func (s *step) result() (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.val, s.err
}

func (s *step) arm(d time.Duration) {
	s.mu.Lock()
	s.remaining, s.started = d, time.Now()
	s.timer = time.AfterFunc(d, s.expire)
	s.mu.Unlock()
}

func (s *step) expire() {
	s.mu.Lock()
	finished := s.finished
	s.mu.Unlock()
	if finished {
		return
	}
	// A synchronous loop never yields; interrupt it so the runtime is usable
	// again. An async handler is simply abandoned.
	if s.r.busy.Load() {
		s.r.vm.Interrupt(errTimeout)
	}
	s.finish(nil, errTimeout)
}

func (s *step) pause() {
	s.mu.Lock()
	if s.timer != nil && s.timer.Stop() {
		s.remaining -= time.Since(s.started)
	}
	s.timer = nil
	s.mu.Unlock()
}

func (s *step) resume() {
	s.mu.Lock()
	if !s.finished && s.timer == nil {
		s.started = time.Now()
		s.timer = time.AfterFunc(max(0, s.remaining), s.expire)
	}
	s.mu.Unlock()
}

// startNext runs the rest of the chain once per step: a handler that calls
// next twice, or is skipped after calling it, never runs Lectern's default
// (sending a message, deciding an approval) a second time.
func (s *step) startNext(e map[string]any, cb func(any, error)) {
	s.mu.Lock()
	if s.nextStarted {
		ch := s.nextDone
		s.mu.Unlock()
		if cb != nil {
			go func() {
				<-ch
				cb(s.nextVal, s.nextErr)
			}()
		}
		return
	}
	s.nextStarted, s.nextDone = true, make(chan struct{})
	s.mu.Unlock()
	s.pause()
	go func() {
		v, err := s.next(e)
		s.nextVal, s.nextErr = v, err
		close(s.nextDone)
		s.resume()
		if cb != nil {
			cb(v, err)
		}
	}()
}

// skip is what a failed or non-matching handler amounts to: next(e), or the
// result of the next(e) it already started.
func (s *step) skip(e map[string]any) (any, error) {
	s.startNext(e, nil)
	<-s.nextDone
	return s.nextVal, s.nextErr
}

// start arms the step and queues build on the loop. build returns what the
// mod's function returned; a promise is followed until it settles.
func (r *runtime) start(budget time.Duration, s *step, build func() (goja.Value, error)) {
	s.arm(budget)
	if !r.do(func() {
		v, err := build()
		if err != nil {
			s.finish(nil, jsErr(err))
			return
		}
		r.settle(s, v)
	}) {
		s.finish(nil, errClosed)
	}
}

// settle runs on the loop.
func (r *runtime) settle(s *step, v goja.Value) {
	obj, ok := v.(*goja.Object)
	if !ok || obj.ExportType() != promiseType {
		s.finish(r.export(v), nil)
		return
	}
	p, ok := obj.Export().(*goja.Promise)
	if !ok {
		s.finish(r.export(v), nil)
		return
	}
	switch p.State() {
	case goja.PromiseStateFulfilled:
		s.finish(r.export(p.Result()), nil)
	case goja.PromiseStateRejected:
		s.finish(nil, valueErr(p.Result()))
	default:
		then, _ := goja.AssertFunction(obj.Get("then"))
		onFulfilled := r.vm.ToValue(func(c goja.FunctionCall) goja.Value {
			s.finish(r.export(c.Argument(0)), nil)
			return goja.Undefined()
		})
		onRejected := r.vm.ToValue(func(c goja.FunctionCall) goja.Value {
			s.finish(nil, valueErr(c.Argument(0)))
			return goja.Undefined()
		})
		if then != nil {
			_, _ = then(obj, onFulfilled, onRejected)
		}
	}
}

// invoke runs handler index for one event. A handler that throws or runs over
// is skipped as if it had called next(e), and the failure is recorded.
func (r *runtime) invoke(ctx context.Context, index int, event string, e map[string]any, next func(map[string]any) (any, error)) (any, error) {
	if r.isPaused() {
		return next(e)
	}
	budget, full := handlerTimeout, true
	if deadline, ok := ctx.Deadline(); ok {
		if rest := time.Until(deadline); rest < budget {
			budget, full = rest, false
		}
	}
	if budget <= 0 {
		return next(e)
	}
	s := newStep(r, next)
	r.start(budget, s, func() (goja.Value, error) {
		h := r.handlers[index]
		if h.matcher != nil {
			if subj, ok := subject(event, e); ok && !r.matches(h.matcher, subj) {
				return nil, errNoMatch
			}
		}
		nextFn := r.vm.ToValue(func(c goja.FunctionCall) goja.Value {
			ne := e
			if m, ok := r.export(c.Argument(0)).(map[string]any); ok {
				ne = m
			}
			p, resolve, reject := r.vm.NewPromise()
			s.startNext(ne, func(v any, err error) {
				r.do(func() {
					if err != nil {
						_ = reject(r.vm.NewGoError(err))
					} else {
						_ = resolve(r.toJS(v))
					}
				})
			})
			return r.vm.ToValue(p)
		})
		return h.fn(goja.Undefined(), r.dollar, r.toJS(e), nextFn)
	})
	select {
	case <-s.done:
	case <-ctx.Done():
		s.finish(nil, ctx.Err())
	}
	val, err := s.result()
	if err == nil {
		return val, nil
	}
	if errors.Is(err, errNoMatch) {
		return s.skip(e)
	}
	s.mu.Lock()
	calledNext := s.nextStarted
	s.mu.Unlock()
	val, nextErr := s.skip(e)
	// A handler that only passed on the chain's own error did not fail;
	// running out of the shared render budget is not the mod's fault either.
	blame := !(calledNext && nextErr != nil) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) &&
		(!errors.Is(err, errTimeout) || full)
	if blame {
		r.recordFailure(fmt.Errorf("%s handler: %w", event, err))
	}
	return val, nextErr
}

// call runs fn($, args…) outside any chain: a command or a button press.
func (r *runtime) call(ctx context.Context, budget time.Duration, what string, fn func() (goja.Callable, []goja.Value, error)) (any, error) {
	if r.isPaused() {
		return nil, errors.New(r.name() + " is paused")
	}
	s := newStep(r, nil)
	r.start(budget, s, func() (goja.Value, error) {
		f, args, err := fn()
		if err != nil {
			return nil, err
		}
		return f(goja.Undefined(), append([]goja.Value{r.dollar}, args...)...)
	})
	select {
	case <-s.done:
	case <-ctx.Done():
		s.finish(nil, ctx.Err())
	}
	val, err := s.result()
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			r.recordFailure(fmt.Errorf("%s: %w", what, err))
		}
		return nil, err
	}
	return val, nil
}

func (r *runtime) runCommand(ctx context.Context, id string, args map[string]any) (any, error) {
	return r.call(ctx, commandTimeout, "command "+id, func() (goja.Callable, []goja.Value, error) {
		run := r.runs[id]
		if run == nil {
			return nil, nil, fmt.Errorf("no command %q", id)
		}
		return run, []goja.Value{r.toJS(args)}, nil
	})
}

func (r *runtime) callToken(ctx context.Context, token string) (any, error) {
	return r.call(ctx, handlerTimeout, "button", func() (goja.Callable, []goja.Value, error) {
		fn, ok := goja.AssertFunction(r.fnFor(token))
		if !ok {
			return nil, nil, errors.New("this button is gone; it was drawn by an older render")
		}
		return fn, nil, nil
	})
}

func (r *runtime) recordFailure(err error) {
	now := time.Now()
	r.mu.Lock()
	recent := r.failures[:0]
	for _, t := range r.failures {
		if now.Sub(t) < failureWindow {
			recent = append(recent, t)
		}
	}
	r.failures = append(recent, now)
	r.lastErr = err.Error()
	r.appendLog("error: " + err.Error())
	pausedNow := !r.paused && len(r.failures) >= failureLimit
	if pausedNow {
		r.paused = true
	}
	r.mu.Unlock()
	if pausedNow {
		r.host.notify(Notice{Kind: NoticeToast, Mod: r.name(), Text: fmt.Sprintf("paused after %d failures in a minute (%s); restart the console to try again", failureLimit, err)})
	}
	r.host.notify(Notice{Kind: NoticeRender})
}

func (r *runtime) problems() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.paused:
		return []string{r.name() + ": paused after repeated failures; last: " + r.lastErr}
	case r.lastErr != "":
		return []string{r.name() + ": " + r.lastErr}
	}
	return nil
}

func (r *runtime) appendLog(line string) {
	if len(line) > 1000 {
		line = line[:1000] + "…"
	}
	r.logs = append(r.logs, line)
	if len(r.logs) > 100 {
		r.logs = r.logs[len(r.logs)-100:]
	}
}

func (r *runtime) logLines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.logs)
}

func (r *runtime) statusText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paused {
		return ""
	}
	return r.status
}

func (r *runtime) commandList() []Command {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paused {
		return nil
	}
	return slices.Clone(r.commands)
}

func (r *runtime) hasCommand(id string) bool {
	for _, c := range r.commandList() {
		if c.ID == id {
			return true
		}
	}
	return false
}

// registerFn keeps fn so a token can call it later. Loop only.
func (r *runtime) registerFn(fn goja.Value) string {
	n := r.nextFn
	r.nextFn++
	r.fns[n] = fn
	delete(r.fns, n-maxFns)
	return r.mod.Key() + "#" + strconv.Itoa(n)
}

// fnFor finds the function behind a token this runtime made. Loop only.
func (r *runtime) fnFor(token string) goja.Value {
	i := strings.LastIndex(token, "#")
	if i < 0 || token[:i] != r.mod.Key() {
		return nil
	}
	n, err := strconv.Atoi(token[i+1:])
	if err != nil {
		return nil
	}
	return r.fns[n]
}

// export copies a JS value out of the runtime as plain JSON-like Go values;
// functions become tokens. Loop only.
func (r *runtime) export(v goja.Value) any { return r.exportDepth(v, 0) }

func (r *runtime) exportDepth(v goja.Value, depth int) any {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) || depth > 32 {
		return nil
	}
	if _, ok := goja.AssertFunction(v); ok {
		return map[string]any{fnKey: r.registerFn(v)}
	}
	obj, ok := v.(*goja.Object)
	if !ok {
		switch x := v.Export().(type) {
		case int64:
			return float64(x)
		case float64:
			if math.IsNaN(x) || math.IsInf(x, 0) {
				return nil
			}
			return x
		case string, bool:
			return x
		default:
			return v.String()
		}
	}
	switch obj.ClassName() {
	case "Array":
		n := min(obj.Get("length").ToInteger(), 10000)
		out := make([]any, 0, n)
		for i := int64(0); i < n; i++ {
			out = append(out, r.exportDepth(obj.Get(strconv.FormatInt(i, 10)), depth+1))
		}
		return out
	case "RegExp", "Date":
		return obj.String()
	case "Error":
		return obj.Get("message").String()
	}
	if obj.ExportType() == promiseType {
		return nil
	}
	out := map[string]any{}
	for _, k := range obj.Keys() {
		out[k] = r.exportDepth(obj.Get(k), depth+1)
	}
	return out
}

// toJS builds native JS values from plain Go ones (not goja's wrappers, which
// behave differently under spread and push). A token this runtime made turns
// back into its function. Loop only.
func (r *runtime) toJS(v any) goja.Value {
	switch x := v.(type) {
	case nil:
		return goja.Null()
	case goja.Value:
		return x
	case map[string]any:
		if token, ok := fnToken(x); ok {
			if fn := r.fnFor(token); fn != nil {
				return fn
			}
		}
		obj := r.vm.NewObject()
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			_ = obj.Set(k, r.toJS(x[k]))
		}
		return obj
	case []any:
		items := make([]any, len(x))
		for i, item := range x {
			items[i] = r.toJS(item)
		}
		return r.vm.NewArray(items...)
	case string, bool, float64, int, int64:
		return r.vm.ToValue(x)
	}
	j, err := jsonValue(v)
	if err != nil {
		return goja.Undefined()
	}
	switch j.(type) {
	case map[string]any, []any, string, bool, float64, nil:
		return r.toJS(j)
	}
	return goja.Undefined()
}

// jsErr turns a goja error into one whose message is what the mod threw.
func jsErr(err error) error {
	var ex *goja.Exception
	if errors.As(err, &ex) {
		return valueErr(ex.Value())
	}
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		if e, ok := interrupted.Value().(error); ok {
			return e
		}
		return errTimeout
	}
	return err
}

func valueErr(v goja.Value) error {
	if obj, ok := v.(*goja.Object); ok {
		if msg := obj.Get("message"); msg != nil && !goja.IsUndefined(msg) {
			return errors.New(msg.String())
		}
	}
	if v == nil || goja.IsUndefined(v) {
		return errors.New("rejected")
	}
	return errors.New(v.String())
}

// makeDollar builds the $ every handler receives. Loop only.
func (r *runtime) makeDollar() *goja.Object {
	vm := r.vm
	fn := func(f func(goja.FunctionCall) goja.Value) goja.Value { return vm.ToValue(f) }
	undefined := goja.Undefined()
	d := vm.NewObject()
	_ = d.Set("surface", "cli")
	mod := vm.NewObject()
	_ = mod.Set("plugin", r.mod.Plugin)
	_ = mod.Set("id", r.mod.ID)
	_ = d.Set("mod", mod)

	ui := vm.NewObject()
	ctors := r.constructors()
	_ = ui.Set("resolve", fn(func(goja.FunctionCall) goja.Value { return ctors }))
	_ = ui.Set("toast", fn(func(c goja.FunctionCall) goja.Value {
		if text := oneLine(argString(c, 0), 500); text != "" {
			r.host.notify(Notice{Kind: NoticeToast, Mod: r.name(), Text: text})
		}
		return undefined
	}))
	_ = ui.Set("status", fn(func(c goja.FunctionCall) goja.Value {
		r.mu.Lock()
		r.status = oneLine(argString(c, 0), 200)
		r.mu.Unlock()
		r.host.notify(Notice{Kind: NoticeRender})
		return undefined
	}))
	_ = ui.Set("open", fn(func(c goja.FunctionCall) goja.Value {
		opts, _ := r.export(c.Argument(0)).(map[string]any)
		id, _ := opts["id"].(string)
		if id == "" {
			panic(vm.NewTypeError("$.ui.open needs {id, title}"))
		}
		title, _ := opts["title"].(string)
		r.host.openPane(Pane{Mod: r.mod.Key(), ID: id, Title: oneLine(firstNonEmpty(title, id), 120)})
		return undefined
	}))
	_ = ui.Set("close", fn(func(c goja.FunctionCall) goja.Value {
		r.host.ClosePane(Pane{Mod: r.mod.Key(), ID: argString(c, 0)})
		return undefined
	}))
	_ = ui.Set("render", fn(func(goja.FunctionCall) goja.Value {
		r.host.notify(Notice{Kind: NoticeRender})
		return undefined
	}))
	_ = d.Set("ui", ui)

	state := vm.NewObject()
	_ = state.Set("get", fn(func(c goja.FunctionCall) goja.Value {
		v := r.stateGet(argString(c, 0))
		if v == nil {
			return undefined
		}
		return r.toJS(v)
	}))
	_ = state.Set("set", fn(func(c goja.FunctionCall) goja.Value {
		if err := r.stateSet(argString(c, 0), r.export(c.Argument(1))); err != nil {
			panic(vm.NewGoError(err))
		}
		r.host.notify(Notice{Kind: NoticeRender})
		return undefined
	}))
	_ = d.Set("state", state)

	command := vm.NewObject()
	_ = command.Set("register", fn(func(c goja.FunctionCall) goja.Value {
		obj, ok := c.Argument(0).(*goja.Object)
		if !ok {
			panic(vm.NewTypeError("$.command.register needs {id, title, run}"))
		}
		id, title := obj.Get("id"), obj.Get("title")
		run, ok := goja.AssertFunction(obj.Get("run"))
		if !ok || id == nil || goja.IsUndefined(id) || id.String() == "" {
			panic(vm.NewTypeError("$.command.register needs {id, title, run}"))
		}
		cmd := Command{Mod: r.mod.Key(), Name: r.name(), ID: id.String(), Title: id.String()}
		if title != nil && !goja.IsUndefined(title) && title.String() != "" {
			cmd.Title = oneLine(title.String(), 120)
		}
		r.runs[cmd.ID] = run
		r.mu.Lock()
		r.commands = slices.DeleteFunc(r.commands, func(x Command) bool { return x.ID == cmd.ID })
		r.commands = append(r.commands, cmd)
		r.mu.Unlock()
		r.host.notify(Notice{Kind: NoticeRender})
		return undefined
	}))
	_ = d.Set("command", command)

	_ = d.Set("navigate", fn(func(goja.FunctionCall) goja.Value { return undefined }))

	api := vm.NewObject()
	for _, method := range []string{"GET", "POST", "PUT", "DELETE"} {
		_ = api.Set(strings.ToLower(method), fn(r.apiFunc(method)))
	}
	_ = d.Set("api", api)

	_ = d.Set("sleep", fn(func(c goja.FunctionCall) goja.Value {
		ms := min(max(c.Argument(0).ToInteger(), 0), 60_000)
		p, resolve, _ := vm.NewPromise()
		time.AfterFunc(time.Duration(ms)*time.Millisecond, func() {
			r.do(func() { _ = resolve(goja.Undefined()) })
		})
		return vm.ToValue(p)
	}))
	_ = d.Set("log", r.logFunc())
	return d
}

func (r *runtime) logFunc() goja.Value {
	return r.vm.ToValue(func(c goja.FunctionCall) goja.Value {
		parts := make([]string, 0, len(c.Arguments))
		for _, a := range c.Arguments {
			if obj, ok := a.(*goja.Object); ok && obj.ClassName() == "Object" {
				if b, err := json.Marshal(r.export(a)); err == nil {
					parts = append(parts, string(b))
					continue
				}
			}
			parts = append(parts, a.String())
		}
		r.mu.Lock()
		r.appendLog(strings.Join(parts, " "))
		r.mu.Unlock()
		return goja.Undefined()
	})
}

// constructors are the element constructors $.ui.resolve returns. Elements
// are plain objects: {type, props}.
func (r *runtime) constructors() *goja.Object {
	vm := r.vm
	out := vm.NewObject()
	for _, typ := range []string{"Box", "Text", "Badge", "Button", "Link"} {
		_ = out.Set(typ, vm.ToValue(func(c goja.FunctionCall) goja.Value {
			props, ok := c.Argument(0).(*goja.Object)
			if !ok {
				props = vm.NewObject()
				if arg := c.Argument(0); !goja.IsUndefined(arg) && !goja.IsNull(arg) {
					key := "text"
					if typ == "Button" || typ == "Link" {
						key = "label"
					}
					_ = props.Set(key, arg.String())
				}
			}
			el := vm.NewObject()
			_ = el.Set("type", typ)
			_ = el.Set("props", props)
			return el
		}))
	}
	return out
}

func (r *runtime) apiFunc(method string) func(goja.FunctionCall) goja.Value {
	return func(c goja.FunctionCall) goja.Value {
		vm := r.vm
		p, resolve, reject := vm.NewPromise()
		path := argString(c, 0)
		var body any
		if method != "GET" && len(c.Arguments) > 1 {
			body = r.export(c.Argument(1))
		}
		api := r.host.opts.API
		err := checkAPI(r.mod.API, method, path)
		if err == nil && api == nil {
			err = errors.New("the console has no API connection")
		}
		if err != nil {
			_ = reject(vm.NewGoError(err))
			return vm.ToValue(p)
		}
		go func() {
			data, err := api.JSON(method, path, body)
			var v any
			if err == nil && len(data) > 0 {
				if json.Unmarshal(data, &v) != nil {
					v = string(data)
				}
			}
			r.do(func() {
				if err != nil {
					_ = reject(r.vm.NewGoError(err))
				} else {
					_ = resolve(r.toJS(v))
				}
			})
		}()
		return vm.ToValue(p)
	}
}

func argString(c goja.FunctionCall, i int) string {
	v := c.Argument(i)
	if goja.IsUndefined(v) || goja.IsNull(v) {
		return ""
	}
	return v.String()
}

// oneLine keeps a mod's text to one printable line of at most n runes.
func oneLine(s string, n int) string {
	s = strings.Join(strings.FieldsFunc(s, func(c rune) bool { return c < ' ' || c == 0x7f || c == ' ' }), " ")
	if rs := []rune(s); len(rs) > n {
		s = string(rs[:n]) + "…"
	}
	return s
}
