package api_test

import (
	"fmt"
	"strings"
	"testing"
)

func TestHealth(t *testing.T) {
	h := newHarness(t)
	got := h.get("/api/health")
	if got["ok"] != true || got["mock"] != true {
		t.Fatalf("health: %v", got)
	}
	// flat, always-present counts for dashboard widgets (the Homepage tile maps
	// fields that must never disappear when a column empties out)
	for _, f := range []string{"running", "queued", "review", "pending_approvals"} {
		if _, ok := got[f].(float64); !ok {
			t.Errorf("%s missing or not a number: %v", f, got[f])
		}
	}
}

func TestCRUDAndBoard(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	task := h.task(pid, "CRUD check", "Do the thing.", obj{"priority": 3})
	if task.str("status") != "backlog" || task.num("priority") != 3 {
		t.Fatalf("new task: %v", task)
	}
	if task.str("project_name") == "" || task.str("target_name") == "" {
		t.Errorf("a card must carry its project and target: %v", task)
	}

	var patched obj
	h.decode("PATCH", fmt.Sprintf("/api/tasks/%d", task.id()),
		obj{"title": "renamed", "priority": 1}, 200, &patched)
	if patched.str("title") != "renamed" {
		t.Errorf("patch: %v", patched)
	}

	// an illegal board move is rejected
	if code := h.status("PATCH", fmt.Sprintf("/api/tasks/%d", task.id()),
		obj{"status": "review"}); code != 409 {
		t.Errorf("backlog → review should be 409, got %d", code)
	}
	// queueing must go through /dispatch, which is what creates the attempt
	if code := h.status("PATCH", fmt.Sprintf("/api/tasks/%d", task.id()),
		obj{"status": "queued"}); code != 409 {
		t.Errorf("PATCH to queued should be 409, got %d", code)
	}

	found := false
	for _, x := range h.getList("/api/tasks?status=backlog") {
		if x.id() == task.id() {
			found = true
		}
	}
	if !found {
		t.Error("the task is missing from its own column")
	}
}

func TestHappyPathToDone(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	task := h.task(pid, "Happy path", "Do the thing.", nil)
	dispatched := h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	if dispatched.str("status") != "queued" {
		t.Fatalf("dispatch: %v", dispatched)
	}

	final := h.waitStatus(task.id(), "review")
	att := final.sub("attempt")
	if att.num("n") != 1 || att.num("exit_code") != 0 {
		t.Fatalf("attempt: %v", att)
	}
	if att.sub("result").num("cost_usd") != 0.0123 {
		t.Errorf("cost was not captured: %v", att.sub("result"))
	}
	if !strings.HasPrefix(att.str("branch"), "lec/task") || att.str("worktree_path") == "" {
		t.Errorf("worktree/branch: %v", att)
	}

	events := h.getList(fmt.Sprintf("/api/tasks/%d/events", task.id()))
	seen := map[string]bool{}
	for _, e := range events {
		seen[e.str("type")] = true
		if e.num("attempt_n") != 1 {
			t.Errorf("event from the wrong attempt: %v", e)
		}
	}
	for _, want := range []string{"init", "text", "tool_use", "tool_result", "result"} {
		if !seen[want] {
			t.Errorf("timeline is missing %q (have %v)", want, seen)
		}
	}

	diff := h.get(fmt.Sprintf("/api/tasks/%d/diff", task.id()))
	files := diff.list("files")
	stats := diff.list("stats")
	if len(files) == 0 || files[0].str("path") != "app.py" {
		t.Fatalf("diff files: %v", files)
	}
	if stats[0].num("additions") != 4 || stats[0].num("deletions") != 1 {
		t.Errorf("diff stats: %v", stats)
	}
	if !strings.Contains(files[0].str("patch"), `+    print("hello, lectern")`) {
		t.Errorf("patch body: %v", files[0].str("patch"))
	}

	done := h.post(fmt.Sprintf("/api/tasks/%d/complete", task.id()), nil, 200)
	if done.str("status") != "done" {
		t.Fatalf("complete: %v", done)
	}
	// done is terminal
	if code := h.status("POST", fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}); code != 409 {
		t.Errorf("dispatching a done task should be 409, got %d", code)
	}
}

func TestFailureAndRetry(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	task := h.task(pid, "Fails", "break [mock:fail]", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	failed := h.waitStatus(task.id(), "failed")
	if failed.sub("attempt").num("exit_code") != 1 {
		t.Fatalf("exit code: %v", failed.sub("attempt"))
	}

	// a retry spawns attempt 2 rather than reusing the failed one
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitUntil("attempt 2", func() bool {
		return h.get(fmt.Sprintf("/api/tasks/%d", task.id())).sub("attempt").num("n") == 2
	})
}

func TestCancelRunning(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	task := h.task(pid, "Cancel me", "take a while [mock:slow]", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitStatus(task.id(), "running")
	got := h.post(fmt.Sprintf("/api/tasks/%d/cancel", task.id()), nil, 200)
	if got.str("status") != "cancelled" {
		t.Fatalf("cancel: %v", got)
	}
}

func TestFollowupResumesInSameWorktree(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	task := h.run(pid, "Follow-up flow", "Do the thing.", nil)
	taskID := task.id()
	first := task.sub("attempt")

	resp := h.post(fmt.Sprintf("/api/tasks/%d/followup", taskID),
		obj{"feedback": "Also rename health to status"}, 200)
	if resp.str("status") != "queued" {
		t.Fatalf("followup: %v", resp)
	}
	second := h.waitStatus(taskID, "review").sub("attempt")
	if second.num("n") != 2 {
		t.Fatalf("expected attempt 2, got %v", second)
	}
	if second.str("worktree_path") != first.str("worktree_path") ||
		second.str("branch") != first.str("branch") {
		t.Errorf("a follow-up must reuse the worktree: %v vs %v", second, first)
	}
	// attempt 2 must have run its OWN session in the reused worktree — a stale
	// exit_code/events.jsonl from attempt 1 once finalised it instantly with the
	// previous run's output, and mock-only tests happily hid it
	seen := map[string]bool{}
	for _, e := range h.getList(fmt.Sprintf("/api/tasks/%d/events?attempt_n=2", taskID)) {
		seen[e.str("type")] = true
	}
	if !seen["init"] || !seen["result"] {
		t.Errorf("attempt 2 has no session of its own: %v", seen)
	}
	if second.num("exit_code") != 0 {
		t.Errorf("attempt 2 exit code: %v", second["exit_code"])
	}
}

func TestValidationErrors(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	if code := h.status("POST", "/api/tasks", obj{"project_id": 999, "title": "x"}); code != 400 {
		t.Errorf("unknown project should be 400, got %d", code)
	}
	if code := h.status("GET", "/api/tasks/9999", nil); code != 404 {
		t.Errorf("unknown task GET: %d", code)
	}
	if code := h.status("POST", "/api/tasks/9999/dispatch", obj{}); code != 404 {
		t.Errorf("unknown task dispatch: %d", code)
	}
	task := h.task(pid, "No diff yet", "", nil)
	if code := h.status("GET", fmt.Sprintf("/api/tasks/%d/diff", task.id()), nil); code != 404 {
		t.Errorf("diff before any run should be 404, got %d", code)
	}
	if code := h.status("POST", fmt.Sprintf("/api/tasks/%d/followup", task.id()),
		obj{"feedback": "x"}); code != 409 {
		t.Errorf("follow-up outside review should be 409, got %d", code)
	}
}
