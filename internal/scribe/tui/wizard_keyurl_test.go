package tui

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// The key step shows the registry's key page and description, so a provider
// with a known page sends the user there and one without shows no link.
func TestWizardKeyViewShowsRegistryKeyURL(t *testing.T) {
	th := theme.Default()
	w := &wizard{chosen: ProviderStatus{
		ID:          "mistral",
		Name:        "Mistral",
		KeyURL:      "https://console.mistral.ai/api-keys",
		Description: "Mistral's hosted models.",
	}}
	out := strings.Join(w.keyView(th, 80), "\n")
	if !strings.Contains(out, "Get one at https://console.mistral.ai/api-keys") {
		t.Errorf("key step should link the provider's key page:\n%s", out)
	}
	if !strings.Contains(out, "Mistral's hosted models.") {
		t.Errorf("key step should carry the provider description:\n%s", out)
	}
}

func TestWizardKeyViewNoLinkWithoutKeyURL(t *testing.T) {
	th := theme.Default()
	w := &wizard{chosen: ProviderStatus{ID: "mistral", Name: "Mistral"}}
	out := strings.Join(w.keyView(th, 80), "\n")
	if strings.Contains(out, "Get one at") {
		t.Errorf("no key page is known, so no link should be shown:\n%s", out)
	}
}
