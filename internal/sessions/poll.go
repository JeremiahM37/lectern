package sessions

import (
	"context"
	"fmt"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/agentevents"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// agentStateFallbackWindow is the 10 minutes docs/agent-events.md section 2
// gives a hooked session before the screen is trusted to write agent_state
// again — long enough that one slow or dropped hook delivery does not flap
// the state back to a coarser screen guess, short enough that a session whose
// hooks have genuinely stopped (agent crashed before SessionEnd, target lost
// the process) is not stuck showing a stale hook-derived state forever.
const agentStateFallbackWindow = 600 // seconds

// screenAgentState maps the screen-derived status onto agent_state, for when
// there is no fresher hook signal to prefer. It is necessarily coarser than a
// real hook: the pane cannot distinguish "waiting for a permission decision"
// from "waiting for the next prompt", so both screen-derived waits land on
// waiting_input. ok is false for StatusStarting, which has no useful
// agent_state guess yet.
func screenAgentState(status string) (state string, ok bool) {
	switch status {
	case StatusRunning:
		return agentevents.StateWorking, true
	case StatusWaiting:
		return agentevents.StateWaitingInput, true
	case StatusIdle:
		return agentevents.StateIdle, true
	case StatusDead:
		return agentevents.StateEnded, true
	default:
		return "", false
	}
}

// Poll refreshes every live session's status from its target.
//
// It batches by target: one capture command per machine per tick, however many
// sessions are on it. Status is derived from what the pane is actually doing —
// see DeriveStatus — and `last_activity_at` is only moved when the pane really
// changed, because "quiet for 40 minutes" is the number an operator acts on.
func (m *Manager) Poll(ctx context.Context) {
	m.pollMu.Lock()
	m.RecoverWorkspaceOperations(ctx)
	groups := m.pollGroups(ctx, nil)
	m.pollMu.Unlock()
	wait := m.PollWait
	if wait <= 0 {
		wait = DefaultPollWait
	}
	m.pollTargets(ctx, groups, wait)
}

// PrepareStartup performs the fast, local part of restart reconciliation before
// the HTTP server is exposed. It only reads SQLite and starts checkpoint
// workers; target probes and recovery launches remain in the scheduler's
// asynchronous poll so a slow SSH target cannot hide the API after boot.
func (m *Manager) PrepareStartup() error {
	m.pollMu.Lock()
	defer m.pollMu.Unlock()
	live, err := m.DB.LiveSessions()
	if err != nil {
		return err
	}
	m.startCheckpointsForLiveRows(live)
	return nil
}

// Startup performs one bounded reconciliation for callers that explicitly
// need synchronous startup behavior. The hosted app uses PrepareStartup and
// lets the scheduler perform the remote poll asynchronously.
func (m *Manager) Startup(ctx context.Context) error {
	m.pollMu.Lock()
	m.RecoverWorkspaceOperations(ctx)
	live, err := m.DB.LiveSessions()
	if err != nil {
		m.pollMu.Unlock()
		return err
	}
	m.startCheckpointsForLiveRows(live)
	groups := m.pollGroups(ctx, nil)
	m.pollMu.Unlock()
	// every target's own timeout bounds this
	m.pollTargets(ctx, groups, 0)
	return nil
}

// Action refreshes visit only the affected target, not every machine in the
// fleet, and wait for it.
func (m *Manager) pollTarget(ctx context.Context, targetID int64) {
	m.pollMu.Lock()
	groups := m.pollGroups(ctx, &targetID)
	m.pollMu.Unlock()
	for id, group := range groups {
		m.pollOneTarget(ctx, id, group, false)
	}
}

// pollGroups reads the live sessions worth polling, grouped by target.
func (m *Manager) pollGroups(ctx context.Context, onlyTarget *int64) map[int64][]*store.Session {
	live, err := m.DB.LiveSessions()
	if err != nil || len(live) == 0 {
		return nil
	}
	// The manager may have been constructed after the agents were launched (for
	// example, the service restarted). Ensure those rows receive the same native
	// identity checkpoint as a fresh launch.
	m.startCheckpointsForLiveRows(live)
	byTarget := map[int64][]*store.Session{}
	now := store.Now()
	for _, s := range live {
		if onlyTarget != nil && s.TargetID != *onlyTarget {
			continue
		}
		// A shell is inserted before its target-side tmux command so it remains
		// trackable if the command or DB update fails. Do not let the first poll
		// race that short launch window and report a false death.
		if s.Agent == "shell" && s.Status == StatusStarting && now-s.CreatedAt < 45 {
			continue
		}
		if s.SetupState == "creating" {
			if !m.setupActive(s.ID) {
				m.recoverSetup(ctx, s)
			}
			continue
		}
		if s.Status == StatusInterrupted {
			continue
		}
		byTarget[s.TargetID] = append(byTarget[s.TargetID], s)
	}
	return byTarget
}

// pollTargets polls every target on its own worker, at most
// MaxConcurrentTargetPolls at once, and waits up to wait (0 = until all are
// done) for them. A target whose previous poll has not finished, or that is
// backing off after failures, is skipped this round; the others are not
// held up by it.
func (m *Manager) pollTargets(ctx context.Context, groups map[int64][]*store.Session, wait time.Duration) {
	r := m.reach()
	done := make(chan struct{}, len(groups))
	started := 0
	for id, group := range groups {
		if !r.begin(id, time.Now()) {
			continue
		}
		started++
		go func(id int64, group []*store.Session) {
			defer func() { done <- struct{}{} }()
			select {
			case r.sem <- struct{}{}:
			case <-ctx.Done():
				r.release(id)
				return
			}
			defer func() { <-r.sem }()
			m.pollOneTarget(ctx, id, group, true)
		}(id, group)
	}
	var timeout <-chan time.Time
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		timeout = timer.C
	}
	for i := 0; i < started; i++ {
		select {
		case <-done:
		case <-timeout:
			return
		case <-ctx.Done():
			return
		}
	}
}

