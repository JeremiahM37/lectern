package api_test

// Regression tripwires. Every test here is a bug that reached a real dispatch.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/agents"
	"github.com/JeremiahM37/lectern/v2/internal/config"
)

func (h *harness) gatedAndWaiting(pid int64, title string) obj {
	h.t.Helper()
	task := h.task(pid, title, "x [mock:approval]", obj{"permission_mode": "default"})
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitApproval(task.id())
	return task
}

// Cancelling a task with a live approval must not leave a ghost 'pending' row —
// the board badge would stick forever and the blocked hook never hears back.
func TestCancelClearsPendingApproval(t *testing.T) {
	h := newHarness(t)
	task := h.gatedAndWaiting(h.seededProjectID(), "cancel ghost")
	if len(h.pendingApprovals()) != 1 {
		t.Fatalf("expected one pending approval: %v", h.pendingApprovals())
	}
	h.post(fmt.Sprintf("/api/tasks/%d/cancel", task.id()), nil, 200)
	h.waitUntil("the approval to resolve", func() bool { return len(h.pendingApprovals()) == 0 })
	if h.get("/api/health").num("pending_approvals") != 0 {
		t.Error("the health badge still claims a pending approval")
	}
	// recorded as expired, not silently dropped — the audit trail matters
	found := false
	for _, a := range h.getList("/api/approvals?status=expired") {
		if int64(a.num("task_id")) == task.id() {
			found = true
		}
	}
	if !found {
		t.Error("the resolution was not recorded as expired")
	}
}

// If an agent files an approval then dies, the approval must not hang pending
// after the attempt finalises.
func TestFinalizeClearsPendingApproval(t *testing.T) {
	h := newHarness(t)
	task := h.gatedAndWaiting(h.seededProjectID(), "finalize ghost")
	att, err := h.App.DB.LatestAttempt(task.id())
	if err != nil {
		t.Fatal(err)
	}
	if n := h.App.Broker.ExpireForAttempt(att.ID); n != 1 {
		t.Fatalf("expected to expire one approval, got %d", n)
	}
	if n := h.App.Broker.ExpireForAttempt(att.ID); n != 0 {
		t.Fatalf("expiring must be idempotent, got %d", n)
	}
	if len(h.pendingApprovals()) != 0 {
		t.Fatalf("still pending: %v", h.pendingApprovals())
	}
}

// A mock/test run must NOT write into another database's diff store: attempt ids
// collide across databases, and a shared store once let a test overwrite
// production patches with the mock diff.
func TestDiffsIsolatedPerDatabase(t *testing.T) {
	h := newHarness(t)
	dir := h.App.Cfg.DiffDir()
	if !strings.HasPrefix(dir, filepath.Dir(h.App.Cfg.DBPath)) {
		t.Fatalf("the diff store must live beside its database: %s", dir)
	}
	task := h.run(h.seededProjectID(), "iso", "x", nil)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	patches := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".patch") {
			patches++
		}
	}
	if patches == 0 {
		t.Fatalf("no patch was written for task %d", task.id())
	}
}

// Deleting a task must leave no orphaned attempts, events, approvals, notes or
// diff files behind.
func TestDeleteTaskRemovesAllTraces(t *testing.T) {
	h := newHarness(t)
	task := h.run(h.seededProjectID(), "delete me", "x [mock:note]", nil)
	att, err := h.App.DB.LatestAttempt(task.id())
	if err != nil {
		t.Fatal(err)
	}
	diffFile := filepath.Join(h.App.Cfg.DiffDir(), fmt.Sprintf("attempt-%d.patch", att.ID))
	if _, err := os.Stat(diffFile); err != nil {
		t.Fatalf("no diff was captured: %v", err)
	}
	if n, _ := h.App.DB.Count("events", "attempt_id=?", att.ID); n == 0 {
		t.Fatal("no events to delete")
	}
	if n, _ := h.App.DB.Count("memories", "created_by_attempt=?", att.ID); n == 0 {
		t.Fatal("the agent's note was never recorded")
	}

	var deleted obj
	h.decode("DELETE", fmt.Sprintf("/api/tasks/%d", task.id()), nil, 200, &deleted)
	if int64(deleted.num("deleted")) != task.id() {
		t.Fatalf("delete response: %v", deleted)
	}

	if code := h.status("GET", fmt.Sprintf("/api/tasks/%d", task.id()), nil); code != 404 {
		t.Errorf("the task survived deletion: %d", code)
	}
	if _, err := h.App.DB.Attempt(att.ID); err == nil {
		t.Error("the attempt survived deletion")
	}
	for _, table := range []struct{ name, where string }{
		{"events", "attempt_id=?"}, {"approvals", "attempt_id=?"},
		{"memories", "created_by_attempt=?"},
	} {
		if n, _ := h.App.DB.Count(table.name, table.where, att.ID); n != 0 {
			t.Errorf("%s rows survived: %d", table.name, n)
		}
	}
	if _, err := os.Stat(diffFile); !os.IsNotExist(err) {
		t.Error("the diff file survived deletion")
	}
	if code := h.status("DELETE", "/api/tasks/99999", nil); code != 404 {
		t.Errorf("deleting a nonexistent task: %d", code)
	}
}

