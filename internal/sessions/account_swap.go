package sessions

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/accounts"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/isolation"
	"github.com/JeremiahM37/lectern/v2/internal/limits"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Account swap (docs/accounts.md). The conversation is copied into the new
// account's directory, the agent's terminal is stopped, and the same session
// row is relaunched under the new account with the CLI's resume-by-id flag,
// in the same workspace and under the same tmux name — the path restart
// recovery uses. The tracker (internal/limits) owns the hold and the nudge.

// baseAccountDir is where the CLI's default login lives for Lectern: whatever
// the agent's definition sets for its config variable (usually nothing).
func (m *Manager) baseAccountDir(agent string) string {
	if spec, ok := Find(m.specs(), agent); ok {
		return spec.Env[accounts.EnvKey(agent)]
	}
	return ""
}

// SwapAccount restarts a session's agent under another account and resumes
// its conversation there. committed is true once the agent runs under the
// new account (the session row names it); err after a commit means the
// resumed agent did not show a prompt within the wait, or exited
// (ErrAgentExited).
func (m *Manager) SwapAccount(ctx context.Context, id, accountID int64) (bool, error) {
	acct, err := m.DB.Account(accountID)
	if err != nil {
		return false, fmt.Errorf("no such account")
	}
	sess, ex, err := m.resolve(id)
	if err != nil {
		return false, err
	}
	key := accounts.EnvKey(sess.Agent)
	switch {
	case sess.EndedAt != nil:
		return false, fmt.Errorf("the session has ended")
	case key == "":
		return false, fmt.Errorf("%s has no account support", sess.Agent)
	case acct.TargetID != sess.TargetID || acct.Agent != sess.Agent:
		return false, fmt.Errorf("account %q is not a %s login on this machine", acct.Label, sess.Agent)
	case sess.Origin != "lectern" || sess.TmuxSession == "":
		return false, fmt.Errorf("only sessions Lectern started can be moved to another account")
	}
	cid := firstNonEmpty(sess.NativeRecoveryCID, sess.ResumeID)
	if cid == "" {
		return false, fmt.Errorf("the conversation id is not known yet, so it cannot be resumed elsewhere")
	}
	cfg, err := m.SessionLaunchConfiguration(sess)
	if err != nil {
		return false, err
	}
	if len(cfg.Spec.ResumeIDArgs) == 0 {
		return false, fmt.Errorf("%s cannot resume a conversation by id", sess.Agent)
	}
	if cfg.Isolation.Normalized().Mode != isolation.None {
		return false, fmt.Errorf("an isolated session only sees its own login; move it by hand")
	}
	base := m.baseAccountDir(sess.Agent)
	from := firstNonEmpty(cfg.Spec.Env[key], base)
	to := firstNonEmpty(acct.Dir, base)
	// An account that was never signed in would only show a login screen.
	if r, err := ex.Run(ctx, accounts.StatusCommand(sess.Agent, to), executor.RunOpts{Timeout: 10}); err == nil &&
		strings.TrimSpace(r.Stdout) == "signed-out" {
		return false, fmt.Errorf("account %q is not signed in", acct.Label)
	}
	stage, err := accounts.StageCommand(sess.Agent, from, to, cid, sess.Workdir)
	if err != nil {
		return false, err
	}
	r, err := ex.Run(ctx, stage, executor.RunOpts{Timeout: 60})
	if err != nil {
		return false, err
	}
	if !r.OK() {
		return false, fmt.Errorf("could not copy the conversation to %s: %s", acct.Label, strings.TrimSpace(r.Stderr))
	}

	// Hold the target's poll lock across stop and relaunch, so no status poll
	// sees the gap between the two terminals and ends the session.
	st := m.reach().state(sess.TargetID)
	st.mu.Lock()
	m.lifecycleMu.Lock()
	committed, err := m.relaunchUnder(ctx, ex, id, sess, cfg, key, to, acct.ID, cid)
	m.lifecycleMu.Unlock()
	st.mu.Unlock()
	if !committed {
		return false, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	ready := m.waitReady(waitCtx, id)
	if m.agentExited(ctx, ex, id) {
		return true, fmt.Errorf("%w right after restarting under %q (did it find the conversation?)",
			ErrAgentExited, acct.Label)
	}
	if !ready {
		return true, fmt.Errorf("the resumed agent did not show a prompt")
	}
	return true, nil
}

// ErrAgentExited is a swap whose agent quit straight after its restart: there
// is nothing running to resume.
var ErrAgentExited = limits.ErrAgentExited

// agentExited asks the target whether the session's agent has gone and left
// its terminal at a shell.
func (m *Manager) agentExited(ctx context.Context, ex executor.Executor, id int64) bool {
	row, err := m.DB.Session(id)
	if err != nil {
		return false
	}
	r, err := ex.Run(ctx, buildAgentProbe([]string{row.TmuxSession}), executor.RunOpts{Timeout: 10})
	if err != nil || !r.OK() {
		return false
	}
	probes, ok := parseAgentProbe(r.Stdout, []string{row.TmuxSession})
	if !ok {
		return false
	}
	p, found := probes[row.TmuxSession]
	return found && AgentExited(row, p)
}

// relaunchUnder stops the session's terminal and starts it again under the
// account directory dir. The caller holds lifecycleMu and the target's poll
// lock.
func (m *Manager) relaunchUnder(ctx context.Context, ex executor.Executor, id int64, before *store.Session, cfg *LaunchConfiguration, key, dir string, accountID int64, cid string) (bool, error) {
	row, err := m.DB.Session(id)
	if err != nil {
		return false, err
	}
	if row.EndedAt != nil || row.TmuxSession != before.TmuxSession || row.TargetID != before.TargetID {
		return false, fmt.Errorf("the session changed while its conversation was being copied")
	}
	m.stopNativeCheckpoint(id)
	if err := m.stopProcess(ctx, ex, row); err != nil {
		m.startNativeCheckpoint(id, row.Agent, row.Workdir, nativeHome(cfg.Spec, row.Agent), row.TmuxSession)
		return false, err
	}
	// The old process is gone; its egress proxy goes with it (a relaunch
	// starts a new one).
	m.IsolationProxies.Stop(id)
	next := *cfg
	next.Spec.Env = map[string]string{}
	for k, v := range cfg.Spec.Env {
		next.Spec.Env[k] = v
	}
	if dir == "" {
		delete(next.Spec.Env, key)
	} else {
		next.Spec.Env[key] = dir
	}
	_, err = m.launch(ctx, LaunchOpts{ReservedID: id, Configuration: &next, TargetID: row.TargetID,
		ProjectID: row.ProjectID, GroupPath: row.GroupPath, Name: row.Name, Agent: row.Agent,
		Model: row.Model, Workdir: row.Workdir, ResumeID: row.ResumeID, RecoveryCID: cid, AccountID: &accountID,
		SkipPrime: true})
	if err != nil {
		return false, fmt.Errorf("the agent stopped but did not restart under the new account: %w", err)
	}
	m.Log.Info("session moved to another account", "session", id, "account", accountID)
	return true, nil
}

// LaunchAccountLogin opens a tracked shell on the account's target with the
// account's directory in the environment and the CLI's own sign-in running
// in it. The operator finishes the sign-in in the web terminal; Lectern
// never sees what is typed there or what the CLI stores.
func (m *Manager) LaunchAccountLogin(ctx context.Context, accountID int64) (*store.Session, error) {
	acct, err := m.DB.Account(accountID)
	if err != nil {
		return nil, err
	}
	target, err := m.DB.Target(acct.TargetID)
	if err != nil {
		return nil, err
	}
	ex, err := m.Reg.For(target)
	if err != nil {
		return nil, err
	}
	workdir, err := m.makeScratch(ctx, ex, "sign-in")
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	if dir := firstNonEmpty(acct.Dir, m.baseAccountDir(acct.Agent)); dir != "" {
		env[accounts.EnvKey(acct.Agent)] = dir
	}
	prefix, err := EnvPrefix(env)
	if err != nil {
		return nil, err
	}
	login := accounts.LoginCommand(acct.Agent)
	banner := fmt.Sprintf("Signing in %s account %q. When it is done, run %s once here to finish its first-run setup, then close this terminal.",
		acct.Agent, acct.Label, acct.Agent)
	command := "printf '%s\\n\\n' " + shellq.Quote(banner) + "; " + login + "; exec \"${SHELL:-/bin/sh}\" -i"
	return m.startShellRoom(ctx, shellRoom{target: target, ex: ex, name: "Sign in · " + acct.Label,
		workdir: workdir, env: prefix, command: command})
}
