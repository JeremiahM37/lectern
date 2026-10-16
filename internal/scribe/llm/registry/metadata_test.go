package registry

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// seed puts a model listing in the cache as if ListModels had fetched it.
func seed(r *Registry, provider string, models ...llm.ModelInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[provider] = cachedModels{at: r.now(), models: models}
}

func TestPrewarmListsEveryUsableProvider(t *testing.T) {
	isolate(t)
	var mu sync.Mutex
	hits := map[string]int{}
	mk := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hits[name]++
			mu.Unlock()
			_, _ = io.WriteString(w, `{"data":[{"id":"m"}]}`)
		}))
	}
	a, b := mk("a"), mk("b")
	t.Cleanup(a.Close)
	t.Cleanup(b.Close)
	r := New(config.Config{Providers: []config.Provider{
		{ID: "a", BaseURL: a.URL},
		{ID: "b", BaseURL: b.URL},
	}})
	r.Prewarm(context.Background())
	if hits["a"] != 1 || hits["b"] != 1 {
		t.Fatalf("hits = %v, want one listing per keyless custom provider", hits)
	}
}

func TestInvalidateModelsForcesRefresh(t *testing.T) {
	isolate(t)
	srv := newModelServer(t)
	r := New(customCfg("lab", srv.URL, ""))
	if _, err := r.ListModels(context.Background(), "lab"); err != nil {
		t.Fatal(err)
	}
	r.InvalidateModels("lab")
	if _, err := r.ListModels(context.Background(), "lab"); err != nil {
		t.Fatal(err)
	}
	if hits, _, _ := srv.snapshot(); hits != 2 {
		t.Fatalf("hits = %d, want 2 after invalidation", hits)
	}
}

func TestGroupedGatewayMatchesBySuffix(t *testing.T) {
	isolate(t)
	r := New(config.Config{})
	seed(r, "llmgateway",
		llm.ModelInfo{ID: "gpt-4.1", ContextWindow: 1047576, InputPerMTok: 2},
		llm.ModelInfo{ID: "openai/gpt-4.1-mini", ContextWindow: 999, InputPerMTok: 9},
	)
	if m := r.ModelInfo("llmgateway/openai/gpt-4.1"); m.ContextWindow != 1047576 || m.InputPerMTok != 2 {
		t.Fatalf("suffix match = %+v", m)
	}
	// Longest listed id wins.
	if m := r.ModelInfo("llmgateway/openai/gpt-4.1-mini"); m.ContextWindow != 999 {
		t.Fatalf("longest match = %+v", m)
	}
	// The match must start on a segment boundary, so the cached gpt-4.1 entry
	// does not answer for xgpt-4.1 (which then falls to the table).
	if m := r.ModelInfo("llmgateway/xgpt-4.1"); m.ContextWindow == 1047576 {
		t.Fatalf("boundary violated: %+v", m)
	}

	// A non-grouped provider is matched by exact id only.
	r2 := New(customCfg("lab", "http://unused", ""))
	seed(r2, "lab", llm.ModelInfo{ID: "zeta-7", ContextWindow: 777})
	if m := r2.ModelInfo("lab/vendor/zeta-7"); m.ContextWindow == 777 {
		t.Fatalf("non-grouped suffix match: %+v", m)
	}
}

func TestCatalogZeroPriceIsFreeAndNotReplacedByTable(t *testing.T) {
	isolate(t)
	r := New(config.Config{})
	m := r.enrich("openrouter", llm.ModelInfo{ID: "deepseek/deepseek-chat", CatalogPriced: true})
	if m.InputPerMTok != 0 || m.OutputPerMTok != 0 || m.Estimated {
		t.Fatalf("catalog zero must stay free: %+v", m)
	}
	// Without a catalog price the table applies.
	m = r.enrich("openrouter", llm.ModelInfo{ID: "deepseek/deepseek-chat"})
	if m.InputPerMTok != 0.27 {
		t.Fatalf("table price = %v, want 0.27", m.InputPerMTok)
	}
}

