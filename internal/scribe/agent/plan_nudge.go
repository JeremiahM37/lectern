package agent

// Plan nudges (spec 03 §2.2). A long plan-mode turn that has not called the
// plan tool is reminded to assemble the plan, at step 10 and again, more
// firmly, at step 20. Hints only; no tool is forced. The reminder is a user message latched into the history
// once, like the mode notices, so the system prompt stays the same bytes.
const (
	planNudgeStep       = 10
	planNudgeStrongStep = 20
)

// planNudgeText is the reminder for the first nudge of a turn.
const planNudgeText = "<system-reminder>This plan-mode turn is at step 10 and has not called the plan tool. Once you know enough, assemble the plan and submit it with the plan tool.</system-reminder>"

// planNudgeStrongText is the reminder for the second nudge of a turn.
const planNudgeStrongText = "<system-reminder>This plan-mode turn is at step 20 and still has no plan. Stop exploring and submit the plan now with the plan tool.</system-reminder>"

// planToolNames are the tools that count as having a plan.
var planToolNames = map[string]bool{"plan": true}

// planNudge adds the plan reminder due at step, if any. It only applies to
// the main agent in plan mode; a subagent's budget already covers its run.
// Each reminder is sent once per turn, and not at all once the history holds
// a plan call.
func (a *Agent) planNudge(step int) {
	if a.opts.Budget != nil || a.Mode() != "plan" {
		return
	}
	stage, text := 0, ""
	switch {
	case step >= planNudgeStrongStep:
		stage, text = 2, planNudgeStrongText
	case step >= planNudgeStep:
		stage, text = 1, planNudgeText
	}
	a.mu.Lock()
	due := stage > a.planNudges && !a.hasPlanCallLocked()
	if due {
		a.planNudges = stage
	}
	a.mu.Unlock()
	if due {
		// appendSteering joins a user message that is already last, so the
		// reminder never leaves two user turns in a row.
		a.appendSteering(text, nil)
	}
}

// hasPlanCallLocked reports whether the history holds a call to the plan or
// task list tool. The caller holds a.mu.
func (a *Agent) hasPlanCallLocked() bool {
	for _, m := range a.msgs {
		for _, c := range m.ToolCalls() {
			if planToolNames[c.ToolName] {
				return true
			}
		}
	}
	return false
}
