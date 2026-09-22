// Package scheduler promotes queued attempts, tails running ones and finalises
// results.
//
// It is restart-safe: all progress (log offset, state) lives in SQLite, and tmux
// sessions on targets survive a control-plane restart to be re-attached on the
// next tick.
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/internal/agents"
	"github.com/JeremiahM37/lectern/internal/broker"
	"github.com/JeremiahM37/lectern/internal/bus"
	"github.com/JeremiahM37/lectern/internal/config"
	"github.com/JeremiahM37/lectern/internal/creds"
	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/memory"
	"github.com/JeremiahM37/lectern/internal/sandbox"
	"github.com/JeremiahM37/lectern/internal/scratch"
	"github.com/JeremiahM37/lectern/internal/sinks"
	"github.com/JeremiahM37/lectern/internal/skills"
	"github.com/JeremiahM37/lectern/internal/state"
	"github.com/JeremiahM37/lectern/internal/store"
	"github.com/JeremiahM37/lectern/internal/worktree"
)

// AgentTaskFooter is appended to every non-reviewer prompt. Scope creep is the
// failure mode that wastes a dispatch; the board is where the extra work goes.
const AgentTaskFooter = `

---
lectern: if you discover out-of-scope work (bugs, refactors, follow-ups), do NOT
expand this task. File a card on the board instead:
  python3 .lectern/lec.py add-task "short title" "detailed prompt"            # → backlog
  python3 .lectern/lec.py add-task "short title" "detailed prompt" --dispatch # runs now
Stay focused on the task above.`

// HostLocalKinds are the target kinds whose agent process runs on the control
// plane host, as the control plane user — the only ones that inherit that user's
// MCP servers. 'sandbox' does host-side pct work locally but runs the agent
// inside the container, so it is deliberately out.
var HostLocalKinds = map[string]bool{"local": true, "mock": true}

// pollErrorLimit is how many consecutive executor errors an attempt survives
// before it is declared unreachable. At the default tick that is about a minute.
const pollErrorLimit = 30

// SessionPoller is the slice of the session manager the scheduler needs. Kept as
// an interface so the scheduler does not depend on the whole manager.
type SessionPoller interface {
	Poll(ctx context.Context)
}

// Scheduler drives every attempt from queued to finalised.
type Scheduler struct {
	Memory   memory.Provider
	DB       *store.DB
	Bus      *bus.Bus
	Broker   *broker.Broker
	Notifier *sinks.Notifier
	Reg      *executor.Registry
	Launcher agents.Launcher
	// AgentDefinitions resolves configured custom CLIs at queue time. Built-ins
	// remain in Launcher; custom definitions use the generic task adapter.
	AgentDefinitions func() map[string]agents.TaskDefinition
	Creds            *creds.Provisioner
	Cfg              *config.Config
	Log              *slog.Logger

	// Sessions is the interactive-session manager. The scheduler drives its poll
	// so there is ONE loop watching targets, not two competing for the same
	// SSH connections.
	Sessions SessionPoller

	// Routines fires any scheduled routine that is due. Injected rather than
	// implemented here because creating a task is the API layer's job, and a
	// routine is exactly a task someone saved.
	Routines func(context.Context)

	mu              sync.Mutex
	pollErrors      map[int64]int
	ghostStrikes    map[int64]int
	lastJanitor     float64
	lastSessionPoll float64

	cancel context.CancelFunc
	done   chan struct{}
}

// New builds a scheduler.
func New(db *store.DB, b *bus.Bus, br *broker.Broker, n *sinks.Notifier,
	reg *executor.Registry, cfg *config.Config, cr *creds.Provisioner, log *slog.Logger) *Scheduler {
	return &Scheduler{
		DB: db, Bus: b, Broker: br, Notifier: n, Reg: reg, Cfg: cfg, Creds: cr, Log: log,
		Launcher: agents.Launcher{ClaudeBin: cfg.ClaudeBin, CodexBin: cfg.CodexBin,
			GeminiBin: cfg.GeminiBin},
		pollErrors:   map[int64]int{},
		ghostStrikes: map[int64]int{},
	}
}

// Start runs the tick loop until Stop.
func (s *Scheduler) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		for {
			func() {
				defer func() {
					if r := recover(); r != nil {
						s.Log.Error("tick panicked", "panic", r)
					}
				}()
				s.Tick(ctx)
			}()
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.Cfg.TickInterval):
			}
		}
	}()
}

// Stop halts the loop and waits for the in-flight tick.
func (s *Scheduler) Stop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	<-s.done
}

// Scratch is the sweep over throwaway workspaces, configured as the server is.
func (s *Scheduler) Scratch() *scratch.Sweeper {
	trash := s.Cfg.ScratchTrashDays
	if trash <= 0 {
		trash = 14
	}
	return &scratch.Sweeper{DB: s.DB, Reg: s.Reg, Log: s.Log, Days: s.Cfg.ScratchDays, TrashDays: trash}
}

