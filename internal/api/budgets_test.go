package api_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func TestBudgetsGetPutRoundtrip(t *testing.T) {
	h := newHarness(t)
	got := h.get("/api/budgets")
	cfg := got.sub("config")
	if cfg.sub("overall").num("daily_usd") != 0 {
		t.Fatalf("a fresh install must start with nothing capped: %v", cfg)
	}

	var saved obj
	h.decode("PUT", "/api/budgets", obj{
		"overall":    obj{"daily_usd": 10, "weekly_usd": 50, "mode": "stop"},
		"per_agent":  obj{"codex": obj{"daily_usd": 2, "mode": "warn"}},
		"thresholds": []int{50, 90, 100},
	}, 200, &saved)
	savedOverall := saved.sub("overall")
	if savedOverall.sub("daily") == nil {
		t.Fatalf("saved status should reflect the new cap: %v", saved)
	}

	reread := h.get("/api/budgets")
	rc := reread.sub("config")
	if rc.sub("overall").num("daily_usd") != 10 || rc.sub("overall").str("mode") != "stop" {
		t.Errorf("overall did not persist: %v", rc)
	}

	// Invalid config must be rejected and must not overwrite the good one.
	if code := h.status("PUT", "/api/budgets", obj{"overall": obj{"daily_usd": -5}}); code != 422 {
		t.Errorf("negative limit should be rejected: %d", code)
	}
	if h.get("/api/budgets").sub("config").sub("overall").num("daily_usd") != 10 {
		t.Error("a rejected PUT must not have overwritten the config")
	}
}

// seedTaskSpend records one finished task attempt's cost directly — the
// same DB shape a real dispatch produces (docs/budgets.md), but without
// depending on the mock agent's per-agent cost quirks: codex genuinely
// reports no dollar figure (internal/agents/parse.go), so a real mock
// codex run can never exercise a codex-scoped budget. budget.Gate reads
// this exact data through internal/store.SpendSince.
func (h *harness) seedTaskSpend(agent string, costUSD float64) {
	h.t.Helper()
	task, err := h.App.DB.InsertTask(&store.Task{ProjectID: h.seededProjectID(), Title: "seed spend", Agent: agent})
	if err != nil {
		h.t.Fatal(err)
	}
	att, err := h.App.DB.InsertAttempt(&store.Attempt{TaskID: task.ID, N: 1})
	if err != nil {
		h.t.Fatal(err)
	}
	now := store.Now()
	if err := h.App.DB.Update("attempts", att.ID, map[string]any{
		"status": "done", "started_at": now - 60, "finished_at": now,
		"result_json": store.J(map[string]any{"cost_usd": costUSD}),
	}); err != nil {
		h.t.Fatal(err)
	}
}

func TestBudgetsStopModeBlocksDispatchAndWarnDoesNot(t *testing.T) {
	h := newHarness(t)
	h.seedTaskSpend("claude", 0.02)

	// warn mode: dispatch still succeeds however much was spent
	h.decode("PUT", "/api/budgets", obj{"overall": obj{"daily_usd": 0.01, "mode": "warn"}}, 200, nil)
	task := h.task(h.seededProjectID(), "should dispatch", "x", nil)
	if code := h.status("POST", fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}); code != 200 {
		t.Errorf("warn mode must not block dispatch: %d", code)
	}

	// stop mode, same exhausted cap: refused
	h.decode("PUT", "/api/budgets", obj{"overall": obj{"daily_usd": 0.01, "mode": "stop"}}, 200, nil)
	blocked := h.task(h.seededProjectID(), "should block", "x", nil)
	code := h.status("POST", fmt.Sprintf("/api/tasks/%d/dispatch", blocked.id()), obj{})
	if code != 409 {
		t.Errorf("stop mode at 100%% must refuse dispatch, got %d", code)
	}
	if h.taskStatus(blocked.id()) != "backlog" {
		t.Errorf("a refused dispatch must leave the task exactly where it was: %s", h.taskStatus(blocked.id()))
	}
}

func TestBudgetsStopModeBlocksSessionLaunch(t *testing.T) {
	h := newHarness(t)
	h.seedTaskSpend("claude", 0.02)
	h.decode("PUT", "/api/budgets", obj{"overall": obj{"daily_usd": 0.01, "mode": "stop"}}, 200, nil)
	code := h.status("POST", "/api/sessions", obj{"name": "s1", "agent": "claude", "scratch": true})
	if code != 409 {
		t.Errorf("stop mode at 100%% must refuse a new session launch, got %d", code)
	}
}

