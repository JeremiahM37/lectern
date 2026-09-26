package api_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/limits"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Usage-limit continuity end to end (docs/rate-limits.md): the mock target
// emits Claude's real rejected rate_limit_event and limit message for tasks,
// and scripts a limited pane for sessions.

func (h *harness) attempts(taskID int64) []*store.Attempt {
	h.t.Helper()
	rows, err := h.App.DB.TaskAttempts(taskID)
	if err != nil {
		h.t.Fatal(err)
	}
	return rows
}

func TestLimitedTaskIsRequeuedForTheResetOnce(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	h.request2("PUT", "/api/limits/policy", obj{"project_id": pid, "policy": obj{"mode": "wait"}}, 200)
	task := h.task(pid, "limited", "Refactor the parser [mock:limit]", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitUntil("the continuation attempt", func() bool { return len(h.attempts(task.id())) == 2 })
	atts := h.attempts(task.id())
	first, next := atts[0], atts[1]
	if first.N > next.N {
		first, next = next, first
	}
	if first.Status != "failed" || next.Status != "queued" || next.NotBefore == nil {
		t.Fatalf("attempts: first=%+v next=%+v", first, next)
	}
	if hold := *next.NotBefore - store.Now(); hold < 3000 || hold > 4000 {
		t.Fatalf("continuation held for %.0fs, want about the hour to the reset", hold)
	}
	if next.ResumeSession == "" || next.WorktreePath != first.WorktreePath || next.Agent != first.Agent {
		t.Fatalf("continuation does not resume the same agent in the same worktree: %+v", next)
	}
	view := h.get(fmt.Sprintf("/api/tasks/%d", task.id()))
	limit := view.sub("limit")
	if view.str("status") != "queued" || limit.str("state") != limits.StateRequeued || limit.num("reset_at") == 0 {
		t.Fatalf("task view: status=%s limit=%v", view.str("status"), limit)
	}
	// Still held: a few ticks must not promote it or add another attempt.
	h.waitUntil("a few scheduler ticks", func() bool { return true })
	if n := len(h.attempts(task.id())); n != 2 {
		t.Fatalf("%d attempts, want 2", n)
	}
	// The reset arrives: the held attempt runs and the task completes.
	h.App.DB.Exec(`UPDATE attempts SET not_before=? WHERE id=?`, store.Now()-1, next.ID)
	h.waitStatus(task.id(), "review")
	if n := len(h.attempts(task.id())); n != 2 {
		t.Fatalf("%d attempts after the resume, want 2", n)
	}
}

func TestLimitedTaskIsHandedToTheFallbackAgent(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	h.request2("PUT", "/api/limits/policy", obj{"project_id": pid,
		"policy": obj{"mode": "handoff", "fallback_agent": "codex"}}, 200)
	task := h.task(pid, "limited", "Add the health endpoint [mock:limit]", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitStatus(task.id(), "review")
	atts := h.attempts(task.id())
	if len(atts) != 2 {
		t.Fatalf("%d attempts, want 2", len(atts))
	}
	next := atts[0]
	if atts[1].N > next.N {
		next = atts[1]
	}
	if next.Agent != "codex" || next.NotBefore != nil || !strings.Contains(next.Prompt, "stopped by its usage limit") ||
		!strings.Contains(next.Prompt, "Add the health endpoint") {
		t.Fatalf("fallback attempt: %+v", next)
	}
}

func TestLimitedTaskNotifiesAndOffersOneTapChoices(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	task := h.task(pid, "limited", "Write docs [mock:limit]", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	view := h.waitStatus(task.id(), "failed")
	limit := view.sub("limit")
	if limit.str("state") != limits.StateWaiting || limit.str("policy") != limits.ModeNotify ||
		!strings.Contains(limit.str("message"), "Usage limit reached") {
		t.Fatalf("task limit: %v", limit)
	}
	path := fmt.Sprintf("/api/limits/%d/choose", limit.id())
	h.post(path, obj{"action": "handoff"}, 422) // no fallback configured anywhere
	chosen := h.post(path, obj{"action": "wait"}, 200)
	if chosen.str("state") != limits.StateRequeued {
		t.Fatalf("choice: %v", chosen)
	}
	h.post(path, obj{"action": "wait"}, 409) // a second tap does nothing
	if n := len(h.attempts(task.id())); n != 2 {
		t.Fatalf("%d attempts, want 2", n)
	}
	if h.taskStatus(task.id()) != "queued" {
		t.Fatal("task not requeued")
	}
}

func TestLimitedSessionShowsOnItsCardAndHandsOffWithoutAWrap(t *testing.T) {
	h := newHarness(t)
	old := h.session(obj{"project_id": h.seededProjectID(), "name": "limited", "agent": "claude"})
	h.waitSessionStatus(old.id(), "waiting", "idle", "running")
	row, _ := h.App.DB.Session(old.id())
	h.mock().SetPaneLimit(row.TmuxSession, "You've hit your session limit · resets 11:59pm (UTC)")
	var limit obj
	h.waitUntil("the card to show the limit", func() bool {
		limit = h.sessionByID(old.id()).sub("limit")
		return limit != nil && limit.str("state") == limits.StateWaiting
	})
	if limit.num("reset_at") == 0 || limit.str("policy") != limits.ModeNotify {
		t.Fatalf("card limit: %v", limit)
	}
	h.post(fmt.Sprintf("/api/limits/%d/choose", limit.id()), obj{"action": "handoff", "agent": "codex"}, 200)
	var nextID int64
	h.waitUntil("the successor", func() bool {
		wraps, _ := h.App.DB.SessionWraps(old.id())
		if len(wraps) > 0 && wraps[0].NextSessionID != nil {
			nextID = *wraps[0].NextSessionID
			return true
		}
		return false
	})
	next, _ := h.App.DB.Session(nextID)
	wraps, _ := h.App.DB.SessionWraps(old.id())
	if next.Agent != "codex" || next.Workdir != row.Workdir || !strings.Contains(wraps[0].Summary, "stopped by its usage limit") {
		t.Fatalf("successor %+v wrap %q", next, wraps[0].Summary)
	}
	if strings.Contains(h.mock().PaneText(row.TmuxSession), "Before this session ends") {
		t.Fatal("the limited agent was asked to write a handoff it cannot write")
	}
	h.waitUntil("the hold to resolve", func() bool {
		held, err := h.App.DB.LimitHold(int64(limit.id()))
		return err == nil && held.State == limits.StateHandedOff && held.SuccessorID != nil && *held.SuccessorID == nextID
	})
	if h.sessionByID(old.id()).str("status") == "dead" {
		t.Fatal("the original session was killed")
	}
}

func TestLimitedSessionResumesOnceWhenItsLimitClears(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.LimitSettle = 300 * time.Millisecond })
	old := h.session(obj{"project_id": h.seededProjectID(), "name": "resume", "agent": "claude"})
	h.waitSessionStatus(old.id(), "waiting", "idle", "running")
	h.request2("PUT", "/api/limits/policy", obj{"session_id": old.id(), "policy": obj{"mode": "wait"}}, 200)
	row, _ := h.App.DB.Session(old.id())
	m := h.mock()
	m.SetPaneLimit(row.TmuxSession, "You've hit your session limit · resets 11:59pm (UTC)")
	var limit obj
	h.waitUntil("the hold", func() bool {
		limit = h.sessionByID(old.id()).sub("limit")
		return limit != nil
	})
	if limit.str("policy") != limits.ModeWait || limit.num("due_at") == 0 {
		t.Fatalf("wait policy not applied: %v", limit)
	}
	// The window resets early; the operator taps "Resume now".
	m.SetPaneLimit(row.TmuxSession, "")
	h.post(fmt.Sprintf("/api/limits/%d/choose", limit.id()), obj{"action": "resume_now"}, 200)
	h.waitUntil("the resume to be verified", func() bool {
		held, err := h.App.DB.LimitHold(int64(limit.id()))
		return err == nil && held.State == limits.StateResumed
	})
	h.waitUntil("a few more ticks", func() bool { return true })
	if n := strings.Count(m.PaneText(row.TmuxSession), limits.NudgeMarker); n != 1 {
		t.Fatalf("%d nudges typed, want 1", n)
	}
	if h.sessionByID(old.id()).sub("limit") != nil {
		t.Fatal("card still shows a limit after the resume")
	}
}

func TestLimitPolicyScopes(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	h.request2("PUT", "/api/limits/policy", obj{"policy": obj{"mode": "handoff"}}, 422)
	h.request2("PUT", "/api/limits/policy", obj{"policy": obj{"mode": "handoff", "fallback_agent": "nope"}}, 422)
	h.request2("PUT", "/api/limits/policy", obj{"policy": obj{"mode": "wait"}}, 200)
	got := h.get(fmt.Sprintf("/api/limits/policy?project_id=%d", pid))
	if got.sub("effective").str("mode") != "wait" || got.str("effective_scope") != "global" || got["own"] != nil {
		t.Fatalf("inherited: %v", got)
	}
	h.request2("PUT", "/api/limits/policy", obj{"project_id": pid,
		"policy": obj{"mode": "handoff", "fallback_agent": "codex", "fallback_model": "gpt-5"}}, 200)
	got = h.get(fmt.Sprintf("/api/limits/policy?project_id=%d", pid))
	if got.sub("effective").str("fallback_agent") != "codex" || got.str("effective_scope") != "project" {
		t.Fatalf("project: %v", got)
	}
	h.request2("PUT", "/api/limits/policy", obj{"project_id": pid, "policy": nil}, 200)
	if got = h.get(fmt.Sprintf("/api/limits/policy?project_id=%d", pid)); got.str("effective_scope") != "global" {
		t.Fatalf("cleared: %v", got)
	}
}
