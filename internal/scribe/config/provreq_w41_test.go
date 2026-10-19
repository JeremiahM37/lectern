package config

import (
	"encoding/json"
	"testing"
)

func TestSpeedForFallsBackToGlobal(t *testing.T) {
	p := Performance{Speed: "fast"}
	if got := p.SpeedFor("anthropic"); got != "fast" {
		t.Fatalf("no entry: SpeedFor = %q, want the global fast", got)
	}
}

func TestSpeedForPerProviderEntryWins(t *testing.T) {
	p := Performance{Speed: "fast", SpeedByProvider: map[string]string{"openrouter": "standard"}}
	if got := p.SpeedFor("openrouter"); got != "standard" {
		t.Fatalf("explicit standard must beat the global fast, got %q", got)
	}
	if got := p.SpeedFor("anthropic"); got != "fast" {
		t.Fatalf("other providers keep the global, got %q", got)
	}
}

func TestMergeSpeedByProviderOverlaysPerKey(t *testing.T) {
	base := Performance{SpeedByProvider: map[string]string{"anthropic": "fast", "openai": "fast"}}
	over := Performance{SpeedByProvider: map[string]string{"openai": "standard"}}
	got := mergePerformance(base, over).SpeedByProvider
	if got["anthropic"] != "fast" {
		t.Fatalf("a project entry must not drop other global entries: %v", got)
	}
	if got["openai"] != "standard" {
		t.Fatalf("the overlay must win for its own key: %v", got)
	}
	if base.SpeedByProvider["openai"] != "fast" {
		t.Fatal("merge must not write into the base map")
	}
}

func TestMergePerformanceKeepsBaseWhenOverlayEmpty(t *testing.T) {
	base := Performance{SpeedByProvider: map[string]string{"anthropic": "fast"}, GroqReasoningFormat: "raw"}
	got := mergePerformance(base, Performance{})
	if got.SpeedByProvider["anthropic"] != "fast" || got.GroqReasoningFormat != "raw" {
		t.Fatalf("empty overlay changed the base: %+v", got)
	}
}

func TestValidateNewPerformanceKeys(t *testing.T) {
	ok := Config{Performance: Performance{
		GroqReasoningFormat: "hidden",
		SpeedByProvider:     map[string]string{"anthropic": "fast", "openai": "standard", "groq": ""},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid values rejected: %v", err)
	}

	badFormat := Config{Performance: Performance{GroqReasoningFormat: "loud"}}
	if err := badFormat.Validate(); err == nil {
		t.Fatal("groq_reasoning_format loud must be rejected")
	}

	badSpeed := Config{Performance: Performance{SpeedByProvider: map[string]string{"openai": "turbo"}}}
	if err := badSpeed.Validate(); err == nil {
		t.Fatal("speed_by_provider turbo must be rejected")
	}

	emptyID := Config{Performance: Performance{SpeedByProvider: map[string]string{"": "fast"}}}
	if err := emptyID.Validate(); err == nil {
		t.Fatal("an empty provider id must be rejected")
	}
}

func TestPerformanceNewKeysRoundTripJSON(t *testing.T) {
	var c Config
	body := `{"performance":{"speed_by_provider":{"anthropic":"fast"},"groq_reasoning_format":"parsed"}}`
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatal(err)
	}
	if c.Performance.SpeedFor("anthropic") != "fast" || c.Performance.GroqReasoningFormat != "parsed" {
		t.Fatalf("keys did not parse: %+v", c.Performance)
	}
}
