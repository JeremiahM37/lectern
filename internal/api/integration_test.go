package api_test

// These run against a REAL local executor, with no mock in the way: launch
// failures and ghost runs are exactly the paths a scripted target cannot prove.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/internal/config"
	"github.com/JeremiahM37/lectern/internal/store"
)

func realLocal(c *config.Config) { c.Mock = false }

// A launch failure (the repo path does not exist) must surface as a failed task,
// not a hang.
func TestDispatchToMissingRepoFailsTask(t *testing.T) {
	h := newHarness(t, realLocal)
	tgt := h.post("/api/targets", obj{"name": "real-local", "kind": "local"}, 201)
	p := h.post("/api/projects",
		obj{"name": "ghostp", "target_id": tgt.id(), "repo_path": "/nonexistent/repo"}, 201)
	task := h.task(p.id(), "doomed", "", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	failed := h.waitStatus(task.id(), "failed")
	errText := failed.sub("attempt").sub("result").str("error")
	if !strings.Contains(strings.ToLower(errText), "worktree") {
		t.Fatalf("the failure must say what went wrong: %q", errText)
	}
}

// An attempt marked running whose tmux session and worktree have vanished must
// be reconciled to failed, rather than sticking on the board forever.
func TestGhostRunningAttemptReconciled(t *testing.T) {
	h := newHarness(t, realLocal)
	tgt := h.post("/api/targets", obj{"name": "real-local2", "kind": "local"}, 201)
	p := h.post("/api/projects",
		obj{"name": "ghostq", "target_id": tgt.id(), "repo_path": "/tmp"}, 201)
	task := h.task(p.id(), "ghost", "", nil)

	// Set the task first: publishing a running attempt lets the scheduler
	// finalize it immediately. A later task write could overwrite that result.
	if err := h.App.DB.Update("tasks", task.id(),
		map[string]any{"status": "running"}); err != nil {
		t.Fatal(err)
	}

	// forge a running attempt pointing at nothing (a crash, or a manual rm)
	att, err := h.App.DB.InsertAttempt(&store.Attempt{
		TaskID: task.id(), N: 1, Status: "running", Token: "tok-ghost",
		WorktreePath: "/nonexistent/wt", Branch: "lec/ghost",
		TmuxSession: "lec-ghost-none"})
	if err != nil {
		t.Fatal(err)
	}

	h.waitStatus(task.id(), "failed")
	fresh, err := h.App.DB.Attempt(att.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != "failed" {
		t.Fatalf("attempt status: %q", fresh.Status)
	}
	if !strings.Contains(store.UnjObj(fresh.ResultJSON)["error"].(string), "disappeared") {
		t.Errorf("the reason must be recorded: %v", fresh.ResultJSON)
	}
}
