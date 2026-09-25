package api_test

// Cost per outcome (docs/outcomes.md): GET /api/outcomes and GET/PUT
// /api/model-prices. This is deliberately a thin wire-level test — the real
// derivation/aggregation math is covered by internal/outcomes' own package
// tests; this just confirms the API wires them up and groups correctly.

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// seedFinishedAttempt creates a done task with one finished, checked attempt
// — cheap enough to build a two-row Outcomes comparison without a real mock
// agent run.
func (h *harness) seedFinishedAttempt(agent, model string, costUSD float64, checkPassed bool, accepted bool) {
	h.t.Helper()
	task, err := h.App.DB.InsertTask(&store.Task{ProjectID: h.seededProjectID(), Title: "outcome seed", Agent: agent})
	if err != nil {
		h.t.Fatal(err)
	}
	status := "review"
	if accepted {
		status = "done"
	}
	if err := h.App.DB.Update("tasks", task.ID, map[string]any{"status": status}); err != nil {
		h.t.Fatal(err)
	}
	att, err := h.App.DB.InsertAttempt(&store.Attempt{TaskID: task.ID, N: 1, Model: model})
	if err != nil {
		h.t.Fatal(err)
	}
	rc := 0
	if !checkPassed {
		rc = 1
	}
	now := store.Now()
	if err := h.App.DB.Update("attempts", att.ID, map[string]any{
		"status": "done", "started_at": now - 30, "finished_at": now,
		"result_json": store.J(map[string]any{"cost_usd": costUSD}),
		"verify_json": store.J(map[string]any{"cmd": "verify", "rc": rc, "output": ""}),
	}); err != nil {
		h.t.Fatal(err)
	}
}

func TestGetOutcomesGroupsByAgentAndRanksBySpend(t *testing.T) {
	h := newHarness(t)
	h.seedFinishedAttempt("claude", "opus", 1.0, true, true)
	h.seedFinishedAttempt("codex", "gpt-6", 9.0, true, true)
	h.seedFinishedAttempt("codex", "gpt-6", 0.0, false, false)

	got := h.get("/api/outcomes?days=30&group=agent")
	rows := got.list("rows")
	if len(rows) != 2 {
		t.Fatalf("want 2 agent groups, got %d: %+v", len(rows), rows)
	}
	// Highest spend first.
	if rows[0].str("key") != "codex" {
		t.Fatalf("codex (9.0 spend) should rank before claude (1.0): %+v", rows)
	}
	if rows[0].num("attempts") != 2 {
		t.Errorf("codex should have 2 attempts, got %v", rows[0].num("attempts"))
	}
	if rows[0].num("passed") != 1 {
		t.Errorf("codex should have 1 passed check (the other failed), got %v", rows[0].num("passed"))
	}
	if rows[0].num("accepted") != 1 {
		t.Errorf("codex should have 1 accepted attempt, got %v", rows[0].num("accepted"))
	}
	costPerPass := rows[0].num("cost_per_pass")
	if costPerPass < 8.999 || costPerPass > 9.001 {
		t.Errorf("codex cost_per_pass = %v, want 9.0 (9.0 total / 1 pass)", costPerPass)
	}

	byModel := h.get("/api/outcomes?days=30&group=model")
	if len(byModel.list("rows")) != 2 {
		t.Fatalf("want 2 model groups (opus, gpt-6), got %+v", byModel.list("rows"))
	}

	if code := h.status("GET", "/api/outcomes?group=bogus", nil); code != 400 {
		t.Errorf("an unknown group must be rejected, got %d", code)
	}
}

func TestModelPricesGetPutRoundtripAndValidation(t *testing.T) {
	h := newHarness(t)
	fresh := h.get("/api/model-prices")
	if prices, ok := fresh["prices"].(map[string]any); !ok || len(prices) != 0 {
		t.Fatalf("a fresh install should have no configured prices: %v", fresh)
	}

	var saved obj
	h.decode("PUT", "/api/model-prices", obj{
		"prices": obj{"gpt-6": obj{"input_per_1m": 2, "output_per_1m": 8}},
	}, 200, &saved)
	prices, _ := saved["prices"].(map[string]any)
	if len(prices) != 1 {
		t.Fatalf("save should round-trip the entry: %v", saved)
	}

	reread := h.get("/api/model-prices")
	rp, _ := reread["prices"].(map[string]any)
	if _, ok := rp["gpt-6"]; !ok {
		t.Errorf("price table did not persist: %v", reread)
	}

	if code := h.status("PUT", "/api/model-prices", obj{
		"prices": obj{"bad": obj{"input_per_1m": -1}},
	}); code != 400 {
		t.Errorf("a negative rate should be rejected, got %d", code)
	}
}
