package config

import "testing"

// A per-provider speed is written through SetField and read back for that
// provider only; the global Speed and the other providers keep their values.
func TestSpeedByProviderSetFieldWritesOneProvider(t *testing.T) {
	c := Config{Performance: Performance{Speed: "fast", SpeedByProvider: map[string]string{"openai": "fast"}}}

	if ok, err := c.SetField("performance.speed_by_provider.anthropic", "standard"); !ok || err != nil {
		t.Fatalf("SetField anthropic = %v, %v", ok, err)
	}
	if got := c.Performance.SpeedFor("anthropic"); got != "standard" {
		t.Fatalf("anthropic SpeedFor = %q, want standard", got)
	}
	if got := c.Performance.SpeedFor("openai"); got != "fast" {
		t.Fatalf("openai SpeedFor = %q, want its own fast entry", got)
	}
	if got := c.Performance.SpeedFor("groq"); got != "fast" {
		t.Fatalf("groq SpeedFor = %q, want the global fast", got)
	}
	if c.Performance.Speed != "fast" {
		t.Fatalf("global Speed changed to %q", c.Performance.Speed)
	}

	// An empty value removes the entry, so the provider inherits the global.
	if _, err := c.SetField("performance.speed_by_provider.anthropic", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Performance.SpeedByProvider["anthropic"]; ok {
		t.Fatal("empty value should remove the anthropic entry")
	}
	if got := c.Performance.SpeedFor("anthropic"); got != "fast" {
		t.Fatalf("after removal anthropic SpeedFor = %q, want global fast", got)
	}

	if _, err := c.SetField("performance.speed_by_provider.openai", "turbo"); err == nil {
		t.Fatal("an unknown speed must be refused")
	}
	if got := c.Performance.SpeedByProvider["openai"]; got != "fast" {
		t.Fatalf("refused write changed openai to %q", got)
	}
}
