package plugins

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/ciloop"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Event is what a hook reads on stdin.
type Event struct {
	Event     string  `json:"event"`
	Plugin    string  `json:"plugin"`
	Time      float64 `json:"time"`
	ProjectID int64   `json:"project_id,omitempty"`
	Data      any     `json:"data"`
}

// HookResult is what a hook prints: one JSON object. Output that is not JSON
// becomes Message (its first line), with OK from the exit status.
type HookResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Notify  string `json:"notify,omitempty"`
}

// Hooks turns Lectern's own events into plugin hook runs. It reads the
// board bus — task, session, approval and CI changes already flow through it
// as typed values — and polls for new usage-limit holds, which have no bus
// event of their own. It never blocks a publisher: the bus drops for a full
// subscriber, and each plugin's hooks run one at a time on its own queue.
type Hooks struct {
	M      *Manager
	Bus    *bus.Bus
	Reg    *executor.Registry
	Notify func(title, body, url string)
	// RunHost is the host command runner; tests replace it with a stand-in.
	RunHost func(ctx context.Context, dir string, argv, env []string, stdin []byte) (stdout []byte, err error)

	mu       sync.Mutex
	tasks    map[int64]string
	sessions map[int64]bool
	ended    map[int64]bool
	approved map[int64]string
	ci       map[string]bool
	lastHold int64
	started  float64
	queues   map[string]chan job
	wg       sync.WaitGroup
}

type job struct {
	hookIndex int
	event     Event
	projectID int64
}

// Start subscribes to the bus and begins watching for limit holds.
func (h *Hooks) Start(ctx context.Context) {
	h.tasks, h.sessions, h.ended = map[int64]string{}, map[int64]bool{}, map[int64]bool{}
	h.approved, h.ci, h.queues = map[int64]string{}, map[string]bool{}, map[string]chan job{}
	h.started = store.Now()
	if holds, err := h.M.DB.RecentLimitHolds(1); err == nil && len(holds) > 0 {
		h.lastHold = holds[0].ID
	}
	if h.RunHost == nil {
		h.RunHost = runHostCommand
	}
	if h.Bus == nil {
		return
	}
	ch := h.Bus.Subscribe("board")
	go func() {
		defer h.Bus.Unsubscribe("board", ch)
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				h.Observe(msg.Event, msg.Data)
			case <-tick.C:
				h.pollLimits()
			}
		}
	}()
}

// Observe maps one bus message to lifecycle events. Exposed so tests can
// drive it without a bus.
func (h *Hooks) Observe(kind string, data any) {
	switch v := data.(type) {
	case *store.Task:
		if v == nil {
			return
		}
		h.mu.Lock()
		prev, seen := h.tasks[v.ID]
		h.tasks[v.ID] = v.Status
		h.mu.Unlock()
		if !seen || prev == v.Status {
			return
		}
		switch {
		case v.Status == "running" && prev != "running":
			h.fire("task.dispatched", v.ProjectID, v)
		case prev == "running" && (v.Status == "review" || v.Status == "done" || v.Status == "failed"):
			h.fire("task.finished", v.ProjectID, v)
		}
	case *store.Session:
		if v == nil {
			return
		}
		pid := int64(0)
		if v.ProjectID != nil {
			pid = *v.ProjectID
		}
		h.mu.Lock()
		seen := h.sessions[v.ID]
		h.sessions[v.ID] = true
		dead := v.Status == "dead" || v.EndedAt != nil
		endedBefore := h.ended[v.ID]
		if dead {
			h.ended[v.ID] = true
		}
		h.mu.Unlock()
		// Only sessions that begin while Lectern is watching start: the first
		// publish after a restart is not a new session.
		if !seen && !dead && v.CreatedAt >= h.started {
			h.fire("session.start", pid, v)
		}
		if dead && !endedBefore && seen {
			h.fire("session.end", pid, v)
		}
	case *store.Approval:
		if v == nil {
			return
		}
		h.mu.Lock()
		prev, seen := h.approved[v.ID]
		h.approved[v.ID] = v.Status
		h.mu.Unlock()
		pid := h.approvalProject(v)
		if !seen && v.Status == "pending" {
			h.fire("approval.requested", pid, v)
		} else if v.Status != "pending" && prev != v.Status && (prev == "pending" || !seen) {
			h.fire("approval.decided", pid, v)
		}
	case *ciloop.View:
		if v == nil || v.State != ciloop.StateFailing && v.State != ciloop.StateCapped {
			return
		}
		key := fmt.Sprintf("%d/%d/%s", v.ID, v.Attempts, v.State)
		h.mu.Lock()
		seen := h.ci[key]
		h.ci[key] = true
		h.mu.Unlock()
		if !seen {
			h.fire("ci.failed", h.ownerProject(v.TaskID, v.SessionID), v)
		}
	}
}

