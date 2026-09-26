// Package ciloop is the CI-aware PR loop (docs/ci-loop.md): once Lectern
// opens a pull request for a task or session on a project that opted in, it
// watches the PR's checks through the target's own `gh`, and when they fail
// it sends the owning agent a short failure report — failing job names and a
// trimmed, redacted log tail — asking it to fix and push. It stops on pass,
// merge, close or the attempt cap, and pushes a phone notification when CI
// goes green after fixes or when it gives up.
//
// There are no webhooks (Lectern is tailnet-only). The loop polls, with
// backoff, and only while a PR still has pending or failing checks.
package ciloop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/sinks"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Watch states stored in ci_watches.state. pending and failing are the
// active ones; everything else is terminal.
const (
	StatePending = "pending" // checks running, or not reported yet
	StateFailing = "failing" // failed; a fix request was sent, waiting for a push
	StatePassed  = "passed"
	StateCapped  = "capped" // still failing after max_attempts fix requests
	StateMerged  = "merged"
	StateClosed  = "closed"
	StateNone    = "none"    // the PR never reported any checks
	StateError   = "error"   // gh cannot do the job (not signed in, not installed, …)
	StateStalled = "stalled" // no progress for StallAfter
)

// Defaults for the poll schedule.
const (
	DefaultMinInterval   = 30 * time.Second
	DefaultMaxInterval   = 10 * time.Minute
	DefaultStallAfter    = 24 * time.Hour
	DefaultNoChecksAfter = 15 * time.Minute
	transientErrorLimit  = 6
)

// SessionSender delivers a message into a live session's agent. It is
// agent-agnostic: *sessions.Manager types it into the tmux pane, which is
// how every CLI Lectern runs receives a message.
type SessionSender interface {
	SendNotice(ctx context.Context, id int64, text string) error
}

// Notifier is the phone-push side; *sinks.Notifier satisfies it.
type Notifier interface {
	Notify(title, body, urlPath string, extra *sinks.Extra)
}

// Watcher owns every ci_watches row.
type Watcher struct {
	DB       *store.DB
	Reg      *executor.Registry
	Bus      *bus.Bus
	Notifier Notifier
	Sessions SessionSender
	Log      *slog.Logger

	MinInterval, MaxInterval, StallAfter, NoChecksAfter time.Duration
	// Now is the clock, replaceable in tests.
	Now func() float64
	// Sync polls due watches inline in Tick instead of on goroutines.
	Sync bool

	mu       sync.Mutex
	inflight map[int64]bool
}

// New builds a Watcher with the default schedule.
func New(db *store.DB, reg *executor.Registry, b *bus.Bus, n Notifier, s SessionSender, log *slog.Logger) *Watcher {
	return &Watcher{DB: db, Reg: reg, Bus: b, Notifier: n, Sessions: s, Log: log,
		MinInterval: DefaultMinInterval, MaxInterval: DefaultMaxInterval,
		StallAfter: DefaultStallAfter, NoChecksAfter: DefaultNoChecksAfter}
}

func (w *Watcher) now() float64 {
	if w.Now != nil {
		return w.Now()
	}
	return store.Now()
}

func dur(d, def time.Duration) float64 {
	if d <= 0 {
		d = def
	}
	return d.Seconds()
}

// Owner is who opened a PR, and so who gets told when its CI fails.
type Owner struct {
	TaskID    *int64
	SessionID *int64
	ProjectID *int64
	TargetID  int64
	Branch    string
}

// ErrNotEnabled is Arm's answer for a project that has not opted in.
var ErrNotEnabled = errors.New("the CI loop is not enabled for this project")

