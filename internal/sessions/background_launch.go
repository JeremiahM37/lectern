package sessions

import (
	"context"
	"fmt"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func (m *Manager) setupActive(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeSetups[id]
}

// LaunchBackground returns a durable session reservation before target setup.
// The worker outlives the initiating HTTP request, with a bounded total lifetime.
func (m *Manager) LaunchBackground(ctx context.Context, options LaunchOpts) (*store.Session, error) {
	if options.Worktree == nil || options.ReservedID != 0 {
		return nil, fmt.Errorf("background setup requires a new isolated workspace")
	}
	type reply struct {
		session *store.Session
		err     error
	}
	ready := make(chan reply, 1)
	options.SetupTimeout = 30 * 60
	workerContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
	go func() {
		defer cancel()
		var reserved int64
		options.OnReserved = func(session *store.Session) error {
			m.mu.Lock()
			if m.activeSetups == nil {
				m.activeSetups = map[int64]bool{}
			}
			m.activeSetups[session.ID] = true
			m.mu.Unlock()
			reserved = session.ID
			if err := m.DB.Update("sessions", reserved, map[string]any{"setup_state": "creating", "setup_error": ""}); err != nil {
				return err
			}
			row, err := m.DB.Session(reserved)
			if err != nil {
				return err
			}
			ready <- reply{session: row}
			m.publish(row)
			return nil
		}
		row, err := m.Launch(workerContext, options)
		if reserved == 0 {
			ready <- reply{session: row, err: err}
			return
		}
		state, message := "ready", ""
		if err != nil {
			state, message = "failed", err.Error()
			m.end(reserved, StatusDead)
		}
		if saveErr := m.DB.Update("sessions", reserved, map[string]any{"setup_state": state, "setup_error": message}); saveErr != nil {
			m.Log.Error("could not save workspace setup result", "session", reserved, "err", saveErr)
		}
		m.mu.Lock()
		delete(m.activeSetups, reserved)
		delete(m.setupLaunching, reserved)
		m.mu.Unlock()
		if fresh, loadErr := m.DB.Session(reserved); loadErr == nil {
			m.publish(fresh)
		}
		// Also unblock callers if reservation persistence failed before publication.
		select {
		case ready <- reply{session: row, err: err}:
		default:
		}
	}()
	select {
	case result := <-ready:
		return result.session, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