func (h *Hooks) pollLimits() {
	holds, err := h.M.DB.LimitHoldsAfter(h.lastHold)
	if err != nil {
		return
	}
	for _, hold := range holds {
		h.lastHold = hold.ID
		h.fire("limit.hit", h.ownerProject(hold.TaskID, hold.SessionID), hold)
	}
}

func (h *Hooks) approvalProject(a *store.Approval) int64 {
	if a.TaskID != 0 {
		id := a.TaskID
		return h.ownerProject(&id, nil)
	}
	if a.SessionID != 0 {
		id := a.SessionID
		return h.ownerProject(nil, &id)
	}
	return 0
}

func (h *Hooks) ownerProject(taskID, sessionID *int64) int64 {
	if taskID != nil {
		if t, err := h.M.DB.Task(*taskID); err == nil {
			return t.ProjectID
		}
	}
	if sessionID != nil {
		if s, err := h.M.DB.Session(*sessionID); err == nil && s.ProjectID != nil {
			return *s.ProjectID
		}
	}
	return 0
}

// fire queues every matching hook of every active plugin in scope.
func (h *Hooks) fire(event string, projectID int64, data any) {
	for _, p := range h.M.ActiveFor(0) {
		if projectID != 0 && !p.InScope(projectID) || projectID == 0 && len(p.ProjectIDs) > 0 {
			continue
		}
		for i, hook := range p.Manifest.Contributes.Hooks {
			if hook.Event != event {
				continue
			}
			ev := Event{Event: event, Plugin: p.ID, Time: store.Now(), ProjectID: projectID, Data: data}
			h.enqueue(p.ID, job{hookIndex: i, event: ev, projectID: projectID})
		}
	}
}

func (h *Hooks) enqueue(pluginID string, j job) {
	h.mu.Lock()
	q := h.queues[pluginID]
	if q == nil {
		q = make(chan job, 64)
		h.queues[pluginID] = q
		go func() {
			for j := range q {
				h.run(pluginID, j)
				h.wg.Done()
			}
		}()
	}
	h.mu.Unlock()
	h.wg.Add(1)
	select {
	case q <- j:
	default:
		h.wg.Done()
		h.M.Log.Warn("plugins: hook queue full, event dropped", "plugin", pluginID, "event", j.event.Event)
	}
}

// Wait blocks until every queued hook has run (tests).
func (h *Hooks) Wait() { h.wg.Wait() }

// run executes one hook, after checking again that the plugin is active and
// its files are the consented ones: a plugin disabled or modified after the
// event was queued does not run.
func (h *Hooks) run(pluginID string, j job) {
	p, ok := h.M.Get(pluginID)
	if !ok || !p.Active() || j.hookIndex >= len(p.Manifest.Contributes.Hooks) || !h.M.Verify(p) {
		return
	}
	hook := p.Manifest.Contributes.Hooks[j.hookIndex]
	timeout := time.Duration(hook.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	stdin, _ := json.Marshal(j.event)
	started := time.Now()
	rec := &store.PluginHookRun{PluginID: p.ID, Event: hook.Event, Run: hook.Run, StartedAt: store.Now()}
	if j.projectID != 0 {
		pid := j.projectID
		rec.ProjectID = &pid
	}
	var out []byte
	var err error
	secrets := p.secrets()
	if hook.Run == "host" {
		root := h.M.storePath(p.Row.ContentHash)
		if p.Bundled {
			err = fmt.Errorf("bundled plugins have no host hooks")
		} else {
			env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"),
				"LECTERN_PLUGIN_ROOT=" + root, "LECTERN_EVENT=" + hook.Event}
			for _, k := range sortedKeys(secrets) {
				env = append(env, k+"="+secrets[k])
			}
			argv := append([]string{}, hook.Command...)
			if strings.Contains(argv[0], "/") {
				argv[0] = filepath.Join(root, filepath.FromSlash(argv[0]))
			}
			out, err = h.RunHost(ctx, root, argv, env, stdin)
		}
	} else {
		out, err = h.runOnTarget(ctx, p, hook.Command, j.projectID, hook.Event, secrets, stdin, timeout)
	}
	rec.DurationMS = time.Since(started).Milliseconds()
	res := parseResult(out, err)
	rec.OK, rec.Message = res.OK, res.Message
	if err != nil {
		rec.Error = trim(err.Error(), 500)
	}
	if err := h.M.DB.InsertPluginHookRun(rec); err != nil {
		h.M.Log.Warn("plugins: could not record hook run", "plugin", p.ID, "err", err)
	}
	if res.Notify != "" && p.Manifest.Capabilities.Notify && h.Notify != nil {
		h.Notify(p.Manifest.Name, trim(res.Notify, 300), "/#settings/plugins")
	}
}

