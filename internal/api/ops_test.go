package api_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// backdate makes an attempt look old enough for the janitor to sweep.
func (h *harness) backdate(taskID int64, days float64) *store.Attempt {
	h.t.Helper()
	att, err := h.App.DB.LatestAttempt(taskID)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.App.DB.Update("attempts", att.ID,
		map[string]any{"finished_at": store.Now() - days*86400}); err != nil {
		h.t.Fatal(err)
	}
	return att
}

func (h *harness) finishedTask(pid int64, title string) obj {
	h.t.Helper()
	task := h.run(pid, title, "x", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/complete", task.id()), nil, 200)
	return task
}

func removedIDs(got obj) map[int64]bool {
	out := map[int64]bool{}
	items, _ := got["removed_attempts"].([]any)
	for _, i := range items {
		out[int64(i.(float64))] = true
	}
	return out
}

func TestJanitorSweepsOldDoneWorktrees(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	old := h.finishedTask(pid, "old done")
	fresh := h.finishedTask(pid, "fresh done")
	att := h.backdate(old.id(), 30)

	got := h.post("/api/admin/janitor", obj{"days": 7}, 200)
	if !removedIDs(got)[att.ID] {
		t.Fatalf("the aged worktree was not swept: %v", got)
	}
	if h.get(fmt.Sprintf("/api/tasks/%d", old.id())).sub("attempt").str("worktree_path") != "" {
		t.Error("the swept attempt should have no worktree path left")
	}
	if h.get(fmt.Sprintf("/api/tasks/%d", fresh.id())).sub("attempt").str("worktree_path") == "" {
		t.Error("a fresh worktree must be left alone")
	}
}

func TestJanitorRespectsKeepWorktrees(t *testing.T) {
	h := newHarness(t)
	p := h.project("keeper", obj{"keep_worktrees": true})
	task := h.finishedTask(p.id(), "kept")
	att := h.backdate(task.id(), 30)
	got := h.post("/api/admin/janitor", obj{"days": 7}, 200)
	if removedIDs(got)[att.ID] {
		t.Fatal("keep_worktrees was ignored")
	}
	if h.get(fmt.Sprintf("/api/tasks/%d", task.id())).sub("attempt").str("worktree_path") == "" {
		t.Error("the kept worktree was reclaimed anyway")
	}
}

func TestJanitorIgnoresReviewTasks(t *testing.T) {
	h := newHarness(t)
	// still under review: the diff is the whole point, and it lives in the worktree
	task := h.run(h.seededProjectID(), "in review", "x", nil)
	att := h.backdate(task.id(), 30)
	got := h.post("/api/admin/janitor", obj{"days": 7}, 200)
	if removedIDs(got)[att.ID] {
		t.Fatal("a task still awaiting review must keep its worktree")
	}
}

// The deep probe both TESTS and HEALS the exact path a dispatch takes.
func TestDeepProbeReportsClaudeAuth(t *testing.T) {
	h := newHarness(t)
	tgt := h.post("/api/targets", obj{"name": "deep", "kind": "mock"}, 201)
	deep := h.post(fmt.Sprintf("/api/targets/%d/check?deep=true", tgt.id()), nil, 200)
	if !strings.Contains(deep.str("info_json"), `"claude_auth":"ok"`) {
		t.Fatalf("deep probe info: %v", deep.str("info_json"))
	}
	// the shallow probe must not spend tokens
	shallow := h.post(fmt.Sprintf("/api/targets/%d/check", tgt.id()), nil, 200)
	if strings.Contains(shallow.str("info_json"), "claude_auth") {
		t.Errorf("a shallow probe should not authenticate: %v", shallow.str("info_json"))
	}
}