// Tick is one scheduling pass. Exported so tests can drive it deterministically.
func (s *Scheduler) Tick(ctx context.Context) {
	s.ProcessTakeovers(ctx)
	if s.Cfg.JanitorDays > 0 && store.Now()-s.lastJanitor > 3600 {
		s.lastJanitor = store.Now()
		if _, err := s.Janitor(ctx, s.Cfg.JanitorDays); err != nil {
			s.Log.Error("janitor sweep failed", "err", err)
		}
		// The scripted mock target has no filesystem to sweep.
		if s.Cfg.ScratchDays > 0 && !s.Cfg.Mock {
			if _, err := s.Scratch().Sweep(ctx, false, 0); err != nil {
				s.Log.Error("scratch sweep failed", "err", err)
			}
		}
	}
	if s.Sessions != nil && store.Now()-s.lastSessionPoll >= s.Cfg.SessionPoll.Seconds() {
		s.lastSessionPoll = store.Now()
		s.Sessions.Poll(ctx)
	}
	if s.Routines != nil {
		s.Routines(ctx)
	}
	s.DeliverMessages(ctx)
	s.promoteQueued(ctx)

	running, err := s.DB.AttemptsWhere("status='running' AND task_id NOT IN (SELECT task_id FROM task_takeovers)")
	if err != nil {
		s.Log.Error("listing running attempts failed", "err", err)
		return
	}
	for _, att := range running {
		err := s.poll(ctx, att)
		if err == nil {
			s.clearPollError(att.ID)
			continue
		}
		var exErr *executor.Error
		if !errors.As(err, &exErr) {
			// the task or attempt was deleted concurrently mid-poll — it is gone
			// from the next query, and aborting the tick here would stall every
			// other running attempt
			s.Log.Info("poll failed", "attempt", att.ID, "err", err)
			s.clearPollError(att.ID)
			continue
		}
		n := s.bumpPollError(att.ID)
		s.Log.Warn("poll error", "attempt", att.ID, "strike", n, "limit", pollErrorLimit, "err", err)
		if n >= pollErrorLimit {
			s.finalize(ctx, att, -1, "target unreachable: "+err.Error())
			if c, err := s.contextFor(att); err == nil {
				s.destroyIfSandbox(ctx, att, c)
			}
		}
	}
}

func (s *Scheduler) bumpPollError(id int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pollErrors[id]++
	return s.pollErrors[id]
}

func (s *Scheduler) clearPollError(id int64) {
	s.mu.Lock()
	delete(s.pollErrors, id)
	s.mu.Unlock()
}

// runCtx is the context for one target operation: it inherits cancellation from
// the scheduler but is never bounded here — the executors carry their own
// per-command timeouts.
type runCtx struct {
	Task    *store.Task
	Project *store.Project
	Target  *store.Target
}

func (s *Scheduler) contextFor(att *store.Attempt) (*runCtx, error) {
	task, err := s.DB.Task(att.TaskID)
	if err != nil {
		return nil, err
	}
	project, err := s.DB.Project(task.ProjectID)
	if err != nil {
		return nil, err
	}
	target, err := s.DB.Target(project.TargetID)
	if err != nil {
		return nil, err
	}
	return &runCtx{task, project, target}, nil
}

// attemptExecutor: sandbox attempts own a container; everyone else shares the
// target's executor.
func (s *Scheduler) attemptExecutor(att *store.Attempt, target *store.Target) (executor.Executor, error) {
	if target.Kind == "sandbox" && att.SandboxVMID != "" && !s.Cfg.Mock {
		return executor.NewPct(att.SandboxVMID), nil
	}
	return s.Reg.For(target)
}

// ---- queued -> running -------------------------------------------------------

func (s *Scheduler) promoteQueued(ctx context.Context) {
	rows, err := s.DB.Query(`SELECT a.id FROM attempts a JOIN tasks t ON t.id=a.task_id
		WHERE a.status='queued' AND t.id NOT IN (SELECT task_id FROM task_takeovers) ORDER BY t.priority DESC, a.id`)
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()

	for _, id := range ids {
		att, err := s.DB.Attempt(id)
		if err != nil || att.Status != "queued" {
			continue
		}
		c, err := s.contextFor(att)
		if err != nil {
			continue
		}
		running, err := s.DB.Count("attempts a JOIN tasks t ON t.id=a.task_id "+
			"JOIN projects p ON p.id=t.project_id",
			"a.status='running' AND p.target_id=?", c.Target.ID)
		if err != nil {
			continue
		}
		slots := c.Target.MaxConcurrent
		if slots <= 0 {
			slots = 4
		}
		if running >= slots {
			continue
		}
		if err := s.launch(ctx, att, c); err != nil {
			s.Log.Error("launch failed", "attempt", att.ID, "err", err)
			s.DB.Update("attempts", att.ID, map[string]any{
				"status": "failed", "result_json": store.J(map[string]any{"error": err.Error()})})
			s.setTaskStatus(att.TaskID, "failed")
		}
	}
}

