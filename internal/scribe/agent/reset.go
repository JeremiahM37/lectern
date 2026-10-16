package agent

import (
	"context"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// ResetHistory replaces the whole history with msgs and, when mode is not
// empty, switches to it. Both happen under the agent lock.
func (a *Agent) ResetHistory(msgs []llm.Message, mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.msgs = append([]llm.Message(nil), msgs...)
	// The new history has no mode notices in it, so the next turn latches
	// its mode afresh.
	a.latchedMode = ""
	a.gen++
	if mode != "" {
		a.mode = mode
	}
	a.lastIn = 0
}

// RunClearing runs one turn like Agent.Run. If the user answers a plan review
// with "Clear context and execute" during it, the turn ends, the history is
// replaced by a single user message holding the saved plan, the mode goes to
// default, and that message starts the continuation of the turn. It returns
// the stats of the last run. p may be nil.
func RunClearing(ctx context.Context, a *Agent, p *Planner, prompt string, images []llm.Part) (TurnStats, error) {
	stats, err := a.Run(ctx, prompt, images)
	for p != nil && ctx.Err() == nil {
		c := p.takeCleared()
		if c == nil {
			break
		}
		a.ResetHistory(nil, "default")
		stats, err = a.Run(ctx, clearedPlanMessage(c), nil)
	}
	if p != nil {
		p.settle(err == nil && ctx.Err() == nil)
	}
	return stats, err
}

// clearedPlanMessage is the one user message that starts the cleared context.
func clearedPlanMessage(c *pendingClear) string {
	var b strings.Builder
	b.WriteString("The user approved this plan and chose to clear the planning conversation. ")
	b.WriteString("Implement the plan now, step by step. Earlier messages are gone; work from this plan alone.\n\n")
	if c.plan.Path != "" {
		b.WriteString("Plan file: " + c.plan.Path + "\n\n")
	}
	b.WriteString(c.md)
	b.WriteString("\n\nCall update_plan_step with status active when you start a step and done when it is finished. ")
	b.WriteString("Verify with tests or builds at the end.")
	return b.String()
}
