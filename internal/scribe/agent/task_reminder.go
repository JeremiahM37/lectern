package agent

import (
	"fmt"
	"strings"
)

// Task list reminder (spec 03 §2.2 step 12). The tab's task list is put in
// front of the model as a user-message reminder, not in the system prompt,
// so the system prompt stays byte-identical across steps. The reminder is
// latched: it is sent when the list is first set and again only when its
// text changes, so an unchanged list costs no tokens on later steps.

// setTaskList records the list task_list just wrote. The planner calls it
// from inside the tool; the loop reads it when it builds the step's hints.
func (a *Agent) setTaskList(list []Task) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.taskList = append([]Task(nil), list...)
}

// taskReminder returns the task list reminder due after this step, or "".
// It is due when there is a list whose text differs from the one last sent
// in the current history. A replaced history (compaction, reset, resume) has
// dropped earlier reminders, so the latch starts over with it. An empty list
// clears the latch, so the next list counts as new.
func (a *Agent) taskReminder() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.taskShownGen != a.gen {
		a.taskShown = ""
		a.taskShownGen = a.gen
	}
	text := renderTaskList(a.taskList)
	if text == "" {
		a.taskShown = ""
		return ""
	}
	if text == a.taskShown {
		return ""
	}
	a.taskShown = text
	return "<system-reminder>\n" + text + "\n</system-reminder>"
}

// renderTaskList is the list as the model reads it: one line per task, in
// the same form the task_list tool answers with. It returns "" for no tasks.
func renderTaskList(list []Task) string {
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Task list (keep it current with task_list as the work changes):\n")
	for _, t := range list {
		fmt.Fprintf(&b, "%s. [%s] %s\n", t.ID, t.Status, t.Text)
	}
	return strings.TrimRight(b.String(), "\n")
}
