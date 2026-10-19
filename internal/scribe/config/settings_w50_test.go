package config

import "testing"

// The four settings with rows in the settings screen must write through the
// flat-key path and clear back to their defaults.
func TestW50FeatureSettingsWriteAndClear(t *testing.T) {
	var c Config
	for _, key := range []string{
		"performance.anthropic_code_execution",
		"performance.anthropic_computer_use",
		"performance.anthropic_server_compaction",
	} {
		if ok, err := c.SetField(key, true); !ok || err != nil {
			t.Fatalf("%s: set = %v, %v", key, ok, err)
		}
		if !c.ClearField(key) {
			t.Fatalf("%s: clear refused", key)
		}
	}
	if c.Performance.AnthropicCodeExecution != nil || c.Performance.AnthropicComputerUse != nil || c.Performance.AnthropicServerCompaction != nil {
		t.Fatalf("clear left values behind: %+v", c.Performance)
	}

	const key = "performance.openai_reasoning_summary"
	for _, v := range []string{"auto", "concise", "detailed", ""} {
		if ok, err := c.SetField(key, v); !ok || err != nil {
			t.Fatalf("summary %q: set = %v, %v", v, ok, err)
		}
		if c.Performance.OpenAIReasoningSummary != v {
			t.Fatalf("summary stored %q, want %q", c.Performance.OpenAIReasoningSummary, v)
		}
	}
	if _, err := c.SetField(key, "verbose"); err == nil {
		t.Fatal("an unknown summary value must be refused")
	}
}
