package agent

// planExecBlocked are the tools an agent may not call while it carries out a
// plan the user approved in the same turn. The plan already says what to
// change, so fanning out (dispatch), researching (web), or drafting another
// plan would only drift from what was approved. Everything else, including
// creating files and running checks, stays available.
var planExecBlocked = map[string]bool{
	"dispatch":         true,
	"explore":          true,
	"web_search":       true,
	"fetch_page":       true,
	"discover_pattern": true,
	"plan":             true,
	"final_response":   true,
}

// PlanExecutionAllowed reports whether tool may run while an approved plan is
// executed. The agent loop checks it from the approval to the end of the turn.
func PlanExecutionAllowed(tool string) bool { return !planExecBlocked[tool] }

// BeginPlanExecution marks the rest of this turn as carrying out an approved
// plan (see PlanExecutionAllowed). It ends with the turn.
func (a *Agent) BeginPlanExecution() {
	a.mu.Lock()
	a.planExec = true
	a.mu.Unlock()
}

func (a *Agent) executingPlan() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.planExec
}
