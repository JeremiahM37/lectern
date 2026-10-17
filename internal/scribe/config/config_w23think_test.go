package config

import "testing"

func TestReasoningThinkingFieldRoundTrips(t *testing.T) {
	var c Config
	ok, err := c.SetReasoningField("reasoning_by_provider.deepseek.thinking", "On")
	if !ok || err != nil {
		t.Fatalf("set on: ok=%v err=%v", ok, err)
	}
	if got := c.ReasoningByProvider["deepseek"].Thinking; got != "on" {
		t.Errorf("Thinking = %q, want on", got)
	}
	if _, err := c.SetReasoningField("reasoning_by_provider.deepseek.thinking", ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, ok := c.ReasoningByProvider["deepseek"]; ok {
		t.Error("clearing the only field should drop the provider entry")
	}
}

func TestReasoningThinkingRejectsOtherValues(t *testing.T) {
	var c Config
	if ok, err := c.SetReasoningField("reasoning_by_provider.deepseek.thinking", "enabled"); !ok || err == nil {
		t.Errorf("enabled should be rejected: ok=%v err=%v", ok, err)
	}
}
