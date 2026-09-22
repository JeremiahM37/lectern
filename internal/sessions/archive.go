package sessions

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/shellq"
	"github.com/JeremiahM37/lectern/internal/store"
)

// Archive retains a record and terminal snapshot after proving the terminal has
// stopped. A live tracked terminal requires an explicit stop request.
func (m *Manager) Archive(ctx context.Context, id int64, stop bool) (*store.Session, error) {
	if m.setupActive(id) {
		return nil, fmt.Errorf("workspace setup is still running; inspect its progress before archiving")
	}
	// Archiving with stop=true has the same race boundary as Kill: recovery must
	// not launch a replacement between the absence check and the archive claim.
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	s, ex, err := m.resolve(id)
	if err != nil {
		return nil, err
	}
	if s.ArchivedAt != nil {
		return s, nil
	}
	if s.EndedAt == nil && !stop {
		return nil, fmt.Errorf("archiving a live session requires stop=true; this ends its terminal process")
	}
	key := fmt.Sprintf("archive/%d", id)
	m.mu.Lock()
	if m.transitions == nil {
		m.transitions = map[string]bool{}
	}
	if m.transitions[key] {
		m.mu.Unlock()
		return nil, fmt.Errorf("this session is already being archived")
	}
	m.transitions[key] = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); delete(m.transitions, key); m.mu.Unlock() }()
	identity := ""

	names := []string{s.TmuxSession}
	check := func(command string) (PollCapture, error) {
		r, e := ex.Run(ctx, command, executor.RunOpts{Timeout: 20})
		if e != nil || !r.OK() {
			return PollCapture{}, fmt.Errorf("could not check the terminal; nothing was archived")
		}
		panes, ok := ParsePollSnapshot(r.Stdout, names)
		if !ok || panes[s.TmuxSession].Failed {
			return PollCapture{}, fmt.Errorf("incomplete terminal capture; nothing was archived")
		}
		return panes[s.TmuxSession], nil
	}
	pane, err := check(buildPollCommand(names, 10000))
	if err != nil {
		return nil, err
	}
	snapshot := pane.Text
	if pane.Missing {
		if err := m.DB.QueryRow("SELECT archive_text FROM sessions WHERE id=?", id).Scan(&snapshot); err != nil {
			return nil, err
		}
		if snapshot == "" {
			snapshot = "[Terminal already stopped; last recorded preview follows.]\n" + s.PaneTail
		}
	} else {
		if s.EndedAt != nil {
			return nil, fmt.Errorf("this untracked terminal is still running; track it again before stopping and archiving it")
		}
		if s.EndedAt == nil {
			existing, readErr := ex.Run(ctx, trackingIdentityCommand(s.TmuxSession, ""), executor.RunOpts{Timeout: 10})
			if readErr == nil && existing.OK() {
				identity = strings.TrimSpace(existing.Stdout)
				if identity == "" {
					identity = captureTrackingIdentity(ctx, ex, s.TmuxSession)
				}
			}
			if !validTrackingIdentity(identity) || (s.TrackingIdentity != "" && identity != s.TrackingIdentity) {
				return nil, fmt.Errorf("could not identify the original terminal; nothing was archived")
			}
		}
		snapshot = limitArchiveSnapshot(snapshot)
		if err := m.DB.Update("sessions", id, map[string]any{"archive_text": snapshot}); err != nil {
			return nil, err
		}
		// tmux checks its session-local identity before ending the exact session.
		// A replaced session with the same name must never inherit this action.
		command := "tmux if-shell -F -t " + shellq.Quote("="+s.TmuxSession+":") + " " + shellq.Quote("#{==:#{"+trackingOption+"},"+identity+"}") + " " + shellq.Quote("kill-session -t "+shellq.Quote("="+s.TmuxSession))
		_, err = ex.Run(ctx, command, executor.RunOpts{Timeout: 20})
		if err != nil {
			return nil, err
		}
		gone, e := check(PollCommand(names))
		if e != nil {
			return nil, e
		}
		if !gone.Missing {
			return nil, fmt.Errorf("terminal did not stop; nothing was archived")
		}
	}

	current, err := m.DB.Session(id)
	if err == nil && (current.TargetID != s.TargetID || current.TmuxSession != s.TmuxSession) {
		err = fmt.Errorf("session moved during archival; refresh before retrying")
	}
	if err == nil {
		now := store.Now()
		ended := now
		if current.EndedAt != nil {
			ended = *current.EndedAt
		}
		err = m.DB.Update("sessions", id, map[string]any{"archived_at": now, "archive_text": snapshot, "ended_at": ended, "status": StatusDead, "updated_at": now})
	}
	if err != nil {
		return nil, err
	}
	fresh, err := m.DB.Session(id)
	if err == nil {
		m.publish(fresh)
	}
	return fresh, err
}

func (m *Manager) Unarchive(id int64) (*store.Session, error) {
	m.lifecycleMu.Lock()
	s, err := m.DB.Session(id)
	if err == nil && s.ArchivedAt != nil {
		err = m.DB.Update("sessions", id, map[string]any{"archived_at": nil, "updated_at": store.Now()})
	}
	m.lifecycleMu.Unlock()
	if err != nil {
		return nil, err
	}
	s, err = m.DB.Session(id)
	if err == nil {
		m.publish(s)
	}
	return s, err
}

func limitArchiveSnapshot(snapshot string) string {
	const note = "[Older output omitted; snapshot limited to 2 MiB.]\n"
	const limit = (2 << 20) - len(note)
	if len(snapshot) <= 2<<20 {
		return snapshot
	}
	snapshot = snapshot[len(snapshot)-limit:]
	for len(snapshot) > 0 && !utf8.RuneStart(snapshot[0]) {
		snapshot = snapshot[1:]
	}
	return note + strings.ToValidUTF8(snapshot, "�")
}