func TestDeleteRunningTaskCancelsFirst(t *testing.T) {
	h := newHarness(t)
	task := h.task(h.seededProjectID(), "delete running", "x [mock:slow]", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitStatus(task.id(), "running")
	h.decode("DELETE", fmt.Sprintf("/api/tasks/%d", task.id()), nil, 200, nil)
	if code := h.status("GET", fmt.Sprintf("/api/tasks/%d", task.id()), nil); code != 404 {
		t.Fatalf("the running task survived deletion: %d", code)
	}
}

// Agent-filed follow-ups may be real work — deleting the parent ORPHANS them, it
// does not delete them.
func TestDeleteOrphansChildrenNotCascade(t *testing.T) {
	h := newHarness(t)
	task := h.run(h.seededProjectID(), "parent", "x [mock:subtask]", nil)
	var child obj
	for _, x := range h.getList("/api/tasks") {
		if int64(x.num("parent_task_id")) == task.id() {
			child = x
		}
	}
	if child == nil {
		t.Fatal("the agent filed no follow-up card")
	}
	h.decode("DELETE", fmt.Sprintf("/api/tasks/%d", task.id()), nil, 200, nil)
	still := h.get(fmt.Sprintf("/api/tasks/%d", child.id()))
	if still.id() != child.id() {
		t.Fatalf("the child was deleted with its parent: %v", still)
	}
	if still["parent_task_id"] != nil {
		t.Errorf("the child should be orphaned, not still pointing at a dead parent: %v",
			still["parent_task_id"])
	}
}

// A task deleted mid-poll must not crash the scheduler with a FOREIGN KEY error,
// which aborted the whole tick and stalled every other running task.
func TestStoreEventsSurvivesConcurrentDelete(t *testing.T) {
	h := newHarness(t)
	task := h.task(h.seededProjectID(), "racer", "x [mock:slow]", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitStatus(task.id(), "running")
	att, err := h.App.DB.LatestAttempt(task.id())
	if err != nil {
		t.Fatal(err)
	}
	// simulate the delete winning the race: the rows are gone, but the scheduler
	// still holds its copy of the attempt
	h.App.DB.Exec(`DELETE FROM events WHERE attempt_id=?`, att.ID)
	h.App.DB.Exec(`DELETE FROM attempts WHERE id=?`, att.ID)
	h.App.DB.Exec(`DELETE FROM tasks WHERE id=?`, task.id())

	if err := h.App.Sched.StoreEvents(att, []agents.Event{
		{Type: "text", Payload: map[string]any{"text": "late event"}}}); err != nil {
		t.Fatalf("a vanished attempt must be a no-op, not an error: %v", err)
	}
	if n, _ := h.App.DB.Count("events", "attempt_id=?", att.ID); n != 0 {
		t.Fatalf("an orphan event was written for a deleted attempt: %d", n)
	}
}

// Security: an agent knows the base URL and its own per-attempt hook token. In
// token mode it must NOT be able to reach the human decision endpoint and
// self-approve — while the hook endpoints stay reachable.
func TestTokenModeBlocksAgentSelfApproval(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.AuthToken = "humanonly"; c.Auth = "token" })

	if code := h.status("GET", "/api/approvals?status=pending", nil); code != 401 {
		t.Errorf("listing approvals without the bearer: %d", code)
	}
	if code := h.status("POST", "/api/approvals/1/decision", obj{"decision": "approved"}); code != 401 {
		t.Errorf("deciding without the bearer: %d", code)
	}
	// the hook path is exempt — an agent reaches it, and is refused on its token
	code := h.status("POST", "/api/hook/approval",
		obj{"token": "bad", "tool_name": "Bash", "tool_input": obj{}})
	if code != 403 && code != 422 {
		t.Errorf("the hook endpoint must stay reachable (403/422), got %d", code)
	}
	// the human, with the bearer, gets through
	authed, _ := h.request("GET", "/api/approvals?status=pending", nil,
		map[string]string{"Authorization": "Bearer humanonly"})
	if authed != 200 {
		t.Errorf("the operator was locked out of their own board: %d", authed)
	}
	// ...and so does a token in the query string, which is the only way
	// EventSource can authenticate
	if code := h.status("GET", "/api/approvals?status=pending&token=humanonly", nil); code != 200 {
		t.Errorf("SSE-style query auth: %d", code)
	}
}

