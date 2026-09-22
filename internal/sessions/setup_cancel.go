package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/JeremiahM37/lectern/internal/worktree"
)

func (m *Manager) checkSetupCancellation(id int64) error {
	row, err := m.DB.Session(id)
	if err != nil {
		return err
	}
	if row.SetupCancelRequested {
		return fmt.Errorf("Workspace setup cancelled; allocated files retained for inspection")
	}
	return nil
}

func (m *Manager) beginSetupAgent(id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkSetupCancellation(id); err != nil {
		return err
	}
	if m.activeSetups[id] {
		if m.setupLaunching == nil {
			m.setupLaunching = map[int64]bool{}
		}
		m.setupLaunching[id] = true
	}
	return nil
}

// CancelSetup records intent before contacting the target. The worker checks
// that durable intent before starting an agent even if target delivery fails.
func (m *Manager) CancelSetup(ctx context.Context, id int64) error {
	row, err := m.DB.Session(id)
	if err != nil {
		return err
	}
	if row.SetupState == "creating" && !m.setupActive(id) {
		m.recoverSetup(ctx, row)
	}
	m.mu.Lock()
	row, err = m.DB.Session(id)
	if err == nil && (m.setupLaunching[id] || (row.SetupState != "creating" && row.SetupState != "failed")) {
		err = fmt.Errorf("setup is no longer cancellable; use the session's End action if an agent has started")
	}
	if err == nil && row.SetupState == "creating" && !m.activeSetups[id] {
		err = fmt.Errorf("setup recovery is still checking the target; retry after terminal ownership is verified")
	}
	if err == nil {
		err = m.DB.Update("sessions", id, map[string]any{"setup_cancel_requested": true})
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if fresh, e := m.DB.Session(id); e == nil {
		m.publish(fresh)
	}
	// Before allocation, the launcher's pre-checkout check observes the flag.
	if row.WorktreeJSON == "" {
		return nil
	}
	var plan worktree.Interactive
	if err := json.Unmarshal([]byte(row.WorktreeJSON), &plan); err != nil {
		return fmt.Errorf("cancellation recorded, but workspace allocation is unreadable")
	}
	if plan.State == "removed" {
		return nil
	}
	_, ex, err := m.resolve(id)
	if err != nil {
		return fmt.Errorf("cancellation recorded, but target delivery failed; retry cancellation: %w", err)
	}
	delivery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := worktree.RunInteractive(delivery, ex, "cancel", &plan); err != nil {
		return fmt.Errorf("cancellation recorded, but target delivery failed; retry cancellation: %w", err)
	}
	return nil
}
