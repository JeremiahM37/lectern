package outcomes

import "testing"

func TestPricesDefaultEmptyEstimatesNothing(t *testing.T) {
	db := testDB(t)
	cfg := LoadPrices(db)
	if len(cfg.Prices) != 0 {
		t.Fatalf("a fresh install should have no configured prices: %+v", cfg)
	}
	if _, ok := cfg.Estimate("gpt-5", 1000, 1000); ok {
		t.Error("an unconfigured model must not produce an estimate")
	}
}

func TestPricesSaveLoadRoundTrip(t *testing.T) {
	db := testDB(t)
	saved, err := SavePrices(db, PriceConfig{Prices: map[string]ModelPrice{
		"gpt-5": {InputPer1M: 3, OutputPer1M: 15},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Prices) != 1 {
		t.Fatalf("save should round-trip the entry: %+v", saved)
	}
	loaded := LoadPrices(db)
	usd, ok := loaded.Estimate("gpt-5", 2_000_000, 1_000_000)
	if !ok {
		t.Fatal("gpt-5 should now be estimable")
	}
	want := 2.0*3 + 1.0*15
	if usd != want {
		t.Fatalf("estimate = %v, want %v", usd, want)
	}
}

func TestPricesValidateRejectsNegativeAndUnnamed(t *testing.T) {
	if err := (PriceConfig{Prices: map[string]ModelPrice{"m": {InputPer1M: -1}}}).Validate(); err == nil {
		t.Error("a negative rate should be rejected")
	}
	if err := (PriceConfig{Prices: map[string]ModelPrice{" ": {InputPer1M: 1}}}).Validate(); err == nil {
		t.Error("a blank model name should be rejected")
	}
	if err := (PriceConfig{Prices: map[string]ModelPrice{"m": {InputPer1M: 1, OutputPer1M: 2}}}).Validate(); err != nil {
		t.Errorf("a valid entry should validate: %v", err)
	}
}

func TestSavePricesRejectsInvalidConfig(t *testing.T) {
	db := testDB(t)
	if _, err := SavePrices(db, PriceConfig{Prices: map[string]ModelPrice{"m": {InputPer1M: -1}}}); err == nil {
		t.Error("SavePrices should refuse to persist an invalid config")
	}
	if raw := db.Setting(PricesSettingsKey); raw != "" {
		t.Errorf("a rejected save must not have written anything: %q", raw)
	}
}

func TestEstimateBillsCachedInputAtCachedRate(t *testing.T) {
	cachedRate := 0.125
	cfg := PriceConfig{Prices: map[string]ModelPrice{
		"gpt-5-codex": {InputPer1M: 1.25, OutputPer1M: 10, CachedInputPer1M: &cachedRate},
		"codex":       {InputPer1M: 2, OutputPer1M: 8},
	}}
	// 1M input of which 0.8M cached, 0.1M output.
	got, ok := cfg.EstimateTokens("codex", "gpt-5-codex", Tokens{Input: 1_000_000, CachedInput: 800_000, Output: 100_000})
	want := 0.2*1.25 + 0.8*0.125 + 0.1*10
	if !ok || got < want-1e-9 || got > want+1e-9 {
		t.Fatalf("cached rate: got %v %v, want %v", got, ok, want)
	}
	// No cached rate configured: cached input is billed as ordinary input.
	got, _ = cfg.EstimateTokens("codex", "", Tokens{Input: 1_000_000, CachedInput: 800_000, Output: 0})
	if got != 2 {
		t.Fatalf("without a cached rate, cached input bills at the input rate: %v", got)
	}
	negative := -1.0
	if err := (PriceConfig{Prices: map[string]ModelPrice{"m": {CachedInputPer1M: &negative}}}).Validate(); err == nil {
		t.Fatal("a negative cached rate must be rejected")
	}
}