// A running or queued card has no diff yet: diff_stat_json is '{}' server-side.
// Serving that as an object once made the client's reduce() throw and abort the
// whole column render.
func TestRunningCardHasListShapedDiffStat(t *testing.T) {
	h := newHarness(t)
	task := h.task(h.seededProjectID(), "Long runner", "work [mock:slow]", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitStatus(task.id(), "running")
	got := h.get(fmt.Sprintf("/api/tasks/%d", task.id()))
	if _, ok := got.sub("attempt")["diff_stat"].([]any); !ok {
		t.Fatalf("diff_stat must always be a list: %#v", got.sub("attempt")["diff_stat"])
	}
}

// Statics must revalidate: an installed PWA otherwise keeps running the previous
// app.js and style.css after a deploy, which looks exactly like the change not
// working.
func TestStaticAssetsRevalidate(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/", "/style.css", "/sw.js",
		"/manifest.webmanifest", "/icon.svg"} {
		resp, err := httpGet(h.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if resp.StatusCode != 200 {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s Cache-Control = %q, want no-cache", path, cc)
		}
		resp.Body.Close()
	}
}

// A result_json of literal "null" unmarshals into a NIL map, and the scheduler
// writes into that map when it finalises ("error") and when a reviewer gate
// lands ("review"). Writing to a nil map panics — inside the tick, which would
// stall every other running attempt on the board.
func TestFinalizeSurvivesANullResultBlob(t *testing.T) {
	h := newHarness(t)
	task := h.task(h.seededProjectID(), "null result", "x [mock:slow]", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitStatus(task.id(), "running")
	att, err := h.App.DB.LatestAttempt(task.id())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.App.DB.Exec(`UPDATE attempts SET result_json='null' WHERE id=?`,
		att.ID); err != nil {
		t.Fatal(err)
	}
	// the tick must carry this attempt to a terminal state, not die on it
	h.waitStatus(task.id(), "review")

	// and every other attempt still runs, which is what a panicking tick breaks
	other := h.run(h.seededProjectID(), "still works", "x", nil)
	if other.str("status") != "review" {
		t.Fatalf("the board stalled: %v", other.str("status"))
	}
}

// The poll reads the event log and the exit code as two separate reads. Whatever
// the agent wrote between them — usually its closing `result`, the summary of
// what it actually did — was lost, because finalising takes the attempt out of
// the running set and nothing ever reads the tail. It showed up as a task in
// review whose timeline simply stopped mid-run.
func TestTheAgentsLastWordsSurviveFinalising(t *testing.T) {
	for i := 0; i < 12; i++ {
		func() {
			h := newHarness(t)
			task := h.run(h.seededProjectID(), "final words", "x", nil)
			h.waitStatus(task.id(), "review")

			seen := map[string]bool{}
			for _, e := range h.getList(fmt.Sprintf("/api/tasks/%d/events", task.id())) {
				seen[e.str("type")] = true
			}
			if !seen["result"] {
				t.Fatalf("run %d: the closing result event is missing: %v", i, seen)
			}
		}()
	}
}