func (h *Hooks) runOnTarget(ctx context.Context, p *Plugin, argv []string, projectID int64, event string,
	secrets map[string]string, stdin []byte, timeout time.Duration) ([]byte, error) {
	if projectID == 0 {
		return nil, fmt.Errorf("event has no project, so there is no machine to run on")
	}
	proj, err := h.M.DB.Project(projectID)
	if err != nil {
		return nil, err
	}
	target, err := h.M.DB.Target(proj.TargetID)
	if err != nil {
		return nil, err
	}
	if h.Reg == nil {
		return nil, fmt.Errorf("no executor registry")
	}
	ex, err := h.Reg.For(target)
	if err != nil {
		return nil, err
	}
	root, err := h.M.StageOnTarget(ctx, ex, target.ID, p)
	if err != nil {
		return nil, err
	}
	args := append([]string{}, argv...)
	if strings.Contains(args[0], "/") {
		args[0] = root + "/" + args[0]
	}
	var cmd strings.Builder
	cmd.WriteString("cd " + shellq.Quote(proj.RepoPath) + " && printf %s " + shellq.Quote(base64.StdEncoding.EncodeToString(stdin)) + " | base64 -d | env")
	cmd.WriteString(" LECTERN_PLUGIN_ROOT=" + shellq.Quote(root) + " LECTERN_EVENT=" + shellq.Quote(event))
	for _, k := range sortedKeys(secrets) {
		cmd.WriteString(" " + k + "=" + shellq.Quote(secrets[k]))
	}
	for _, a := range args {
		cmd.WriteString(" " + shellq.Quote(a))
	}
	r, err := ex.Run(ctx, cmd.String(), executor.RunOpts{Timeout: timeout.Seconds()})
	if err != nil {
		return nil, err
	}
	if !r.OK() {
		return []byte(r.Stdout), fmt.Errorf("exit %d: %s", r.RC, trim(strings.TrimSpace(r.Stderr), 300))
	}
	return []byte(r.Stdout), nil
}

// runHostCommand runs argv with exactly env, in dir, feeding stdin.
func runHostCommand(ctx context.Context, dir string, argv, env []string, stdin []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env, cmd.Stdin = dir, env, bytes.NewReader(stdin)
	var stdout, stderr limitedBuffer
	stdout.max, stderr.max = 64<<10, 8<<10
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return stdout.Bytes(), fmt.Errorf("timed out")
	}
	if err != nil {
		return stdout.Bytes(), fmt.Errorf("%v: %s", err, trim(strings.TrimSpace(stderr.String()), 300))
	}
	return stdout.Bytes(), nil
}

type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		if len(p) > room {
			b.Buffer.Write(p[:room])
		} else {
			b.Buffer.Write(p)
		}
	}
	return len(p), nil
}

func parseResult(out []byte, runErr error) HookResult {
	text := strings.TrimSpace(string(out))
	var res HookResult
	if strings.HasPrefix(text, "{") && json.Unmarshal([]byte(text), &res) == nil {
		if runErr != nil {
			res.OK = false
		}
		res.Message = trim(res.Message, 500)
		return res
	}
	first, _, _ := strings.Cut(text, "\n")
	return HookResult{OK: runErr == nil, Message: trim(first, 500)}
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
