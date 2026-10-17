package registry

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// withProviderWindow sets a known-wrong window for the test only.
func withProviderWindow(t *testing.T, ref string, w int) {
	t.Helper()
	old := providerWindowOverrides
	providerWindowOverrides = map[string]int{ref: w}
	t.Cleanup(func() { providerWindowOverrides = old })
}

// The table ships empty: no wrong value is known from the spec.
func TestProviderWindowTableStartsEmpty(t *testing.T) {
	if len(providerWindowOverrides) != 0 {
		t.Fatalf("providerWindowOverrides has %d entries; add only sourced figures", len(providerWindowOverrides))
	}
}

// A known-wrong window beats the table in ModelInfo.
func TestProviderWindowAppliesInModelInfo(t *testing.T) {
	isolate(t)
	withProviderWindow(t, "deepseek/deepseek-chat", 64000)
	r := New(config.Config{})
	if m := r.ModelInfo("deepseek/deepseek-chat"); m.ContextWindow != 64000 {
		t.Fatalf("window = %d, want 64000", m.ContextWindow)
	}
}

// The user's own override wins over the provider's known-wrong value.
func TestUserOverrideBeatsProviderWindow(t *testing.T) {
	isolate(t)
	withProviderWindow(t, "deepseek/deepseek-chat", 64000)
	r := New(config.Config{ContextWindowOverrides: map[string]int{"deepseek/deepseek-chat": 32000}})
	if m := r.ModelInfo("deepseek/deepseek-chat"); m.ContextWindow != 32000 {
		t.Fatalf("window = %d, want the user's 32000", m.ContextWindow)
	}
}

// enrich replaces a window the listing reported, because the provider's
// value is the wrong one the registry is correcting.
func TestProviderWindowBeatsListingInEnrich(t *testing.T) {
	isolate(t)
	withProviderWindow(t, "deepseek/deepseek-chat", 64000)
	r := New(config.Config{})
	m := r.enrich("deepseek", llm.ModelInfo{ID: "deepseek-chat", ContextWindow: 999999})
	if m.ContextWindow != 64000 {
		t.Fatalf("enrich window = %d, want 64000", m.ContextWindow)
	}
}

// With a user override, enrich keeps the listed window. ModelInfo applies the
// user's value later, so enrich must not pick the provider correction here.
func TestUserOverrideKeepsListingInEnrich(t *testing.T) {
	isolate(t)
	withProviderWindow(t, "deepseek/deepseek-chat", 64000)
	r := New(config.Config{ContextWindowOverrides: map[string]int{"deepseek/deepseek-chat": 32000}})
	m := r.enrich("deepseek", llm.ModelInfo{ID: "deepseek-chat", ContextWindow: 999999})
	if m.ContextWindow != 999999 {
		t.Fatalf("enrich window = %d, want the listed 999999", m.ContextWindow)
	}
}

// Without any correction, a model the listing did not size keeps the table's
// window, as before.
func TestNoProviderWindowKeepsTable(t *testing.T) {
	isolate(t)
	r := New(config.Config{})
	m := r.enrich("deepseek", llm.ModelInfo{ID: "deepseek-chat"})
	if m.ContextWindow != 128000 {
		t.Fatalf("enrich window = %d, want the table's 128000", m.ContextWindow)
	}
}
