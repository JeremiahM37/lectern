// Package mods runs plugin mods (docs/mods.md) inside the terminal console.
//
// Every mod gets a goja runtime of its own, which is its sandbox: the runtime
// has no require, file system, timers or network, only the $ object. A goja
// runtime is not goroutine-safe, so each one is owned by a loop goroutine and
// everything that touches it — a handler call, a promise settling, a button
// press — is a job queued on that loop. The chain of one event crosses
// runtimes through Go: next(e) in one mod resolves on another goroutine and
// comes back to the caller's loop as a job.
package mods

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Mod is one entry of GET /api/plugins/mods?surface=cli. Script is already the
// classic script pluginpkg.ModScript makes of the module.
type Mod struct {
	Plugin string `json:"plugin"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	API    string `json:"api,omitempty"`
	Hash   string `json:"hash"`
	Script string `json:"script,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Key names a mod across reloads: plugin/id.
func (m Mod) Key() string { return m.Plugin + "/" + m.ID }

// API is the part of console.Client the mods' $.api uses. It is called as
// the person running the console.
type API interface {
	JSON(method, path string, body any) ([]byte, error)
}

// Options configure a Host.
type Options struct {
	API API
	// StateDir keeps each mod's $.state as JSON; empty keeps it in memory.
	StateDir string
	// Version is reported to app.start.
	Version string
}

// The limits docs/mods.md promises. They are variables so tests can shorten
// them.
var (
	handlerTimeout = 2 * time.Second
	renderBudget   = 100 * time.Millisecond
	commandTimeout = 10 * time.Second
	failureWindow  = time.Minute
	failureLimit   = 5
	stateLimit     = 256 << 10
)

// NoticeKind says what changed.
type NoticeKind int

const (
	// NoticeRender: something a mod draws may have changed; render again.
	NoticeRender NoticeKind = iota
	// NoticeToast: Text is a notification from Mod.
	NoticeToast
	// NoticePanes: a pane was opened or closed.
	NoticePanes
)

// Notice is something the console should react to between its own events.
type Notice struct {
	Kind NoticeKind
	Mod  string // the mod's display name
	Text string
}

// Command is a palette command a mod registered with $.command.register.
type Command struct {
	Mod   string // Mod.Key()
	Name  string // the mod's display name
	ID    string
	Title string
}

// Pane is a pane opened with $.ui.open.
type Pane struct {
	Mod   string
	ID    string
	Title string
}

// Host owns the runtimes of the loaded mods, in install order.
type Host struct {
	opts    Options
	notices chan Notice

	mu       sync.Mutex
	runtimes []*runtime
	panes    []Pane
	problems map[string]string // load errors by mod key
	viewport [2]int
}

// NewHost starts an empty host; Load gives it mods.
func NewHost(opts Options) *Host {
	return &Host{opts: opts, notices: make(chan Notice, 64), problems: map[string]string{}, viewport: [2]int{100, 30}}
}

// Notices delivers toasts and redraw requests. Sends never block: a console
// that falls behind loses redraw hints, not its own events.
func (h *Host) Notices() <-chan Notice { return h.notices }

func (h *Host) notify(n Notice) {
	select {
	case h.notices <- n:
	default:
	}
}

// SetViewport records the terminal size ui.render reports.
func (h *Host) SetViewport(width, height int) {
	h.mu.Lock()
	h.viewport = [2]int{width, height}
	h.mu.Unlock()
}

// Signature identifies a mod list, so a poller can tell whether anything
// changed without comparing the scripts.
func Signature(list []Mod) string {
	parts := make([]string, 0, len(list))
	for _, m := range list {
		parts = append(parts, m.Key()+"@"+m.Hash+"#"+m.API+"!"+m.Error+fmt.Sprint(len(m.Script)))
	}
	return strings.Join(parts, "\n")
}

// Load replaces the running mods with list. An unchanged mod keeps its
// runtime, state in memory, commands and panes; a new one is started and gets
// app.start. It reports whether anything changed.
func (h *Host) Load(list []Mod) bool {
	h.mu.Lock()
	old := map[string]*runtime{}
	for _, r := range h.runtimes {
		old[r.mod.Key()] = r
	}
	var keep, fresh []*runtime
	problems := map[string]string{}
	changed := false
	for _, m := range list {
		if m.Error != "" || m.Script == "" {
			problems[m.Key()] = nameOf(m) + ": " + firstNonEmpty(m.Error, "no code")
			continue
		}
		if r := old[m.Key()]; r != nil && r.mod == m {
			delete(old, m.Key())
			keep = append(keep, r)
			continue
		}
		changed = true
		r, err := startRuntime(h, m)
		if err != nil {
			problems[m.Key()] = nameOf(m) + ": " + err.Error()
			continue
		}
		keep = append(keep, r)
		fresh = append(fresh, r)
	}
	for key, r := range old {
		changed = true
		r.close()
		h.panes = slices.DeleteFunc(h.panes, func(p Pane) bool { return p.Mod == key })
	}
	if !maps(problems, h.problems) {
		changed = true
	}
	h.runtimes, h.problems = keep, problems
	version := h.opts.Version
	h.mu.Unlock()
	if len(fresh) > 0 {
		go func() {
			_, _ = h.dispatch(context.Background(), fresh, "app.start", map[string]any{"surface": "cli", "version": version}, nil)
			h.notify(Notice{Kind: NoticeRender})
		}()
	}
	return changed
}

func maps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func nameOf(m Mod) string {
	if m.Name != "" {
		return m.Name
	}
	return m.Key()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// Close stops every runtime.
func (h *Host) Close() {
	h.mu.Lock()
	list := h.runtimes
	h.runtimes, h.panes = nil, nil
	h.mu.Unlock()
	for _, r := range list {
		r.close()
	}
}

func (h *Host) snapshot() []*runtime {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.runtimes)
}

// Loaded reports how many mods are running.
func (h *Host) Loaded() int { return len(h.snapshot()) }

// Handles reports whether any running mod has a handler for event, so the
// console can skip work nobody listens to.
func (h *Host) Handles(event string) bool {
	for _, r := range h.snapshot() {
		if len(r.handlerIndexes(event)) > 0 {
			return true
		}
	}
	return false
}

// Dispatch runs event through every mod's handlers, in mod order, ending in
// def (Lectern's own behaviour; nil does nothing). It returns what the first
// handler returned, exported to plain Go values.
func (h *Host) Dispatch(ctx context.Context, event string, e map[string]any, def func(map[string]any) (any, error)) (any, error) {
	return h.dispatch(ctx, h.snapshot(), event, e, def)
}

type link struct {
	r     *runtime
	index int
}

func (h *Host) dispatch(ctx context.Context, list []*runtime, event string, e map[string]any, def func(map[string]any) (any, error)) (any, error) {
	var chain []link
	for _, r := range list {
		for _, i := range r.handlerIndexes(event) {
			chain = append(chain, link{r, i})
		}
	}
	var call func(i int, e map[string]any) (any, error)
	call = func(i int, e map[string]any) (any, error) {
		if i == len(chain) {
			if def == nil {
				return nil, nil
			}
			return def(e)
		}
		return chain[i].r.invoke(ctx, chain[i].index, event, e, func(next map[string]any) (any, error) { return call(i+1, next) })
	}
	return call(0, e)
}

// Denied reads a {deny: "reason"} answer.
func Denied(result any) (string, bool) {
	m, ok := result.(map[string]any)
	if !ok {
		return "", false
	}
	switch v := m["deny"].(type) {
	case string:
		if strings.TrimSpace(v) != "" {
			return v, true
		}
		return "blocked by a mod", true
	case bool:
		return "blocked by a mod", v
	}
	return "", false
}

// Rendered is one ui.render result.
type Rendered struct {
	Hidden bool
	Append []Element
}

func (h *Host) renderEvent(component string, props map[string]any) map[string]any {
	h.mu.Lock()
	vp := h.viewport
	h.mu.Unlock()
	if props == nil {
		props = map[string]any{}
	}
	return map[string]any{"component": component, "props": props, "surface": "cli", "viewport": map[string]any{"width": vp[0], "height": vp[1]}}
}

func renderDefault(map[string]any) (any, error) {
	return map[string]any{"hidden": false, "append": []any{}}, nil
}

// Render asks the mods how to draw component. It waits at most the render
// budget (100 ms); handlers still running then are drawn without.
func (h *Host) Render(component string, props map[string]any) (hidden bool, elems []Element) {
	r := h.RenderMany(component, []map[string]any{props})[0]
	return r.Hidden, r.Append
}

// RenderMany renders several instances of one component (every session row)
// under one shared budget, so a slow mod costs the console 100 ms per redraw,
// not 100 ms per row.
func (h *Host) RenderMany(component string, props []map[string]any) []Rendered {
	out := make([]Rendered, len(props))
	if !h.Handles("ui.render") {
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), renderBudget)
	defer cancel()
	var wg sync.WaitGroup
	for i := range props {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := h.Dispatch(ctx, "ui.render", h.renderEvent(component, props[i]), renderDefault)
			if err == nil {
				out[i] = h.rendered(res)
			}
		}(i)
	}
	wg.Wait()
	return out
}

