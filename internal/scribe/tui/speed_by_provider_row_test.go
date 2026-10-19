package tui

import "testing"

// The per-provider Speed row writes only that provider's entry and reads it
// back for that provider only.
func TestProviderSpeedRowWritesOneProvider(t *testing.T) {
	be := NewFakeBackend()
	if err := be.SetField7("performance.speed", "fast", false); err != nil {
		t.Fatal(err)
	}
	s := &settings{be: be}

	row := s.providerSpeedRow("anthropic", fields7(be).Performance)
	if row.key != "performance.speed_by_provider.anthropic" || row.value != "inherit" {
		t.Fatalf("anthropic row key=%q value=%q, want its key and inherit", row.key, row.value)
	}
	row.act() // inherit -> fast
	p := fields7(be).Performance
	if p.SpeedFor("anthropic") != "fast" {
		t.Fatalf("anthropic speed = %q after the row, want fast", p.SpeedFor("anthropic"))
	}
	if p.SpeedFor("openai") != "fast" || p.SpeedByProvider["openai"] != "" {
		t.Fatalf("openai was touched: map=%v", p.SpeedByProvider)
	}

	row = s.providerSpeedRow("anthropic", fields7(be).Performance)
	if row.value != "fast" {
		t.Fatalf("anthropic row shows %q after write, want fast", row.value)
	}
	row.act() // fast -> standard
	p = fields7(be).Performance
	if p.SpeedFor("anthropic") != "standard" {
		t.Fatalf("anthropic speed = %q, want standard (fast off for this provider)", p.SpeedFor("anthropic"))
	}
	if p.Speed != "fast" {
		t.Fatalf("global speed changed to %q", p.Speed)
	}

	row = s.providerSpeedRow("anthropic", fields7(be).Performance)
	row.act() // standard -> inherit, the entry is removed
	if _, ok := fields7(be).Performance.SpeedByProvider["anthropic"]; ok {
		t.Fatal("inherit should remove the anthropic entry")
	}
}