func (s *Scheduler) launch(ctx context.Context, att *store.Attempt, c *runCtx) error {
	if c.Target.Kind == "sandbox" {
		return s.launchSandbox(ctx, att, c)
	}
	ex, err := s.Reg.For(c.Target)
	if err != nil {
		return err
	}
	wt := att.WorktreePath
	branch := att.Branch
	if branch == "" {
		_, branch = s.taskWorktree(c, att)
	}
	if wt == "" {
		wt, _ = s.taskWorktree(c, att)
		base := firstNonEmpty(c.Task.BaseBranch, c.Project.DefaultBaseBranch, "main")
		if err := worktree.Ensure(ctx, ex, c.Project.RepoPath, base, branch, wt); err != nil {
			return err
		}
	}
	if err := skills.Reassert(ctx, ex, s.DB, c.Project, firstNonEmpty(c.Task.Agent, "claude"), wt); err != nil {
		return fmt.Errorf("project skills: %w", err)
	}

	launchKW, err := s.stageRuntime(ctx, ex, wt, att, c)
	if err != nil {
		return err
	}
	// push CURRENT auth so the agent never runs on a rotated-out credential copy
	s.Creds.Provision(ctx, ex, c.Target.Kind, c.Target.Name, c.Task.Agent)

	sess := fmt.Sprintf("lec-%d", att.ID)
	cmd, err := s.buildLaunch(att, c, wt, sess, false, launchKW)
	if err != nil {
		return err
	}
	r, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 60})
	if err != nil {
		return err
	}
	if !r.OK() {
		return executor.Errf("tmux launch failed: %s", strings.TrimSpace(r.Stderr))
	}
	s.DB.Update("attempts", att.ID, map[string]any{
		"status": "running", "worktree_path": wt, "branch": branch,
		"tmux_session": sess, "started_at": store.Now(), "log_offset": 0})
	s.setTaskStatus(att.TaskID, "running")
	s.Log.Info("attempt launched", "attempt", att.ID, "target", c.Target.Name, "worktree", wt)
	return nil
}

// launchSandbox is the ephemeral flow: clone template -> repo inside container ->
// agent -> destroy at finalise.
func (s *Scheduler) launchSandbox(ctx context.Context, att *store.Attempt, c *runCtx) error {
	host, err := s.Reg.For(c.Target)
	if err != nil {
		return err
	}
	vmid, err := sandbox.Provision(ctx, host, c.Target.Host, att.ID, s.Log)
	if err != nil {
		return err
	}
	s.DB.Update("attempts", att.ID, map[string]any{"sandbox_vmid": vmid})
	att.SandboxVMID = vmid

	if err := s.launchSandboxInner(ctx, att, c, host, vmid); err != nil {
		// ANY failure after the container exists must not leak it — the generic
		// handler above only marks the attempt failed
		sandbox.Destroy(ctx, host, vmid, s.Log)
		s.DB.Update("attempts", att.ID, map[string]any{"worktree_path": ""})
		return err
	}
	return nil
}

// SandboxInnerHook lets a test inject a failure after the container exists, to
// prove the container is still destroyed.
var SandboxInnerHook func() error

