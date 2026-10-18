package app

import (
	"context"
	"errors"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// ContinueTurn re-runs the session's interrupted turn (`/session continue`,
// `lectern code --continue`). It does not add the prompt again: the history
// already holds it. It returns agent.ErrNothingToContinue when the last turn
// finished.
func (a *App) ContinueTurn(ctx context.Context) (agent.TurnStats, error) {
	msgs := a.Agent.Messages()
	if !agent.Unfinished(msgs) {
		return agent.TurnStats{}, agent.ErrNothingToContinue
	}
	prompt := promptOf(msgs)
	a.mu.Lock()
	a.turn++
	turn := a.turn
	a.mu.Unlock()
	a.markTurnUI(turn)
	if a.Checks != nil {
		a.Checks.Begin(turn, prompt)
	}
	if err := a.dailyBudgetGate(); err != nil {
		return a.refuseTurn(err)
	}
	before, _ := a.Agent.Usage()
	stats, err := a.Agent.Continue(ctx)
	a.recordDailyTokens(turnOnly(stats, before))
	if a.Checks != nil {
		_ = a.Checks.End()
	}
	a.ext.afterTurn(a, stats)
	a.saveMeta(prompt, stats)
	return stats, err
}

// promptOf returns the text of the last user message, which is the prompt of
// the turn being continued (or a reminder the loop added to it).
func promptOf(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != llm.RoleUser {
			continue
		}
		return msgs[i].Text()
	}
	return ""
}

// Continue implements tui.Continuer. It re-runs an unfinished turn in the
// background, the same way Submit runs a prompt, and reports false when the
// last turn finished. A turn that is already running is an error.
func (b *scribeBackend) Continue() (bool, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return false, errors.New("this session is closed")
	}
	if b.turn {
		b.mu.Unlock()
		return false, errTurnRunning
	}
	a := b.app
	b.mu.Unlock()
	if !agent.Unfinished(a.Agent.Messages()) {
		return false, nil
	}
	b.mu.Lock()
	if b.turn || b.closed {
		b.mu.Unlock()
		return false, errTurnRunning
	}
	b.turn, b.turnDone = true, false
	b.turns.Add(1)
	b.mu.Unlock()
	go b.runContinue(a)
	return true, nil
}

// runContinue runs the continued turn, then any prompts that were submitted
// while it ran, using the same queue Submit uses.
func (b *scribeBackend) runContinue(a *App) {
	defer b.turns.Done()
	stats, err := a.ContinueTurn(context.Background())
	if turnFailed(stats, err) {
		a.emit(agent.Event{Kind: agent.EvError, Text: err.Error()})
	}
	b.mu.Lock()
	if len(b.queued) == 0 || b.app != a || b.closed {
		b.turn = false
		b.queued = nil
		b.mu.Unlock()
		return
	}
	next := b.queued[0]
	b.queued = b.queued[1:]
	b.turnDone = false
	b.turns.Add(1) // runTurns releases its own count
	b.mu.Unlock()
	b.runTurns(a, next.prompt, next.images)
}

// Shutdown ends the session for good, as the process exits: it aborts a
// running turn, waits up to timeout for the turn to write its checkpoints and
// session metadata, then closes the App. It is safe after the TUI has already
// closed the backend.
func (b *scribeBackend) Shutdown(timeout time.Duration) {
	b.mu.Lock()
	b.queued = nil
	a := b.app
	b.mu.Unlock()
	a.Agent.Abort()
	deadline := time.Now().Add(timeout)
	for b.busy() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	_ = b.Close()
}

// turnFailed reports whether a turn that returned err should show it as an
// error. A turn stopped on purpose (Esc, or the process ending) returns
// context.Canceled: the transcript already marks it Interrupted, so it must
// not also say "Something went wrong".
func turnFailed(stats agent.TurnStats, err error) bool {
	return err != nil && stats.Error == "" && !stats.Aborted && !errors.Is(err, context.Canceled)
}
