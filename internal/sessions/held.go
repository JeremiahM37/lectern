package sessions

import (
	"context"
	"fmt"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/sessions/backend"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// captureHeld captures each row's terminal through the backend that holds it
// (backend.ForSession), keyed by tmux name, and fails as a whole when any
// backend's answer is unusable — the same all-or-nothing rule as
// ParsePollSnapshot.
//
// A terminal its own backend reports missing counts as gone only when no
// other backend on the target has a session of that name. What the target
// picks for new sessions can change (a setting, tmux installed or removed),
// and a session polled through a backend that does not hold it reads as
// missing: on 2026-10-01 that ended eight live tmux sessions when a stray
// PTY host made the server switch. Where another backend does hold it, the
// row is corrected to that backend; a row from before backends were recorded
// gets the one it answered in. When the other backends cannot be asked, the
// terminal reads as unknown (Failed), never as missing.
func (m *Manager) captureHeld(ctx context.Context, ex executor.Executor, rows []*store.Session, lines int, timeout float64) (map[string]PollCapture, error) {
	type group struct {
		be   backend.Backend
		rows []*store.Session
	}
	groups := map[string]*group{}
	var order []string
	panes := map[string]PollCapture{}
	seen := map[string]bool{}
	for _, s := range rows {
		if seen[s.TmuxSession] {
			continue
		}
		seen[s.TmuxSession] = true
		be, ok := backend.ForSession(ex, s)
		if !ok {
			// Its backend cannot be driven here now; nothing can tell
			// whether it is still running.
			panes[s.TmuxSession] = PollCapture{Failed: true}
			continue
		}
		g := groups[be.Name()]
		if g == nil {
			g = &group{be: be}
			groups[be.Name()] = g
			order = append(order, be.Name())
		}
		g.rows = append(g.rows, s)
	}
	for _, name := range order {
		g := groups[name]
		got, err := runPoll(ctx, ex, g.be, sessionNames(g.rows), lines, timeout)
		if err != nil {
			return nil, err
		}
		var missing []*store.Session
		for _, s := range g.rows {
			pane := got[s.TmuxSession]
			switch {
			case pane.Missing:
				missing = append(missing, s)
				continue
			case !pane.Failed && s.SessionBackend == "":
				m.recordBackend(s, name)
			}
			panes[s.TmuxSession] = pane
		}
		for _, s := range missing {
			panes[s.TmuxSession] = PollCapture{Missing: true}
		}
		if len(missing) == 0 {
			continue
		}
		for _, other := range backend.Others(ex, name) {
			got, err := runPoll(ctx, ex, other, sessionNames(missing), lines, timeout)
			if err != nil {
				for _, s := range missing {
					panes[s.TmuxSession] = PollCapture{Failed: true}
				}
				break
			}
			var still []*store.Session
			for _, s := range missing {
				pane := got[s.TmuxSession]
				switch {
				case pane.Missing:
					still = append(still, s)
				case pane.Failed:
					panes[s.TmuxSession] = pane
				default:
					m.Log.Warn("session is held by another backend than recorded; following it",
						"session", s.ID, "name", s.TmuxSession, "recorded", s.SessionBackend, "found", other.Name())
					m.recordBackend(s, other.Name())
					panes[s.TmuxSession] = pane
				}
			}
			missing = still
			if len(missing) == 0 {
				break
			}
		}
	}
	return panes, nil
}

// runPoll runs one backend's batched poll and validates the whole answer.
func runPoll(ctx context.Context, ex executor.Executor, be backend.Backend, names []string, lines int, timeout float64) (map[string]PollCapture, error) {
	r, err := ex.Run(ctx, be.Poll(names, lines), executor.RunOpts{Timeout: timeout})
	if err != nil {
		return nil, err
	}
	if !r.OK() {
		return nil, fmt.Errorf("status poll failed (exit %d)", r.RC)
	}
	panes, complete := ParsePollSnapshot(r.Stdout, names)
	if !complete {
		return nil, fmt.Errorf("incomplete status poll")
	}
	return panes, nil
}

// recordBackend notes the backend a session was found in, on its row and on
// the copy in hand, so every later command reaches it there.
func (m *Manager) recordBackend(s *store.Session, name string) {
	if s.SessionBackend == name {
		return
	}
	if err := m.DB.Update("sessions", s.ID, map[string]any{"session_backend": name}); err == nil {
		s.SessionBackend = name
	}
}

func sessionNames(rows []*store.Session) []string {
	names := make([]string, 0, len(rows))
	for _, s := range rows {
		names = append(names, s.TmuxSession)
	}
	return names
}

// sessionBackend is the backend a command for this session goes to; see
// backend.SessionOrTarget.
func sessionBackend(ex executor.Executor, s *store.Session) backend.Backend {
	return backend.SessionOrTarget(ex, s)
}
