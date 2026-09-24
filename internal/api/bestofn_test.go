package api_test

import (
	"fmt"
	"testing"
)

// TestVariantsDispatchCreatesNAttempts generalises the A/B two-attempt shape:
// a caller can now name 1..8 variants, each with its own agent/model, and
// every one becomes its own attempt in its own worktree.
func TestVariantsDispatchCreatesNAttempts(t *testing.T) {
	h := newHarness(t)
	task := h.task(h.seededProjectID(), "N-way race", "try it", obj{"model": "sonnet"})
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{"variants": []obj{
		{"model": "opus"},
		{"model": "haiku"},
		{"agent": "codex"},
	}}, 200)
	got := h.waitStatus(task.id(), "review")

	attempts := got.list("attempts")
	if len(attempts) != 3 {
		t.Fatalf("expected 3 attempts, got %d: %v", len(attempts), attempts)
	}
	for i, want := range []struct {
		model, agent string
	}{
		{"opus", "claude"}, {"haiku", "claude"}, {"", "codex"},
	} {
		a := attempts[i]
		if a.str("model") != want.model {
			t.Errorf("attempt %d model: got %q want %q", i+1, a.str("model"), want.model)
		}
		if a.str("agent") != want.agent {
			t.Errorf("attempt %d agent: got %q want %q", i+1, a.str("agent"), want.agent)
		}
		if a.str("status") != "done" {
			t.Errorf("attempt %d did not finish: %v", i+1, a)
		}
	}
	// the task's own agent/permission_mode are untouched by variant overrides
	if got.str("agent") != "claude" {
		t.Errorf("task agent should stay claude, got %q", got.str("agent"))
	}
}

func TestVariantsRejectsTooManyOrBadAgent(t *testing.T) {
	h := newHarness(t)
	task := h.task(h.seededProjectID(), "too many", "x", nil)
	var nine []obj
	for i := 0; i < 9; i++ {
		nine = append(nine, obj{"model": "sonnet"})
	}
	if code := h.status("POST", fmt.Sprintf("/api/tasks/%d/dispatch", task.id()),
		obj{"variants": nine}); code != 422 {
		t.Fatalf("9 variants should be refused, got %d", code)
	}

	task2 := h.task(h.seededProjectID(), "bad agent", "x", nil)
	if code := h.status("POST", fmt.Sprintf("/api/tasks/%d/dispatch", task2.id()),
		obj{"variants": []obj{{"agent": "nonexistent-agent"}}}); code != 422 {
		t.Fatalf("an unknown agent should be refused, got %d", code)
	}
	// the task must be untouched — still dispatchable
	if h.taskStatus(task2.id()) != "backlog" {
		t.Errorf("a rejected dispatch must not half-apply: %v", h.taskStatus(task2.id()))
	}

	task3 := h.task(h.seededProjectID(), "both shapes", "x", nil)
	if code := h.status("POST", fmt.Sprintf("/api/tasks/%d/dispatch", task3.id()),
		obj{"variants": []obj{{"model": "opus"}}, "model_b": "haiku"}); code != 422 {
		t.Fatalf("variants + model_b together should be refused, got %d", code)
	}
}

// TestPickAttemptSwapsLatestAndArchivesOthers checks the Compare view's "Pick
// this one": the winner becomes the LatestAttempt every existing single-
// attempt endpoint already reads, and every other worktree is reclaimed.
func TestPickAttemptSwapsLatestAndArchivesOthers(t *testing.T) {
	h := newHarness(t)
	task := h.task(h.seededProjectID(), "pick one", "try it", obj{"model": "sonnet"})
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{"model_b": "opus"}, 200)
	h.waitStatus(task.id(), "review")

	before := h.get(fmt.Sprintf("/api/tasks/%d", task.id()))
	if before.sub("attempt").num("n") != 2 {
		t.Fatalf("latest attempt should be #2 before picking: %v", before.sub("attempt"))
	}
	var attempt1ID, attempt2ID int64
	for _, a := range before.list("attempts") {
		if a.num("n") == 1 {
			attempt1ID = a.id()
		} else {
			attempt2ID = a.id()
		}
	}

	resp := h.post(fmt.Sprintf("/api/tasks/%d/pick_attempt", task.id()), obj{"n": 1}, 200)
	archivedRaw, _ := resp["archived_attempts"].([]any)
	if len(archivedRaw) != 1 || int(archivedRaw[0].(float64)) != 2 {
		t.Fatalf("expected attempt #2 archived, got %v", archivedRaw)
	}

	// picking #1 swaps its N with whatever held the highest N (#2) — so the
	// SAME ROW that was #1 is now LatestAttempt, and the row that was #2 is
	// demoted. Identity is tracked by id, not by the N label, which the swap
	// deliberately reassigns.
	after := h.get(fmt.Sprintf("/api/tasks/%d", task.id()))
	if after.sub("attempt").id() != attempt1ID {
		t.Fatalf("attempt formerly #1 should now be latest: %v", after.sub("attempt"))
	}
	if after.sub("attempt").str("worktree_path") == "" {
		t.Error("the winner must keep its worktree")
	}
	for _, a := range after.list("attempts") {
		if a.id() == attempt1ID {
			continue
		}
		if a.id() != attempt2ID {
			t.Fatalf("unexpected attempt id: %v", a)
		}
		if a.str("worktree_path") != "" {
			t.Errorf("the demoted attempt should have its worktree reclaimed: %v", a)
		}
	}

	if code := h.status("POST", fmt.Sprintf("/api/tasks/%d/pick_attempt", task.id()),
		obj{"n": 99}); code != 404 {
		t.Fatalf("picking a nonexistent attempt should 404, got %d", code)
	}
}

// TestJudgeTaskRecordsVerdictOnParent drives a real (mock) judge task end to
// end: dispatch two attempts, ask for a judge, and check its verdict lands on
// the parent task's latest attempt once the judge card finishes.
func TestJudgeTaskRecordsVerdictOnParent(t *testing.T) {
	h := newHarness(t)
	task := h.task(h.seededProjectID(), "judge me", "try it [mock:judge:2]", obj{"model": "sonnet"})
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{"model_b": "opus"}, 200)
	h.waitStatus(task.id(), "review")

	if code := h.status("POST", fmt.Sprintf("/api/tasks/%d/judge", task.id()), obj{}); code != 201 {
		t.Fatalf("judge dispatch: %d", code)
	}
	var judgeTaskID int64
	h.waitUntil("a judge card", func() bool {
		for _, x := range h.getList("/api/tasks") {
			if x.str("created_by") == "judge" && int64(x.num("parent_task_id")) == task.id() {
				judgeTaskID = x.id()
				return true
			}
		}
		return false
	})
	h.waitUntil("the judge card to close itself", func() bool {
		return h.taskStatus(judgeTaskID) == "done"
	})

	parent := h.get(fmt.Sprintf("/api/tasks/%d", task.id()))
	judge := parent.sub("attempt").sub("result").sub("judge")
	if int(judge.num("winner_attempt")) != 2 {
		t.Fatalf("judge verdict: %v", judge)
	}

	// judging fewer than two attempts is refused
	solo := h.task(h.seededProjectID(), "solo", "x", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", solo.id()), obj{}, 200)
	h.waitStatus(solo.id(), "review")
	if code := h.status("POST", fmt.Sprintf("/api/tasks/%d/judge", solo.id()), obj{}); code != 409 {
		t.Fatalf("judging one attempt should be refused, got %d", code)
	}
}
