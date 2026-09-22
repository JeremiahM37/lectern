package sessions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/shellq"
	"github.com/JeremiahM37/lectern/internal/store"
)

// A local tmux option survives detachment but disappears with the session.
// Names, working directories and recent timestamps cannot prove this identity.
const trackingOption = "@lectern-tracking-identity"

// legacyTrackingOption is the name the previous binary set. A session that has
// been running since before the rename carries only this one, and it is the
// only proof of that session's identity; so it is read, never set.
const legacyTrackingOption = "@agentdeck-tracking-identity"

// trackingFormat is a tmux format that yields the identity under either name.
const trackingFormat = "#{?#{" + trackingOption + "},#{" + trackingOption + "},#{" + legacyTrackingOption + "}}"

func trackingIdentityCommand(name, seed string) string {
	target := shellq.Quote("=" + name + ":")
	show := func(option string) string { return "tmux show-options -qv -t " + target + " " + option }
	if seed == "" {
		return `v=$(` + show(trackingOption) + `); [ -n "$v" ] || v=$(` + show(legacyTrackingOption) + `); printf '%s\n' "$v"`
	}
	// A session from before the rename already has an identity under the old
	// name; seeding a second one under the new name would make it look like a
	// different session. Only a session with neither is given one.
	return `v=$(` + show(legacyTrackingOption) + `); if [ -n "$v" ]; then printf '%s\n' "$v"; else ` +
		"tmux set-option -o -t " + target + " " + trackingOption + " " + shellq.Quote(seed) + " && " + show(trackingOption) + `; fi`
}

func validTrackingIdentity(value string) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == 16
}

func captureTrackingIdentity(ctx context.Context, ex executor.Executor, name string) string {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ""
	}
	r, err := ex.Run(ctx, trackingIdentityCommand(name, hex.EncodeToString(nonce[:])), executor.RunOpts{Timeout: 10})
	value := strings.TrimSpace(r.Stdout)
	if err != nil || !r.OK() || !validTrackingIdentity(value) {
		return ""
	}
	return value
}

// EnsureTrackingIdentity labels an existing live terminal once. It only sets a
// tmux option and returns the observed marker; promotion persists it after all
// proof and lifecycle checks pass. It never restarts the process.
func (m *Manager) EnsureTrackingIdentity(ctx context.Context, row *store.Session) (string, error) {
	target, err := m.DB.Target(row.TargetID)
	if err != nil {
		return "", err
	}
	ex, err := m.Reg.For(target)
	if err != nil {
		return "", err
	}
	identity := captureTrackingIdentity(ctx, ex, row.TmuxSession)
	if identity == "" {
		return "", fmt.Errorf("could not establish terminal identity")
	}
	return identity, nil
}

// Restore resumes monitoring the original adopted tmux session. It never
// launches an agent, recreates tmux or guesses a native conversation ID.
func (m *Manager) Restore(ctx context.Context, id int64) (*store.Session, error) {
	sess, ex, err := m.resolve(id)
	if err != nil {
		return nil, err
	}
	if sess.ArchivedAt != nil {
		return nil, fmt.Errorf("unarchive this record before restoring tracking")
	}
	if sess.EndedAt == nil {
		return nil, fmt.Errorf("this session is already tracked")
	}
	if sess.Origin != "discovered" || sess.Status == StatusDead || !validTrackingIdentity(sess.TrackingIdentity) {
		return nil, fmt.Errorf("this record cannot identify a running session; use Find running sessions to adopt one explicitly")
	}
	if _, err := m.DB.SessionByTmux(sess.TargetID, sess.TmuxSession); err == nil {
		return nil, fmt.Errorf("this tmux session is already tracked by another record")
	} else if err != store.ErrNotFound {
		return nil, err
	}
	r, err := ex.Run(ctx, trackingIdentityCommand(sess.TmuxSession, ""), executor.RunOpts{Timeout: 10})
	if err != nil {
		return nil, err
	}
	if !r.OK() || strings.TrimSpace(r.Stdout) != sess.TrackingIdentity {
		return nil, fmt.Errorf("the original tmux session is gone or has changed; use Find running sessions instead")
	}
	err = func() error {
		m.lifecycleMu.Lock()
		defer m.lifecycleMu.Unlock()
		current, err := m.DB.Session(id)
		if err != nil {
			return err
		}
		if current.EndedAt == nil {
			return fmt.Errorf("this session is already tracked")
		}
		if current.ArchivedAt != nil || current.Status == StatusDead || current.TrackingIdentity != sess.TrackingIdentity || current.TargetID != sess.TargetID || current.TmuxSession != sess.TmuxSession {
			return fmt.Errorf("session changed while checking identity; retry recovery")
		}
		if _, err := m.DB.SessionByTmux(sess.TargetID, sess.TmuxSession); err == nil {
			return ErrAlreadyAdopted
		} else if err != store.ErrNotFound {
			return err
		}
		return m.DB.Update("sessions", id, map[string]any{"ended_at": nil, "status": StatusIdle, "updated_at": store.Now()})
	}()
	if err != nil {
		return nil, err
	}
	m.pollTarget(ctx, sess.TargetID)
	fresh, err := m.DB.Session(id)
	if err != nil {
		return nil, err
	}
	m.publish(fresh)
	return fresh, nil
}
