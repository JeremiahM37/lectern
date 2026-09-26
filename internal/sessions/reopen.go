package sessions

import (
	"context"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Reopening is the one restore path behind Restore, Undo and `lectern
// restore`. The methods here cover what the older paths did not: a session a
// host restart left "interrupted", a shell, and continuing a closed session's
// context in a different agent.

// terminalGone proves the row's tmux session no longer exists. Every reopen
// that starts a process checks this first, so a reopen can never run beside
// the original terminal.
func (m *Manager) terminalGone(ctx context.Context, ex executor.Executor, row *store.Session) error {
	if row.TmuxSession == "" {
		return nil
	}
	names := []string{row.TmuxSession}
	r, err := ex.Run(ctx, PollCommand(names), executor.RunOpts{Timeout: 10})
	if err != nil || !r.OK() {
		return fmt.Errorf("could not check the original terminal; retry when the machine is reachable")
	}
	panes, complete := ParsePollSnapshot(r.Stdout, names)
	if !complete || panes[row.TmuxSession].Failed {
		return fmt.Errorf("incomplete terminal check; retry when the machine is reachable")
	}
	if !panes[row.TmuxSession].Missing {
		return fmt.Errorf("the original terminal is still running; attach to it instead")
	}
	return nil
}

// Relaunch starts an interrupted session again in its own record, the way
// restart recovery does: with its saved conversation when one is bound,
// otherwise fresh, typed prime first when given. It is for rows a restart
// left "interrupted" — automatic recovery either had no conversation to
// resume or failed to launch.
func (m *Manager) Relaunch(ctx context.Context, id int64, prime string) (*store.Session, error) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	row, ex, err := m.resolve(id)
	if err != nil {
		return nil, err
	}
	if row.EndedAt != nil || row.Status != StatusInterrupted {
		return nil, fmt.Errorf("only an interrupted session can be relaunched in place")
	}
	if row.Origin != "lectern" || row.Agent == "shell" {
		return nil, fmt.Errorf("this session cannot be relaunched in place")
	}
	if err := m.terminalGone(ctx, ex, row); err != nil {
		return nil, err
	}
	boot, _ := ProbeBootID(ctx, ex)
	res, err := m.DB.Exec("UPDATE sessions SET boot_id=?, status=? WHERE id=? AND ended_at IS NULL AND status=?", boot, StatusStarting, row.ID, StatusInterrupted)
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return nil, fmt.Errorf("session changed while reopening; refresh and retry")
	}
	m.stopNativeCheckpoint(row.ID)
	restore := func(err error) (*store.Session, error) {
		_ = m.DB.Update("sessions", row.ID, map[string]any{"status": StatusInterrupted, "ended_at": nil, "end_reason": "", "updated_at": store.Now()})
		return nil, err
	}
	config, err := m.SessionLaunchConfiguration(row)
	if err != nil {
		return restore(err)
	}
	next, err := m.launch(ctx, LaunchOpts{ReservedID: row.ID, Configuration: config, TargetID: row.TargetID, ProjectID: row.ProjectID, GroupPath: row.GroupPath, Name: row.Name, Agent: row.Agent, Model: row.Model, Workdir: row.Workdir, ResumeID: row.ResumeID, RecoveryCID: row.NativeRecoveryCID, Prime: prime})
	if err != nil {
		return restore(err)
	}
	return next, nil
}

// Retire ends an interrupted row whose terminal is gone, so a replacement can
// take over its work. Rows that are already ended are left as they are.
func (m *Manager) Retire(ctx context.Context, id int64) (*store.Session, error) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	row, ex, err := m.resolve(id)
	if err != nil {
		return nil, err
	}
	if row.EndedAt != nil {
		return row, nil
	}
	if row.Status != StatusInterrupted {
		return nil, fmt.Errorf("this session is still running; stop it first")
	}
	if err := m.terminalGone(ctx, ex, row); err != nil {
		return nil, err
	}
	m.stopNativeCheckpoint(row.ID)
	m.endWith(row.ID, StatusDead, EndRestart)
	return m.DB.Session(row.ID)
}

