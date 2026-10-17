package agent

import (
	"slices"
	"strings"
)

// MarkTask sets the status of the task-list item a dispatch task named (its
// taskId, spec 03 §3.1). The dispatch calls it with "done" when the task
// finished and "blocked" when it failed. taskID may hold several ids joined
// by commas: a dispatch task that absorbed another over the task limit keeps
// both ids (dispatch_plan.go). An id that matches no item is ignored, because
// the subagent ran either way and the model should not lose its report over a
// stale id.
func (p *Planner) MarkTask(taskID, status string) {
	ids := strings.Split(taskID, ",")
	for i := range ids {
		ids[i] = strings.TrimSpace(ids[i])
	}
	p.mu.Lock()
	changed := false
	for i := range p.tasks {
		if slices.Contains(ids, p.tasks[i].ID) && p.tasks[i].Status != status {
			p.tasks[i].Status = status
			changed = true
		}
	}
	list := append([]Task(nil), p.tasks...)
	p.mu.Unlock()
	if !changed {
		return
	}
	// Same path as a task_list update, so the TUI and the loop's reminder
	// both see the new status.
	if p.Sink != nil {
		p.Sink(Event{Kind: EvTasks, Tasks: list})
	}
	if p.Agent != nil {
		p.Agent.setTaskList(list)
	}
}
