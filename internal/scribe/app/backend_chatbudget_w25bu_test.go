package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// chatBudgetBackend is a backend on a fixed local day with no budgets set.
// The clock is restored when the test ends.
func chatBudgetBackend(t *testing.T, at time.Time) *scribeBackend {
	t.Helper()
	be, _ := testBackend(t)
	old := budgetNow
	budgetNow = func() time.Time { return at }
	t.Cleanup(func() { budgetNow = old })
	return be.(*scribeBackend)
}

func TestChatBudgetReportsEachChatsSpendAndLimit(t *testing.T) {
	b := chatBudgetBackend(t, time.Date(2026, 10, 17, 9, 0, 0, 0, time.Local))
	writeGlobalKey(t, "chat_daily_token_budget", 1000)
	if err := saveDailyUse(dailyUse{Date: "2026-10-17", Tokens: 900, Chats: map[string]chatUse{
		"chat-a": {Tokens: 600},
		"chat-b": {Tokens: 300},
	}}); err != nil {
		t.Fatal(err)
	}
	a := b.current()
	a.Meta.ID = "chat-a"
	if got := b.ChatBudget(); got.Today != 600 || got.Limit != 1000 {
		t.Fatalf("chat-a budget = %+v, want 600 of 1000", got)
	}
	a.Meta.ID = "chat-b"
	if got := b.ChatBudget(); got.Today != 300 {
		t.Fatalf("chat-b budget = %+v, want its own 300", got)
	}
}

func TestChatBudgetYesterdaysSpendDoesNotCount(t *testing.T) {
	b := chatBudgetBackend(t, time.Date(2026, 10, 17, 9, 0, 0, 0, time.Local))
	writeGlobalKey(t, "chat_daily_token_budget", 1000)
	if err := saveDailyUse(dailyUse{Date: "2026-10-16", Chats: map[string]chatUse{"chat-a": {Tokens: 800}}}); err != nil {
		t.Fatal(err)
	}
	b.current().Meta.ID = "chat-a"
	if got := b.ChatBudget(); got.Today != 0 {
		t.Fatalf("yesterday's spend shows as today: %+v", got)
	}
}

func TestChatBudgetWithoutLimitStillReportsSpend(t *testing.T) {
	b := chatBudgetBackend(t, time.Date(2026, 10, 17, 9, 0, 0, 0, time.Local))
	a := b.current()
	a.Meta.ID = "" // an unsaved chat is keyed by claimOwner
	if err := saveDailyUse(dailyUse{Date: "2026-10-17", Chats: map[string]chatUse{a.claimOwner: {Tokens: 42}}}); err != nil {
		t.Fatal(err)
	}
	if got := b.ChatBudget(); got.Limit != 0 || got.Today != 42 {
		t.Fatalf("no per-chat limit: budget = %+v, want 42 with limit 0", got)
	}
}

func TestSetChatBudgetWritesGlobalAndGoesLive(t *testing.T) {
	b := chatBudgetBackend(t, time.Date(2026, 10, 17, 9, 0, 0, 0, time.Local))
	if err := b.SetChatBudget(250000); err != nil {
		t.Fatalf("SetChatBudget: %v", err)
	}
	if got := b.current().liveConfig().ChatDailyBudget(); got != 250000 {
		t.Fatalf("live per-chat limit = %d, want 250000", got)
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("SCRIBE_HOME"), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["chat_daily_token_budget"] != float64(250000) {
		t.Fatalf("global config chat_daily_token_budget = %v", saved["chat_daily_token_budget"])
	}
	if _, set := saved["daily_token_budget"]; set {
		t.Fatal("setting the per-chat budget must not write the global daily_token_budget")
	}
}

func TestSetChatBudgetZeroRemovesTheLimit(t *testing.T) {
	b := chatBudgetBackend(t, time.Date(2026, 10, 17, 9, 0, 0, 0, time.Local))
	if err := b.SetChatBudget(500); err != nil {
		t.Fatal(err)
	}
	if err := b.SetChatBudget(0); err != nil {
		t.Fatal(err)
	}
	if got := b.current().liveConfig().ChatDailyBudget(); got != 0 {
		t.Fatalf("limit after 0 = %d, want none", got)
	}
}

func TestSetChatBudgetRejectsNegativeWithoutSaving(t *testing.T) {
	b := chatBudgetBackend(t, time.Date(2026, 10, 17, 9, 0, 0, 0, time.Local))
	if err := b.SetChatBudget(-1); err == nil {
		t.Fatal("a negative per-chat budget was accepted")
	}
	if got := b.current().liveConfig().ChatDailyBudget(); got != 0 {
		t.Fatalf("a rejected budget became live: %d", got)
	}
}
