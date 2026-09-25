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