func (s *Scheduler) launchSandboxInner(ctx context.Context, att *store.Attempt, c *runCtx,
	host executor.Executor, vmid string) error {
	if SandboxInnerHook != nil {
		if err := SandboxInnerHook(); err != nil {
			return err
		}
	}
	inside, err := s.attemptExecutor(att, c.Target)
	if err != nil {
		return err
	}
	base := firstNonEmpty(c.Task.BaseBranch, c.Project.DefaultBaseBranch, "main")
	workdir := c.Project.RepoPath // repo baked into the template
	if sandbox.IsRepoURL(c.Project.RepoPath) {
		workdir = "/root/work/" + c.Project.Name
		r, err := inside.Run(ctx, fmt.Sprintf("git clone --branch %s %s %s",
			base, c.Project.RepoPath, workdir), executor.RunOpts{Timeout: 600})
		if err != nil {
			return err
		}
		if !r.OK() {
			return executor.Errf("repo clone failed: %s", clipEnd(strings.TrimSpace(r.Stderr), 400))
		}
	}
	branch := worktree.BranchName(c.Task.ID, att.N)
	if _, err := inside.Run(ctx, "git checkout -b "+branch,
		executor.RunOpts{Cwd: workdir, Timeout: 60}); err != nil {
		return err
	}
	if err := worktree.AddExcludes(ctx, inside, workdir); err != nil {
		return err
	}

	launchKW, err := s.stageRuntime(ctx, inside, workdir, att, c)
	if err != nil {
		return err
	}
	// provision current auth into the container via its own executor (mock-safe)
	s.Creds.Provision(ctx, inside, "pct", "sandbox-"+vmid, c.Task.Agent)

	sess := fmt.Sprintf("lec-%d", att.ID)
	cmd, err := s.buildLaunch(att, c, workdir, sess, true, launchKW)
	if err != nil {
		return err
	}
	r, err := inside.Run(ctx, cmd, executor.RunOpts{Timeout: 60})
	if err != nil {
		return err
	}
	if !r.OK() {
		return executor.Errf("tmux launch failed in sandbox: %s", strings.TrimSpace(r.Stderr))
	}
	s.DB.Update("attempts", att.ID, map[string]any{
		"status": "running", "worktree_path": workdir, "branch": branch,
		"tmux_session": sess, "started_at": store.Now(), "log_offset": 0})
	s.setTaskStatus(att.TaskID, "running")
	s.Log.Info("attempt launched in sandbox", "attempt", att.ID, "vmid", vmid)
	return nil
}

func (s *Scheduler) buildLaunch(att *store.Attempt, c *runCtx, workdir, sess string,
	isSandbox bool, kw launchKW) (string, error) {
	env := map[string]string{}
	for k, v := range s.Creds.BaseAgentEnv() {
		env[k] = v
	}
	for k, v := range kw.Env {
		env[k] = v
	}
	if kw.Env == nil {
		for k, v := range projectEnv(c.Project) {
			env[k] = v
		}
	}
	agent := firstNonEmpty(c.Task.Agent, "claude")
	if kw.Agent != "" {
		agent = kw.Agent
	}
	return s.Launcher.Command(agents.LaunchSpec{
		Agent:          agent,
		Worktree:       workdir,
		TmuxSession:    sess,
		PermissionMode: c.Task.PermissionMode,
		Model:          firstNonEmpty(att.Model, c.Task.Model),
		ResumeSession:  att.ResumeSession,
		Sandbox:        isSandbox,
		Env:            env,
		SettingsPath:   kw.SettingsPath,
		MCPConfig:      kw.MCPConfig,
		StrictMCP:      kw.StrictMCP,
		ExtraArgs:      kw.ExtraArgs,
		Definition:     kw.Definition,
	})
}

func projectEnv(p *store.Project) map[string]string {
	out := map[string]string{}
	var raw map[string]any
	if err := json.Unmarshal([]byte(nz(p.EnvJSON, "{}")), &raw); err != nil {
		return out
	}
	for k, v := range raw {
		out[k] = fmt.Sprint(v)
	}
	return out
}

// ---- running: tail + finalise -------------------------------------------------

// drainEvents reads whatever the agent has written since the last poll and
// stores the complete lines, returning the raw chunk it saw.
func (s *Scheduler) drainEvents(ctx context.Context, ex executor.Executor,
	att *store.Attempt, c *runCtx, rt string) ([]byte, error) {
	chunk, err := ex.ReadFile(ctx, rt+"/events.jsonl", att.LogOffset)
	if err != nil {
		return nil, err
	}
	if len(chunk) == 0 {
		return chunk, nil
	}
	// a trailing partial line is left for the next read rather than parsed half
	nl := lastIndexByte(chunk, '\n')
	if nl < 0 {
		// A plain custom CLI is allowed to finish without a final newline. Once
		// exit_code exists the bytes are complete, so do not strand its last
		// message in the in-memory remainder forever.
		exitRaw, exitErr := ex.ReadFile(ctx, rt+"/exit_code", 0)
		if exitErr == nil && strings.TrimSpace(string(exitRaw)) != "" {
			// Parsers intentionally retain an unterminated line during normal
			// polling. At process exit it is complete by definition; synthesize
			// the delimiter only for parsing and advance the real byte offset by
			// the bytes that were actually read.
			events, _ := s.parseAttemptEvents(att, c, string(chunk)+"\n")
			if err := s.StoreEvents(att, events); err != nil {
				return nil, err
			}
			att.LogOffset += int64(len(chunk))
			s.DB.Update("attempts", att.ID, map[string]any{"log_offset": att.LogOffset})
		}
		return chunk, nil
	}
	events, _ := s.parseAttemptEvents(att, c, string(chunk[:nl+1]))
	if err := s.StoreEvents(att, events); err != nil {
		return nil, err
	}
	att.LogOffset += int64(nl) + 1
	s.DB.Update("attempts", att.ID, map[string]any{"log_offset": att.LogOffset})
	return chunk, nil
}

