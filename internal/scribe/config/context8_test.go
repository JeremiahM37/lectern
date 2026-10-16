package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDailyBudgetSetterAndUnset(t *testing.T) {
	var c Config
	if c.DailyBudget() != 0 {
		t.Fatalf("unset budget = %d, want no limit", c.DailyBudget())
	}
	if ok, err := c.SetField("daily_token_budget", "500000"); !ok || err != nil {
		t.Fatalf("set: handled=%v err=%v", ok, err)
	}
	if c.DailyBudget() != 500000 {
		t.Fatalf("budget = %d", c.DailyBudget())
	}
	if _, err := c.SetField("daily_token_budget", "0"); err != nil {
		t.Fatal(err)
	}
	if c.DailyTokenBudget == nil || c.DailyBudget() != 0 {
		t.Fatalf("0 must be stored as an explicit no-limit, got %v", c.DailyTokenBudget)
	}
	for _, bad := range []any{"-1", "lots", -5} {
		if _, err := c.SetField("daily_token_budget", bad); err == nil {
			t.Errorf("SetField(%v) accepted a bad budget", bad)
		}
	}
}

func TestCompactKeepRecentSetter(t *testing.T) {
	var c Config
	if _, err := c.SetField("compact_keep_recent", 6); err != nil || c.CompactKeepRecent != 6 {
		t.Fatalf("set 6: %d, %v", c.CompactKeepRecent, err)
	}
	if _, err := c.SetField("compact_keep_recent", "-2"); err == nil {
		t.Fatal("a negative keep count was accepted")
	}
	if _, err := c.SetField("compact_keep_recent", "many"); err == nil {
		t.Fatal("a word was accepted as a count")
	}
}

func TestDailyBudgetMergeProjectWins(t *testing.T) {
	global := 1000
	base := Config{DailyTokenBudget: &global, CompactKeepRecent: 4}
	if got := Merge(base, Config{}); got.DailyBudget() != 1000 || got.CompactKeepRecent != 4 {
		t.Fatalf("project without the keys changed the global values: %+v", got)
	}
	zero := 0
	if got := Merge(base, Config{DailyTokenBudget: &zero}); got.DailyBudget() != 0 {
		t.Fatalf("project 0 should switch the global budget off, got %d", got.DailyBudget())
	}
	if got := Merge(base, Config{CompactKeepRecent: 10}); got.CompactKeepRecent != 10 {
		t.Fatalf("project keep = %d, want 10", got.CompactKeepRecent)
	}
}

func TestContextKeysLoadFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"daily_token_budget": 250000, "compact_keep_recent": 6, "compact_strategy": "v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.DailyBudget() != 250000 || c.CompactKeepRecent != 6 || c.CompactStrategy != "v1" {
		t.Fatalf("loaded %+v", c)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid file rejected: %v", err)
	}
}

func TestValidateRejectsNegativeContextKeys(t *testing.T) {
	neg := -1
	c := Config{CompactKeepRecent: -2, DailyTokenBudget: &neg}
	err := c.Validate()
	if err == nil {
		t.Fatal("negative keys validated")
	}
	for _, want := range []string{"compact_keep_recent", "daily_token_budget"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	b, _ := json.Marshal(c)
	if !strings.Contains(string(b), "compact_keep_recent") {
		t.Fatalf("the key is not written to JSON: %s", b)
	}
}