// Arm starts watching prURL for owner. Unless force is set (a human asking
// for it explicitly), the owner's project must have opted in; otherwise it
// returns ErrNotEnabled and records nothing.
func (w *Watcher) Arm(o Owner, prURL string, force bool) (*store.CIWatch, error) {
	prURL = strings.TrimRight(strings.TrimSpace(prURL), "/")
	if !ValidPRURL(prURL) {
		return nil, fmt.Errorf("not a pull request URL: %q", prURL)
	}
	if (o.TaskID == nil) == (o.SessionID == nil) {
		return nil, errors.New("a CI watch needs exactly one owner: a task or a session")
	}
	max := 0
	var proj *store.Project
	if o.ProjectID != nil {
		proj, _ = w.DB.Project(*o.ProjectID)
	}
	if proj != nil {
		max = proj.CIMaxAttempts
	}
	if !force && (proj == nil || proj.CILoop == 0) {
		return nil, ErrNotEnabled
	}
	row, err := w.DB.UpsertCIWatch(&store.CIWatch{
		TaskID: o.TaskID, SessionID: o.SessionID, ProjectID: o.ProjectID,
		TargetID: o.TargetID, Branch: o.Branch, PRURL: prURL, MaxAttempts: max,
	})
	if err != nil {
		return nil, err
	}
	w.publish(row)
	return row, nil
}

// Tick polls every due watch. The scheduler calls it once per tick; each poll
// runs on its own goroutine (unless Sync) so a slow `gh` never holds up task
// scheduling, and a watch is never polled twice at once.
func (w *Watcher) Tick(ctx context.Context) {
	due, err := w.DB.DueCIWatches(w.now())
	if err != nil {
		w.Log.Warn("ci loop: listing due watches failed", "err", err)
		return
	}
	for _, cw := range due {
		id := cw.ID
		w.mu.Lock()
		if w.inflight == nil {
			w.inflight = map[int64]bool{}
		}
		if w.inflight[id] {
			w.mu.Unlock()
			continue
		}
		w.inflight[id] = true
		w.mu.Unlock()
		run := func() {
			defer func() {
				w.mu.Lock()
				delete(w.inflight, id)
				w.mu.Unlock()
			}()
			pctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			w.Poll(pctx, id)
		}
		if w.Sync {
			run()
		} else {
			go run()
		}
	}
}

// Poll is one observation of one watch: read the PR and its checks, decide,
// act, and schedule the next look.
func (w *Watcher) Poll(ctx context.Context, id int64) {
	cw, err := w.DB.CIWatch(id)
	if err != nil || (cw.State != StatePending && cw.State != StateFailing) {
		return
	}
	target, err := w.DB.Target(cw.TargetID)
	if err != nil {
		w.finish(cw, StateError, "the target this PR was opened from no longer exists", nil)
		return
	}
	ex, err := w.Reg.For(target)
	if err != nil {
		w.transient(cw, err)
		return
	}
	info, err := viewPR(ctx, ex, cw.PRURL)
	if err != nil {
		w.ghFailed(cw, err)
		return
	}
	switch info.State {
	case "MERGED":
		w.finish(cw, StateMerged, "", nil)
		return
	case "CLOSED":
		w.finish(cw, StateClosed, "", nil)
		return
	}
	checks, err := listChecks(ctx, ex, cw.PRURL)
	if err != nil {
		w.ghFailed(cw, err)
		return
	}
	// A fix request that could not be handed to the task (its worktree was
	// cleaned up, say) will never produce a push; say so instead of waiting.
	if cw.State == StateFailing && cw.TaskID != nil && cw.AskedSHA == info.HeadSHA {
		if msg := w.undeliverable(cw); msg != "" {
			w.finish(cw, StateError, "the fix request could not be delivered: "+msg, nil)
			return
		}
	}

	now := w.now()
	shaChanged := info.HeadSHA != cw.HeadSHA
	fields := map[string]any{"head_sha": info.HeadSHA, "errors": 0, "detail": ""}
	var failing []check
	pending := false
	for _, c := range checks {
		switch {
		case c.Bucket == "pending":
			pending = true
		case c.failed():
			failing = append(failing, c)
		}
	}
	names := make([]string, 0, len(failing))
	for _, c := range failing {
		names = append(names, c.Name)
	}
	fields["failing_json"] = store.J(names)

	switch {
	case len(checks) == 0:
		since := cw.LastChangeAt
		if shaChanged {
			since = now
		}
		if now-since > dur(w.NoChecksAfter, DefaultNoChecksAfter) {
			w.finish(cw, StateNone, "no checks were reported for this pull request", fields)
			return
		}
		w.schedule(cw, StatePending, shaChanged, fields)
	case pending:
		w.schedule(cw, StatePending, shaChanged || cw.State != StatePending, fields)
	case len(failing) == 0:
		w.finish(cw, StatePassed, "", fields)
	case cw.AskedSHA == info.HeadSHA:
		// already reported for this commit; wait for the agent's push
		w.schedule(cw, StateFailing, shaChanged, fields)
	case cw.Attempts >= cw.MaxAttempts:
		w.finish(cw, StateCapped, strings.Join(names, ", "), fields)
	default:
		n := cw.Attempts + 1
		report := w.report(ctx, ex, cw, info.HeadSHA, failing, n)
		if err := w.deliver(ctx, cw, n, report); err != nil {
			if errors.Is(err, errNoRecipient) {
				w.finish(cw, StateError, err.Error(), fields)
				return
			}
			w.transient(cw, err)
			return
		}
		fields["attempts"] = n
		fields["asked_sha"] = info.HeadSHA
		w.schedule(cw, StateFailing, true, fields)
	}
}

