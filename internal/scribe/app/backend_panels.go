package app

import (
	"errors"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
)

var errNoDispatch = errors.New("no dispatch has run in this session")

// Panel data for the TUI: the model log, the compaction log and the
// subagents of the newest dispatch. All of it is read from the current
// session, so a new session starts with empty panels.

// ModelEvents is the current session's model log.
func (b *scribeBackend) ModelEvents() []agent.Event {
	return b.current().Agent.ModelEvents()
}

// CompactionLog is the current session's compaction history.
func (b *scribeBackend) CompactionLog() []agent.Event {
	return b.current().Agent.CompactionLog()
}

// Subagents lists the subagents of the newest dispatch in the current session.
func (b *scribeBackend) Subagents() []agent.AgentInfo {
	ap := b.current()
	if ap.Dispatch == nil {
		return nil
	}
	return ap.Dispatch.Status()
}

// CancelSubagent stops one subagent of the newest dispatch.
func (b *scribeBackend) CancelSubagent(id string) error {
	ap := b.current()
	if ap.Dispatch == nil {
		return errNoDispatch
	}
	return ap.Dispatch.CancelAgent(id)
}

// CancelDispatch stops every subagent of the running dispatch (/cancel all).
func (b *scribeBackend) CancelDispatch() error {
	a := b.current()
	if a.Dispatch == nil {
		return errors.New("subagents are not available in this session")
	}
	return a.Dispatch.CancelDispatch()
}
