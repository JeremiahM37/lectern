package triggers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// NewTaskSpec is what a matched event asks the host application to turn into
// a real task. Manager never touches internal/scheduler or internal/api
// itself — CreateTask is injected, the same pattern internal/app wires
// Scheduler.Routines/Evals with, so this package cannot import either and
// there is exactly one path onto the board regardless of who dispatched it.
type NewTaskSpec struct {
	Title, Prompt, BaseBranch, Agent, Model, CreatedBy string
	Labels                                             []string
}

// Manager owns polling GitHub/Linear and Slack's Socket Mode connections. One
// instance serves every project; sources are read fresh from the database
// each tick, so adding, editing or deleting one takes effect on the next
// poll/reconnect with no restart.
type Manager struct {
	DB  *store.DB
	Reg *executor.Registry
	Log *slog.Logger

	// CreateTask makes (and dispatches) a task for a project. See NewTaskSpec.
	CreateTask func(project *store.Project, spec NewTaskSpec) (*store.Task, error)

	// HTTP is the client Linear's GraphQL polling and Slack's REST calls use.
	// Tests point it at an httptest server.
	HTTP *http.Client

	// now is a test seam for rate-limit windows; production leaves it nil.
	now func() time.Time

	mu    sync.Mutex
	slack map[int64]*slackConn // source id -> live Socket Mode connection
}

// New builds a Manager. Reg may be nil in tests that only exercise Linear or
// Slack (neither needs a target executor).
func New(db *store.DB, reg *executor.Registry, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{DB: db, Reg: reg, Log: log, HTTP: &http.Client{Timeout: 20 * time.Second},
		slack: map[int64]*slackConn{}}
}

func (m *Manager) nowFn() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// Tick is one pass: poll every due GitHub/Linear source, reconcile Slack's
// live sockets against configuration, and post back the outcome of any
// trigger-created task that has finished since the last pass. Called from
// the scheduler's own tick loop (see internal/app wiring Scheduler.Routines),
// so — like routines — a source that isn't due yet costs one cheap query.
func (m *Manager) Tick(ctx context.Context) {
	sources, err := m.DB.AllTriggerSources()
	if err != nil {
		m.Log.Error("triggers: listing sources failed", "err", err)
		return
	}
	for _, src := range sources {
		if !src.Enabled {
			continue
		}
		switch Kind(src.Kind) {
		case KindGitHub:
			m.pollIfDue(ctx, src, m.pollGitHub)
		case KindLinear:
			m.pollIfDue(ctx, src, m.pollLinear)
		case KindSlack:
			// handled by syncSlack below, not the poll loop
		}
	}
	m.syncSlack(ctx, sources)
	m.processPostbacks(ctx)
}

func (m *Manager) pollIfDue(ctx context.Context, src *store.TriggerSource, poll func(context.Context, *store.TriggerSource) error) {
	interval := src.IntervalS
	if interval <= 0 {
		interval = defaultPollInterval
	}
	if src.LastPollAt != nil && m.nowFn().Sub(unixToTime(*src.LastPollAt)) < time.Duration(interval)*time.Second {
		return
	}
	if err := poll(ctx, src); err != nil {
		m.Log.Warn("triggers: poll failed", "source", src.ID, "kind", src.Kind, "err", err)
		if recErr := m.DB.RecordPoll(src.ID, src.CursorJSON, "error", err.Error()); recErr != nil {
			m.Log.Error("triggers: recording poll failure failed", "source", src.ID, "err", recErr)
		}
	}
}

func unixToTime(sec float64) time.Time {
	return time.Unix(0, int64(sec*float64(time.Second)))
}

// candidate is one inbound event a poller found, before allowlist/rate-limit
// gating decides what happens to it.
type candidate struct {
	ExternalID, Kind, Author, Summary string
	Title, Prompt, BaseBranch         string
	Labels                            []string
	Raw                               map[string]any
}

// intake is the one gate every candidate from every source passes through:
// dedup, then author allowlist, then per-source rate limit, then task
// creation — and the audit row every outcome leaves behind. Kind-specific
// pollers build a candidate and hand it here; this function knows nothing
// about GitHub, Slack or Linear.
//
// Dedup is enforced by RESERVING the (source_id, external_id) row before any
// decision is made — not by recording the outcome afterward — so a
// redelivery is refused before it can cause a second CreateTask call, not
// merely logged as having caused one. intake returns the event row it
// recorded, or nil when the event was a redelivery of one already claimed
// (store.ErrDuplicateEvent) — callers that want to react to the outcome
// (Slack posts an immediate reply; GitHub/Linear pollers do not need to)
// check for nil first.
func (m *Manager) intake(project *store.Project, src *store.TriggerSource, cfgAllow []string, agent, model, baseBranch string, maxPerHour int, c candidate) *store.TriggerEvent {
	id, err := m.DB.ReserveTriggerEvent(src.ID, project.ID, c.ExternalID, c.Kind, c.Author, c.Summary, store.J(c.Raw))
	if err != nil {
		if !errors.Is(err, store.ErrDuplicateEvent) {
			m.Log.Error("triggers: reserving event failed", "source", src.ID, "external_id", c.ExternalID, "err", err)
		}
		return nil
	}

	var action, reason string
	var taskID *int64
	switch {
	case !authorAllowed(c.Author, cfgAllow):
		action, reason = "skipped", "author not on this source's allowlist"
	case !m.withinRate(src, maxPerHour):
		action, reason = "skipped", "rate limit reached for this source"
	default:
		title := c.Title
		if title == "" {
			title = c.Summary
		}
		base := c.BaseBranch
		if base == "" {
			base = baseBranch
		}
		task, cerr := m.CreateTask(project, NewTaskSpec{
			Title: title, Prompt: c.Prompt, BaseBranch: base, Agent: agent, Model: model,
			Labels: c.Labels, CreatedBy: "trigger:" + src.Kind + ":" + strconv.FormatInt(src.ID, 10),
		})
		if cerr != nil {
			action, reason = "skipped", "creating task failed: "+cerr.Error()
		} else {
			action, taskID = "task_created", &task.ID
		}
	}
	if err := m.DB.FinalizeTriggerEvent(id, action, reason, taskID); err != nil {
		m.Log.Error("triggers: finalizing event failed", "source", src.ID, "external_id", c.ExternalID, "err", err)
	}
	ev, err := m.DB.TriggerEvent(id)
	if err != nil {
		m.Log.Error("triggers: reading back finalized event failed", "id", id, "err", err)
		return nil
	}
	return ev
}

func authorAllowed(author string, allowlist []string) bool {
	if strings.TrimSpace(author) == "" {
		return false
	}
	for _, a := range allowlist {
		if strings.EqualFold(strings.TrimSpace(a), author) {
			return true
		}
	}
	return false
}

// withinRate reports whether this source may act on one more event this
// hour. A source with no allowlisted authors never gets here in practice
// (authorAllowed fails first), but the check is cheap and self-contained
// either way.
func (m *Manager) withinRate(src *store.TriggerSource, maxPerHour int) bool {
	if maxPerHour <= 0 {
		maxPerHour = defaultMaxPerHour
	}
	since := float64(m.nowFn().Add(-time.Hour).UnixNano()) / 1e9
	n, err := m.DB.EventsInWindow(src.ID, since)
	if err != nil {
		m.Log.Error("triggers: rate check failed", "source", src.ID, "err", err)
		return false // fail closed: an unreadable counter must not mean unlimited spend
	}
	return n < maxPerHour
}