// ReopenShell starts a new tracked shell in a closed shell's folder, with its
// name, project and group. A shell has no conversation to resume; its
// scrollback went with the old terminal.
func (m *Manager) ReopenShell(ctx context.Context, id int64) (*store.Session, error) {
	row, err := m.DB.Session(id)
	if err != nil {
		return nil, err
	}
	if row.Agent != "shell" {
		return nil, fmt.Errorf("session %d is not a shell", id)
	}
	if row.EndedAt == nil {
		if row, err = m.Retire(ctx, id); err != nil {
			return nil, err
		}
	}
	target, err := m.DB.Target(row.TargetID)
	if err != nil {
		return nil, err
	}
	ex, err := m.Reg.For(target)
	if err != nil {
		return nil, err
	}
	r, err := ex.Run(ctx, "test -d "+shellq.Quote(row.Workdir), executor.RunOpts{Timeout: 10})
	if err != nil {
		return nil, err
	}
	if !r.OK() {
		return nil, fmt.Errorf("the shell's folder no longer exists on %s: %s", target.Name, row.Workdir)
	}
	next, err := m.startShellRoom(ctx, shellRoom{target: target, ex: ex, name: row.Name, workdir: row.Workdir, projectID: row.ProjectID})
	if err != nil {
		return nil, err
	}
	if row.GroupPath != "" {
		if err := m.DB.Update("sessions", next.ID, map[string]any{"group_path": row.GroupPath}); err == nil {
			next.GroupPath = row.GroupPath
		}
	}
	return next, nil
}

// ContinueOpts picks the successor for Continue.
type ContinueOpts struct {
	Agent     string
	Model     string
	ProfileID int64
	Name      string
	// Context is what the successor is primed with: the latest wrap, or an
	// excerpt of the saved conversation. Empty starts the successor fresh.
	Context string
	// WrapID is the wrap Context came from, if any. A wrap nobody continued
	// yet is linked to the successor, so it shows where it came from.
	WrapID int64
	// Excerpt marks Context as material the old session left behind (the end
	// of its conversation or screen) rather than a handoff it wrote.
	Excerpt bool
}

// ExcerptPrompt primes a successor with what a session left behind when it
// wrote no handoff — worded so the agent does not mistake it for one.
func ExcerptPrompt(projectName, excerpt, priming string) string {
	var b strings.Builder
	b.WriteString("You are continuing work on ")
	b.WriteString(firstNonEmpty(projectName, "this project"))
	b.WriteString(". The previous session ended without writing a handoff. This is what it left:\n\n---\n")
	b.WriteString(strings.TrimSpace(excerpt))
	b.WriteString("\n---\n")
	if strings.TrimSpace(priming) != "" {
		b.WriteString("\n" + strings.TrimSpace(priming) + "\n")
	}
	b.WriteString("\nRead what you need from the repo to confirm where things stand, " +
		"then tell me what you think was being worked on and what you plan to do next. " +
		"Do not start changing things until I say so.")
	return b.String()
}

// Continue starts a new session in a closed session's place — same project,
// folder and group — optionally with a different agent, model or launch
// profile, primed with the context it is given. It reuses the handoff launch
// rules. The closed record is left untouched.
func (m *Manager) Continue(ctx context.Context, sourceID int64, o ContinueOpts) (*store.Session, error) {
	source, err := m.DB.Session(sourceID)
	if err != nil {
		return nil, err
	}
	if source.EndedAt == nil {
		if source.Status != StatusInterrupted {
			return nil, fmt.Errorf("this session is still running; use Switch to move it to another agent")
		}
		if source, err = m.Retire(ctx, sourceID); err != nil {
			return nil, err
		}
	}
	if source.Agent == "shell" {
		return nil, fmt.Errorf("a shell has no conversation to continue")
	}
	launch, err := m.handoffLaunch(source, HandoffOpts{Agent: o.Agent, Model: o.Model, ProfileID: o.ProfileID, QuickSwitch: o.Agent != "" && o.Agent != source.Agent})
	if err != nil {
		return nil, err
	}
	if name := strings.TrimSpace(o.Name); name != "" {
		launch.Name = name
	}
	if brief := strings.TrimSpace(o.Context); brief != "" {
		prompt := ResumePrompt
		if o.Excerpt {
			prompt = ExcerptPrompt
		}
		launch.Prime = prompt(source.ProjectName, brief, m.projectPrime(ctx, source.ProjectName))
	}
	next, err := m.Launch(ctx, launch)
	if err != nil {
		return nil, err
	}
	if o.WrapID != 0 {
		_, _ = m.DB.Exec("UPDATE session_wraps SET next_session_id=? WHERE id=? AND session_id=? AND next_session_id IS NULL", next.ID, o.WrapID, source.ID)
	}
	return next, nil
}