// pollOneTarget runs one target's poll under its own timeout and records
// whether the target answered. round is true when pollTargets claimed the
// target (begin) for this call; an action refresh polls without a claim, but
// never at the same time as a round, because both hold the target's lock.
func (m *Manager) pollOneTarget(ctx context.Context, targetID int64, group []*store.Session, round bool) {
	r := m.reach()
	st := r.state(targetID)
	st.mu.Lock()
	defer st.mu.Unlock()
	timeout := m.TargetPollTimeout
	if timeout <= 0 {
		timeout = DefaultTargetPollTimeout
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// A poll that is slow to answer shows as unreachable before it times out.
	slow := time.AfterFunc(UnreachableAfter, func() { m.publishReach(targetID) })
	started := time.Now()
	errText := m.pollTargetGroup(tctx, targetID, group)
	slow.Stop()
	if r.finish(targetID, started, time.Now(), errText, round) {
		if errText != "" {
			m.Log.Warn("target unreachable; its sessions keep their last status", "target", targetID, "err", errText)
		} else {
			m.Log.Info("target reachable again", "target", targetID)
		}
		m.publishReach(targetID)
	}
}

// publishReach tells clients a target's reachability changed; the board
// refetches its sessions, whose rows carry the new state.
func (m *Manager) publishReach(targetID int64) {
	if m.Bus == nil {
		return
	}
	m.Bus.Publish("board", "target_reach", map[string]any{"target_id": targetID, "reach": m.Reach(targetID)})
}

// pollTargetGroup polls one target's sessions and returns why the target
// could not be read, or "" when it answered.
func (m *Manager) pollTargetGroup(ctx context.Context, targetID int64, group []*store.Session) string {
	target, err := m.DB.Target(targetID)
	if err != nil {
		return "" // the target was deleted; nothing to report about it
	}
	ex, err := m.Reg.For(target)
	if err != nil {
		return err.Error()
	}
	boot, known := ProbeBootID(ctx, ex)
	if ctx.Err() != nil {
		return "not answering"
	}
	if known {
		m.recoverAfterBoot(ctx, target, ex, group, boot)
		// Recovery may have ended a row or replaced its tmux process. Do not
		// apply the pre-recovery snapshot to stale pointers: that can mark a
		// freshly relaunched session dead immediately.
		fresh, ferr := m.DB.LiveSessions()
		if ferr != nil {
			return ""
		}
		group = group[:0]
		for _, candidate := range fresh {
			if candidate.TargetID == targetID && candidate.SetupState != "creating" && candidate.Status != StatusInterrupted {
				group = append(group, candidate)
			}
		}
		if len(group) == 0 {
			return ""
		}
	}
	names := make([]string, 0, len(group))
	for _, s := range group {
		names = append(names, s.TmuxSession)
	}
	r, err := ex.Run(ctx, PollCommand(names), executor.RunOpts{Timeout: 45})
	if err != nil || !r.OK() {
		// an unreachable target is not evidence a session died; leave the
		// rows alone and try again after a backoff
		m.Log.Debug("session poll failed", "target", target.Name, "err", err, "exit_code", r.RC)
		if err != nil {
			if ctx.Err() != nil {
				return "not answering"
			}
			return err.Error()
		}
		return fmt.Sprintf("status poll failed (exit %d)", r.RC)
	}
	panes, complete := ParsePollSnapshot(r.Stdout, names)
	if !complete {
		m.Log.Debug("incomplete session poll", "target", target.Name)
		return "incomplete status poll"
	}
	for _, s := range group {
		pane := panes[s.TmuxSession]
		if pane.Failed {
			continue
		}
		// A successful missing-pane result cannot distinguish a reboot from
		// a dead process while the target's boot identity is unavailable.
		// Keep a Lectern-owned row recoverable until a known boot probe
		// establishes that decision.
		if !known && pane.Missing && s.Origin == "lectern" && s.BootID != "" {
			continue
		}
		// Older Lectern rows may predate boot checkpoints. Bind one only
		// after a known boot and a live, identity-bound pane are observed;
		// a missing pane must remain unresolved until a later probe.
		if known && s.BootID == "" && !pane.Missing && s.Origin == "lectern" && validTrackingIdentity(s.TrackingIdentity) {
			identity, identityErr := ProbeTrackingIdentity(ctx, ex, s.TmuxSession)
			if identityErr == nil && identity == s.TrackingIdentity {
				if err := m.DB.Update("sessions", s.ID, map[string]any{"boot_id": boot, "updated_at": store.Now()}); err == nil {
					s.BootID = boot
				}
			}
		}
		m.applyPane(s, pane.Text, pane.Missing)
	}
	m.pollCodexUsage(ctx, ex, group)
	return ""
}

// applyPane folds one capture into a session row, publishing only on a real
// change so the board's SSE stream stays quiet while nothing is happening.
func (m *Manager) applyPane(s *store.Session, pane string, missing bool) {
	if s.SetupState == "creating" {
		if m.setupActive(s.ID) {
			return
		}
		current, err := m.DB.Session(s.ID)
		if err != nil || current.SetupState != "creating" {
			return // A snapshot taken before setup completed is not an interruption.
		}
		// A persisted reservation without this process's worker was interrupted.
		// Keep any target allocation for inspection; do not launch twice.
		now := store.Now()
		if err := m.DB.Update("sessions", s.ID, map[string]any{"setup_state": "failed", "setup_error": "Setup was interrupted; inspect the workspace before retrying", "status": StatusDead, "ended_at": now, "updated_at": now, "end_reason": EndFailed}); err == nil {
			m.IsolationProxies.Stop(s.ID)
			if fresh, err := m.DB.Session(s.ID); err == nil {
				m.publish(fresh)
			}
		}
		return
	}
	now := store.Now()
	fields := map[string]any{"updated_at": now}
	var status string

	if missing {
		// Only an explicit missing-session response establishes absence. A brand-new session gets a grace period
		// before we call it dead, since launch and first paint are not instant.
		if s.Status == StatusStarting && now-s.CreatedAt < 20 {
			return
		}
		status = StatusDead
		fields["ended_at"] = now
		fields["end_reason"] = EndExited
	} else {
		status = DeriveStatus(pane, s.PaneHash)
		hash := Hash(pane)
		if hash != s.PaneHash {
			fields["pane_hash"] = hash
			fields["pane_tail"] = Preview(pane, 8)
			// The FIRST time we see a pane we cannot know it just changed — an
			// adopted session may have been sitting there for days. Moving the
			// activity clock here would reset every adopted session to "quiet
			// 0s" the moment lectern noticed it.
			if s.PaneHash != "" {
				fields["last_activity_at"] = now
			}
		}
		if pct := ContextPct(pane); pct != nil {
			fields["context_pct"] = *pct
		}
	}
	// Agent hooks (docs/agent-events.md section 2): agent_state is
	// hook-preferred. The screen is only allowed to write it when no hook has
	// reached this session in the last 10 minutes (or ever) — status itself
	// keeps being derived from the pane exactly as before, unconditionally,
	// so nothing here changes what the UI's primary indicator shows.
	if screenState, ok := screenAgentState(status); ok {
		hookFresh := s.HookSeenAt != nil && now-*s.HookSeenAt < agentStateFallbackWindow
		if !hookFresh && (screenState != s.AgentState || s.StateSource != agentevents.SourceScreen) {
			fields["agent_state"] = screenState
			fields["state_source"] = agentevents.SourceScreen
			fields["state_at"] = now
		}
	}
	changed := status != s.Status || len(fields) > 1
	fields["status"] = status
	if err := m.DB.Update("sessions", s.ID, fields); err != nil {
		return
	}
	if !changed {
		return
	}
	// Checks fallback (docs/agent-events.md section 4): a session with no
	// lifecycle hooks (or whose hooks have gone quiet) never gets a Stop
	// event, so busy->idle on the screen-derived status is the only signal
	// it has that a turn just ended. A hooked session's real Stop event
	// fires this same runner independently through internal/agentevents; the
	// runner's own fingerprint is what keeps the two from double-running.
	if m.Checks != nil && s.Status == StatusRunning && status == StatusIdle {
		m.Checks.OnAgentStop(s.ID)
	}
	if status == StatusDead && s.Status != StatusDead {
		m.IsolationProxies.Stop(s.ID)
	}
	if fresh, err := m.DB.Session(s.ID); err == nil {
		m.publish(fresh)
		if status == StatusDead && s.Status != StatusDead {
			m.Log.Info("session ended", "session", s.ID, "name", s.Name)
		}
	}
}

// IdleFor is how long a session has been quiet — the honest signal behind every
// status label, and the one the UI shows next to it.
func IdleFor(s *store.Session) time.Duration {
	last := s.CreatedAt
	if s.LastActivityAt != nil && *s.LastActivityAt > last {
		last = *s.LastActivityAt
	}
	d := time.Duration((store.Now() - last) * float64(time.Second))
	if d < 0 {
		return 0
	}
	return d
}
