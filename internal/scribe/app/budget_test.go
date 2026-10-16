package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// budgetTest is a backend with a daily budget of limit tokens and a clock
// the test controls. The clock is restored when the test ends.
func budgetTest(t *testing.T, limit int, at time.Time) (*App, func(time.Time)) {
	t.Helper()
	be, _ := testBackend(t)
	a := be.(*scribeBackend).current()
	a.Cfg.DailyTokenBudget = &limit
	writeGlobalBudget(t, limit)
	now := at
	old := budgetNow
	budgetNow = func() time.Time { return now }
	t.Cleanup(func() { budgetNow = old })
	return a, func(n time.Time) { now = n }
}

// writeGlobalBudget sets daily_token_budget in the global config file, the
// way a user does, so the test covers the path liveConfig reads.
func writeGlobalBudget(t *testing.T, limit int) {
	t.Helper()
	path := filepath.Join(os.Getenv("SCRIBE_HOME"), "config.json")
	raw := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &raw)
	}
	raw["daily_token_budget"] = limit
	b, _ := json.Marshal(raw)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRaisedBudgetTakesEffectWithoutRestart(t *testing.T) {
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.Local)
	a, _ := budgetTest(t, 100, now)
	if err := saveDailyUse(dailyUse{Date: "2026-10-16", Tokens: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "hello", nil); err == nil {
		t.Fatal("spent budget should refuse")
	}
	// The user edits the file while Scribe is running.
	writeGlobalBudget(t, 0)
	if err := a.dailyBudgetGate(); err != nil {
		t.Fatalf("budget raised to 0 on disk still refuses: %v", err)
	}
}

func readBudget(t *testing.T) dailyUse {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(os.Getenv("SCRIBE_HOME"), "budget.json"))
	if err != nil {
		t.Fatalf("budget file: %v", err)
	}
	var u dailyUse
	if err := json.Unmarshal(b, &u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestDailyBudgetRefusesTurnOnceSpent(t *testing.T) {
	now := time.Date(2026, 10, 16, 14, 0, 0, 0, time.Local)
	a, _ := budgetTest(t, 1000, now)
	if err := saveDailyUse(dailyUse{Date: "2026-10-16", Tokens: 1000}); err != nil {
		t.Fatal(err)
	}
	before := len(a.Agent.Messages())
	var done []agent.Event
	a.Subscribe(func(e agent.Event) {
		if e.Kind == agent.EvTurnDone {
			done = append(done, e)
		}
	})
	stats, err := a.Run(context.Background(), "review the parser", nil)
	if err == nil {
		t.Fatal("a turn ran although today's budget is spent")
	}
	// The TUI clears its busy state only on turn_done, so the refusal must
	// end the turn with one, carrying the error.
	if len(done) != 1 || done[0].Stats == nil || !strings.Contains(done[0].Stats.Error, "daily token budget") {
		t.Fatalf("turn_done events = %+v, want one carrying the budget error", done)
	}
	for _, want := range []string{"daily token budget reached", "1,000 of 1,000", "daily_token_budget", "local midnight"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q lacks %q", err, want)
		}
	}
	if stats.Steps != 0 || len(a.Agent.Messages()) != before {
		t.Fatalf("a refused turn reached the model: steps %d, messages %d -> %d", stats.Steps, before, len(a.Agent.Messages()))
	}
}

func TestDailyBudgetCountsTurnsAndWarnsOnce(t *testing.T) {
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.Local)
	a, _ := budgetTest(t, 1000, now)
	var mu sync.Mutex
	var notices []string
	a.Subscribe(func(e agent.Event) {
		if e.Kind == agent.EvNotice {
			mu.Lock()
			notices = append(notices, e.Text)
			mu.Unlock()
		}
	})
	// Cache reads are not counted: the prefix was already paid for.
	a.recordDailyTokens(agent.TurnStats{Tokens: llm.Usage{InputTokens: 500, OutputTokens: 100, CacheReadTokens: 90000}})
	if u := readBudget(t); u.Tokens != 600 || u.Warned {
		t.Fatalf("after 600 tokens: %+v", u)
	}
	a.recordDailyTokens(agent.TurnStats{Tokens: llm.Usage{InputTokens: 300}})
	a.recordDailyTokens(agent.TurnStats{Tokens: llm.Usage{OutputTokens: 50}})
	u := readBudget(t)
	if u.Tokens != 950 || !u.Warned {
		t.Fatalf("after 950 tokens: %+v", u)
	}
	mu.Lock()
	defer mu.Unlock()
	// The warning fires on the record that crosses 80 % (900 of 1,000).
	if len(notices) != 1 || !strings.Contains(notices[0], "900 of 1,000") {
		t.Fatalf("notices = %q, want one warning with the count", notices)
	}
}

func TestDailyBudgetResetsAtLocalMidnight(t *testing.T) {
	late := time.Date(2026, 10, 16, 23, 59, 0, 0, time.Local)
	a, setClock := budgetTest(t, 1000, late)
	a.recordDailyTokens(agent.TurnStats{Tokens: llm.Usage{InputTokens: 900}})
	if err := a.dailyBudgetGate(); err != nil {
		t.Fatalf("900 of 1000 should still run: %v", err)
	}
	a.recordDailyTokens(agent.TurnStats{Tokens: llm.Usage{InputTokens: 100}})
	if err := a.dailyBudgetGate(); err == nil {
		t.Fatal("1000 of 1000 should refuse")
	}
	setClock(time.Date(2026, 10, 17, 0, 1, 0, 0, time.Local))
	if err := a.dailyBudgetGate(); err != nil {
		t.Fatalf("the budget did not reset at local midnight: %v", err)
	}
	a.recordDailyTokens(agent.TurnStats{Tokens: llm.Usage{InputTokens: 5}})
	if u := readBudget(t); u.Date != "2026-10-17" || u.Tokens != 5 || u.Warned {
		t.Fatalf("the new day should start from zero, got %+v", u)
	}
}

func TestDailyBudgetOffWithoutLimit(t *testing.T) {
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.Local)
	a, _ := budgetTest(t, 0, now)
	if err := saveDailyUse(dailyUse{Date: "2026-10-16", Tokens: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	if err := a.dailyBudgetGate(); err != nil {
		t.Fatalf("budget 0 must mean no limit: %v", err)
	}
	a.Cfg.DailyTokenBudget = nil
	writeGlobalBudget(t, 0)
	if err := a.dailyBudgetGate(); err != nil {
		t.Fatalf("unset budget must mean no limit: %v", err)
	}
}

func TestDailyBudgetFileIsPrivateAndAtomic(t *testing.T) {
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.Local)
	a, _ := budgetTest(t, 1000, now)
	a.recordDailyTokens(agent.TurnStats{Tokens: llm.Usage{InputTokens: 10}})
	info, err := os.Stat(filepath.Join(os.Getenv("SCRIBE_HOME"), "budget.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("budget file mode %v, want 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("SCRIBE_HOME"), "budget.json.tmp")); !os.IsNotExist(err) {
		t.Fatalf("temporary file left behind: %v", err)
	}
}

func TestGroupInt(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1250000: "1,250,000", -4200: "-4,200"} {
		if got := groupInt(n); got != want {
			t.Errorf("groupInt(%d) = %q, want %q", n, got, want)
		}
	}
}
