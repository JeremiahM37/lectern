// Package checks runs a project's check command against a session's
// worktree when its agent stops, and stores the result for the card to show.
//
// It is the session half of docs/agent-events.md section 4 ("Checks"). Tasks
// keep their existing one-shot auto-verify (internal/scheduler, unchanged
// attempts.verify_json shape); this package's Resolve/Execute are what that
// path now calls too, so "verify_cmd, or auto-detected .verify.yaml" is one
// rule instead of two. Sessions are different from a task attempt in one
// important way: they are ongoing, so running the same command again every
// time an agent goes idle — often with nothing new in the worktree — would
// be pure waste. RunForSession exists to make that cheap: a fingerprint over
// the worktree's diff decides whether there is anything worth checking.
package checks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/sinks"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/worktree"
)

// Status values stored in session_checks.status (see store/schema.go).
const (
	StatusRunning = "running"
	StatusPassed  = "passed"
	StatusFailed  = "failed"
	StatusError   = "error"
	StatusSkipped = "skipped"
)

// Trigger reasons stored in session_checks.reason.
const (
	ReasonStop   = "stop"   // an agent Stop (or codex AgentTurnComplete) hook
	ReasonScreen = "screen" // the screen-derived busy->idle fallback
	ReasonManual = "manual" // a human pressed "Run check"
)

// DefaultTimeout is 15 minutes, matching the literal the task auto-verify
// path used before this package existed.
const DefaultTimeout = 15 * time.Minute

// outputTailLines/outputTailBytes bound what a check result keeps: enough to
// see why something failed, not a full CI log stored forever.
const outputTailLines = 200
const outputTailBytes = 16000

// AutoVerifyCommand is what a project with no verify_cmd but a checked-in
// .verify.yaml and `verify` on the target's PATH gets for free — see
// probeAutoCommand and docs/agent-events.md section 4.
const AutoVerifyCommand = "verify run .verify.yaml"

// StopListener is how something that observes an agent's lifecycle (a Stop
// hook, or the screen-derived status fallback) tells the checks runner a
// session may have new work to check. It is defined here, not imported by
// its callers as a concrete type, so internal/agentevents and
// internal/sessions never need to import internal/checks — *Runner satisfies
// this interface structurally, wired up in internal/app.
type StopListener interface {
	// OnAgentStop is fire-and-forget: it must not block its caller (a hook
	// handler that owes the agent a fast HTTP response, or a poll tick that
	// owes the rest of the fleet its turn). The runner performs the actual
	// check on its own goroutine, coalescing overlapping requests.
	OnAgentStop(sessionID int64)
}

// Runner owns check execution for both sessions (this file) and, via
// RunForTask, the task auto-verify path (internal/scheduler).
type Runner struct {
	DB       *store.DB
	Reg      *executor.Registry
	Bus      *bus.Bus
	Notifier *sinks.Notifier
	Log      *slog.Logger
	// Timeout bounds one check run; DefaultTimeout is used when unset.
	Timeout time.Duration

	mu      sync.Mutex
	running map[int64]bool   // session id -> a check is executing right now
	pending map[int64]string // session id -> reason of the coalesced follow-up run
}

// New builds a Runner.
func New(db *store.DB, reg *executor.Registry, b *bus.Bus, notifier *sinks.Notifier, timeout time.Duration, log *slog.Logger) *Runner {
	return &Runner{DB: db, Reg: reg, Bus: b, Notifier: notifier, Timeout: timeout, Log: log,
		running: map[int64]bool{}, pending: map[int64]string{}}
}

func (r *Runner) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return DefaultTimeout
}

// OnAgentStop implements StopListener. The actual run happens on a fresh
// goroutine with its own background context: the caller (a hook's HTTP
// handler, or a poll tick) must get its own turn back immediately, and a
// check that legitimately takes minutes must not be cancelled just because
// the request that triggered it already answered.
func (r *Runner) OnAgentStop(sessionID int64) {
	go r.RunForSession(context.Background(), sessionID, ReasonStop)
}

