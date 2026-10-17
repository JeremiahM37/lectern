package app

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"
)

// A session's dispatcher marks the session's own task list, so a dispatch
// task that names item 2 finishes that item in the list the model reads.
func TestDispatcherMarksTheSessionsTaskList(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "scribe@example.test"},
		{"config", "user.name", "Scribe Test"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	a, err := Open(context.Background(), Options{Workdir: repo, Host: NewTUIHost(), Version: "test"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if a.Dispatch.TaskStatus == nil {
		t.Fatal("the dispatcher has no TaskStatus, so a taskId would mark nothing")
	}

	tl, ok := a.Tools.Get("task_list")
	if !ok {
		t.Fatal("task_list tool not registered")
	}
	args, _ := json.Marshal(map[string]any{"action": "set", "tasks": []string{"first", "second"}})
	if res := tl.Run(context.Background(), nil, args); res.IsError {
		t.Fatalf("task_list set: %s", res.Output)
	}

	a.Dispatch.TaskStatus("2", "blocked")
	got := a.Planner.Tasks()
	if len(got) != 2 || got[0].Status != "pending" || got[1].Status != "blocked" {
		t.Fatalf("tasks = %+v, want item 1 pending and item 2 blocked", got)
	}
}
