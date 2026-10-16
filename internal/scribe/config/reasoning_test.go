package config

import "testing"

func TestSetReasoningFieldFlatKeys(t *testing.T) {
	var c Config
	for _, step := range []struct {
		key string
		v   any
	}{
		{"reasoning_by_provider.anthropic.effort", "medium"},
		{"reasoning_by_provider.anthropic.budget", 8000},
		{"reasoning_by_provider.anthropic.service_tier", "auto"},
		{"reasoning_by_provider.openai.service_tier", "priority"},
	} {
		handled, err := c.SetReasoningField(step.key, step.v)
		if !handled || err != nil {
			t.Fatalf("%s: handled=%v err=%v", step.key, handled, err)
		}
	}
	want := Reasoning{Effort: "medium", Budget: 8000, ServiceTier: "auto"}
	if c.ReasoningByProvider["anthropic"] != want {
		t.Fatalf("anthropic: %+v", c.ReasoningByProvider["anthropic"])
	}

	// Budget can come in as the string the settings screen may send.
	if _, err := c.SetReasoningField("reasoning_by_provider.anthropic.budget", "5000"); err != nil {
		t.Fatal(err)
	}
	if c.ReasoningByProvider["anthropic"].Budget != 5000 {
		t.Fatalf("string budget not parsed: %+v", c.ReasoningByProvider["anthropic"])
	}

	// An empty value clears the field and an all-empty provider is removed.
	c.SetReasoningField("reasoning_by_provider.openai.service_tier", "")
	if _, ok := c.ReasoningByProvider["openai"]; ok {
		t.Fatal("a provider with no fields left should be removed")
	}
}

func TestSetReasoningFieldRejectsBadInput(t *testing.T) {
	var c Config
	if handled, _ := c.SetReasoningField("model", "x"); handled {
		t.Fatal("non-reasoning keys must not be claimed")
	}
	if handled, err := c.SetReasoningField("reasoning_by_provider.anthropic.speed", "fast"); !handled || err == nil {
		t.Fatalf("unknown field: handled=%v err=%v", handled, err)
	}
	if _, err := c.SetReasoningField("reasoning_by_provider.anthropic.budget", "lots"); err == nil {
		t.Fatal("non-numeric budget must be an error")
	}
	if _, err := c.SetReasoningField("reasoning_by_provider.budget", 1); err == nil {
		t.Fatal("a key with no provider must be an error")
	}
}