// RunForSession resolves a session's check command and runs it if the
// worktree has changed since the last check (or reason is ReasonManual,
// which always runs). Only one check runs at a time per session; a request
// that arrives while one is already running is coalesced into a single
// extra run afterward, using whichever reason arrived last.
func (r *Runner) RunForSession(ctx context.Context, sessionID int64, reason string) {
	r.mu.Lock()
	if r.running == nil {
		r.running = map[int64]bool{}
		r.pending = map[int64]string{}
	}
	if r.running[sessionID] {
		r.pending[sessionID] = reason
		r.mu.Unlock()
		return
	}
	r.running[sessionID] = true
	r.mu.Unlock()

	r.runOnce(ctx, sessionID, reason)
	for {
		r.mu.Lock()
		next, ok := r.pending[sessionID]
		if ok {
			delete(r.pending, sessionID)
		} else {
			delete(r.running, sessionID)
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		r.runOnce(ctx, sessionID, next)
	}
}

// runOnce is the whole pipeline for one attempt: resolve, fingerprint, skip
// or run, store, publish, notify. Every early return is a deliberate no-op
// (session gone, no workdir, no command, nothing changed) rather than an
// error, because most calls into this package are speculative — "an agent
// went idle" is not evidence there is anything to check.
func (r *Runner) runOnce(ctx context.Context, sessionID int64, reason string) {
	sess, err := r.DB.Session(sessionID)
	if err != nil {
		return
	}
	target, err := r.DB.Target(sess.TargetID)
	if err != nil {
		return
	}
	ex, err := r.Reg.For(target)
	if err != nil {
		return
	}
	dir, err := sessionCheckWorkdir(sess)
	if err != nil {
		return
	}
	var project *store.Project
	if sess.ProjectID != nil {
		project, _ = r.DB.Project(*sess.ProjectID)
	}
	cmd, _, ok := r.resolve(ctx, ex, project, dir)
	if !ok {
		if reason == ReasonManual {
			r.recordSkipped(sessionID, "no check command configured (set verify_cmd, or add .verify.yaml with verify on PATH)")
		}
		return
	}
	fp, err := worktreeFingerprint(ctx, ex, dir)
	if err != nil {
		return
	}
	previous, _ := r.DB.LatestSessionCheck(sessionID)
	if reason != ReasonManual && previous != nil && previous.Fingerprint == fp {
		return // nothing changed since the last check; don't burn a run on it
	}

	started := store.Now()
	row, err := r.DB.InsertSessionCheck(&store.SessionCheck{
		SessionID: sessionID, Fingerprint: fp, Command: cmd,
		Status: StatusRunning, StartedAt: started, Reason: reason,
	})
	if err != nil {
		r.Log.Warn("could not record check start", "session", sessionID, "err", err)
		return
	}
	r.publish(sessionID, row)

	res, runErr := ex.Run(ctx, cmd, executor.RunOpts{Cwd: dir, Timeout: r.timeout().Seconds()})
	finished := store.Now()
	fields := map[string]any{"finished_at": finished}
	status := StatusPassed
	if runErr != nil {
		status = StatusError
		fields["output_tail"] = clipBytes(runErr.Error(), outputTailBytes)
	} else {
		rc := res.RC
		fields["exit_code"] = rc
		if rc != 0 {
			status = StatusFailed
		}
		fields["output_tail"] = clipBytes(lastLines(res.Stdout+res.Stderr, outputTailLines), outputTailBytes)
	}
	fields["status"] = status
	if err := r.DB.Update("session_checks", row.ID, fields); err != nil {
		r.Log.Warn("could not record check result", "session", sessionID, "err", err)
		return
	}
	fresh, err := r.DB.SessionCheck(row.ID)
	if err != nil {
		return
	}
	r.publish(sessionID, fresh)
	r.maybeNotify(sess, previous, fresh)
}

// recordSkipped is the visible feedback a human gets from pressing "Run
// check" on a session with nothing configured — silent for the automatic
// triggers (Stop, screen), which fire far more often and would otherwise
// fill the table with rows nobody asked to see.
func (r *Runner) recordSkipped(sessionID int64, note string) {
	now := store.Now()
	row, err := r.DB.InsertSessionCheck(&store.SessionCheck{
		SessionID: sessionID, Status: StatusSkipped, StartedAt: now, FinishedAt: &now,
		Reason: ReasonManual, OutputTail: note,
	})
	if err != nil {
		return
	}
	r.publish(sessionID, row)
}

// maybeNotify pushes on a check going from anything else to failed, and on
// recovering from failed/error back to passed — never on a repeat of the
// same outcome, which would just be noise on every idle transition.
func (r *Runner) maybeNotify(sess *store.Session, previous, current *store.SessionCheck) {
	if r.Notifier == nil || current == nil {
		return
	}
	wasFailing := previous != nil && (previous.Status == StatusFailed || previous.Status == StatusError)
	url := fmt.Sprintf("/#session/%d", sess.ID)
	name := sess.Name
	switch {
	case current.Status == StatusFailed || current.Status == StatusError:
		if !wasFailing {
			r.Notifier.Notify("Check failed", name+": "+current.Command, url, nil)
		}
	case current.Status == StatusPassed && wasFailing:
		r.Notifier.Notify("Check passed", name+" is green again: "+current.Command, url, nil)
	}
}

func (r *Runner) publish(sessionID int64, c *store.SessionCheck) {
	if r.Bus == nil {
		return
	}
	r.Bus.Publish("board", "session.check", c)
	r.Bus.Publish(fmt.Sprintf("session:%d", sessionID), "session.check", c)
}

// ---- task auto-verify (moved here from internal/scheduler unchanged) ------

// RunForTask runs a project's check command against a finished task
// attempt's worktree, in the exact {"cmd","rc","output"} shape scheduler.go
// has always stored in attempts.verify_json — callers must not change that
// shape, since the timeline event and the board badge both parse it. Unlike
// a session, a task attempt is one-shot: there is nothing to fingerprint
// against and no coalescing concern, so this is just resolve-then-execute.
func (r *Runner) RunForTask(ctx context.Context, ex executor.Executor, project *store.Project, worktreePath string) (map[string]any, bool) {
	cmd, _, ok := r.resolve(ctx, ex, project, worktreePath)
	if !ok {
		return nil, false
	}
	verify := map[string]any{"cmd": cmd}
	vr, err := ex.Run(ctx, cmd, executor.RunOpts{Cwd: worktreePath, Timeout: r.timeout().Seconds()})
	if err != nil {
		verify["rc"] = -1
		verify["output"] = err.Error()
	} else {
		verify["rc"] = vr.RC
		verify["output"] = clipBytes(vr.Stdout+vr.Stderr, 4000)
	}
	return verify, true
}

// ---- command resolution ---------------------------------------------------

// resolve is "verify_cmd, or auto-detect .verify.yaml" — the one rule behind
// both RunForSession/RunForTask and the project settings UI's preview
// (DetectCommand). source is "configured"|"auto"|"none", exposed so the UI
// can say which one it is rather than just showing a command.
func (r *Runner) resolve(ctx context.Context, ex executor.Executor, project *store.Project, dir string) (cmd, source string, ok bool) {
	if project != nil && strings.TrimSpace(project.VerifyCmd) != "" {
		return project.VerifyCmd, "configured", true
	}
	if ex == nil || strings.TrimSpace(dir) == "" {
		return "", "none", false
	}
	if probeAutoCommand(ctx, ex, dir) {
		return AutoVerifyCommand, "auto", true
	}
	return "", "none", false
}

// DetectCommand is resolve for a project with no live worktree yet — the
// project settings UI's "Check command: auto-detected: ..." preview. It
// probes the project's own repo checkout on its target, since .verify.yaml
// is checked into the repo and so is present in any worktree cut from it.
func (r *Runner) DetectCommand(ctx context.Context, project *store.Project) (cmd, source string) {
	if project == nil {
		return "", "none"
	}
	target, err := r.DB.Target(project.TargetID)
	if err != nil {
		return "", "none"
	}
	ex, err := r.Reg.For(target)
	if err != nil {
		return "", "none"
	}
	cmd, source, _ = r.resolve(ctx, ex, project, project.RepoPath)
	return cmd, source
}

// probeAutoCommand asks the target, in one round trip, whether .verify.yaml
// exists in dir AND verify is on PATH. Both conditions are folded into one
// shell command deliberately: the UI only needs a yes/no, and a single probe
// is one exec instead of two on every check trigger.
func probeAutoCommand(ctx context.Context, ex executor.Executor, dir string) bool {
	q := executor.ShellQuote
	cmd := fmt.Sprintf("test -f %s && command -v verify >/dev/null 2>&1",
		q(filepath.Join(dir, ".verify.yaml")))
	res, err := ex.Run(ctx, cmd, executor.RunOpts{Cwd: dir, Timeout: 10})
	return err == nil && res.OK()
}

// ---- session workdir resolution --------------------------------------------

// sessionCheckWorkdir mirrors internal/api's sessionRepoDiffs (session_review.go)
// closely enough to reuse its worktree_json shape, but simplified to the one
// directory a check runs in: the session's own project's repository when a
// grouped multi-repo workspace has one, otherwise the first repository with a
// worktree, otherwise the session's single worktree/workdir. A session with
// no worktree and no workdir has nothing to check.
func sessionCheckWorkdir(row *store.Session) (string, error) {
	var plan worktree.Interactive
	hasPlan := row.WorktreeJSON != "" && json.Unmarshal([]byte(row.WorktreeJSON), &plan) == nil
	switch {
	case hasPlan && len(plan.Repositories) > 0:
		for _, repo := range plan.Repositories {
			if repo.Worktree == nil || repo.Worktree.Path == "" {
				continue
			}
			if repo.ProjectID != nil && row.ProjectID != nil && *repo.ProjectID == *row.ProjectID {
				return repo.Worktree.Path, nil
			}
		}
		for _, repo := range plan.Repositories {
			if repo.Worktree != nil && repo.Worktree.Path != "" {
				return repo.Worktree.Path, nil
			}
		}
		return "", fmt.Errorf("no repository worktree available to check")
	case hasPlan && plan.Path != "":
		return plan.Path, nil
	case row.Workdir != "":
		return row.Workdir, nil
	default:
		return "", fmt.Errorf("session has no workdir to check")
	}
}

// ---- worktree fingerprint ---------------------------------------------------

// worktreeFingerprint hashes HEAD's sha, `git status --porcelain` and a diff
// against HEAD (which covers both staged and unstaged changes) — enough to
// tell "the worktree has moved since the last check" from "nothing to see
// here" without capturing or storing the diff itself.
func worktreeFingerprint(ctx context.Context, ex executor.Executor, dir string) (string, error) {
	q := executor.ShellQuote
	head, err := ex.Run(ctx, "git -C "+q(dir)+" rev-parse HEAD", executor.RunOpts{Timeout: 15})
	if err != nil {
		return "", err
	}
	status, err := ex.Run(ctx, "git -C "+q(dir)+" status --porcelain", executor.RunOpts{Timeout: 30})
	if err != nil {
		return "", err
	}
	diff, err := ex.Run(ctx, "git -C "+q(dir)+" diff HEAD", executor.RunOpts{Timeout: 60})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(head.Stdout) + "\x00" + status.Stdout + "\x00" + diff.Stdout))
	return hex.EncodeToString(sum[:]), nil
}

// ---- small string helpers ---------------------------------------------------

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