func TestBudgetsPerAgentLimitOnlyBlocksThatAgent(t *testing.T) {
	h := newHarness(t)
	h.seedTaskSpend("codex", 0.02)
	h.decode("PUT", "/api/budgets", obj{
		"per_agent": obj{"codex": obj{"daily_usd": 0.01, "mode": "stop"}},
	}, 200, nil)

	blocked := h.task(h.seededProjectID(), "codex task", "x", obj{"agent": "codex"})
	if code := h.status("POST", fmt.Sprintf("/api/tasks/%d/dispatch", blocked.id()), obj{}); code != 409 {
		t.Errorf("codex's own exhausted budget should block a codex dispatch, got %d", code)
	}
	ok := h.task(h.seededProjectID(), "claude task", "x", obj{"agent": "claude"})
	if code := h.status("POST", fmt.Sprintf("/api/tasks/%d/dispatch", ok.id()), obj{}); code != 200 {
		t.Errorf("claude has no cap of its own and should not be blocked, got %d", code)
	}
}

func TestPerTaskBudgetCancelsOverBudgetAttempt(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	// The mock agent's fixed cost is $0.0123 — a cap well below that must
	// cancel the attempt once its 'result' event lands, instead of letting
	// it reach review.
	task := h.task(pid, "capped", "x", obj{"budget_usd": 0.001})
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitStatus(task.id(), "cancelled")
}

// TestInteractiveSessionGetsBudgetNoteNeverKilled covers docs/budgets.md's
// "Interactive sessions": a session is never touched directly, but once a
// stop-mode budget is exhausted its next UserPromptSubmit hook response
// carries an additionalContext note telling the agent to stop and
// summarise — the same channel cross-agent awareness's briefing already
// uses (internal/api/hooks_agentevents.go).
func TestInteractiveSessionGetsBudgetNoteNeverKilled(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	sess := h.session(obj{"project_id": pid, "name": "long runner", "agent": "claude"})
	tok := hookToken(t, h, sess.id())
	path := fmt.Sprintf("/api/hook/session/%d/UserPromptSubmit", sess.id())

	// Under budget: no note at all.
	code, body := h.rawRequest("POST", path, hookFixtureUserPromptSubmit, tok)
	if code != 200 {
		t.Fatalf("got %d: %s", code, body)
	}
	if strings.Contains(string(body), "Budget notice") {
		t.Fatalf("must not warn before any budget is exhausted: %s", body)
	}

	h.seedTaskSpend("claude", 0.02)
	h.decode("PUT", "/api/budgets", obj{"overall": obj{"daily_usd": 0.01, "mode": "stop"}}, 200, nil)

	code, body = h.rawRequest("POST", path, hookFixtureUserPromptSubmit, tok)
	if code != 200 {
		t.Fatalf("got %d: %s", code, body)
	}
	if !strings.Contains(string(body), "Budget notice") || !strings.Contains(string(body), "additionalContext") {
		t.Errorf("expected a budget additionalContext note, got: %s", body)
	}

	// The session itself must never be killed for this — still listed live.
	live := h.getList("/api/sessions")
	found := false
	for _, s := range live {
		if s.id() == sess.id() {
			found = true
		}
	}
	if !found {
		t.Error("an exhausted budget must never end an interactive session")
	}
}

func TestTaskBudgetSettableAtCreateDispatchAndPatch(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	task := h.task(pid, "budgeted", "x", obj{"budget_usd": 5})
	if task.num("budget_usd") != 5 {
		t.Fatalf("budget_usd not set at create: %v", task)
	}
	patched := h.patch(fmt.Sprintf("/api/tasks/%d", task.id()), obj{"budget_usd": 7}, 200)
	if patched.num("budget_usd") != 7 {
		t.Errorf("budget_usd not updated by patch: %v", patched)
	}
	if code := h.status("PATCH", fmt.Sprintf("/api/tasks/%d", task.id()), obj{"budget_usd": -1}); code != 422 {
		t.Errorf("a negative budget_usd must be rejected, got %d", code)
	}
}