func (s *Scheduler) parseAttemptEvents(att *store.Attempt, c *runCtx, buf string) ([]agents.Event, string) {
	if att.LaunchConfigJSON != "" {
		var cfg agents.TaskLaunchConfig
		if json.Unmarshal([]byte(att.LaunchConfigJSON), &cfg) == nil && !cfg.Definition.Builtin {
			return agents.ParseTaskStreamLines(cfg.Agent, cfg.Definition.OutputMode, buf)
		}
	}
	return agents.ParseStreamLines(firstNonEmpty(c.Task.Agent, "claude"), buf)
}

func (s *Scheduler) poll(ctx context.Context, att *store.Attempt) error {
	c, err := s.contextFor(att)
	if err != nil {
		return err
	}
	ex, err := s.attemptExecutor(att, c.Target)
	if err != nil {
		return err
	}
	rt := agents.RuntimeDir(att.WorktreePath)

	chunk, err := s.drainEvents(ctx, ex, att, c, rt)
	if err != nil {
		return err
	}

	exitRaw, err := ex.ReadFile(ctx, rt+"/exit_code", 0)
	if err != nil {
		return err
	}
	if trimmed := strings.TrimSpace(string(exitRaw)); trimmed != "" {
		// One last read before finalising. The chunk above and this exit code are
		// two separate reads: whatever the agent wrote between them — which is
		// usually its closing `result`, the summary of what it did — would
		// otherwise be lost, because finalising takes the attempt out of the
		// running set and nothing ever reads the tail.
		if _, err := s.drainEvents(ctx, ex, att, c, rt); err != nil {
			s.Log.Warn("final event drain failed", "attempt", att.ID, "err", err)
		}
		rc := -1
		fmt.Sscanf(trimmed, "%d", &rc)
		return s.captureAndFinalize(ctx, att, c, rc)
	}

	if len(chunk) == 0 { // no output and no exit code — is the session even alive?
		alive, err := ex.Run(ctx, fmt.Sprintf("tmux has-session -t =lec-%d 2>/dev/null", att.ID),
			executor.RunOpts{Timeout: 20})
		if err != nil {
			return err
		}
		if alive.OK() {
			s.mu.Lock()
			delete(s.ghostStrikes, att.ID)
			s.mu.Unlock()
			return nil
		}
		// two consecutive strikes: a single miss can be a transient read race
		// (session ended but exit_code not yet visible through the executor)
		s.mu.Lock()
		s.ghostStrikes[att.ID]++
		strikes := s.ghostStrikes[att.ID]
		s.mu.Unlock()
		if strikes >= 2 {
			s.mu.Lock()
			delete(s.ghostStrikes, att.ID)
			s.mu.Unlock()
			s.finalize(ctx, att, -1, "tmux session disappeared without exit code")
			s.destroyIfSandbox(ctx, att, c)
		}
	}
	return nil
}

// StoreEvents persists normalised events and streams them to the task channel.
// Exported so a test can drive the concurrent-delete race directly.
func (s *Scheduler) StoreEvents(att *store.Attempt, events []agents.Event) error {
	if len(events) == 0 {
		return nil
	}
	// fast path: skip entirely if the attempt was deleted concurrently. Writing
	// an event for a vanished attempt raises a FOREIGN KEY error that used to
	// abort the whole tick and stall every other running attempt.
	if !s.DB.Exists("attempts", "id=?", att.ID) {
		return nil
	}
	seq, err := s.DB.MaxEventSeq(att.ID)
	if err != nil {
		return err
	}
	for _, ev := range events {
		seq++
		if err := s.DB.InsertEvent(att.ID, seq, ev.Type, store.J(ev.Payload)); err != nil {
			return err
		}
		switch ev.Type {
		case "init":
			if sid, _ := ev.Payload["session_id"].(string); sid != "" {
				s.DB.Update("attempts", att.ID, map[string]any{"session_id": sid})
			}
		case "result":
			s.DB.Update("attempts", att.ID, map[string]any{"result_json": store.J(ev.Payload)})
		}
		s.Bus.Publish(taskChannel(att.TaskID), "agent_event", map[string]any{
			"attempt_id": att.ID, "seq": seq, "type": ev.Type, "payload": ev.Payload})
	}
	return nil
}

