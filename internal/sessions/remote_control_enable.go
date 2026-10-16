package sessions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/agentevents"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/sessions/backend"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// ErrRemoteControlBusy refuses turning Remote Control on while the agent is
// mid-turn or waiting on a prompt: the command would land in its input or in
// the prompt instead of running.
var ErrRemoteControlBusy = errors.New("the agent is busy: turn Remote Control on once it is waiting for you")

// ErrRemoteControlConsent is Claude Code's one-time "Enable Remote Control"
// question. Lectern does not answer it for the person: it closes the question
// and says where to accept it.
var ErrRemoteControlConsent = errors.New("Claude Code asks once, per login, before Remote Control is first turned on in a running session. " +
	"Open this session's terminal, run /remote-control and choose Enable Remote Control; after that Lectern can turn it on by itself")

// remoteControlWait is how long EnableRemoteControl watches the screen for
// claude's answer. Tests shorten it.
var remoteControlWait = 12 * time.Second

// EnableRemoteControl connects a running claude session to claude.ai/code and
// the Claude app by typing /remote-control into it (docs/remote-control.md),
// and records it so every continuation keeps it on. It is a no-op for a
// session that is already marked.
func (m *Manager) EnableRemoteControl(ctx context.Context, id int64) (*store.Session, error) {
	sess, ex, err := m.resolve(id)
	if err != nil {
		return nil, err
	}
	if sess.RemoteControl {
		return sess, nil
	}
	if sess.EndedAt != nil || sess.Status == StatusDead || sess.AgentExitedAt != nil {
		return nil, fmt.Errorf("session #%d has ended; restore it first", id)
	}
	config, err := m.SessionLaunchConfiguration(sess)
	if err != nil {
		return nil, err
	}
	if why := RemoteControlUnavailable(config.Spec, config.Spec.Env, config.Isolation); why != "" {
		return nil, errors.New(why)
	}
	if remoteControlBusy(sess, store.Now()) {
		return nil, ErrRemoteControlBusy
	}
	if err := m.SendNudge(ctx, id, "/remote-control "+RemoteControlName(sess.Name)); err != nil {
		return nil, err
	}
	url, err := m.awaitRemoteControl(ctx, ex, sess)
	if err != nil {
		return nil, err
	}
	config.RemoteControl = true
	if err := m.DB.Update("sessions", id, map[string]any{"launch_config_json": store.J(config)}); err != nil {
		return nil, err
	}
	if err := m.DB.SetSessionRemoteControl(id, true, url); err != nil {
		return nil, err
	}
	fresh, err := m.DB.Session(id)
	if err != nil {
		return nil, err
	}
	m.publish(fresh)
	return fresh, nil
}

// remoteControlBusy says the agent is mid-turn or waiting on a prompt. A
// recent hook report is the agent's own word and wins over the screen, whose
// status reads "running" whenever the pane changed since the last poll.
func remoteControlBusy(s *store.Session, now float64) bool {
	if s.Status == StatusStarting {
		return true
	}
	if s.StateSource == agentevents.SourceHook && s.HookSeenAt != nil && now-*s.HookSeenAt < agentStateFallbackWindow {
		return s.AgentState == "working" || s.AgentState == "waiting_permission"
	}
	return s.Status == StatusRunning || s.AgentState == "waiting_permission"
}

// awaitRemoteControl watches the screen after /remote-control until claude
// says it is connected (returning the session link when it printed one) or
// shows its one-time consent question, which is dismissed unanswered. If
// neither shows in time the command was still sent, so the session is marked
// and the link is picked up later by the poller.
func (m *Manager) awaitRemoteControl(ctx context.Context, ex executor.Executor, sess *store.Session) (string, error) {
	deadline := time.Now().Add(remoteControlWait)
	for {
		pane := m.screen(ctx, ex, sess)
		if remoteControlConsentShown(pane) {
			if err := m.SendKey(ctx, sess.ID, "escape"); err != nil {
				m.Log.Warn("could not close the Remote Control question", "session", sess.ID, "err", err)
			}
			return "", ErrRemoteControlConsent
		}
		if remoteControlActive(pane) {
			return RemoteControlURL(pane), nil
		}
		if time.Now().After(deadline) {
			m.Log.Info("remote control: no confirmation on screen yet", "session", sess.ID)
			return "", nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(400 * time.Millisecond):
		}
	}
}

// screen is the session's visible pane plus a little history, or "" when it
// cannot be read.
func (m *Manager) screen(ctx context.Context, ex executor.Executor, sess *store.Session) string {
	cmd := fmt.Sprintf("printf '%%s' %s; %s", shellq.Quote(PollDelimiter+sess.TmuxSession+"\n"),
		sessionBackend(ex, sess).CapturePane(backend.Pane(sess.TmuxSession), 60, true))
	r, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 15})
	if err != nil || !r.OK() {
		return ""
	}
	return ParsePoll(r.Stdout)[sess.TmuxSession]
}

// noteRemoteControlURL records the claude.ai link the first time it shows on
// a Remote Control session's screen, so the card can open it.
func (m *Manager) noteRemoteControlURL(s *store.Session, pane string, fields map[string]any) {
	if !s.RemoteControl {
		return
	}
	if url := RemoteControlURL(pane); url != "" && url != s.RemoteControlURL && strings.HasPrefix(url, "https://claude.ai/") {
		fields["remote_control_url"] = url
	}
}