func TestCatalogPricingFromListing(t *testing.T) {
	isolate(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"vendor/model-x","context_length":65536,"pricing":{"prompt":"0.000004","completion":"0.000012"}}]}`)
	}))
	t.Cleanup(srv.Close)
	r := New(customCfg("cat", srv.URL, ""))
	if _, err := r.ListModels(context.Background(), "cat"); err != nil {
		t.Fatal(err)
	}
	m := r.ModelInfo("cat/vendor/model-x")
	if math.Abs(m.InputPerMTok-4) > 1e-9 || math.Abs(m.OutputPerMTok-12) > 1e-9 || m.ContextWindow != 65536 {
		t.Fatalf("catalog metadata = %+v", m)
	}
}

func TestFreeSuffixBeatsTheTable(t *testing.T) {
	isolate(t)
	r := New(config.Config{})
	m := r.ModelInfo("openrouter/deepseek/deepseek-chat:free")
	if m.InputPerMTok != 0 || m.OutputPerMTok != 0 || m.Estimated {
		t.Fatalf(":free must be free: %+v", m)
	}
}

func TestHeuristicPricingForUnknownVariants(t *testing.T) {
	isolate(t)
	r := New(config.Config{})
	cases := []struct {
		ref       string
		in, out   float64
		estimated bool
	}{
		{"lab/future-opus-thing", 5, 25, true},
		{"lab/grok-9", 3, 15, true},
		{"lab/xgpt-thing", 1.25, 10, true},
		{"lab/mystery-7", 3, 15, true}, // Sonnet-tier default
	}
	for _, c := range cases {
		m := r.ModelInfo(c.ref)
		if m.InputPerMTok != c.in || m.OutputPerMTok != c.out || m.Estimated != c.estimated {
			t.Errorf("%s = %+v, want in %v out %v estimated %v", c.ref, m, c.in, c.out, c.estimated)
		}
	}
}

func TestFireworksDeepSeekTierPricing(t *testing.T) {
	isolate(t)
	r := New(config.Config{})
	m := r.ModelInfo("fireworks/accounts/fireworks/models/deepseek-v3p1")
	if m.InputPerMTok != 0.56 || m.OutputPerMTok != 1.68 || m.Estimated {
		t.Fatalf("fireworks deepseek-v3 = %+v", m)
	}
	// The same id on the DeepSeek API keeps its own price.
	if m := r.ModelInfo("deepseek/deepseek-v3"); m.InputPerMTok == 0.56 {
		t.Fatalf("fireworks tier leaked to deepseek: %+v", m)
	}
}

func TestKeyPriorityAppPrefersStore(t *testing.T) {
	isolate(t)
	t.Setenv("LAB_KEY", "env-key")
	r := New(customCfg("lab", "http://unused", "LAB_KEY"))
	if err := r.SetKey("lab", "store-key"); err != nil {
		t.Fatal(err)
	}
	if info, _ := r.Lookup("lab"); info.KeySource != "env:LAB_KEY" {
		t.Fatalf("default priority source = %q, want env", info.KeySource)
	}
	r.SetKeyPriority("app")
	if info, _ := r.Lookup("lab"); info.KeySource != "store" {
		t.Fatalf("app priority source = %q, want store", info.KeySource)
	}
	r.SetKeyPriority("bogus")
	if info, _ := r.Lookup("lab"); info.KeySource != "env:LAB_KEY" {
		t.Fatalf("unknown priority must fall back to env: %q", info.KeySource)
	}
}

func TestAvailableProbesKeylessCustomProviders(t *testing.T) {
	isolate(t)
	ok401 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(ok401.Close)
	boom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(boom.Close)
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	r := New(config.Config{Providers: []config.Provider{
		{ID: "up", BaseURL: ok401.URL},
		{ID: "err", BaseURL: boom.URL},
		{ID: "gone", BaseURL: closedURL},
		{ID: "keyed", BaseURL: ok401.URL, APIKeyEnv: "KEYED_KEY"},
	}})
	ctx := context.Background()
	if !r.Available(ctx, "up") {
		t.Error("401 from a keyless server means it is up")
	}
	if r.Available(ctx, "err") || r.Available(ctx, "gone") {
		t.Error("500 and connection refused are not available")
	}
	if r.Available(ctx, "keyed") {
		t.Error("a keyed provider with no key is not available")
	}
	t.Setenv("KEYED_KEY", "k")
	if !r.Available(ctx, "keyed") {
		t.Error("a keyed provider with a key is available")
	}
	if !r.Available(ctx, "ollama") {
		t.Error("a keyless built-in is always available")
	}
}

func TestFamilyFromProviderOrModelID(t *testing.T) {
	cases := []struct{ provider, model, want string }{
		{"anthropic", "claude-sonnet-5-5", "claude"},
		{"openai", "gpt-5.4", "openai"},
		{"gemini", "gemini-2.5-flash", "google"},
		{"openrouter", "anthropic/claude-x", "claude"},
		{"llmgateway", "openai/gpt-4.1", "openai"},
		{"llmgateway", "vendor/o3-mini", "openai"},
		{"llmgateway", "x/gemini-3", "google"},
		{"llmgateway", "x/grok-4", "xai"},
		{"llmgateway", "x/deepseek-chat", "deepseek"},
		{"llmgateway", "x/kimi-k2", "kimi"},
		{"llmgateway", "x/moonshot-v1", "moonshot"},
		{"lab", "mistral-large", ""},
	}
	for _, c := range cases {
		if got := Family(c.provider, c.model); got != c.want {
			t.Errorf("Family(%s, %s) = %q, want %q", c.provider, c.model, got, c.want)
		}
	}
}

func TestContextOverrideWinsOverListing(t *testing.T) {
	isolate(t)
	r := New(config.Config{})
	seed(r, "lab", llm.ModelInfo{ID: "m", ContextWindow: 8000})
	r.SetContextOverrides(map[string]int{"lab/m": 65000})
	if m := r.ModelInfo("lab/m"); m.ContextWindow != 65000 {
		t.Fatalf("override = %d, want 65000", m.ContextWindow)
	}
}

func TestModelInfoAsyncFetchesMetadata(t *testing.T) {
	isolate(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"m","meta":{"n_ctx_train":32768}}]}`)
	}))
	t.Cleanup(srv.Close)
	r := New(customCfg("lab", srv.URL, ""))
	if m := r.ModelInfoAsync(context.Background(), "lab/m"); m.ContextWindow != 32768 {
		t.Fatalf("async context = %d, want 32768", m.ContextWindow)
	}
}

func TestModelInfoAsyncGivesUpAfterFiveSeconds(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the metadata timeout")
	}
	isolate(t)
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-stop:
		case <-time.After(10 * time.Second):
		}
	}))
	t.Cleanup(func() { close(stop); srv.Close() })
	r := New(customCfg("lab", srv.URL, ""))
	start := time.Now()
	m := r.ModelInfoAsync(context.Background(), "lab/m")
	if el := time.Since(start); el > 7*time.Second {
		t.Fatalf("took %v, want about five seconds", el)
	}
	if m.ContextWindow != defaultContext {
		t.Fatalf("fallback context = %d", m.ContextWindow)
	}
}