func (s *Scheduler) captureAndFinalize(ctx context.Context, att *store.Attempt, c *runCtx, rc int) error {
	ex, err := s.attemptExecutor(att, c.Target)
	if err != nil {
		return err
	}
	base := firstNonEmpty(c.Task.BaseBranch, c.Project.DefaultBaseBranch, "main")
	patch, files, derr := worktree.CaptureDiff(ctx, ex, att.WorktreePath, base)
	if derr != nil {
		patch = ""
		files = []worktree.FileStat{{
			Path: fmt.Sprintf("(diff capture failed: %v)", derr)}}
	}
	dir := s.Cfg.DiffDir()
	if err := os.MkdirAll(dir, 0o755); err == nil {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("attempt-%d.patch", att.ID)),
			[]byte(patch), 0o644)
	}
	s.DB.Update("attempts", att.ID, map[string]any{"diff_stat_json": store.J(files)})

	// auto-verify: run the project's own test command in the worktree and badge
	// the result on the card — the one signal that says a diff is more than
	// plausible-looking
	if rc == 0 && strings.TrimSpace(c.Project.VerifyCmd) != "" {
		verify := map[string]any{"cmd": c.Project.VerifyCmd}
		vr, verr := ex.Run(ctx, c.Project.VerifyCmd,
			executor.RunOpts{Cwd: att.WorktreePath, Timeout: 900})
		if verr != nil {
			verify["rc"] = -1
			verify["output"] = verr.Error()
		} else {
			verify["rc"] = vr.RC
			verify["output"] = clipEnd(vr.Stdout+vr.Stderr, 4000)
		}
		s.DB.Update("attempts", att.ID, map[string]any{"verify_json": store.J(verify)})
		s.StoreEvents(att, []agents.Event{{Type: "verify", Payload: map[string]any{
			"cmd": verify["cmd"], "rc": verify["rc"],
			"output": clipEnd(fmt.Sprint(verify["output"]), 1200)}}})
	}
	s.finalize(ctx, att, rc, "")
	// ephemeral sandbox: events, diff and verify are already on the control
	// plane, so the container has served its purpose
	s.destroyIfSandbox(ctx, att, c)
	return nil
}

func (s *Scheduler) destroyIfSandbox(ctx context.Context, att *store.Attempt, c *runCtx) {
	if c.Target.Kind != "sandbox" {
		return
	}
	vmid := att.SandboxVMID
	if vmid == "" {
		if fresh, err := s.DB.Attempt(att.ID); err == nil {
			vmid = fresh.SandboxVMID
		}
	}
	if vmid == "" {
		return
	}
	host, err := s.Reg.For(c.Target)
	if err != nil {
		return
	}
	sandbox.Destroy(ctx, host, vmid, s.Log)
	s.DB.Update("attempts", att.ID, map[string]any{"worktree_path": ""})
}

func (s *Scheduler) finalize(ctx context.Context, att *store.Attempt, rc int, note string) {
	s.Broker.ExpireForAttempt(att.ID) // no ghost approvals on a dead attempt
	ok := rc == 0
	result := map[string]any{}
	if fresh, err := s.DB.Attempt(att.ID); err == nil {
		result = store.UnjObj(fresh.ResultJSON)
	}
	if note != "" {
		result["error"] = note
	}
	status := "failed"
	if ok {
		status = "done"
	}
	s.DB.Update("attempts", att.ID, map[string]any{
		"status": status, "finished_at": store.Now(), "exit_code": rc,
		"result_json": store.J(result)})
	s.clearPollError(att.ID)

	task, err := s.DB.Task(att.TaskID)
	if err != nil {
		return
	}
	// A/B: the task stays running until its last active attempt lands
	others, _ := s.DB.Count("attempts", "task_id=? AND status IN ('queued','running') AND id!=?",
		att.TaskID, att.ID)
	if task.Status != "running" || others > 0 {
		return
	}
	anyOK, _ := s.DB.Count("attempts", "task_id=? AND status='done'", att.TaskID)
	if anyOK > 0 {
		s.setTaskStatus(task.ID, "review")
		if n, _ := s.DB.Count("task_messages", "task_id=? AND status='pending'", task.ID); n > 0 {
			return
		}
		s.Notifier.Notify("Ready for review", clip(task.Title, 80),
			fmt.Sprintf("/#task/%d", task.ID), nil)
		if task.CreatedBy == "reviewer-gate" {
			s.applyReviewVerdict(task, result)
		} else {
			s.maybeSpawnReviewer(ctx, task, att)
		}
		return
	}
	s.setTaskStatus(task.ID, "failed")
	s.Notifier.Notify("Task failed", clip(task.Title, 80),
		fmt.Sprintf("/#task/%d", task.ID), nil)
}

// ---- reviewer gate -----------------------------------------------------------

