package api_test

import (
	"fmt"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// TestUsageReportCombinesSessionAndTaskSpend exercises the two data sources
// GET /api/usage merges: usage_daily (session statusline ingest) and
// attempts.result_json (task cost accounting, same source as GET
// /api/stats) — see internal/api/usage.go's doc comment for why they are
// different shapes.
func TestUsageReportCombinesSessionAndTaskSpend(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()

	// Task side: a completed mock task books a known cost into
	// attempts.result_json (see TestStatsAggregatesCosts).
	task := h.run(pid, "cost me", "x", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/complete", task.id()), nil, 200)

	// Session side: a real statusline hook, same fixture as
	// TestHookSessionStatuslineBooksUsage.
	sess := h.session(obj{"project_id": pid, "name": "usage-view", "agent": "claude"})
	tok := hookToken(t, h, sess.id())
	code, body := h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/statusline", sess.id()), hookFixtureStatusline, tok)
	if code != 200 {
		t.Fatalf("statusline hook: got %d: %s", code, body)
	}

	got := h.get("/api/usage?days=30")
	if got.num("days") != 30 {
		t.Fatalf("days = %v", got["days"])
	}
	if got.num("today_usd") <= 0 {
		t.Errorf("today_usd should include today's task cost: %v", got["today_usd"])
	}

	daily := got.list("daily")
	if len(daily) == 0 {
		t.Fatal("expected at least one daily bucket")
	}
	var sawCost bool
	for _, d := range daily {
		if d.num("cost_usd") > 0 {
			sawCost = true
		}
	}
	if !sawCost {
		t.Errorf("no daily bucket booked any cost: %v", daily)
	}

	byAgentModel := got.list("by_agent_model")
	if len(byAgentModel) == 0 {
		t.Fatal("expected an agent/model split")
	}
	var sawClaudeOpus bool
	for _, row := range byAgentModel {
		if row.str("model") == "claude-opus-5-5" {
			sawClaudeOpus = true
		}
	}
	if !sawClaudeOpus {
		t.Errorf("expected the statusline's model in by_agent_model: %v", byAgentModel)
	}

	byProject := got.list("by_project")
	if len(byProject) == 0 {
		t.Fatal("expected a project split")
	}

	topSessions := got.list("top_sessions")
	if len(topSessions) == 0 || topSessions[0].num("cost_usd") <= 0 {
		t.Errorf("top_sessions: %v", topSessions)
	}
	topTasks := got.list("top_tasks")
	if len(topTasks) == 0 || topTasks[0].num("cost_usd") <= 0 {
		t.Errorf("top_tasks: %v", topTasks)
	}

	// Quota should reflect the rate_limits setting the statusline just wrote
	// (docs/agent-events.md: "keep the latest in a settings key rate_limits").
	quota := got.sub("quota")
	if quota == nil || quota["empty"] == true {
		t.Fatalf("quota should be populated after a statusline: %v", got["quota"])
	}
	fiveHour := quota.sub("five_hour")
	if fiveHour == nil || fiveHour.num("used_percentage") != 4 {
		t.Errorf("five_hour.used_percentage = %v", quota["five_hour"])
	}
	if b, _ := quota["stale"].(bool); b {
		t.Error("a statusline just posted; quota must not read stale")
	}
}

// TestUsageQuotaEmptyBeforeAnyStatusline covers the cold-start case: no
// session has ever posted a statusline, so there is no rate_limits setting
// to read, and the quota chip must say "no data" rather than fabricate 0%.
func TestUsageQuotaEmptyBeforeAnyStatusline(t *testing.T) {
	h := newHarness(t)
	got := h.get("/api/usage?days=7")
	quota := got.sub("quota")
	if quota == nil {
		t.Fatal("quota key missing")
	}
	if b, _ := quota["empty"].(bool); !b {
		t.Errorf("expected empty=true with no rate_limits setting: %v", quota)
	}
}

// TestUsageQuotaStaleAfterThirtyMinutes exercises the staleness threshold
// directly against the store, since waiting 30 real minutes in a test is not
// an option.
func TestUsageQuotaStaleAfterThirtyMinutes(t *testing.T) {
	h := newHarness(t)
	old := store.Now() - 3600 // one hour ago
	rl := store.J(map[string]any{
		"five_hour": map[string]any{"used_percentage": 50, "resets_at": old + 18000},
		"seven_day": map[string]any{"used_percentage": 10, "resets_at": old + 604800},
		"at":        old,
	})
	if err := h.App.DB.SetSetting("rate_limits", rl); err != nil {
		t.Fatal(err)
	}
	got := h.get("/api/usage?days=7")
	quota := got.sub("quota")
	if b, _ := quota["stale"].(bool); !b {
		t.Errorf("an hour-old rate_limits snapshot must read stale: %v", quota)
	}
}
