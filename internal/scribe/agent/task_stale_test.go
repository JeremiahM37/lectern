package agent

import (
	"strings"
	"testing"
)

func TestStaleTaskListIsShownAgainWithAPrompt(t *testing.T) {
	a := New(baseOptions(newFake(t), lookRegistry(), &eventLog{}, t.TempDir()))
	a.setTaskList([]Task{{ID: "1", Text: "Add Sub", Status: "pending"}, {ID: "2", Text: "Add tests", Status: "pending"}})
	if r := a.taskReminder(); !strings.Contains(r, "Add Sub") {
		t.Fatalf("first reminder = %q", r)
	}
	for i := 1; i < taskStaleSteps; i++ {
		if r := a.taskReminder(); r != "" {
			t.Fatalf("step %d: unchanged list re-sent too early: %q", i, r)
		}
	}
	if r := a.taskReminder(); !strings.Contains(r, "Mark finished items done") {
		t.Fatalf("stale list not re-shown with a prompt: %q", r)
	}
	// A finished list is never nagged about.
	a.setTaskList([]Task{{ID: "1", Text: "Add Sub", Status: "done"}})
	_ = a.taskReminder()
	for i := 0; i < 2*taskStaleSteps; i++ {
		if r := a.taskReminder(); r != "" {
			t.Fatalf("finished list re-sent: %q", r)
		}
	}
}