func (s *Scheduler) maybeSpawnReviewer(ctx context.Context, task *store.Task, att *store.Attempt) {
	project, err := s.DB.Project(task.ProjectID)
	if err != nil || project.ReviewGate == 0 {
		return
	}
	target, err := s.DB.Target(project.TargetID)
	if err == nil && target.Kind == "sandbox" {
		s.Log.Info("review gate skipped for sandbox task (worktree is destroyed)",
			"task", task.ID)
		return
	}
	if s.DB.Exists("tasks", "created_by='reviewer-gate' AND created_by_attempt=?", att.ID) {
		return
	}
	patch := ""
	if raw, err := os.ReadFile(filepath.Join(s.Cfg.DiffDir(),
		fmt.Sprintf("attempt-%d.patch", att.ID))); err == nil {
		patch = clip(string(raw), 12000)
	}
	prompt := "You are a strict code reviewer. Another agent completed this task in " +
		"the current worktree (you may read files and run read-only checks):\n\n" +
		"TASK: " + task.Title + "\n" + task.Prompt + "\n\nDIFF:\n```diff\n" + patch + "\n```\n\n" +
		"Review for correctness, edge cases, and scope creep. Your FINAL line " +
		"must be exactly one of:\nVERDICT: APPROVE — <one-line reason>\n" +
		"VERDICT: REQUEST_CHANGES — <specific required changes>"

	attID := att.ID
	rtask, err := s.DB.InsertTask(&store.Task{
		ProjectID: task.ProjectID, Title: clip("Review: "+task.Title, 90),
		Prompt: prompt, Status: "queued", Priority: task.Priority,
		PermissionMode: "plan", CreatedBy: "reviewer-gate",
		ParentTaskID: &task.ID, CreatedByAttempt: &attID})
	if err != nil {
		s.Log.Error("reviewer gate: could not file review task", "task", task.ID, "err", err)
		return
	}
	// the reviewer works in the SAME worktree, read-only via plan mode
	if _, err := s.CreateAttempt(rtask, AttemptOpts{
		WorktreePath: att.WorktreePath, Branch: att.Branch}); err != nil {
		s.Log.Error("reviewer gate: could not create attempt", "task", rtask.ID, "err", err)
		return
	}
	s.Bus.Publish("board", "task", rtask)
	s.Log.Info("reviewer gate spawned", "review_task", rtask.ID, "task", task.ID)
}

var verdictRe = regexp.MustCompile(`VERDICT:\s*(APPROVE|REQUEST_CHANGES)`)

func (s *Scheduler) applyReviewVerdict(rtask *store.Task, result map[string]any) {
	text, _ := result["result"].(string)
	verdict := "UNCLEAR"
	if m := verdictRe.FindAllStringSubmatch(text, -1); len(m) > 0 {
		verdict = m[len(m)-1][1] // the last verdict wins
	}
	if rtask.ParentTaskID == nil {
		return
	}
	parentAtt, err := s.DB.LatestAttempt(*rtask.ParentTaskID)
	if err == nil {
		pres := store.UnjObj(parentAtt.ResultJSON)
		pres["review"] = map[string]any{
			"verdict": verdict, "notes": clipEnd(text, 1500), "reviewer_task_id": rtask.ID}
		s.DB.Update("attempts", parentAtt.ID, map[string]any{"result_json": store.J(pres)})
		s.StoreEvents(parentAtt, []agents.Event{{Type: "review_verdict", Payload: map[string]any{
			"verdict": verdict, "notes": clipEnd(text, 800)}}})
	}
	if parent, err := s.DB.Task(*rtask.ParentTaskID); err == nil {
		s.Bus.Publish("board", "task", parent)
		s.Notifier.Notify("Review: "+strings.ToLower(strings.ReplaceAll(verdict, "_", " ")),
			clip(parent.Title, 80), fmt.Sprintf("/#task/%d", parent.ID), nil)
	}
	// the reviewer card served its purpose — off the board
	s.setTaskStatus(rtask.ID, "done")
}

// ---- housekeeping ------------------------------------------------------------

// Janitor sweeps worktrees of finished tasks.
func (s *Scheduler) Janitor(ctx context.Context, days float64) (map[string]any, error) {
	cutoff := store.Now() - days*86400
	rows, err := s.DB.Query(`SELECT a.id FROM attempts a JOIN tasks t ON t.id=a.task_id
		JOIN projects p ON p.id=t.project_id
		WHERE t.status IN ('done','cancelled') AND a.worktree_path!=''
		AND a.finished_at IS NOT NULL AND a.finished_at<? AND p.keep_worktrees=0
        AND NOT EXISTS(SELECT 1 FROM sessions se WHERE se.target_id=p.target_id AND se.workdir=a.worktree_path)
        AND t.id NOT IN (SELECT task_id FROM task_takeovers)`, cutoff)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()

	removed := []int64{}
	for _, id := range ids {
		att, err := s.DB.Attempt(id)
		if err != nil {
			continue
		}
		c, err := s.contextFor(att)
		if err != nil {
			continue
		}
		ex, err := s.Reg.For(c.Target)
		if err != nil {
			continue
		}
		if cleanErr := skills.Clean(ctx, ex, s.DB, c.Project, att.WorktreePath); cleanErr != nil {
			s.Log.Warn("janitor: skill cleanup failed; retaining worktree and ownership evidence", "attempt", att.ID, "err", cleanErr)
			continue
		}
		if err := worktree.Remove(ctx, ex, c.Project.RepoPath, att.WorktreePath); err != nil {
			s.Log.Warn("janitor: worktree removal failed", "attempt", att.ID, "err", err)
			continue
		}
		s.DB.Update("attempts", att.ID, map[string]any{"worktree_path": ""})
		removed = append(removed, att.ID)
	}
	if len(removed) > 0 {
		s.Log.Info("janitor removed worktrees", "count", len(removed))
	}
	return map[string]any{"removed_attempts": removed}, nil
}

