package config

import (
	"encoding/json"
	"testing"
	"time"
)

// The retry block carries base_delay_ms and max_attempts (spec 10).

func TestRetryParsesBaseDelayAndMaxAttempts(t *testing.T) {
	var r Retry
	if err := json.Unmarshal([]byte(`{"transient":2,"stall":1,"base_delay_ms":500,"max_attempts":4}`), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.Transient != 2 || r.Stall != 1 || r.BaseDelayMs != 500 || r.MaxAttempts != 4 {
		t.Fatalf("parsed %+v", r)
	}
	if got := r.BaseDelay(); got != 500*time.Millisecond {
		t.Fatalf("BaseDelay = %v, want 500ms", got)
	}
}

func TestRetryBaseDelayUnsetLeavesTheAgentDefault(t *testing.T) {
	if got := (Retry{}).BaseDelay(); got != 0 {
		t.Fatalf("BaseDelay of an unset retry = %v, want 0", got)
	}
}

func TestMergeRetryOverlaysTheNewFields(t *testing.T) {
	base := Retry{Transient: 3, BaseDelayMs: 300, MaxAttempts: 2}
	got := mergeRetry(base, Retry{BaseDelayMs: 700})
	if got.BaseDelayMs != 700 || got.MaxAttempts != 2 || got.Transient != 3 {
		t.Fatalf("merged %+v, want base delay 700 and the rest kept", got)
	}
	got = mergeRetry(base, Retry{MaxAttempts: 6})
	if got.MaxAttempts != 6 || got.BaseDelayMs != 300 {
		t.Fatalf("merged %+v, want max attempts 6 and base delay 300 kept", got)
	}
}

func TestValidateRejectsNegativeRetryFields(t *testing.T) {
	var ok Config
	ok.Retry = Retry{BaseDelayMs: 0, MaxAttempts: 0}
	if err := ok.Validate(); err != nil {
		t.Fatalf("unset retry fields rejected: %v", err)
	}
	for name, r := range map[string]Retry{
		"base_delay_ms": {BaseDelayMs: -1},
		"max_attempts":  {MaxAttempts: -1},
	} {
		c := Config{Retry: r}
		if err := c.Validate(); err == nil {
			t.Fatalf("%s = negative passed validation", name)
		}
	}
}
