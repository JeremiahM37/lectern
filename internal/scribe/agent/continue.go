package agent

import (
	"context"
	"errors"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// ErrNothingToContinue means the history does not end in an unfinished turn.
var ErrNothingToContinue = errors.New("nothing to continue: the last turn finished")

// Unfinished reports whether a saved history ends in a turn that never got
// an answer: its last message is a user message (the prompt, a steering note
// or a reminder the loop appended) or a tool result. An assistant message
// after the last user message or tool result means the turn finished.
func Unfinished(msgs []llm.Message) bool {
	if len(msgs) == 0 {
		return false
	}
	switch msgs[len(msgs)-1].Role {
	case llm.RoleUser, llm.RoleTool:
		return true
	}
	return false
}

// Continue re-runs an interrupted turn from the history as it stands. Unlike
// Run it appends no user message, because the prompt is already in the
// history; the model simply picks up after the last message it saw. It
// returns ErrNothingToContinue when the last turn finished.
func (a *Agent) Continue(ctx context.Context) (TurnStats, error) {
	start := time.Now()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return TurnStats{}, errors.New("agent is already running")
	}
	if !Unfinished(a.msgs) {
		a.mu.Unlock()
		return TurnStats{}, ErrNothingToContinue
	}
	a.running, a.cancel, a.stopTurn = true, cancel, false
	a.checkRounds = 0
	mode := a.mode
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.running, a.cancel = false, nil
		a.mu.Unlock()
	}()

	stats := TurnStats{}
	// No Prompt: the turn's user message is already in the transcript.
	a.emit(Event{Kind: EvTurnStart, Model: a.CurrentModel().Ref, Mode: mode})

	final, err := a.loop(ctx, &stats)
	stats.Duration = time.Since(start)
	a.mu.Lock()
	stats.Tokens, stats.CostUSD = a.total, a.cost
	a.mu.Unlock()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			stats.Aborted = true
			err = context.Canceled
		} else {
			stats.Error = err.Error()
		}
	}
	if a.opts.Hooks != nil {
		a.opts.Hooks.Stop(context.WithoutCancel(ctx), stats)
	}
	a.emit(Event{Kind: EvTurnDone, Text: final, Stats: &stats})
	return stats, err
}
