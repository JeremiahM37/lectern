package sessions

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Restarting sessions in place (docs/sessions-restart.md). The agent's
// terminal is stopped and the same row is launched again with the CLI's
// resume-by-id flag, in the same workspace, under the same tmux name, with its
// saved launch settings: the path an account swap and restart recovery take.
// Name, project, group, worktree and the conversation (with its Grimoire
// briefing) stay; a claude session that can use Remote Control comes back
// with it (RemoteControlByDefault).

// Restart outcomes, beside the Remote Control ones they share a shape with.
const (
	RestartDone    = "restarted"  // running again, same conversation
	RestartQueued  = "restarting" // idle; restarted now, one at a time
	RestartPending = "pending"    // busy; restarted once it waits for you
	RestartSkipped = "skipped"    // cannot be restarted in place (Reason)
	RestartFailed  = "failed"
)

// RestartOutcome is what happened to one session.
type RestartOutcome struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// errRestartBusy defers a restart that would interrupt the agent or lose
// text typed at its prompt.
var errRestartBusy = errors.New("busy: it is restarted once the agent is waiting for you")

// restartBlocked says why a session cannot be restarted in place, or "".
func (m *Manager) restartBlocked(sess *store.Session) (string, *LaunchConfiguration) {
	switch {
	case sess.EndedAt != nil || sess.Status == StatusDead:
		return "it has ended", nil
	case sess.Agent == "shell":
		return "a shell has no conversation to resume", nil
	case sess.Origin != "lectern" || sess.TmuxSession == "":
		return "Lectern did not start it", nil
	case firstNonEmpty(sess.NativeRecoveryCID, sess.ResumeID) == "":
		return "its conversation id is not known yet, so a restart would start a new conversation", nil
	}
	cfg, err := m.SessionLaunchConfiguration(sess)
	if err != nil {
		return err.Error(), nil
	}
	if len(cfg.Spec.ResumeIDArgs) == 0 {
		return sess.Agent + " cannot resume a conversation by id", nil
	}
	return "", cfg
}

// restartBusy is remoteControlBusy plus, for claude, text typed at the prompt
// that a restart would throw away.
func (m *Manager) restartBusy(ctx context.Context, sess *store.Session) bool {
	if remoteControlBusy(sess, store.Now()) || sess.AgentExitedAt != nil {
		return true
	}
	if sess.Agent != "claude" {
		return false
	}
	_, ex, err := m.resolve(sess.ID)
	if err != nil {
		return true
	}
	typed, ok := remoteControlPromptText(m.promptScreen(ctx, ex, sess))
	return !ok || typed != ""
}

// RestartInPlace stops one session's agent and resumes its conversation in the
// same session. It refuses (errRestartBusy) while the agent is working, asking
// or has text typed at its prompt, and never stops one mid-turn.
func (m *Manager) RestartInPlace(ctx context.Context, id int64) error {
	sess, ex, err := m.resolve(id)
	if err != nil {
		return err
	}
	why, cfg := m.restartBlocked(sess)
	if why != "" {
		return errors.New(why)
	}
	if m.restartBusy(ctx, sess) {
		return errRestartBusy
	}
	cid := firstNonEmpty(sess.NativeRecoveryCID, sess.ResumeID)
	// Hold the target's poll lock across stop and relaunch, so no status poll
	// sees the gap between the two terminals and ends the session.
	st := m.reach().state(sess.TargetID)
	st.mu.Lock()
	m.lifecycleMu.Lock()
	err = func() error {
		row, err := m.DB.Session(id)
		if err != nil {
			return err
		}
		if row.EndedAt != nil || row.TmuxSession != sess.TmuxSession || row.TargetID != sess.TargetID {
			return fmt.Errorf("the session changed; refresh and retry")
		}
		if remoteControlBusy(row, store.Now()) {
			return errRestartBusy
		}
		m.stopNativeCheckpoint(id)
		if err := m.stopProcess(ctx, ex, row); err != nil {
			m.startNativeCheckpoint(id, row.Agent, row.Workdir, nativeHome(cfg.Spec, row.Agent), row.TmuxSession)
			return err
		}
		m.IsolationProxies.Stop(id)
		_, err = m.launch(ctx, LaunchOpts{ReservedID: id, Configuration: cfg, TargetID: row.TargetID,
			ProjectID: row.ProjectID, GroupPath: row.GroupPath, Name: row.Name, Agent: row.Agent,
			Model: row.Model, Workdir: row.Workdir, ResumeID: row.ResumeID, RecoveryCID: cid,
			AccountID: row.AccountID, SkipPrime: true})
		if err != nil {
			return fmt.Errorf("the agent stopped but did not start again: %w", err)
		}
		return nil
	}()
	m.lifecycleMu.Unlock()
	st.mu.Unlock()
	if err != nil {
		return err
	}
	m.Log.Info("session restarted in place", "session", id)
	waitCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	ready := m.waitReady(waitCtx, id)
	if m.agentExited(ctx, ex, id) {
		return fmt.Errorf("%w right after its restart (did it find the conversation?)", ErrAgentExited)
	}
	if !ready {
		return fmt.Errorf("restarted, but the resumed agent did not show a prompt yet")
	}
	return nil
}

