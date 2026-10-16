package registry

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// A provider with modelsAPI false never asks the server for a list. The
// static models are the list, and the network is not touched.
func TestModelsAPIFalseListsStaticModelsWithoutNetwork(t *testing.T) {
	isolate(t)
	srv := newModelServer(t)
	cfg := config.Config{Providers: []config.Provider{{
		ID: "lab", BaseURL: srv.URL + "/v1",
		Models:    []string{"static-a", "static-b"},
		ModelsAPI: &config.ModelsAPI{Off: true},
	}}}
	r := New(cfg)
	models, err := r.ListModels(context.Background(), "lab")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "static-a" {
		t.Fatalf("models = %+v", models)
	}
	if hits, _, _ := srv.snapshot(); hits != 0 {
		t.Fatalf("server was asked %d times; modelsAPI false must not list", hits)
	}
}

// With listing off and no static models there is nothing to show, and the
// error says why.
func TestModelsAPIFalseWithoutModelsIsAnError(t *testing.T) {
	isolate(t)
	r := New(config.Config{Providers: []config.Provider{{
		ID: "lab", BaseURL: "http://127.0.0.1:1/v1", ModelsAPI: &config.ModelsAPI{Off: true},
	}}})
	if _, err := r.ListModels(context.Background(), "lab"); err == nil {
		t.Fatal("expected an error when listing is off and no models are configured")
	}
}

// An explicit modelsAPI URL is used as the list address, whatever baseURL is.
func TestModelsAPIExplicitURL(t *testing.T) {
	isolate(t)
	srv := newModelServer(t)
	r := New(config.Config{Providers: []config.Provider{{
		ID: "lab", BaseURL: "http://127.0.0.1:1/v1", APIKeyEnv: "",
		ModelsAPI: &config.ModelsAPI{URL: srv.URL + "/catalog/list"},
	}}})
	models, err := r.ListModels(context.Background(), "lab")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, path := srv.snapshot(); path != "/catalog/list" {
		t.Fatalf("list fetched from %q, want /catalog/list", path)
	}
	if len(models) != 1 || models[0].ID != "served-model" {
		t.Fatalf("models = %+v", models)
	}
}

// Without modelsAPI the list stays at baseURL + /models, as before.
func TestModelsAPIDefaultIsBaseURLModels(t *testing.T) {
	isolate(t)
	srv := newModelServer(t)
	r := New(customCfg("lab", srv.URL+"/v1", ""))
	if _, err := r.ListModels(context.Background(), "lab"); err != nil {
		t.Fatal(err)
	}
	if _, _, path := srv.snapshot(); path != "/v1/models" {
		t.Fatalf("default list path = %q, want /v1/models", path)
	}
}

// keyPriority and contextWindowOverrides come from the config; the registry
// applies them itself so the app does not have to call setters.
func TestConfigKeyPriorityAndContextOverridesApplied(t *testing.T) {
	isolate(t)
	r := New(config.Config{
		KeyPriority:            "app",
		ContextWindowOverrides: map[string]int{"lab/big": 123456},
		Providers:              []config.Provider{{ID: "lab", BaseURL: "http://127.0.0.1:1/v1"}},
	})
	if r.keyPriority != "app" {
		t.Fatalf("keyPriority = %q, want app", r.keyPriority)
	}
	if got := r.ModelInfo("lab/big").ContextWindow; got != 123456 {
		t.Fatalf("context window = %d, want the override 123456", got)
	}
	if got := New(config.Config{}).keyPriority; got != "env" {
		t.Fatalf("default keyPriority = %q, want env", got)
	}
}

func TestSecretPriorityAndDefaults(t *testing.T) {
	isolate(t)
	home := os.Getenv("SCRIBE_HOME")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	r := New(config.Config{})
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	if v, src := r.Secret(BraveKeyID, ""); v != "" || src != "" {
		t.Fatalf("nothing set: got %q %q", v, src)
	}
	if err := writeKeys(filepath.Join(home, "keys.json"), map[string]string{BraveKeyID: "stored"}); err != nil {
		t.Fatal(err)
	}
	if v, src := r.Secret(BraveKeyID, ""); v != "stored" || src != "store" {
		t.Fatalf("store only: got %q %q", v, src)
	}
	t.Setenv("BRAVE_SEARCH_API_KEY", "from-env")
	if v, src := r.Secret(BraveKeyID, ""); v != "from-env" || src != "env:BRAVE_SEARCH_API_KEY" {
		t.Fatalf("env beats store by default: got %q %q", v, src)
	}
	r.SetKeyPriority("app")
	if v, _ := r.Secret(BraveKeyID, ""); v != "stored" {
		t.Fatalf("app priority: store must win, got %q", v)
	}
	// A custom variable name from config.search.brave_key_env replaces the default.
	t.Setenv("MY_BRAVE", "custom")
	r.SetKeyPriority("env")
	if v, src := r.Secret(BraveKeyID, "MY_BRAVE"); v != "custom" || src != "env:MY_BRAVE" {
		t.Fatalf("custom env name: got %q %q", v, src)
	}
	if v, _ := r.Secret(JinaKeyID, ""); v != "" {
		t.Fatalf("jina has no key here, got %q", v)
	}
}

func TestSecretFallsBackToStoreWhenEnvEmpty(t *testing.T) {
	home := isolate(t)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeKeys(filepath.Join(home, "keys.json"), map[string]string{JinaKeyID: "j-store"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JINA_API_KEY", "   ")
	r := New(config.Config{})
	if v, src := r.Secret(JinaKeyID, ""); v != "j-store" || src != "store" {
		t.Fatalf("blank env must fall through to the store: %q %q", v, src)
	}
}
