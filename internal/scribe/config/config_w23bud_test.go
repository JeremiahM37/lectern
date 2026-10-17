package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func intPtr(n int) *int { return &n }

// The per-chat budget and the tool-result count follow the same merge rule
// as the global budget: a project value wins, and an explicit zero switches
// a global limit off.
func TestChatBudgetMergesLikeGlobalBudget(t *testing.T) {
	global := Config{DailyTokenBudget: intPtr(5000), ChatDailyTokenBudget: intPtr(1000)}

	inherited := Merge(global, Config{})
	if inherited.ChatDailyBudget() != 1000 {
		t.Fatalf("unset project: chat budget = %d, want the global 1000", inherited.ChatDailyBudget())
	}

	narrower := Merge(global, Config{ChatDailyTokenBudget: intPtr(250)})
	if narrower.ChatDailyBudget() != 250 || narrower.DailyBudget() != 5000 {
		t.Fatalf("project 250: chat %d, global %d", narrower.ChatDailyBudget(), narrower.DailyBudget())
	}

	off := Merge(global, Config{ChatDailyTokenBudget: intPtr(0)})
	if off.ChatDailyBudget() != 0 {
		t.Fatalf("explicit 0 in project should lift the chat limit, got %d", off.ChatDailyBudget())
	}
}

func TestChatBudgetNegativeMeansNoLimit(t *testing.T) {
	if got := (Config{ChatDailyTokenBudget: intPtr(-5)}).ChatDailyBudget(); got != 0 {
		t.Fatalf("negative chat budget = %d, want 0", got)
	}
	if got := (Config{}).ChatDailyBudget(); got != 0 {
		t.Fatalf("unset chat budget = %d, want 0", got)
	}
}

func TestToolResultsKeptDefaultsWhenUnset(t *testing.T) {
	// Zero means the compactor's default, so an unset config changes nothing.
	for _, c := range []Config{{}, {MaxToolResults: -3}} {
		if got := c.ToolResultsKept(); got != 0 {
			t.Errorf("MaxToolResults %d: ToolResultsKept = %d, want 0 (default)", c.MaxToolResults, got)
		}
	}
	if got := (Config{MaxToolResults: 12}).ToolResultsKept(); got != 12 {
		t.Fatalf("ToolResultsKept = %d, want 12", got)
	}
}

func TestToolResultsMergeProjectWins(t *testing.T) {
	global := Config{MaxToolResults: 10}
	if got := Merge(global, Config{}).ToolResultsKept(); got != 10 {
		t.Fatalf("unset project: %d, want 10", got)
	}
	if got := Merge(global, Config{MaxToolResults: 4}).ToolResultsKept(); got != 4 {
		t.Fatalf("project 4: %d, want 4", got)
	}
}

func TestNewBudgetKeysAreWrittenUnderTheirNames(t *testing.T) {
	b, err := json.Marshal(Config{ChatDailyTokenBudget: intPtr(900), MaxToolResults: 7})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"chat_daily_token_budget":900`, `"max_tool_results":7`} {
		if !strings.Contains(s, want) {
			t.Errorf("marshalled config %s lacks %s", s, want)
		}
	}
}
