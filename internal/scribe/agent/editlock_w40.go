package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// The edit lock (spec 03 section 3.7): one subagent edits a path at a time,
// and the others queue behind it in arrival order. A hold longer than
// editLockMaxHold is force-released with an error, so one stuck agent cannot
// stop the rest of the dispatch. The lock lives for one dispatch, next to its
// AgentBus, and every subagent of the dispatch shares it.

// editLockMaxHold is how long one agent may hold a path before it is
// force-released (spec 03 section 3.7: 180 seconds).
const editLockMaxHold = 180 * time.Second

// editTools are the subagent tools that write one file named by "path". Their
// edits take the path's lock and are recorded as edits by the agent.
var editTools = []string{"edit_file", "multi_edit", "write_file", "ast_edit"}

// editLocks is the per-path edit lock and the record of who edited what for
// one dispatch. The bus records ownership too (ClaimEdit), but it has no
// reader for the owner list, so check_edit_conflicts reads editors here.
type editLocks struct {
	mu      sync.Mutex
	paths   map[string]*pathLock
	editors map[string][]string // file -> agents that edited it, first first
	forced  []string            // one line per forced release, for the report
	// report hears each forced release as it happens. It may be nil.
	report  func(msg string)
	maxHold time.Duration
}

// pathLock is the lock on one file. depth lets the holder take it again
// (a second edit by the same agent) without queuing behind itself.
type pathLock struct {
	holder string
	depth  int
	since  time.Time
	queue  []*lockWaiter
}

// lockWaiter is an agent queued for a path. granted is set under editLocks.mu
// when the lock is handed to it; ready is closed at the same time.
type lockWaiter struct {
	id      string
	granted bool
	ready   chan struct{}
}

func newEditLocks(report func(string)) *editLocks {
	return &editLocks{
		paths:   map[string]*pathLock{},
		editors: map[string][]string{},
		report:  report,
		maxHold: editLockMaxHold,
	}
}

// acquire blocks until agent id holds the lock on file, or ctx ends. The
// returned func releases the lock; the caller must call it exactly once.
func (l *editLocks) acquire(ctx context.Context, file, id string) (func(), error) {
	l.mu.Lock()
	pl := l.lockFor(file)
	if pl.holder == "" || pl.holder == id {
		l.take(pl, id)
		l.mu.Unlock()
		return l.releaser(file, id), nil
	}
	w := &lockWaiter{id: id, ready: make(chan struct{})}
	pl.queue = append(pl.queue, w)
	l.mu.Unlock()

	for {
		// The timer is set for the current hold's limit, so a waiter wakes
		// when the holder has kept the path too long, not on a fixed poll.
		l.mu.Lock()
		if w.granted {
			l.mu.Unlock()
			return l.releaser(file, id), nil
		}
		since := pl.since
		wait := max(l.holdLimit()-time.Since(since), 0)
		l.mu.Unlock()

		timer := time.NewTimer(wait)
		select {
		case <-w.ready:
			timer.Stop()
		case <-timer.C:
			l.expire(file, since)
		case <-ctx.Done():
			timer.Stop()
			l.abandon(file, w, id)
			return nil, ctx.Err()
		}
	}
}

// Caller holds l.mu.
func (l *editLocks) holdLimit() time.Duration {
	if l.maxHold <= 0 {
		return editLockMaxHold
	}
	return l.maxHold
}

// lockFor returns the lock record of file, creating it. Caller holds l.mu.
func (l *editLocks) lockFor(file string) *pathLock {
	pl := l.paths[file]
	if pl == nil {
		pl = &pathLock{}
		l.paths[file] = pl
	}
	return pl
}

// take makes id the holder of pl, or takes one more level for the holder.
// Caller holds l.mu.
func (l *editLocks) take(pl *pathLock, id string) {
	if pl.holder == "" {
		pl.holder = id
		pl.since = time.Now()
	}
	pl.depth++
}

// releaser returns the release func for one acquire by id on file.
func (l *editLocks) releaser(file, id string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			pl := l.lockFor(file)
			if pl.holder != id {
				return // the lock was force-released while id held it
			}
			pl.depth--
			if pl.depth <= 0 {
				l.handOff(pl)
			}
		})
	}
}

// handOff gives pl to the first queued agent, or frees it. Caller holds l.mu.
func (l *editLocks) handOff(pl *pathLock) {
	pl.depth = 0
	if len(pl.queue) == 0 {
		pl.holder = ""
		return
	}
	next := pl.queue[0]
	pl.queue = pl.queue[1:]
	pl.holder = next.id
	pl.since = time.Now()
	pl.depth = 1
	next.granted = true
	close(next.ready)
}

// expire force-releases file when the hold that began at since is still in
// place after the limit. A hold that has already changed hands is left alone.
func (l *editLocks) expire(file string, since time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	pl := l.lockFor(file)
	if pl.holder == "" || !pl.since.Equal(since) {
		return
	}
	msg := fmt.Sprintf("edit lock on %s held by %s for more than %s: force released", file, pl.holder, l.holdLimit())
	l.forced = append(l.forced, msg)
	if l.report != nil {
		l.report(msg)
	}
	l.handOff(pl)
}

