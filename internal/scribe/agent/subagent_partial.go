package agent

import (
	"context"
	"errors"
	"strings"
)

// setPartial records the answer text a failed model call left behind.
func (a *Agent) setPartial(text string) {
	a.mu.Lock()
	a.partial = text
	a.mu.Unlock()
}

// Partial is the answer text the last model call left when it failed for
// good, after its retries. It is "" when that call left no text, or when
// the last call succeeded.
func (a *Agent) Partial() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.partial
}

// SubagentReport is what a subagent hands back when its run is over. A run
// that succeeded reports its last answer. A run that failed reports the
// partial answer of the call that gave up, marked as partial, so the work it
// did is not lost (spec 03 §3.6). It falls back to the last answer when
// there is no partial text.
func SubagentReport(a *Agent, runErr error) string {
	if runErr != nil {
		if p := a.Partial(); strings.TrimSpace(p) != "" {
			return "Partial output (the agent stopped before it finished):\n\n" + p
		}
	}
	return lastAssistantText(a.Messages())
}

// SalvageSucceeds says whether a failed subagent's report counts as a done
// task. The spec counts it as success only when salvage text exists (spec
// 03 §3.6). A cancellation is never salvaged: the user or the dispatch
// stopped the run on purpose.
func SalvageSucceeds(runErr error, report string) bool {
	if strings.TrimSpace(report) == "" {
		return false
	}
	for _, stop := range []error{
		context.Canceled, errCancelledByUser, errDispatchCancelled,
		errBreakerOpen, errBreakerTripped,
	} {
		if errors.Is(runErr, stop) {
			return false
		}
	}
	return true
}
