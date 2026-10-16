package app

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/registry"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

func TestSetConfigProviderAddIsUsableWithoutRestart(t *testing.T) {
	be, _ := testBackend(t)
	if err := be.SetConfig("provider.add", map[string]any{"id": "myproxy", "base_url": "http://127.0.0.1:9/v1", "api_key_env": ""}, false); err != nil {
		t.Fatalf("provider.add: %v", err)
	}
	found := false
	for _, p := range be.Providers() {
		found = found || p.ID == "myproxy"
	}
	if !found {
		t.Fatal("the new provider should appear without restarting")
	}
	if err := be.SetKey("myproxy", "k"); err != nil {
		t.Fatalf("SetKey on the new provider: %v", err)
	}
}

func TestSetConfigProviderAddRefusesBadInput(t *testing.T) {
	be, _ := testBackend(t)
	cases := map[string]map[string]any{
		"built-in id": {"id": "anthropic", "base_url": "https://example.test/v1"},
		"no scheme":   {"id": "myproxy", "base_url": "example.test/v1"},
		"slash in id": {"id": "a/b", "base_url": "https://example.test/v1"},
		"missing id":  {"base_url": "https://example.test/v1"},
	}
	for name, v := range cases {
		if err := be.SetConfig("provider.add", v, false); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestSetConfigAllowDenyRoundTrip(t *testing.T) {
	be, _ := testBackend(t)
	if err := be.SetConfig("allow", []string{"go test", "git status"}, true); err != nil {
		t.Fatal(err)
	}
	if err := be.SetConfig("deny", []string{"rm -rf"}, true); err != nil {
		t.Fatal(err)
	}
	cfg := be.Config()
	if got := cfg["allow"]; !reflect.DeepEqual(got, []string{"go test", "git status"}) {
		t.Fatalf("allow = %v", got)
	}
	if got := cfg["deny"]; !reflect.DeepEqual(got, []string{"rm -rf"}) {
		t.Fatalf("deny = %v", got)
	}
}

func TestOllamaReachableAsksTheNativeAPI(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"models":[]}`))
	}))
	defer up.Close()
	t.Setenv("OLLAMA_HOST", strings.TrimPrefix(up.URL, "http://"))
	if !ollamaReachable(registry.New(config.Config{})) {
		t.Fatal("a server answering /api/tags should count as Ollama running")
	}

	up.Close()
	if ollamaReachable(registry.New(config.Config{})) {
		t.Fatal("a closed port should not count as running")
	}
}

// needsSetup asks the optional setup check the TUI uses on start.
func needsSetup(be tui.Backend) bool {
	sc, ok := be.(interface{ NeedsSetup() bool })
	return ok && sc.NeedsSetup()
}

func TestNeedsSetupOnlyWithoutKeysOrOllama(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[]}`))
	}))
	defer up.Close()
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	// Ollama down, no keys: the wizard is needed.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OLLAMA_HOST", strings.TrimPrefix(deadURL, "http://"))
	be, _ := testBackend(t)
	if !needsSetup(be) {
		t.Fatal("no key and no Ollama should need setup")
	}

	// A key on any provider is enough.
	if err := be.SetKey("fake", "k"); err != nil {
		t.Fatal(err)
	}
	if needsSetup(be) {
		t.Fatal("a stored key should satisfy setup")
	}

	// Ollama answering is enough even without a key.
	t.Setenv("OLLAMA_HOST", strings.TrimPrefix(up.URL, "http://"))
	be2, _ := testBackend(t)
	if needsSetup(be2) {
		t.Fatal("a running Ollama should satisfy setup")
	}
}

func TestSetConfigReasoningByProviderIsSaved(t *testing.T) {
	be, _ := testBackend(t)
	if err := be.SetConfig("reasoning_by_provider.anthropic.effort", "high", false); err != nil {
		t.Fatalf("set effort: %v", err)
	}
	c, err := config.LoadFile(config.GlobalPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := c.ReasoningByProvider["anthropic"].Effort; got != "high" {
		t.Fatalf("saved effort = %q, want high", got)
	}
}