// schedule records an active state and the next poll time: back to the
// minimum interval whenever something moved, doubling up to the maximum
// while nothing does, and giving up as stalled after StallAfter of silence.
func (w *Watcher) schedule(cw *store.CIWatch, state string, changed bool, fields map[string]any) {
	now := w.now()
	min, max := dur(w.MinInterval, DefaultMinInterval), dur(w.MaxInterval, DefaultMaxInterval)
	interval := min
	if !changed {
		interval = cw.IntervalS * 2
		if interval < min {
			interval = min
		}
		if interval > max {
			interval = max
		}
		if now-cw.LastChangeAt > dur(w.StallAfter, DefaultStallAfter) {
			w.finish(cw, StateStalled, "no progress on this pull request's checks for a day", fields)
			return
		}
	} else {
		fields["last_change_at"] = now
	}
	fields["state"] = state
	fields["interval_s"] = interval
	fields["next_poll_at"] = now + interval
	w.update(cw, fields, false)
}

func (w *Watcher) transient(cw *store.CIWatch, err error) {
	n := cw.Errors + 1
	if n >= transientErrorLimit {
		w.finish(cw, StateError, err.Error(), nil)
		return
	}
	w.Log.Info("ci loop: poll failed, will retry", "watch", cw.ID, "pr", cw.PRURL, "err", err)
	fields := map[string]any{"errors": n, "detail": clip(err.Error(), 300)}
	w.schedule(cw, cw.State, false, fields)
}

func (w *Watcher) ghFailed(cw *store.CIWatch, err error) {
	var ge *ghError
	if errors.As(err, &ge) && ge.fatal {
		w.finish(cw, StateError, ge.msg, nil)
		return
	}
	w.transient(cw, err)
}

// finish records a terminal state and sends the one notification the loop
// owes: CI green after at least one fix, or the cap reached.
func (w *Watcher) finish(cw *store.CIWatch, state, detail string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["state"] = state
	fields["detail"] = detail
	fields["next_poll_at"] = 0
	fields["last_change_at"] = w.now()
	fresh := w.update(cw, fields, true)
	if fresh == nil || w.Notifier == nil {
		return
	}
	name, link := w.ownerLabel(fresh)
	switch {
	case state == StatePassed && fresh.Attempts > 0:
		w.Notifier.Notify("CI passed", fmt.Sprintf("%s: %s is green after %d fix %s",
			name, fresh.PRURL, fresh.Attempts, plural(fresh.Attempts, "attempt", "attempts")), link, nil)
	case state == StateCapped:
		w.Notifier.Notify("CI still failing", fmt.Sprintf("%s: gave up after %d fix %s — %s",
			name, fresh.Attempts, plural(fresh.Attempts, "attempt", "attempts"), fresh.PRURL), link, nil)
	}
}

func (w *Watcher) update(cw *store.CIWatch, fields map[string]any, final bool) *store.CIWatch {
	fields["updated_at"] = w.now()
	if err := w.DB.Update("ci_watches", cw.ID, fields); err != nil {
		w.Log.Warn("ci loop: recording watch failed", "watch", cw.ID, "err", err)
		return nil
	}
	fresh, err := w.DB.CIWatch(cw.ID)
	if err != nil {
		return nil
	}
	if final || fresh.State != cw.State || fresh.Attempts != cw.Attempts || fresh.HeadSHA != cw.HeadSHA ||
		fresh.Detail != cw.Detail {
		w.publish(fresh)
	}
	return fresh
}

