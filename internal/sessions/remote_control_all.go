package sessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Remote Control for every session (docs/remote-control.md "Every session").

const (
	// RemoteControlDefaultSetting is the global settings row: new claude
	// sessions open in the Claude app unless it is "0".
	RemoteControlDefaultSetting = "remote_control_default"
	// EnvRemoteControlDefault ("0" or "1") overrides the setting.
	EnvRemoteControlDefault = "LECTERN_REMOTE_CONTROL_DEFAULT"
)

// RemoteControlByDefault says whether a claude launch that can use Remote
// Control gets it without being asked.
func (m *Manager) RemoteControlByDefault() bool {
	if v := strings.TrimSpace(os.Getenv(EnvRemoteControlDefault)); v != "" {
		return v != "0" && !strings.EqualFold(v, "false") && !strings.EqualFold(v, "off")
	}
	return strings.TrimSpace(m.DB.Setting(RemoteControlDefaultSetting)) != "0"
}

// Outcomes of turning Remote Control on for one session.
const (
	RemoteControlOn      = "on"      // turned on now
	RemoteControlAlready = "already" // it was on
	RemoteControlPending = "pending" // busy; turned on once it waits for you
	RemoteControlSkipped = "skipped" // cannot use it (Reason says why)
	RemoteControlConsent = "consent" // Claude Code's one-time question
	RemoteControlFailed  = "failed"
)

// RemoteControlOutcome is what happened to one session.
type RemoteControlOutcome struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	URL    string `json:"url,omitempty"`
}

// EnableRemoteControlAll turns Remote Control on in every live session that
// can use it. A session that is busy is remembered and turned on by
// RetryRemoteControl once it is waiting for you.
func (m *Manager) EnableRemoteControlAll(ctx context.Context) ([]RemoteControlOutcome, error) {
	live, err := m.DB.LiveSessions()
	if err != nil {
		return nil, err
	}
	out := []RemoteControlOutcome{}
	for _, sess := range live {
		row := RemoteControlOutcome{ID: sess.ID, Name: sess.Name}
		if sess.RemoteControl {
			row.Status, row.URL = RemoteControlAlready, sess.RemoteControlURL
			out = append(out, row)
			continue
		}
		if sess.Agent != "claude" {
			// A shell or another agent; its launch settings may not even
			// resolve as an agent's.
			row.Status, row.Reason = RemoteControlSkipped, "Remote Control is only available for the built-in claude agent"
			out = append(out, row)
			continue
		}
		if config, err := m.SessionLaunchConfiguration(sess); err != nil {
			row.Status, row.Reason = RemoteControlFailed, err.Error()
			out = append(out, row)
			continue
		} else if why := RemoteControlUnavailable(config.Spec, config.Spec.Env, config.Isolation); why != "" {
			row.Status, row.Reason = RemoteControlSkipped, why
			out = append(out, row)
			continue
		}
		out = append(out, m.enableRemoteControlOne(ctx, row))
	}
	return out, nil
}

func (m *Manager) enableRemoteControlOne(ctx context.Context, row RemoteControlOutcome) RemoteControlOutcome {
	fresh, err := m.EnableRemoteControl(ctx, row.ID)
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case errors.Is(err, ErrRemoteControlBusy):
		if m.rcPending == nil {
			m.rcPending = map[int64]bool{}
		}
		m.rcPending[row.ID] = true
		row.Status, row.Reason = RemoteControlPending, "busy: it is turned on once the agent is waiting for you"
	case errors.Is(err, ErrRemoteControlTooOld):
		delete(m.rcPending, row.ID)
		row.Status, row.Reason = RemoteControlSkipped, err.Error()
	case errors.Is(err, ErrRemoteControlConsent):
		delete(m.rcPending, row.ID)
		row.Status, row.Reason = RemoteControlConsent, err.Error()
	case err != nil:
		delete(m.rcPending, row.ID)
		row.Status, row.Reason = RemoteControlFailed, err.Error()
	default:
		delete(m.rcPending, row.ID)
		row.Status, row.URL = RemoteControlOn, fresh.RemoteControlURL
	}
	return row
}