func (h *Host) rendered(res any) Rendered {
	m, _ := res.(map[string]any)
	out := Rendered{Hidden: truthy(m["hidden"])}
	list, _ := m["append"].([]any)
	for _, v := range list {
		if e, ok := h.element(v, 0); ok {
			out.Append = append(out.Append, e)
		}
	}
	return out
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case int64:
		return x != 0
	case nil:
		return false
	}
	return true
}

// Commands lists the palette commands of the running mods, in mod order.
func (h *Host) Commands() []Command {
	var out []Command
	for _, r := range h.snapshot() {
		out = append(out, r.commandList()...)
	}
	return out
}

// RunCommand runs a mod command through command.run; the end of the chain
// calls the command's own run($, args).
func (h *Host) RunCommand(ctx context.Context, id string, args map[string]any) (any, error) {
	if args == nil {
		args = map[string]any{}
	}
	return h.Dispatch(ctx, "command.run", map[string]any{"id": id, "args": args}, func(e map[string]any) (any, error) {
		want, _ := e["id"].(string)
		a, _ := e["args"].(map[string]any)
		for _, r := range h.snapshot() {
			if r.hasCommand(want) {
				return r.runCommand(ctx, want, a)
			}
		}
		return nil, fmt.Errorf("no command %q", want)
	})
}

