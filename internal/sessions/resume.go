package sessions

import (
	"context"
	"fmt"

	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/store"
)

// ResumeConversation starts an exact native conversation after its old terminal
// has stopped. History validation belongs to the API's target-side reader.
func (m *Manager) ResumeConversation(ctx context.Context, sourceID int64, cid, name string) (*store.Session, error) {
	// Keep the absence check and successor launch in one lifecycle critical
	// section. Recovery and stop use the same gate; otherwise a concurrent
	// recovery can create the same native CID after this poll but before launch.
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	source, ex, err := m.resolve(sourceID)
	if err != nil {
		return nil, err
	}
	if source.ArchivedAt != nil {
		return nil, fmt.Errorf("unarchive this record before resuming its conversation")
	}
	if source.EndedAt == nil {
		return nil, fmt.Errorf("stop the original session before resuming; use Fork to branch a running conversation")
	}
	config, err := m.SessionLaunchConfiguration(source)
	if err != nil {
		return nil, err
	}
	if cid == "" || len(config.Spec.ResumeIDArgs) == 0 {
		return nil, fmt.Errorf("this agent cannot resume that exact conversation")
	}
	key := fmt.Sprintf("%d/%s/%s", source.TargetID, source.Agent, cid)
	m.mu.Lock()
	if m.transitions == nil {
		m.transitions = map[string]bool{}
	}
	if m.transitions[key] {
		m.mu.Unlock()
		return nil, fmt.Errorf("this conversation is already being resumed")
	}
	m.transitions[key] = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); delete(m.transitions, key); m.mu.Unlock() }()

	// Include released records: stopping tracking does not stop their processes.
	rows, err := m.DB.Sessions(true)
	if err != nil {
		return nil, err
	}
	names := []string{source.TmuxSession}
	seenNames := map[string]bool{source.TmuxSession: true}
	for _, row := range rows {
		if row.ID == source.ID {
			continue
		}
		if row.TargetID == source.TargetID && row.Agent == source.Agent && (row.ResumeID == cid || row.NativeRecoveryCID == cid) {
			if row.TmuxSession == "" {
				return nil, fmt.Errorf("session %d has an unfinished launch; inspect it before retrying", row.ID)
			}
			if !seenNames[row.TmuxSession] {
				names = append(names, row.TmuxSession)
				seenNames[row.TmuxSession] = true
			}
		}
	}
	result, err := ex.Run(ctx, PollCommand(names), executor.RunOpts{Timeout: 10})
	if err != nil || !result.OK() {
		return nil, fmt.Errorf("could not establish that the previous terminal has stopped")
	}
	panes, complete := ParsePollSnapshot(result.Stdout, names)
	if !complete {
		return nil, fmt.Errorf("incomplete terminal check; retry when the target is reachable")
	}
	for _, pane := range panes {
		if !pane.Missing {
			return nil, fmt.Errorf("a previous terminal is still running or could not be checked; attach to it or stop it before resuming")
		}
	}
	current, err := m.DB.Session(sourceID)
	if err != nil {
		return nil, err
	}
	if current.ArchivedAt != nil || current.EndedAt == nil || current.TargetID != source.TargetID || current.TmuxSession != source.TmuxSession || current.Workdir != source.Workdir || current.Agent != source.Agent {
		return nil, fmt.Errorf("source session changed; refresh before resuming")
	}
	if name == "" {
		name = source.Name + " · resumed"
	}
	return m.launch(ctx, LaunchOpts{Configuration: config, TargetID: source.TargetID, ProjectID: source.ProjectID, GroupPath: source.GroupPath, Name: name, Agent: source.Agent, Model: source.Model, Workdir: source.Workdir, ResumeID: cid})
}
