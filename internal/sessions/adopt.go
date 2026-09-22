package sessions

import (
	"context"
	"errors"
	"fmt"

	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/store"
)

// Discover finds agents running on every registered target, whether or not
// lectern started them.
//
// This closes the gap that makes a pure task board useless for long projects:
// the sessions you care about most are the ones you started by hand, in a
// terminal, weeks ago. Adopting one is non-destructive — the tmux session is
// left exactly as it is and lectern simply starts watching it.
func (m *Manager) Discover(ctx context.Context) ([]Candidate, error) {
	targets, err := m.DB.Targets()
	if err != nil {
		return nil, err
	}
	projects, err := m.DB.Projects()
	if err != nil {
		return nil, err
	}
	repoPaths := map[int64]string{}
	projectNames := map[int64]string{}
	for _, p := range projects {
		repoPaths[p.ID] = p.RepoPath
		projectNames[p.ID] = p.Name
	}

	out := []Candidate{}
	for _, t := range targets {
		if t.Kind == "sandbox" {
			continue // ephemeral by definition; nothing there outlives its task
		}
		ex, err := m.Reg.For(t)
		if err != nil {
			continue
		}
		r, err := ex.Run(ctx, DiscoverCommand(), executor.RunOpts{Timeout: 30})
		if err != nil {
			m.Log.Debug("discovery failed", "target", t.Name, "err", err)
			continue
		}
		for _, c := range ParseDiscover(r.Stdout) {
			c.TargetID, c.TargetName = t.ID, t.Name
			if known, err := m.DB.SessionByTmux(t.ID, c.TmuxSession); err == nil {
				c.Adopted, c.SessionID, c.ProjectID = true, known.ID, known.ProjectID
			}
			if pid, ok := MatchProject(c.Workdir, repoPaths); ok {
				c.ProjectID = &pid
				c.ProjectName = projectNames[pid]
			}
			out = append(out, c)
		}
	}
	return out, nil
}

// AdoptOpts identifies the tmux session to take over.
type AdoptOpts struct {
	TargetID    int64
	TmuxSession string
	ProjectID   *int64
	Name        string
	Agent       string
	Model       string
	Workdir     string
}

// ErrAlreadyAdopted means lectern is already watching this tmux session.
var ErrAlreadyAdopted = errors.New("this tmux session is already tracked")

// Adopt starts tracking an externally-started agent.
func (m *Manager) Adopt(ctx context.Context, o AdoptOpts) (*store.Session, error) {
	if o.TmuxSession == "" {
		return nil, fmt.Errorf("a tmux session name is required")
	}
	if _, err := m.DB.SessionByTmux(o.TargetID, o.TmuxSession); err == nil {
		return nil, ErrAlreadyAdopted
	}
	target, err := m.DB.Target(o.TargetID)
	if err != nil {
		return nil, err
	}
	ex, err := m.Reg.For(target)
	if err != nil {
		return nil, err
	}
	// refuse to adopt something that is not actually there, so the board never
	// shows a session that was already gone when we claimed it — and take tmux's
	// own timestamps while we are asking, so an agent you started three days ago
	// says "up 3d" instead of "up 4s"
	r, err := ex.Run(ctx, TimesCommand(o.TmuxSession), executor.RunOpts{Timeout: 20})
	if err != nil {
		return nil, err
	}
	if !r.OK() {
		return nil, fmt.Errorf("no tmux session %q on %s", o.TmuxSession, target.Name)
	}
	name := o.Name
	if name == "" {
		name = o.TmuxSession
	}
	agent := o.Agent
	if agent == "" {
		agent = "claude"
	}
	identity := captureTrackingIdentity(ctx, ex, o.TmuxSession)
	var sess *store.Session
	err = func() error {
		m.lifecycleMu.Lock()
		defer m.lifecycleMu.Unlock()
		if _, err := m.DB.SessionByTmux(o.TargetID, o.TmuxSession); err == nil {
			return ErrAlreadyAdopted
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		sess, err = m.DB.InsertSession(&store.Session{
			ProjectID: o.ProjectID, TargetID: o.TargetID, Name: name, Agent: agent,
			Model: o.Model, Workdir: o.Workdir, TmuxSession: o.TmuxSession,
			Status: StatusIdle, Origin: "discovered",
		})
		if err != nil {
			return err
		}
		if created, activity, ok := ParseTimes(r.Stdout); ok {
			fields := map[string]any{"created_at": created}
			if activity > 0 {
				fields["last_activity_at"] = activity
			}
			m.DB.Update("sessions", sess.ID, fields)
		}
		if identity != "" {
			if err := m.DB.Update("sessions", sess.ID, map[string]any{"tracking_identity": identity}); err != nil {
				return err
			}
		}
		return nil
	}()
	if err != nil {
		return nil, err
	}
	// pull its state straight away so the card is truthful the moment it appears
	m.pollTarget(ctx, sess.TargetID)
	fresh, err := m.DB.Session(sess.ID)
	if err != nil {
		return sess, nil
	}
	m.publish(fresh)
	m.Log.Info("session adopted", "session", fresh.ID, "tmux", o.TmuxSession,
		"target", target.Name)
	return fresh, nil
}
