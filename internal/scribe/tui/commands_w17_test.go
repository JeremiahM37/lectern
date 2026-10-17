package tui

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

func TestSpeedWithNoArgumentShowsCurrentAndChoices(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	fb.flat.Performance.Speed = "fast"
	f := flashOf(t, speedCommand(m, ""))
	if f.kind != "info" || !strings.Contains(f.text, "Speed: fast") || !strings.Contains(f.text, "off, standard, fast") {
		t.Fatalf("status should show fast and the choices, got %q (%s)", f.text, f.kind)
	}
}

func TestSpeedSetsAndClears(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	f := flashOf(t, speedCommand(m, "Fast"))
	if fb.flat.Performance.Speed != "fast" || f.kind != "ok" {
		t.Fatalf("/speed Fast should save fast and confirm, got %q (%s)", fb.flat.Performance.Speed, f.kind)
	}
	if !strings.Contains(f.text, "next session") {
		t.Fatalf("confirmation should say when speed applies, got %q", f.text)
	}
	speedCommand(m, "off")
	if fb.flat.Performance.Speed != "" {
		t.Fatalf("/speed off should clear the value, got %q", fb.flat.Performance.Speed)
	}
}

func TestSpeedRejectsUnknownChoiceAndListsValid(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	fb.flat.Performance.Speed = "standard"
	f := flashOf(t, speedCommand(m, "turbo"))
	if f.kind != "err" || !strings.Contains(f.text, "Valid: off, standard, fast") {
		t.Fatalf("a bad speed should list the valid choices, got %q (%s)", f.text, f.kind)
	}
	if fb.flat.Performance.Speed != "standard" {
		t.Fatalf("a bad speed must not change the setting, now %q", fb.flat.Performance.Speed)
	}
}

func TestTierActsOnTheCurrentProvider(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	// The fake starts on anthropic/claude-sonnet-5.
	tierCommand(m, "auto")
	last := fb.writes[len(fb.writes)-1]
	if last.Key != "reasoning_by_provider.anthropic.service_tier" || last.Value != "auto" {
		t.Fatalf("/tier auto should write the anthropic tier, got %+v", last)
	}
	tierCommand(m, "OFF")
	last = fb.writes[len(fb.writes)-1]
	if last.Key != "reasoning_by_provider.anthropic.service_tier" || last.Value != "" {
		t.Fatalf("/tier off should clear the tier, got %+v", last)
	}
}

func TestTierShowsCurrentValueWithNoArgument(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	fb.flat.ReasoningByProvider = map[string]config.Reasoning{"anthropic": {ServiceTier: "standard_only"}}
	f := flashOf(t, tierCommand(m, ""))
	if !strings.Contains(f.text, "standard_only") || !strings.Contains(f.text, "off, auto, standard_only") {
		t.Fatalf("status should show the tier and its choices, got %q", f.text)
	}
}

func TestTierRejectsAValueTheProviderDoesNotTake(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	before := len(fb.writes)
	f := flashOf(t, tierCommand(m, "priority"))
	if f.kind != "err" || !strings.Contains(f.text, "off, auto, standard_only") {
		t.Fatalf("anthropic has no priority tier; the reply should list its choices, got %q", f.text)
	}
	if len(fb.writes) != before {
		t.Fatal("a rejected tier must not be written")
	}
}

func TestTierSaysWhenTheProviderHasNoTier(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	if err := fb.SetModel("xai/grok-4"); err != nil {
		t.Fatal(err)
	}
	f := flashOf(t, tierCommand(m, "auto"))
	if f.kind != "err" || !strings.Contains(f.text, "xai") {
		t.Fatalf("a provider without tiers should say so, got %q (%s)", f.text, f.kind)
	}
}

func TestSendReasoningTogglesTheFlag(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	f := flashOf(t, sendReasoningCommand(m, ""))
	if !strings.Contains(f.text, "Send reasoning: off") {
		t.Fatalf("default should show off, got %q", f.text)
	}
	sendReasoningCommand(m, "on")
	if p := fb.flat.Performance.SendReasoning; p == nil || !*p {
		t.Fatal("/sendreasoning on should set the flag")
	}
	f = flashOf(t, sendReasoningCommand(m, ""))
	if !strings.Contains(f.text, "Send reasoning: on") {
		t.Fatalf("status should now show on, got %q", f.text)
	}
	sendReasoningCommand(m, "off")
	if p := fb.flat.Performance.SendReasoning; p == nil || *p {
		t.Fatal("/sendreasoning off should clear the flag to false")
	}
}

func TestSendReasoningRejectsOtherWords(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	f := flashOf(t, sendReasoningCommand(m, "maybe"))
	if f.kind != "err" || !strings.Contains(f.text, "Valid: on, off") {
		t.Fatalf("a bad value should list on and off, got %q (%s)", f.text, f.kind)
	}
	if fb.flat.Performance.SendReasoning != nil {
		t.Fatal("a rejected value must not be written")
	}
}
