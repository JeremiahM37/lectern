package api_test

import (
	"fmt"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/agentevents"
	"github.com/JeremiahM37/lectern/v2/internal/agents"
	"github.com/JeremiahM37/lectern/v2/internal/budget"
	"github.com/JeremiahM37/lectern/v2/internal/outcomes"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// A Codex attempt reports tokens and no dollars. With the model priced, the
// stored result carries an estimate labelled as such, live_cost_usd moves (so
// a per-task budget can act), and a codex-only stop budget sees the spend.
func TestCodexResultEstimatedFromPriceTable(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	db := h.App.DB
	task, err := db.InsertTask(&store.Task{ProjectID: pid, Title: "codex work", Agent: "codex", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	att, err := db.InsertAttempt(&store.Attempt{TaskID: task.ID, N: 1, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	result := func() []agents.Event {
		events, _ := agents.ParseStreamLines("codex",
			`{"type":"turn.completed","usage":{"input_tokens":1000000,"cached_input_tokens":0,"output_tokens":100000}}`+"\n")
		return events
	}

	// Unpriced: nothing is invented.
	if err := h.App.Sched.StoreEvents(att, result()); err != nil {
		t.Fatal(err)
	}
	fresh, _ := db.Attempt(att.ID)
	if fresh.LiveCostUSD != 0 || store.UnjObj(fresh.ResultJSON)["cost_usd"] != nil {
		t.Fatalf("no price configured must mean no cost, got live=%v result=%s", fresh.LiveCostUSD, fresh.ResultJSON)
	}

	// Priced under the agent name, since the attempt names no model.
	if _, err := outcomes.SavePrices(db, outcomes.PriceConfig{Prices: map[string]outcomes.ModelPrice{
		"codex": {InputPer1M: 1.25, OutputPer1M: 10}}}); err != nil {
		t.Fatal(err)
	}
	if err := h.App.Sched.StoreEvents(att, result()); err != nil {
		t.Fatal(err)
	}
	fresh, _ = db.Attempt(att.ID)
	stored := store.UnjObj(fresh.ResultJSON)
	want := 1.25 + 1.0 // 1M input at $1.25/M + 0.1M output at $10/M
	if fresh.LiveCostUSD != want || stored["cost_usd"] != want || stored["cost_source"] != "estimated" {
		t.Fatalf("want estimated %v, got live=%v result=%s", want, fresh.LiveCostUSD, fresh.ResultJSON)
	}

	// Finished, the estimate counts toward a codex stop budget.
	if err := db.Update("attempts", att.ID, map[string]any{"finished_at": store.Now(), "status": "done"}); err != nil {
		t.Fatal(err)
	}
	h.decode("PUT", "/api/budgets", obj{"per_agent": obj{"codex": obj{"daily_usd": 2, "mode": "stop"}}}, 200, nil)
	if err := budget.Gate(db, "codex"); err == nil {
		t.Fatal("an estimated $2.25 of codex spend should exhaust a $2 codex stop budget")
	}
	next := h.task(pid, "next codex task", "x", obj{"agent": "codex"})
	if code := h.status("POST", fmt.Sprintf("/api/tasks/%d/dispatch", next.id()), obj{}); code != 409 {
		t.Fatalf("dispatch over an estimated codex budget should be refused, got %d", code)
	}

	// The Usage page labels the estimate.
	usage := h.get("/api/usage?days=7")
	found := false
	for _, raw := range usage["by_agent_model"].([]any) {
		row := obj(raw.(map[string]any))
		if row.str("agent") == "codex" && row.num("estimated_usd") == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("usage should label codex spend as estimated: %v", usage["by_agent_model"])
	}

	// Outcomes keep the estimated label rather than treating it as reported.
	if err := outcomes.Rebuild(db, 30); err != nil {
		t.Fatal(err)
	}
	facts, _ := db.OutcomeFactsSince("2000-01-01")
	for _, f := range facts {
		if f.Scope == "attempt" && f.RefID == att.ID && (f.CostSource != "estimated" || f.CostUSD != want) {
			t.Fatalf("outcome fact should be estimated %v, got %+v", want, f)
		}
	}
}

// An interactive Codex session's token deltas are priced the same way into
// usage_daily, where budgets read session spend.
func TestCodexSessionUsageEstimatedFromPriceTable(t *testing.T) {
	h := newHarness(t)
	db := h.App.DB
	sess := h.session(obj{"project_id": h.seededProjectID(), "name": "codex live", "agent": "codex"})
	row, err := db.Session(sess.id())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outcomes.SavePrices(db, outcomes.PriceConfig{Prices: map[string]outcomes.ModelPrice{
		"gpt-5-codex": {InputPer1M: 2, OutputPer1M: 8}}}); err != nil {
		t.Fatal(err)
	}
	in := agentevents.New(db, h.App.Bus)
	if err := in.IngestCodexUsage(row, &agentevents.CodexUsage{Model: "gpt-5-codex", InputTokens: 500_000, OutputTokens: 125_000}); err != nil {
		t.Fatal(err)
	}
	var cost, estimated float64
	if err := db.QueryRow(`SELECT cost_usd, estimated_usd FROM usage_daily WHERE session_id=?`, row.ID).Scan(&cost, &estimated); err != nil {
		t.Fatal(err)
	}
	if cost != 2 || estimated != 2 {
		t.Fatalf("want $2 estimated (0.5M×$2 + 0.125M×$8), got cost=%v estimated=%v", cost, estimated)
	}
	spend, err := db.SpendSince(0, "codex")
	if err != nil || spend != 2 {
		t.Fatalf("codex budget spend should include the estimate, got %v %v", spend, err)
	}
}