// RestartPlan is what RestartAll would do, without doing it: the confirm
// dialog's counts.
func (m *Manager) RestartPlan(ctx context.Context) ([]RestartOutcome, error) {
	return m.restartAll(ctx, false)
}

// RestartAll queues every live session that can be restarted in place and
// returns at once: idle ones are restarted one at a time in the background,
// busy ones once each waits for you (RetryPending). RestartStatus follows it.
func (m *Manager) RestartAll(ctx context.Context) ([]RestartOutcome, error) {
	out, err := m.restartAll(ctx, true)
	if err == nil {
		go m.RetryPending(context.WithoutCancel(ctx))
	}
	return out, err
}

func (m *Manager) restartAll(ctx context.Context, apply bool) ([]RestartOutcome, error) {
	live, err := m.DB.LiveSessions()
	if err != nil {
		return nil, err
	}
	out := []RestartOutcome{}
	for _, sess := range live {
		row := RestartOutcome{ID: sess.ID, Name: sess.Name}
		if why, _ := m.restartBlocked(sess); why != "" {
			row.Status, row.Reason = RestartSkipped, why
		} else if m.restartBusy(ctx, sess) {
			row.Status, row.Reason = RestartPending, errRestartBusy.Error()
		} else {
			row.Status = RestartQueued
		}
		if apply {
			m.mu.Lock()
			if m.restartResults == nil {
				m.restartResults = map[int64]RestartOutcome{}
			}
			m.restartResults[row.ID] = row
			m.mu.Unlock()
			if row.Status != RestartSkipped {
				m.markRestartPending(row.ID, true)
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// RestartStatus is the latest "restart all" run, one row per session it saw.
func (m *Manager) RestartStatus() []RestartOutcome {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RestartOutcome, 0, len(m.restartResults))
	for _, row := range m.restartResults {
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (m *Manager) restartOne(ctx context.Context, row RestartOutcome) RestartOutcome {
	m.mu.Lock()
	if m.restartResults != nil {
		m.restartResults[row.ID] = RestartOutcome{ID: row.ID, Name: row.Name, Status: RestartQueued}
	}
	m.mu.Unlock()
	err := m.RestartInPlace(ctx, row.ID)
	switch {
	case errors.Is(err, errRestartBusy):
		m.markRestartPending(row.ID, true)
		row.Status, row.Reason = RestartPending, err.Error()
	case err != nil:
		m.markRestartPending(row.ID, false)
		row.Status, row.Reason = RestartFailed, err.Error()
	default:
		m.markRestartPending(row.ID, false)
		row.Status = RestartDone
	}
	m.mu.Lock()
	if m.restartResults != nil {
		m.restartResults[row.ID] = row
	}
	m.mu.Unlock()
	return row
}

func (m *Manager) markRestartPending(id int64, on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !on {
		delete(m.restartPending, id)
		return
	}
	if m.restartPending == nil {
		m.restartPending = map[int64]bool{}
	}
	m.restartPending[id] = true
}

// retryRestarts restarts each pending session that is now waiting for you.
func (m *Manager) retryRestarts(ctx context.Context) {
	m.mu.Lock()
	ids := make([]int64, 0, len(m.restartPending))
	for id := range m.restartPending {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		sess, err := m.DB.Session(id)
		if err != nil || sess.EndedAt != nil || sess.Status == StatusDead {
			m.markRestartPending(id, false)
			continue
		}
		if why, _ := m.restartBlocked(sess); why != "" {
			m.markRestartPending(id, false)
			m.mu.Lock()
			m.restartResults[id] = RestartOutcome{ID: id, Name: sess.Name, Status: RestartSkipped, Reason: why}
			m.mu.Unlock()
			continue
		}
		if m.restartBusy(ctx, sess) {
			// Idle when the run started, busy by its turn: it waits like
			// any busy session, and says so instead of "restarting".
			m.mu.Lock()
			if row, ok := m.restartResults[id]; ok && row.Status == RestartQueued {
				row.Status, row.Reason = RestartPending, errRestartBusy.Error()
				m.restartResults[id] = row
			}
			m.mu.Unlock()
			continue
		}
		row := m.restartOne(ctx, RestartOutcome{ID: id, Name: sess.Name})
		m.Log.Info("restart all sessions", "session", id, "status", row.Status, "reason", row.Reason)
	}
}