func (w *Watcher) publish(cw *store.CIWatch) {
	if w.Bus == nil {
		return
	}
	v := ViewOf(cw)
	w.Bus.Publish("board", "ci", v)
	if cw.TaskID != nil {
		w.Bus.Publish(fmt.Sprintf("task:%d", *cw.TaskID), "ci", v)
	}
	if cw.SessionID != nil {
		w.Bus.Publish(fmt.Sprintf("session:%d", *cw.SessionID), "ci", v)
	}
}

func (w *Watcher) ownerLabel(cw *store.CIWatch) (name, link string) {
	switch {
	case cw.TaskID != nil:
		name = fmt.Sprintf("Task #%d", *cw.TaskID)
		if t, err := w.DB.Task(*cw.TaskID); err == nil && t.Title != "" {
			name += " " + t.Title
		}
		return name, fmt.Sprintf("/#task/%d", *cw.TaskID)
	case cw.SessionID != nil:
		name = fmt.Sprintf("Session %d", *cw.SessionID)
		if s, err := w.DB.Session(*cw.SessionID); err == nil && s.Name != "" {
			name = s.Name
		}
		return name, fmt.Sprintf("/#session/%d", *cw.SessionID)
	}
	return "Pull request", "/"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// ---- the report and its delivery ------------------------------------------

// report is the message the agent receives: which checks failed, where, and
// the end of each failing job's log, trimmed and redacted.
func (w *Watcher) report(ctx context.Context, ex executor.Executor, cw *store.CIWatch,
	sha string, failing []check, n int) string {
	var b strings.Builder
	short := sha
	if len(short) > 7 {
		short = short[:7]
	}
	fmt.Fprintf(&b, "CI is failing on your pull request %s (commit %s). This is fix request %d of %d.\n\n",
		cw.PRURL, short, n, cw.MaxAttempts)
	b.WriteString("Failing checks:\n")
	for i, c := range failing {
		if i == maxChecksListed {
			fmt.Fprintf(&b, "- … and %d more\n", len(failing)-i)
			break
		}
		fmt.Fprintf(&b, "- %s", c.Name)
		if c.Bucket == "cancel" {
			b.WriteString(" (cancelled)")
		}
		if c.Link != "" {
			fmt.Fprintf(&b, " — %s", c.Link)
		}
		b.WriteString("\n")
	}
	seen := map[string]bool{}
	logged := 0
	for _, c := range failing {
		if logged >= maxJobsLogged || b.Len() >= reportBytes {
			break
		}
		if c.Link == "" || seen[c.Link] {
			continue
		}
		seen[c.Link] = true
		raw, ok := failedLog(ctx, ex, cw.PRURL, c.Link)
		if !ok {
			continue
		}
		logged++
		body := trimJobLog(raw)
		if room := reportBytes - b.Len() - 200; len(body) > room {
			if room < 200 {
				break
			}
			body = tail(body, jobTailLines, room)
		}
		fmt.Fprintf(&b, "\nLog tail for %q:\n```\n%s\n```\n", c.Name, strings.TrimSpace(body))
	}
	branch := "the pull request's branch"
	if cw.Branch != "" {
		branch = cw.Branch
	}
	fmt.Fprintf(&b, "\nPlease fix these failures, commit, and push to %s so the same pull request updates. "+
		"Lectern is watching the checks and will tell you if they still fail.\n", branch)
	return b.String()
}

var errNoRecipient = errors.New("no live agent to send the CI failure to")

// deliver hands the report to the owning agent: straight into a live
// session (a session owner, or the interactive session a task was taken over
// into), otherwise as a task message, which the scheduler turns into a
// resumed follow-up attempt in the same worktree once the task is idle.
func (w *Watcher) deliver(ctx context.Context, cw *store.CIWatch, n int, text string) error {
	if cw.SessionID != nil {
		return w.sendSession(ctx, *cw.SessionID, text)
	}
	if cw.TaskID == nil {
		return errNoRecipient
	}
	if tk, err := w.DB.Takeover(*cw.TaskID); err == nil && tk != nil && tk.SessionID != nil {
		return w.sendSession(ctx, *tk.SessionID, text)
	}
	_, err := w.DB.Exec(`INSERT INTO task_messages(task_id,request_id,text,interrupt,created_at)
		VALUES(?,?,?,0,?) ON CONFLICT(task_id,request_id) DO NOTHING`,
		*cw.TaskID, requestID(cw.ID, n), text, store.Now())
	if err != nil {
		return err
	}
	if w.Bus != nil {
		w.Bus.Publish("board", "task_message", map[string]any{"task_id": *cw.TaskID})
	}
	return nil
}

func requestID(watchID int64, n int) string { return fmt.Sprintf("ci-%d-%d", watchID, n) }

func (w *Watcher) sendSession(ctx context.Context, id int64, text string) error {
	sess, err := w.DB.Session(id)
	if err != nil || sess.EndedAt != nil || sess.Status == sessions.StatusDead || w.Sessions == nil {
		return fmt.Errorf("%w: the session has ended", errNoRecipient)
	}
	return w.Sessions.SendNotice(ctx, id, text)
}

// undeliverable reports why the latest fix request queued as a task message
// failed, or "" if it has not.
func (w *Watcher) undeliverable(cw *store.CIWatch) string {
	var status, msg string
	err := w.DB.QueryRow(`SELECT status, error FROM task_messages WHERE task_id=? AND request_id=?`,
		*cw.TaskID, requestID(cw.ID, cw.Attempts)).Scan(&status, &msg)
	if err != nil || status != "failed" {
		return ""
	}
	if msg == "" {
		msg = "the task could not take the message"
	}
	return msg
}

// ---- what the card shows ----------------------------------------------------

// View is the compact form a task or session card renders.
type View struct {
	ID          int64    `json:"id"`
	TaskID      *int64   `json:"task_id,omitempty"`
	SessionID   *int64   `json:"session_id,omitempty"`
	PRURL       string   `json:"pr_url"`
	State       string   `json:"state"`
	Attempts    int      `json:"attempts"`
	MaxAttempts int      `json:"max_attempts"`
	Failing     []string `json:"failing"`
	Detail      string   `json:"detail,omitempty"`
	Label       string   `json:"label"`
	Active      bool     `json:"active"`
	UpdatedAt   float64  `json:"updated_at"`
}

// ViewOf builds a card view of a watch.
func ViewOf(cw *store.CIWatch) *View {
	if cw == nil {
		return nil
	}
	v := &View{ID: cw.ID, TaskID: cw.TaskID, SessionID: cw.SessionID, PRURL: cw.PRURL,
		State: cw.State, Attempts: cw.Attempts, MaxAttempts: cw.MaxAttempts,
		Failing: store.UnjStrings(cw.FailingJSON), Detail: cw.Detail, Label: Label(cw),
		Active: cw.State == StatePending || cw.State == StateFailing, UpdatedAt: cw.UpdatedAt}
	if v.Failing == nil {
		v.Failing = []string{}
	}
	return v
}

// Label is the one-line status a card shows, e.g. "CI failing — attempt 2/3".
func Label(cw *store.CIWatch) string {
	of := fmt.Sprintf("%d/%d", cw.Attempts, cw.MaxAttempts)
	switch cw.State {
	case StatePending:
		if cw.Attempts > 0 {
			return "CI running — attempt " + of
		}
		return "CI running"
	case StateFailing:
		return "CI failing — attempt " + of
	case StatePassed:
		if cw.Attempts > 0 {
			return fmt.Sprintf("CI passed after %d %s", cw.Attempts, plural(cw.Attempts, "fix", "fixes"))
		}
		return "CI passed"
	case StateCapped:
		return "CI failing — gave up after " + of
	case StateMerged:
		return "PR merged"
	case StateClosed:
		return "PR closed"
	case StateNone:
		return "No CI checks"
	case StateStalled:
		return "CI watch stalled"
	case StateError:
		return "CI watch stopped"
	}
	return "CI " + cw.State
}
