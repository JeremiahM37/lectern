package registry

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// Every built-in provider needs a one-line description for the setup
// screens. Custom providers have none and show their id.
func TestBuiltinsHaveDescriptions(t *testing.T) {
	isolate(t)
	r := New(config.Config{})
	for _, p := range r.List() {
		if p.Custom {
			continue
		}
		if p.Description == "" {
			t.Errorf("built-in %s has no description", p.ID)
		}
		if strings.Contains(p.Description, "\n") || len(p.Description) > 120 {
			t.Errorf("%s description is not one short line: %q", p.ID, p.Description)
		}
	}
}

// Key pages are listed exactly, so a wrong or guessed URL fails here. A provider
// gets a page only when its docs confirm the URL; everything else stays empty.
func TestKeyURLsAreTheConfirmedPages(t *testing.T) {
	want := map[string]string{
		"anthropic":  "https://console.anthropic.com/settings/keys",
		"openai":     "https://platform.openai.com/api-keys",
		"deepseek":   "https://platform.deepseek.com/api_keys",
		"openrouter": "https://openrouter.ai/keys",
		"gemini":     "https://aistudio.google.com/apikey",
		"groq":       "https://console.groq.com/keys",
	}
	isolate(t)
	r := New(config.Config{})
	for _, p := range r.List() {
		if p.Custom {
			continue
		}
		if got := p.KeyURL; got != want[p.ID] {
			t.Errorf("%s keyUrl = %q, want %q", p.ID, got, want[p.ID])
		}
	}
}

// Lookup carries the text too, so the wizard can show it for one provider.
func TestLookupExposesDescription(t *testing.T) {
	isolate(t)
	r := New(config.Config{})
	p, ok := r.Lookup("ollama")
	if !ok {
		t.Fatal("ollama not found")
	}
	if p.Description != providerText["ollama"].Description {
		t.Fatalf("ollama description = %q", p.Description)
	}
}

// A provider text entry for an id that is not built in is dead text, so the
// test catches a renamed or removed provider.
func TestProviderTextMatchesBuiltins(t *testing.T) {
	isolate(t)
	r := New(config.Config{})
	for id := range providerText {
		if _, ok := r.Lookup(id); !ok {
			t.Errorf("providerText has %q, which is not a built-in provider", id)
		}
	}
}
