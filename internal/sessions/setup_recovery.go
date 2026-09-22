package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/shellq"
	"github.com/JeremiahM37/lectern/internal/store"
	"github.com/JeremiahM37/lectern/internal/worktree"
	"strings"
)

// The marker is set by tmux new-session itself, before the launch client can
// disappear. It identifies the exact reserved allocation without relying on a
// reused session name or launching another agent during recovery.
func (m *Manager) setupLaunchToken(id int64) (string, error) {
	row, err := m.DB.Session(id)
	if err != nil {
		return "", err
	}
	if row.SetupState != "creating" {
		return "", nil
	}
	var plan worktree.Interactive
	if json.Unmarshal([]byte(row.WorktreeJSON), &plan) != nil || !validTrackingIdentity(plan.Token) || plan.State != "ready" {
		return "", fmt.Errorf("completed setup allocation is unavailable for terminal identity")
	}
	return plan.Token, nil
}

func (m *Manager) recoverSetup(ctx context.Context, row *store.Session) {
	if m.setupActive(row.ID) {
		return
	}
	current, err := m.DB.Session(row.ID)
	if err != nil || current.SetupState != "creating" {
		return
	}
	row = current
	var plan worktree.Interactive
	if json.Unmarshal([]byte(row.WorktreeJSON), &plan) != nil || plan.State != "ready" || !validTrackingIdentity(plan.Token) {
		// The controller records completed checkout before issuing an agent launch.
		m.applyPane(row, "", true)
		return
	}
	pending := func(message string) { m.saveSetupRecovery(row.ID, map[string]any{"setup_error": message}) }
	_, ex, err := m.resolve(row.ID)
	if err != nil {
		pending("Lectern restarted; target unavailable while checking whether the agent started")
		return
	}
	result, err := ex.Run(ctx, PollCommand([]string{row.TmuxSession}), executor.RunOpts{Timeout: 15})
	if err != nil || !result.OK() {
		pending("Lectern restarted; target unavailable while checking whether the agent started")
		return
	}
	panes, complete := ParsePollSnapshot(result.Stdout, []string{row.TmuxSession})
	pane := panes[row.TmuxSession]
	if !complete || pane.Failed {
		pending("Lectern restarted; terminal status could not be verified yet")
		return
	}
	if pane.Missing {
		m.applyPane(row, "", true)
		return
	}
	marker := func() (bool, bool) {
		result, err := ex.Run(ctx, "tmux show-environment -t "+shellq.Quote("="+row.TmuxSession+":")+" LECTERN_SETUP_TOKEN", executor.RunOpts{Timeout: 10})
		if err != nil {
			return false, false
		}
		if !result.OK() {
			return false, result.RC == 1 && strings.Contains(result.Stderr, "unknown variable")
		}
		return strings.TrimSpace(result.Stdout) == "LECTERN_SETUP_TOKEN="+plan.Token, true
	}
	matched, verified := marker()
	if !verified {
		pending("Lectern restarted; target unavailable while verifying terminal ownership")
		return
	}
	if !matched {
		m.saveSetupRecovery(row.ID, map[string]any{"setup_state": "failed", "setup_error": "A terminal uses this setup's name, but its ownership could not be verified. It was left untouched; inspect it before restoring tracking.", "status": StatusDead, "ended_at": store.Now()})
		return
	}
	identity := captureTrackingIdentity(ctx, ex, row.TmuxSession)
	matched, verified = marker()
	if !validTrackingIdentity(identity) || !matched || !verified {
		pending("Lectern restarted; terminal identity changed during recovery, retrying verification")
		return
	}
	m.saveSetupRecovery(row.ID, map[string]any{"setup_state": "ready", "setup_error": "", "status": StatusStarting, "ended_at": nil, "tracking_identity": identity})
}

func (m *Manager) saveSetupRecovery(id int64, fields map[string]any) {
	m.mu.Lock()
	if m.activeSetups[id] {
		m.mu.Unlock()
		return
	}
	row, err := m.DB.Session(id)
	if err == nil && row.SetupState == "creating" {
		err = m.DB.Update("sessions", id, fields)
	}
	m.mu.Unlock()
	if err == nil {
		if fresh, e := m.DB.Session(id); e == nil {
			m.publish(fresh)
		}
	}
}