// RemoteControlPendingIDs are the sessions waiting to be turned on.
func (m *Manager) RemoteControlPendingIDs() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]int64, 0, len(m.rcPending))
	for id := range m.rcPending {
		ids = append(ids, id)
	}
	return ids
}

// RetryRemoteControl turns Remote Control on in each pending session that is
// now waiting for you, and forgets those that ended or got it another way.
// The scheduler calls it after every poll; a call while one is still running
// returns at once, since each enable can watch a screen for a few seconds.
func (m *Manager) RetryRemoteControl(ctx context.Context) {
	m.mu.Lock()
	if m.rcRetrying || len(m.rcPending) == 0 {
		m.mu.Unlock()
		return
	}
	m.rcRetrying = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.rcRetrying = false
		m.mu.Unlock()
	}()
	for _, id := range m.RemoteControlPendingIDs() {
		sess, err := m.DB.Session(id)
		if err != nil || sess.RemoteControl || sess.EndedAt != nil || sess.Status == StatusDead || sess.AgentExitedAt != nil {
			m.mu.Lock()
			delete(m.rcPending, id)
			m.mu.Unlock()
			continue
		}
		if remoteControlBusy(sess, store.Now()) {
			continue
		}
		row := m.enableRemoteControlOne(ctx, RemoteControlOutcome{ID: id, Name: sess.Name})
		if row.Status != RemoteControlPending {
			m.Log.Info("remote control for every session", "session", id, "status", row.Status, "reason", row.Reason)
		}
	}
}

// ErrRemoteControlTooOld is a machine whose Claude Code predates Remote
// Control: its --help does not list --remote-control.
var ErrRemoteControlTooOld = errors.New("Claude Code on this machine is too old for Remote Control: update it (claude update)")

// remoteControlProbeTTL is how long a machine's answer is trusted.
const remoteControlProbeTTL = 10 * time.Minute

type remoteControlProbe struct {
	ok bool
	at time.Time
}

// claudeHasRemoteControl asks the machine whether its claude lists
// --remote-control. An older one refuses the flag and the launch dies, and
// takes /remote-control for an unknown command. The scripted mock target, an
// unreadable command and a machine that cannot answer all count as yes: the
// launch reports what is really wrong.
func (m *Manager) claudeHasRemoteControl(ctx context.Context, ex executor.Executor, targetID int64, spec Spec) bool {
	if _, mock := ex.(*executor.Mock); mock {
		return true
	}
	word := executableWord(spec.Command)
	if word == "" {
		return true
	}
	key := fmt.Sprintf("%d\x00%s\x00%s", targetID, word, spec.Env["PATH"])
	m.mu.Lock()
	if probe, ok := m.rcProbes[key]; ok && time.Since(probe.at) < remoteControlProbeTTL {
		m.mu.Unlock()
		return probe.ok
	}
	m.mu.Unlock()
	// Exit 0: it lists the flag; 1: it answered without it; anything else
	// (no answer within 5 s, not found) is no answer. A login shell is tried
	// for a program only on its PATH, as checkAgentInstalled does.
	help := "$T " + shellq.Quote(word) + " --help </dev/null 2>&1"
	probe := `T=; command -v timeout >/dev/null 2>&1 && T="timeout 5"; export T; ` +
		`out=$(` + help + `); [ $? -eq 127 ] && out=$(${SHELL:-sh} -lc ` + shellq.Quote(help) + `); ` +
		`case "$out" in *--remote-control*) exit 0;; esac; case "$out" in *Usage*|*usage*) exit 1;; esac; exit 2`
	if path := spec.Env["PATH"]; path != "" {
		probe = "PATH=" + shellq.Quote(path) + "; export PATH; " + probe
	}
	// A program that does not answer --help quickly is not stopped by it
	// again for every launch: no answer counts as yes, and is kept too.
	r, err := ex.Run(ctx, probe, executor.RunOpts{Timeout: 8})
	ok := err != nil || r.RC != 1
	m.mu.Lock()
	if m.rcProbes == nil {
		m.rcProbes = map[string]remoteControlProbe{}
	}
	m.rcProbes[key] = remoteControlProbe{ok: ok, at: time.Now()}
	m.mu.Unlock()
	return ok
}