// CancelAttempt stops a live attempt: kills its tmux session, destroys its
// sandbox, and resolves any approval it left pending.
func (s *Scheduler) CancelAttempt(ctx context.Context, att *store.Attempt) {
	s.Broker.ExpireForAttempt(att.ID)
	if c, err := s.contextFor(att); err == nil {
		if ex, err := s.attemptExecutor(att, c.Target); err == nil {
			ex.Run(ctx, fmt.Sprintf("tmux kill-session -t =lec-%d 2>/dev/null || true", att.ID),
				executor.RunOpts{Timeout: 20})
		}
		if c.Target.Kind == "sandbox" && att.SandboxVMID != "" {
			if host, err := s.Reg.For(c.Target); err == nil {
				sandbox.Destroy(ctx, host, att.SandboxVMID, s.Log)
			}
			s.DB.Update("attempts", att.ID, map[string]any{"worktree_path": ""})
		}
	}
	s.DB.Update("attempts", att.ID, map[string]any{
		"status": "cancelled", "finished_at": store.Now()})
	s.setTaskStatus(att.TaskID, "cancelled")
}

// AttemptOpts are the optional inputs of CreateAttempt.
type AttemptOpts struct {
	Prompt        string
	ResumeSession string
	WorktreePath  string
	Branch        string
	Model         string
}

// CreateAttempt queues attempt N+1 for a task.
func (s *Scheduler) CreateAttempt(task *store.Task, o AttemptOpts) (*store.Attempt, error) {
	prev, err := s.DB.TaskAttempts(task.ID)
	if err != nil {
		return nil, err
	}
	n := 1
	for _, a := range prev {
		if a.N >= n {
			n = a.N + 1
		}
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	project, err := s.DB.Project(task.ProjectID)
	if err != nil {
		return nil, err
	}
	launchConfig, err := s.taskLaunchConfig(&store.Attempt{}, &runCtx{Task: task, Project: project})
	if err != nil {
		return nil, err
	}
	launchJSON := store.J(launchConfig)
	return s.DB.InsertAttempt(&store.Attempt{
		TaskID: task.ID, N: n, Status: "queued", Token: token, Prompt: o.Prompt,
		ResumeSession: o.ResumeSession, WorktreePath: o.WorktreePath,
		Branch: o.Branch, Model: o.Model, LaunchConfigJSON: launchJSON})
}

// SetTaskStatus is the one place a task's column changes, so every move is
// validated and every move is broadcast.
func (s *Scheduler) SetTaskStatus(taskID int64, next string) error {
	return s.setTaskStatus(taskID, next)
}

func (s *Scheduler) setTaskStatus(taskID int64, next string) error {
	task, err := s.DB.Task(taskID)
	if err != nil {
		return err
	}
	if err := state.Check(task.Status, next); err != nil {
		s.Log.Warn("illegal task transition", "task", taskID, "from", task.Status,
			"to", next, "err", err)
		return err
	}
	if err := s.DB.Update("tasks", taskID, map[string]any{
		"status": next, "updated_at": store.Now()}); err != nil {
		return err
	}
	if fresh, err := s.DB.Task(taskID); err == nil {
		s.Bus.Publish("board", "task", fresh)
	}
	return nil
}

// BuildNotesPrefix renders a project's memory into a prompt header, oldest first
// so the agent reads it chronologically.
func BuildNotesPrefix(notes []string) string {
	var b strings.Builder
	b.WriteString("## Project memory (notes left by previous agents)\n")
	for i := len(notes) - 1; i >= 0; i-- {
		b.WriteString("- " + clip(notes[i], 400) + "\n")
	}
	b.WriteString("\n---\n\n")
	return b.String()
}

func taskChannel(id int64) string { return fmt.Sprintf("task:%d", id) }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func nz(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func clipEnd(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func lastIndexByte(b []byte, c byte) int {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == c {
			return i
		}
	}
	return -1
}
