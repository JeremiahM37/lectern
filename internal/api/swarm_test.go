package api_test

import (
	"fmt"
	"testing"
)

// attemptToken reads the per-attempt hook token straight from the database, the
// way a staged .lectern/env would hand it to a running agent.
func (h *harness) attemptToken(taskID int64) string {
	h.t.Helper()
	att, err := h.App.DB.LatestAttempt(taskID)
	if err != nil {
		h.t.Fatalf("no attempt for task %d: %v", taskID, err)
	}
	return att.Token
}

func TestAgentFilesFollowupCard(t *testing.T) {
	h := newHarness(t)
	task := h.run(h.seededProjectID(), "spawner", "build it [mock:subtask]", nil)
	var filed []obj
	for _, x := range h.getList("/api/tasks") {
		if x.str("created_by") == "agent" && int64(x.num("parent_task_id")) == task.id() {
			filed = append(filed, x)
		}
	}
	if len(filed) != 1 {
		t.Fatalf("expected one filed card, got %d", len(filed))
	}
	if filed[0].str("title") != "Agent follow-up: add tests" {
		t.Errorf("title: %v", filed[0].str("title"))
	}
	// dispatch=false means the human decides when it runs
	if filed[0].str("status") != "backlog" {
		t.Errorf("status: %v", filed[0].str("status"))
	}
}

// The cap is what stops a looping agent from burying the board.
func TestHookTasksAuthAndCap(t *testing.T) {
	h := newHarness(t)
	if code := h.status("POST", "/api/hook/tasks", obj{"token": "bogus", "title": "x"}); code != 403 {
		t.Fatalf("an unknown token must be refused, got %d", code)
	}
	pid := h.seededProjectID()
	task := h.task(pid, "cap host", "long one [mock:slow]", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitStatus(task.id(), "running")
	token := h.attemptToken(task.id())

	for i := 0; i < 10; i++ {
		body := obj{"token": token, "title": fmt.Sprintf("sub %d", i), "dispatch": i == 0}
		if code := h.status("POST", "/api/hook/tasks", body); code != 201 {
			t.Fatalf("filing card %d: %d", i, code)
		}
	}
	if code := h.status("POST", "/api/hook/tasks",
		obj{"token": token, "title": "over cap"}); code != 429 {
		t.Fatalf("the 11th card must be refused, got %d", code)
	}

	var subs []obj
	for _, x := range h.getList("/api/tasks") {
		if int64(x.num("parent_task_id")) == task.id() {
			subs = append(subs, x)
		}
	}
	if len(subs) != 10 {
		t.Fatalf("expected 10 filed cards, got %d", len(subs))
	}
	for _, s := range subs {
		if s.str("title") == "sub 0" {
			switch s.str("status") {
			case "queued", "running", "review", "done":
			default:
				t.Errorf("dispatch=true should have started it: %v", s.str("status"))
			}
		}
	}
}

func TestReviewerGateApprove(t *testing.T) {
	h := newHarness(t)
	p := h.project("gated-ok", obj{"review_gate": true})
	task := h.run(p.id(), "gated work", "change stuff [mock:approve-verdict]", nil)

	var reviewer obj
	h.waitUntil("a reviewer card", func() bool {
		for _, x := range h.getList("/api/tasks") {
			if x.str("created_by") == "reviewer-gate" &&
				int64(x.num("parent_task_id")) == task.id() {
				reviewer = x
				return true
			}
		}
		return false
	})
	// the reviewer runs read-only, in the parent's worktree
	if reviewer.str("permission_mode") != "plan" {
		t.Errorf("reviewer permission mode: %v", reviewer.str("permission_mode"))
	}
	h.waitUntil("the reviewer card to close itself", func() bool {
		return h.taskStatus(reviewer.id()) == "done"
	})

	parent := h.get(fmt.Sprintf("/api/tasks/%d", task.id()))
	review := parent.sub("attempt").sub("result").sub("review")
	if review.str("verdict") != "APPROVE" {
		t.Fatalf("verdict: %v", review)
	}
	ratt, err := h.App.DB.LatestAttempt(reviewer.id())
	if err != nil {
		t.Fatal(err)
	}
	if ratt.WorktreePath != parent.sub("attempt").str("worktree_path") {
		t.Errorf("the reviewer must share the parent's worktree: %q", ratt.WorktreePath)
	}
	found := false
	for _, e := range h.getList(fmt.Sprintf("/api/tasks/%d/events", task.id())) {
		if e.str("type") == "review_verdict" && e.sub("payload").str("verdict") == "APPROVE" {
			found = true
		}
	}
	if !found {
		t.Error("the verdict must show on the parent's timeline")
	}
}

func TestReviewerGateRequestChanges(t *testing.T) {
	h := newHarness(t)
	p := h.project("gated-no", obj{"review_gate": true})
	task := h.run(p.id(), "rejected work", "sloppy change [mock:reject-verdict]", nil)
	h.waitUntil("a REQUEST_CHANGES verdict on the parent", func() bool {
		got := h.get(fmt.Sprintf("/api/tasks/%d", task.id()))
		return got.sub("attempt").sub("result").sub("review").str("verdict") == "REQUEST_CHANGES"
	})
	// no reviewer-of-reviewer recursion
	count := 0
	for _, x := range h.getList("/api/tasks") {
		if x.str("created_by") == "reviewer-gate" && int64(x.num("project_id")) == p.id() {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one reviewer card, got %d", count)
	}
}

func TestABParallelAttempts(t *testing.T) {
	h := newHarness(t)
	task := h.task(h.seededProjectID(), "AB race", "try it", obj{"model": "sonnet"})
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{"model_b": "opus"}, 200)
	got := h.waitStatus(task.id(), "review")

	attempts := got.list("attempts")
	if len(attempts) != 2 || attempts[0].num("n") != 1 || attempts[1].num("n") != 2 {
		t.Fatalf("attempts: %v", attempts)
	}
	if attempts[1].str("model") != "opus" {
		t.Errorf("the B side must carry its own model: %v", attempts[1])
	}
	for _, a := range attempts {
		if a.str("status") != "done" {
			t.Errorf("attempt %v did not finish: %v", a.num("n"), a.str("status"))
		}
	}
	// per-attempt diff and events, so the two can actually be compared
	for _, n := range []int{1, 2} {
		d := h.get(fmt.Sprintf("/api/tasks/%d/diff?attempt_n=%d", task.id(), n))
		if int(d.num("attempt_n")) != n {
			t.Errorf("diff for attempt %d: %v", n, d)
		}
		evs := h.getList(fmt.Sprintf("/api/tasks/%d/events?attempt_n=%d", task.id(), n))
		if len(evs) == 0 {
			t.Errorf("attempt %d has no events", n)
		}
		for _, e := range evs {
			if int(e.num("attempt_n")) != n {
				t.Errorf("event leaked across attempts: %v", e)
			}
		}
	}
}

func TestABStaysRunningUntilBothLand(t *testing.T) {
	h := newHarness(t)
	task := h.task(h.seededProjectID(), "AB slow", "x [mock:slow]", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{"model_b": "haiku"}, 200)
	h.waitStatus(task.id(), "running")
	got := h.get(fmt.Sprintf("/api/tasks/%d", task.id()))
	unfinished := 0
	for _, a := range got.list("attempts") {
		if a.str("status") == "queued" || a.str("status") == "running" {
			unfinished++
		}
	}
	if unfinished > 0 && got.str("status") != "running" {
		t.Fatalf("the task left running while an attempt was still live: %v", got.str("status"))
	}
	h.waitStatus(task.id(), "review")
}
