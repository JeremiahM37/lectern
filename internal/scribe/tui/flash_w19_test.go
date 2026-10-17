package tui

import (
	"testing"
	"time"
)

func TestFlashDurationGrowsWithLengthAndIsCapped(t *testing.T) {
	short := flashDuration("Copied")
	long := flashDuration("Service tier: not available for ollama. Tiers apply to anthropic and openai; /model switches provider.")
	if short < 3*time.Second || long <= short || long > 6*time.Second {
		t.Fatalf("short %v, long %v", short, long)
	}
}

func TestTierOnAProviderWithoutTiersIsInformation(t *testing.T) {
	m, fb := newTestModel(t, 120, 40)
	if err := fb.SetModel("ollama/qwen3.6:35b-a3b"); err != nil {
		t.Fatal(err)
	}
	f := flashOf(t, tierCommand(m, ""))
	if f.kind != "info" {
		t.Fatalf("flash kind %q (%s), want info", f.kind, f.text)
	}
}