// abandon drops a waiter whose context ended. If the lock was handed to it
// in the same moment, it gives the lock on, so the path is not left held by
// an agent that is no longer editing.
func (l *editLocks) abandon(file string, w *lockWaiter, id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	pl := l.lockFor(file)
	if w.granted {
		if pl.holder == id {
			pl.depth--
			if pl.depth <= 0 {
				l.handOff(pl)
			}
		}
		return
	}
	for i, q := range pl.queue {
		if q == w {
			pl.queue = append(pl.queue[:i], pl.queue[i+1:]...)
			return
		}
	}
}

// recordEditor notes that agent id edited file. The first editor stays first.
func (l *editLocks) recordEditor(file, id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.editors[file] {
		if e == id {
			return
		}
	}
	l.editors[file] = append(l.editors[file], id)
}

// peerState reports, for agent id, the other agents that have edited file and
// the agent that holds its lock now when that is someone else ("" otherwise).
func (l *editLocks) peerState(file, id string) (editors []string, holder string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.editors[file] {
		if e != id {
			editors = append(editors, e)
		}
	}
	if pl := l.paths[file]; pl != nil && pl.holder != "" && pl.holder != id {
		holder = pl.holder
	}
	return editors, holder
}

// forcedReleases returns the forced-release messages, oldest first.
func (l *editLocks) forcedReleases() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.forced...)
}

// absPath is the path an edit tool writes, joined onto the workdir.
func absPath(workdir, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(workdir, p))
}

// editPath reads the "path" argument of an edit call, or "" when there is none.
func editPath(raw json.RawMessage) string {
	var a struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(raw, &a) != nil {
		return ""
	}
	return strings.TrimSpace(a.Path)
}

// editLockTool wraps an edit tool: it takes the path's lock, records the
// agent as an editor and the bus claim, then runs the edit. The lock is held
// for the one call, so the next agent edits the file on the current content.
type editLockTool struct {
	tools.Tool
	id    string
	bus   *AgentBus
	locks *editLocks
}

func (t editLockTool) Run(ctx context.Context, env *tools.Env, raw json.RawMessage) tools.Result {
	p := editPath(raw)
	if p == "" {
		return t.Tool.Run(ctx, env, raw) // the tool reports the missing path itself
	}
	workdir := ""
	if env != nil {
		workdir = env.Workdir
	}
	file := absPath(workdir, p)
	release, err := t.locks.acquire(ctx, file, t.id)
	if err != nil {
		return tools.Errorf("%s was not run: waiting for another agent to finish editing %s was cancelled (%v)", t.Spec().Name, p, err)
	}
	defer release()
	t.locks.recordEditor(file, t.id)
	t.bus.ClaimEdit(file, t.id)
	return t.Tool.Run(ctx, env, raw)
}

// checkEditConflictsTool is check_edit_conflicts (spec 03 section 3.4): code
// agents call it before they touch a file, to learn whether another agent has
// edited it or is editing it now.
type checkEditConflictsTool struct {
	id    string
	locks *editLocks
}

func (checkEditConflictsTool) ReadOnly() bool { return true }

func (checkEditConflictsTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name: "check_edit_conflicts",
		Description: "Check whether another subagent of this dispatch has edited a file, or is editing it now, before you change it. " +
			"Call it before you edit a file you did not own from the start. If another agent edited it, read the file again before you change it, " +
			"or report the change you need with report_finding instead.",
		Schema: tools.Schema(map[string]any{
			"path": tools.Str("The file to check, relative to the project root"),
		}, "path"),
	}
}

func (t checkEditConflictsTool) Run(_ context.Context, env *tools.Env, raw json.RawMessage) tools.Result {
	p := editPath(raw)
	if p == "" {
		return tools.Errorf("check_edit_conflicts needs the file to check: pass its path")
	}
	workdir := ""
	if env != nil {
		workdir = env.Workdir
	}
	editors, holder := t.locks.peerState(absPath(workdir, p), t.id)
	switch {
	case holder != "":
		return tools.Result{Output: fmt.Sprintf("%s is being edited by agent %s right now. Your edit will wait for it to finish. Do other work first, or report what you need with report_finding.", p, holder), Summary: "file held by " + holder}
	case len(editors) > 0:
		return tools.Result{Output: fmt.Sprintf("Agent %s already edited %s in this dispatch. Read the file again before you change it, and keep your edit to the part your task needs.", strings.Join(editors, ", "), p), Summary: "edited by " + strings.Join(editors, ", ")}
	}
	return tools.Result{Output: fmt.Sprintf("No other agent has edited %s in this dispatch. You may edit it.", p), Summary: "no conflict"}
}

// guardEdits puts the edit lock on the edit tools of one subagent and, for a
// code agent, registers check_edit_conflicts. It changes only the subagent's
// own registry, never a registry another agent shares.
func guardEdits(reg *tools.Registry, role, id string, bus *AgentBus, locks *editLocks) {
	if reg == nil || locks == nil {
		return
	}
	for _, name := range editTools {
		if t, ok := reg.Get(name); ok {
			reg.Register(editLockTool{Tool: t, id: id, bus: bus, locks: locks})
		}
	}
	if role == "code" {
		reg.Register(checkEditConflictsTool{id: id, locks: locks})
	}
}
