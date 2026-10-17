package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// writeGlobalKey sets one top-level key in the global config file, the way a
// user does, so liveConfig reads it.
func writeGlobalKey(t *testing.T, key string, v any) {
	t.Helper()
	path := filepath.Join(os.Getenv("SCRIBE_HOME"), "config.json")
	raw := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &raw)
	}
	raw[key] = v
	b, _ := json.Marshal(raw)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// chatBudgetTest is a backend with a per-chat budget of limit tokens and no
// global budget, and a clock the test controls.
func chatBudgetTest(t *testing.T, limit int, at time.Time) *App {
	t.Helper()
	be, _ := testBackend(t)
	a := be.(*scribeBackend).current()
	writeGlobalKey(t, "chat_daily_token_budget", limit)
	now := at
	old := budgetNow
	budgetNow = func() time.Time { return now }
	t.Cleanup(func() { budgetNow = old })
	return a
}

func TestChatBudgetRefusesOnlyTheChatThatSpentIt(t *testing.T) {
	a := chatBudgetTest(t, 1000, time.Date(2026, 10, 16, 10, 0, 0, 0, time.Local))
	if err := saveDailyUse(dailyUse{Date: "2026-10-16", Chats: map[string]chatUse{"chat-a": {Tokens: 1000}}}); err != nil {
		t.Fatal(err)
	}
	a.Meta.ID = "chat-a"
	err := a.dailyBudgetGate()
	if err == nil {
		t.Fatal("chat-a spent its budget but was not refused")
	}
	for _, want := range []string{"daily token budget reached for this chat", "1,000 of 1,000", "chat_daily_token_budget", "local midnight"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q lacks %q", err, want)
		}
	}
	a.Meta.ID = "chat-b"
	if err := a.dailyBudgetGate(); err != nil {
		t.Fatalf("chat-b has spent nothing but was refused: %v", err)
	}
}

func TestChatBudgetCountsEachChatAndWarnsOnce(t *testing.T) {
	a := chatBudgetTest(t, 1000, time.Date(2026, 10, 16, 9, 0, 0, 0, time.Local))
	var notices []string
	a.Subscribe(func(e agent.Event) {
		if e.Kind == agent.EvNotice {
			notices = append(notices, e.Text)
		}
	})
	a.Meta.ID = "chat-a"
	a.recordDailyTokens(agent.TurnStats{Tokens: llm.Usage{InputTokens: 600}})
	a.Meta.ID = "chat-b"
	a.recordDailyTokens(agent.TurnStats{Tokens: llm.Usage{InputTokens: 100}})
	a.Meta.ID = "chat-a"
	a.recordDailyTokens(agent.TurnStats{Tokens: llm.Usage{InputTokens: 300}})

	u := readBudget(t)
	if u.Chats["chat-a"].Tokens != 900 || u.Chats["chat-b"].Tokens != 100 {
		t.Fatalf("per-chat counts = %+v", u.Chats)
	}
	if !u.Chats["chat-a"].Warned || u.Chats["chat-b"].Warned {
		t.Fatalf("warned flags = %+v", u.Chats)
	}
	// The global total is still the sum of every chat.
	if u.Tokens != 1000 {
		t.Fatalf("global tokens = %d, want 1000", u.Tokens)
	}
	// Chat-a's warning fires at 800 of 1,000 (80 %), once.
	var chatNotices []string
	for _, n := range notices {
		if strings.Contains(n, "This chat's daily token budget") {
			chatNotices = append(chatNotices, n)
		}
	}
	if len(chatNotices) != 1 || !strings.Contains(chatNotices[0], "900 of 1,000") {
		t.Fatalf("chat notices = %q, want one warning at 900 of 1,000", chatNotices)
	}
}

func TestChatBudgetWithoutLimitOnlyCounts(t *testing.T) {
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.Local)
	be, _ := testBackend(t)
	a := be.(*scribeBackend).current()
	budgetNow = func() time.Time { return now }
	t.Cleanup(func() { budgetNow = time.Now })
	if err := saveDailyUse(dailyUse{Date: "2026-10-16", Chats: map[string]chatUse{"chat-a": {Tokens: 99999999}}}); err != nil {
		t.Fatal(err)
	}
	a.Meta.ID = "chat-a"
	if err := a.dailyBudgetGate(); err != nil {
		t.Fatalf("no per-chat limit set, but refused: %v", err)
	}
	a.recordDailyTokens(agent.TurnStats{Tokens: llm.Usage{InputTokens: 10}})
	if u := readBudget(t); u.Chats["chat-a"].Tokens != 99999999+10 {
		t.Fatalf("spend not counted without a limit: %+v", u.Chats)
	}
}

func TestChatBudgetUnsavedChatUsesItsOwnKey(t *testing.T) {
	a := chatBudgetTest(t, 100, time.Date(2026, 10, 16, 9, 0, 0, 0, time.Local))
	a.Meta.ID = ""
	if a.chatKey() == "" || a.chatKey() != a.claimOwner {
		t.Fatalf("unsaved chat key = %q, want the App's own id %q", a.chatKey(), a.claimOwner)
	}
	if err := saveDailyUse(dailyUse{Date: "2026-10-16", Chats: map[string]chatUse{a.claimOwner: {Tokens: 100}}}); err != nil {
		t.Fatal(err)
	}
	if err := a.dailyBudgetGate(); err == nil {
		t.Fatal("unsaved chat that spent its budget was not refused")
	}
}

func TestChatBudgetRefusesTurnBeforeTheModel(t *testing.T) {
	a := chatBudgetTest(t, 1000, time.Date(2026, 10, 16, 14, 0, 0, 0, time.Local))
	a.Meta.ID = "chat-a"
	if err := saveDailyUse(dailyUse{Date: "2026-10-16", Chats: map[string]chatUse{"chat-a": {Tokens: 1000}}}); err != nil {
		t.Fatal(err)
	}
	before := len(a.Agent.Messages())
	stats, err := a.Run(context.Background(), "keep going", nil)
	if err == nil || !strings.Contains(err.Error(), "for this chat") {
		t.Fatalf("err = %v, want the per-chat refusal", err)
	}
	if stats.Steps != 0 || len(a.Agent.Messages()) != before {
		t.Fatalf("a refused chat turn reached the model: steps %d", stats.Steps)
	}
}
