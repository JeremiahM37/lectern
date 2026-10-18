package app

import (
	"fmt"
	"net/http"
	"strings"
)

// CloseProviders closes the idle HTTP connections the LLM providers keep open
// (06 §8.3, "deactivate provider"). Every provider client the registry builds
// uses the default transport, so closing its idle connections releases them
// all. Connections in use are left alone, and the TUI calls this only after
// every turn has stopped, so none is in use.
func (b *scribeBackend) CloseProviders() error {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
	return nil
}

// KillProcessGroups is the last shutdown step (06 §8.3, "kill the process
// group"). Close has already stopped this session's background jobs through
// KillAll. This step makes sure none is still running: each job that is still
// running after the kill is killed again, and any that will not exit is named
// in the error so the caller can report it.
func (b *scribeBackend) KillProcessGroups() error {
	a := b.current()
	if a == nil || a.ext.jobs == nil {
		return nil
	}
	var stuck []string
	for _, ji := range a.ext.jobs.List() {
		if ji.Status != "running" {
			continue
		}
		// Kill waits up to 3 s for the job to exit.
		_ = a.ext.jobs.Kill(ji.ID)
		job, ok := a.ext.jobs.Get(ji.ID)
		if !ok {
			continue
		}
		select {
		case <-job.Done():
		default:
			stuck = append(stuck, ji.ID)
		}
	}
	if len(stuck) > 0 {
		return fmt.Errorf("background jobs still running after kill: %s", strings.Join(stuck, ", "))
	}
	return nil
}