// Status is every mod's $.ui.status text, in mod order.
func (h *Host) Status() []string {
	var out []string
	for _, r := range h.snapshot() {
		if s := r.statusText(); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Problems lists what keeps mods from running: load errors, paused mods, and
// the most recent handler error of each mod.
func (h *Host) Problems() []string {
	h.mu.Lock()
	var out []string
	for _, p := range h.problems {
		out = append(out, p)
	}
	h.mu.Unlock()
	sort.Strings(out)
	for _, r := range h.snapshot() {
		out = append(out, r.problems()...)
	}
	return out
}

// Logs returns what a mod passed to $.log, newest last.
func (h *Host) Logs(key string) []string {
	for _, r := range h.snapshot() {
		if r.mod.Key() == key {
			return r.logLines()
		}
	}
	return nil
}

// OpenPanes lists the panes the mods opened, the most recent last.
func (h *Host) OpenPanes() []Pane {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.panes)
}

// ClosePane closes a pane as Esc does.
func (h *Host) ClosePane(p Pane) {
	h.mu.Lock()
	h.panes = slices.DeleteFunc(h.panes, func(q Pane) bool { return q.Mod == p.Mod && q.ID == p.ID })
	h.mu.Unlock()
	h.notify(Notice{Kind: NoticePanes})
}

func (h *Host) openPane(p Pane) {
	h.mu.Lock()
	h.panes = slices.DeleteFunc(h.panes, func(q Pane) bool { return q.Mod == p.Mod && q.ID == p.ID })
	h.panes = append(h.panes, p)
	h.mu.Unlock()
	h.notify(Notice{Kind: NoticePanes})
}

// RenderPane draws an open pane: the ui.render "pane" result's append.
func (h *Host) RenderPane(p Pane) []Element {
	_, elems := h.Render("pane", map[string]any{"id": p.ID})
	return elems
}

// jsonValue normalises a Go value to what encoding/json would decode it to,
// so values look the same whether they came from a mod or from the API.
func jsonValue(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	err = json.Unmarshal(b, &out)
	return out, err
}
