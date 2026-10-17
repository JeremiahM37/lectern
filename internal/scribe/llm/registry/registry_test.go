package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/anthropic"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/openai"
)

// isolate points Scribe's home at a fresh directory and clears every key
// variable the built-in table reads, so the developer's environment does not
// leak into the tests.
func isolate(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "scribe")
	t.Setenv("SCRIBE_HOME", home)
	for _, d := range builtins {
		for _, e := range d.EnvVars {
			t.Setenv(e, "")
		}
	}
	t.Setenv("OLLAMA_HOST", "")
	t.Setenv("LM_STUDIO_URL", "")
	return home
}

// modelServer serves GET /v1/models with one model and counts the hits and the
// Authorization header of the most recent request.
type modelServer struct {
	*httptest.Server
	mu   sync.Mutex
	hits int
	auth string
	path string
}

func newModelServer(t *testing.T) *modelServer {
	t.Helper()
	ms := &modelServer{}
	ms.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ms.mu.Lock()
		ms.hits++
		ms.auth = r.Header.Get("Authorization")
		ms.path = r.URL.Path
		ms.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"served-model"}]}`)
	}))
	t.Cleanup(ms.Close)
	return ms
}

func (ms *modelServer) snapshot() (int, string, string) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return ms.hits, ms.auth, ms.path
}

func customCfg(id, base, keyEnv string) config.Config {
	return config.Config{Providers: []config.Provider{{ID: id, Name: "Lab", BaseURL: base, APIKeyEnv: keyEnv}}}
}

func TestKeyStorePermissions(t *testing.T) {
	home := isolate(t)
	r := New(config.Config{})
	if err := r.SetKey("openai", "sk-stored"); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	fi, err := os.Stat(filepath.Join(home, "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("keys.json mode = %v, want 0600", fi.Mode().Perm())
	}
	di, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("home mode = %v, want 0700", di.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(home, "keys.json.tmp")); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}

	// A fresh registry reads the same store.
	info, ok := New(config.Config{}).Lookup("openai")
	if !ok || !info.HasKey || info.KeySource != "store" {
		t.Fatalf("openai info after SetKey = %+v", info)
	}

	if err := r.RemoveKey("openai"); err != nil {
		t.Fatalf("RemoveKey: %v", err)
	}
	if err := r.RemoveKey("openai"); err != nil {
		t.Fatalf("second RemoveKey: %v", err)
	}
	if info, _ := New(config.Config{}).Lookup("openai"); info.HasKey {
		t.Fatal("key still present after RemoveKey")
	}
}

func TestSetKeyRejectsUnknownAndEmpty(t *testing.T) {
	isolate(t)
	r := New(config.Config{})
	if err := r.SetKey("nope", "k"); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if err := r.SetKey("openai", "   "); err == nil {
		t.Fatal("empty key accepted")
	}
}

func TestEnvironmentBeatsStore(t *testing.T) {
	isolate(t)
	srv := newModelServer(t)
	cfg := customCfg("lab", srv.URL+"/v1", "LAB_TEST_KEY")
	r := New(cfg)
	if err := r.SetKey("lab", "stored-key"); err != nil {
		t.Fatal(err)
	}

	t.Setenv("LAB_TEST_KEY", "env-key")
	if _, err := r.ListModels(context.Background(), "lab"); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if _, auth, _ := srv.snapshot(); auth != "Bearer env-key" {
		t.Fatalf("auth with env set = %q, want env key", auth)
	}

	// With the variable unset, the stored key is used. A new registry avoids
	// the model cache.
	t.Setenv("LAB_TEST_KEY", "")
	r2 := New(cfg)
	if _, err := r2.ListModels(context.Background(), "lab"); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if _, auth, _ := srv.snapshot(); auth != "Bearer stored-key" {
		t.Fatalf("auth without env = %q, want stored key", auth)
	}
	if info, _ := r2.Lookup("lab"); info.KeySource != "store" {
		t.Fatalf("KeySource = %q", info.KeySource)
	}
}

func TestListShowsKeyState(t *testing.T) {
	isolate(t)
	r := New(config.Config{})
	info, _ := r.Lookup("openai")
	if !info.NeedsKey || info.HasKey {
		t.Fatalf("openai before key = %+v", info)
	}
	t.Setenv("OPENAI_API_KEY", "sk-env")
	info, _ = r.Lookup("openai")
	if !info.HasKey || info.KeySource != "env:OPENAI_API_KEY" {
		t.Fatalf("openai with env = %+v", info)
	}
	ol, _ := r.Lookup("ollama")
	if ol.NeedsKey {
		t.Fatal("ollama should not need a key")
	}
	if got := len(r.List()); got != len(builtins) {
		t.Fatalf("List() has %d entries, want %d", got, len(builtins))
	}
}

func TestResolve(t *testing.T) {
	isolate(t)
	srv := newModelServer(t)
	r := New(customCfg("lab", srv.URL, ""))

	p, model, err := r.Resolve("lab/vendor/model-x")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if p.ID() != "lab" || model != "vendor/model-x" {
		t.Fatalf("Resolve = %s, %q", p.ID(), model)
	}
	if _, ok := p.(*openai.Provider); !ok {
		t.Fatalf("custom provider type = %T", p)
	}

	for _, bad := range []string{"", "none", "noslash", "lab/", "lab/none", "nope/x"} {
		if _, _, err := r.Resolve(bad); err == nil {
			t.Errorf("Resolve(%q) succeeded, want error", bad)
		}
	}

	// A built-in with no key names the variable to set.
	_, _, err = r.Resolve("openai/gpt-5.4")
	if err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("missing key error = %v", err)
	}
	t.Setenv("OPENAI_API_KEY", "sk-env")
	p, model, err = r.Resolve("openai/gpt-5.4")
	if err != nil || p.ID() != "openai" || model != "gpt-5.4" {
		t.Fatalf("Resolve openai = %v, %q, %v", p, model, err)
	}
}

func TestAnthropicUsesAnthropicAdapter(t *testing.T) {
	isolate(t)
	r := New(config.Config{})
	if _, err := r.Provider("anthropic"); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("missing key error = %v", err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant")
	p, err := r.Provider("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*anthropic.Provider); !ok {
		t.Fatalf("anthropic provider type = %T", p)
	}
	if p.ID() != "anthropic" {
		t.Fatalf("ID = %q", p.ID())
	}
}

func TestCustomProviderRenaming(t *testing.T) {
	isolate(t)
	cfg := config.Config{Providers: []config.Provider{
		{ID: "openai", BaseURL: "http://x.test/v1"},
		{ID: "lab", BaseURL: "http://a.test/v1"},
		{ID: "lab", BaseURL: "http://b.test/v1"},
		{ID: "lab", BaseURL: "http://c.test/v1"},
		{ID: "", BaseURL: "http://skipped.test/v1"},
		{ID: "nourl"},
	}}
	r := New(cfg)

	ids := map[string]ProviderInfo{}
	for _, p := range r.List() {
		ids[p.ID] = p
	}
	for _, want := range []string{"openai-custom", "lab", "lab-custom", "lab-custom-2"} {
		if _, ok := ids[want]; !ok {
			t.Fatalf("missing provider %q; have %v", want, keysOf(ids))
		}
	}
	if ids["openai"].Custom || ids["openai"].BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("built-in openai was replaced: %+v", ids["openai"])
	}
	if !ids["openai-custom"].Custom || ids["openai-custom"].BaseURL != "http://x.test/v1" {
		t.Fatalf("custom openai-custom = %+v", ids["openai-custom"])
	}
	if _, ok := ids["nourl"]; ok {
		t.Fatal("provider without base_url was registered")
	}

	// A custom provider with no api_key_env needs no key.
	if _, err := r.Provider("openai-custom"); err != nil {
		t.Fatalf("keyless custom provider: %v", err)
	}
}

// ollamaServer answers the native Ollama API: /api/tags lists one model, and
// /api/show reports its context window. It records the last path it saw.
type ollamaServer struct {
	*httptest.Server
	mu   sync.Mutex
	path string
	chat []byte
}

func newOllamaServer(t *testing.T, window int) *ollamaServer {
	t.Helper()
	sv := &ollamaServer{}
	sv.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sv.mu.Lock()
		sv.path = r.URL.Path
		sv.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"served-model","model":"served-model"}]}`)
		case "/api/show":
			fmt.Fprintf(w, `{"model_info":{"qwen35moe.context_length":%d},"capabilities":["tools"]}`, window)
		case "/api/chat":
			raw, _ := io.ReadAll(r.Body)
			sv.mu.Lock()
			sv.chat = raw
			sv.mu.Unlock()
			fmt.Fprint(w, `{"message":{"content":"ok"},"done":true,"done_reason":"stop"}`+"\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(sv.Close)
	return sv
}

func (o *ollamaServer) lastPath() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.path
}

// chatNumCtx streams one tiny request through the registry's ollama provider
// and returns the options.num_ctx the server received.
func chatNumCtx(t *testing.T, r *Registry, model string, srv *ollamaServer) float64 {
	t.Helper()
	p, m, err := r.Resolve("ollama/" + model)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	ch, err := p.Stream(context.Background(), llm.Request{Model: m, Messages: []llm.Message{llm.UserText("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	srv.mu.Lock()
	raw := srv.chat
	srv.mu.Unlock()
	var body struct {
		Options struct {
			NumCtx float64 `json:"num_ctx"`
		} `json:"options"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("chat body: %v (%s)", err, raw)
	}
	return body.Options.NumCtx
}

func TestOllamaBaseFromOLLAMA_HOST(t *testing.T) {
	isolate(t)
	srv := newOllamaServer(t, 8192)
	t.Setenv("OLLAMA_HOST", strings.TrimPrefix(srv.URL, "http://"))

	r := New(config.Config{})
	info, _ := r.Lookup("ollama")
	if info.BaseURL != srv.URL+"/v1" {
		t.Fatalf("ollama base = %q, want %q", info.BaseURL, srv.URL+"/v1")
	}
	models, err := r.ListModels(context.Background(), "ollama")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 1 || models[0].ID != "served-model" {
		t.Fatalf("models = %+v", models)
	}
	if got := srv.lastPath(); got != "/api/show" {
		t.Fatalf("last path = %q, want /api/show (listing goes to /api/tags first)", got)
	}
}

func TestOllamaProviderUsesNativeRootWithOrWithoutV1(t *testing.T) {
	for _, suffix := range []string{"", "/v1", "/v1/"} {
		isolate(t)
		srv := newOllamaServer(t, 8192)
		t.Setenv("OLLAMA_HOST", srv.URL+suffix)

		r := New(config.Config{})
		if _, err := r.Provider("ollama"); err != nil {
			t.Fatalf("suffix %q: Provider: %v", suffix, err)
		}
		if n := chatNumCtx(t, r, "served-model", srv); n != 65536 {
			t.Fatalf("suffix %q: num_ctx = %v, want 65536", suffix, n)
		}
		if got := srv.lastPath(); got != "/api/chat" {
			t.Fatalf("suffix %q: chat path = %q, want /api/chat", suffix, got)
		}
	}
}

func TestOllamaWindowCapAndPrecedence(t *testing.T) {
	isolate(t)
	t.Setenv("SCRIBE_OLLAMA_NUM_CTX", "")
	srv := newOllamaServer(t, 262144)
	t.Setenv("OLLAMA_HOST", srv.URL)

	// No listing yet: the static fallback is 128000, capped to 65536.
	r := New(config.Config{})
	if w := r.ModelInfo("ollama/served-model").ContextWindow; w != 65536 {
		t.Fatalf("uncached window = %d, want 65536", w)
	}
	// The listing reports 262144 and the cap still applies to it.
	models, err := r.ListModels(context.Background(), "ollama")
	if err != nil || len(models) != 1 {
		t.Fatalf("ListModels: %v %+v", err, models)
	}
	if models[0].ContextWindow != 65536 {
		t.Fatalf("listed window = %d, want 65536", models[0].ContextWindow)
	}
	if w := r.ModelInfo("ollama/served-model").ContextWindow; w != 65536 {
		t.Fatalf("cached window = %d, want 65536", w)
	}
	if n := chatNumCtx(t, r, "served-model", srv); n != 65536 {
		t.Fatalf("num_ctx = %v, want the header window 65536", n)
	}

	// SCRIBE_OLLAMA_NUM_CTX replaces the cap, and is what num_ctx carries.
	t.Setenv("SCRIBE_OLLAMA_NUM_CTX", "32768")
	r2 := New(config.Config{})
	if w := r2.ModelInfo("ollama/served-model").ContextWindow; w != 32768 {
		t.Fatalf("env window = %d, want 32768", w)
	}
	if n := chatNumCtx(t, r2, "served-model", srv); n != 32768 {
		t.Fatalf("env num_ctx = %v, want 32768", n)
	}
	// A value that is not a positive integer is ignored.
	t.Setenv("SCRIBE_OLLAMA_NUM_CTX", "lots")
	if w := New(config.Config{}).ModelInfo("ollama/served-model").ContextWindow; w != 65536 {
		t.Fatalf("bad env window = %d, want the cap 65536", w)
	}

	// A user context override beats both the cap and the env value.
	t.Setenv("SCRIBE_OLLAMA_NUM_CTX", "32768")
	ro := New(config.Config{ContextWindowOverrides: map[string]int{"ollama/served-model": 131072}})
	if w := ro.ModelInfo("ollama/served-model").ContextWindow; w != 131072 {
		t.Fatalf("override window = %d, want 131072", w)
	}
	if n := chatNumCtx(t, ro, "served-model", srv); n != 131072 {
		t.Fatalf("override num_ctx = %v, want 131072", n)
	}

	// Other providers keep their own windows.
	if w := New(config.Config{}).ModelInfo("lab/whatever-1").ContextWindow; w != 128000 {
		t.Fatalf("non-ollama window = %d, want 128000", w)
	}
}

func TestLocalBaseNormalising(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:11434":        "http://127.0.0.1:11434/v1",
		"0.0.0.0:11434":          "http://127.0.0.1:11434/v1",
		"http://h.test:1/v1/":    "http://h.test:1/v1",
		"https://gpu.test":       "https://gpu.test/v1",
		"http://lmstudio.lan:12": "http://lmstudio.lan:12/v1",
	}
	for in, want := range cases {
		if got := localBase(in); got != want {
			t.Errorf("localBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListModelsCachesThirtyMinutes(t *testing.T) {
	isolate(t)
	srv := newModelServer(t)
	r := New(customCfg("lab", srv.URL, ""))
	clock := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return clock }

	for i := 0; i < 3; i++ {
		if _, err := r.ListModels(context.Background(), "lab"); err != nil {
			t.Fatal(err)
		}
	}
	if hits, _, _ := srv.snapshot(); hits != 1 {
		t.Fatalf("hits within TTL = %d, want 1", hits)
	}

	clock = clock.Add(29 * time.Minute)
	if _, err := r.ListModels(context.Background(), "lab"); err != nil {
		t.Fatal(err)
	}
	if hits, _, _ := srv.snapshot(); hits != 1 {
		t.Fatalf("hits at 29 min = %d, want 1", hits)
	}

	clock = clock.Add(2 * time.Minute)
	if _, err := r.ListModels(context.Background(), "lab"); err != nil {
		t.Fatal(err)
	}
	if hits, _, _ := srv.snapshot(); hits != 2 {
		t.Fatalf("hits after TTL = %d, want 2", hits)
	}
}

func TestListModelsErrorIsNotCached(t *testing.T) {
	isolate(t)
	var mu sync.Mutex
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"gpt-5-mini"}]}`)
	}))
	t.Cleanup(srv.Close)
	r := New(customCfg("lab", srv.URL, ""))
	if _, err := r.ListModels(context.Background(), "lab"); err == nil {
		t.Fatal("expected error from 503")
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	models, err := r.ListModels(context.Background(), "lab")
	if err != nil {
		t.Fatalf("retry after recovery: %v", err)
	}
	if len(models) != 1 || models[0].ContextWindow != 400000 || models[0].InputPerMTok != 0.25 {
		t.Fatalf("enriched model = %+v", models)
	}
}

func TestModelInfoPricingAndContext(t *testing.T) {
	isolate(t)
	r := New(config.Config{})

	mini := r.ModelInfo("openai/gpt-5-mini")
	if mini.InputPerMTok != 0.25 || mini.OutputPerMTok != 2 || mini.ContextWindow != 400000 {
		t.Fatalf("gpt-5-mini = %+v", mini)
	}
	// Longest key wins: gpt-4.1-mini must not be priced as gpt-4.1.
	if m := r.ModelInfo("openai/gpt-4.1-mini"); m.InputPerMTok != 0.4 {
		t.Fatalf("gpt-4.1-mini input = %v", m.InputPerMTok)
	}
	// Gateway ids match after a slash.
	if m := r.ModelInfo("openrouter/meta-llama/llama-3.3-70b-instruct"); m.InputPerMTok != 0.59 {
		t.Fatalf("gateway llama input = %v", m.InputPerMTok)
	}
	if m := r.ModelInfo("groq/llama-3.3-70b-versatile"); m.OutputPerMTok != 0.79 {
		t.Fatalf("groq llama output = %v", m.OutputPerMTok)
	}
	if m := r.ModelInfo("deepseek/deepseek-reasoner"); m.InputPerMTok != 0.55 {
		t.Fatalf("deepseek-reasoner = %+v", m)
	}
	if m := r.ModelInfo("gemini/gemini-2.5-flash"); m.InputPerMTok != 0.3 || m.ContextWindow != 1048576 {
		t.Fatalf("gemini-2.5-flash = %+v", m)
	}

	claude := r.ModelInfo("anthropic/claude-sonnet-5-5")
	if claude.InputPerMTok <= 0 || claude.ContextWindow != 1000000 {
		t.Fatalf("claude from KnownModels = %+v", claude)
	}

	// An unknown id is priced at the Sonnet-class default and flagged.
	unknown := r.ModelInfo("lab/whatever-1")
	if unknown.InputPerMTok != 3 || unknown.OutputPerMTok != 15 || unknown.ContextWindow != 128000 {
		t.Fatalf("unknown = %+v", unknown)
	}
	if !unknown.Estimated {
		t.Fatal("unknown model must be marked Estimated")
	}
	if claude.Estimated {
		t.Fatal("a known model must not be marked Estimated")
	}
	local := r.ModelInfo("ollama/gpt-5-mini")
	if local.InputPerMTok != 0 || local.Estimated {
		t.Fatalf("local provider should be free and not estimated: %+v", local)
	}
	if free := r.ModelInfo("openrouter/some-model:free"); free.InputPerMTok != 0 || free.Estimated {
		t.Fatalf(":free variant should be free: %+v", free)
	}
}

func TestEnrichKeepsProviderPricesAndFlagsEstimates(t *testing.T) {
	isolate(t)
	r := New(config.Config{})
	// A provider that reports its own price is not estimated.
	own := r.enrich("lab", llm.ModelInfo{ID: "whatever-1", InputPerMTok: 0.5, OutputPerMTok: 1})
	if own.Estimated || own.InputPerMTok != 0.5 {
		t.Fatalf("provider price overwritten: %+v", own)
	}
	// One with no price from the provider gets the default and the flag.
	est := r.enrich("lab", llm.ModelInfo{ID: "whatever-1"})
	if !est.Estimated || est.InputPerMTok != 3 {
		t.Fatalf("default not applied: %+v", est)
	}
}

func TestReasoningStyleInTable(t *testing.T) {
	isolate(t)
	r := New(config.Config{})
	cases := map[string]string{
		"anthropic/claude-opus-5-5":   llm.ReasoningAdaptive,
		"anthropic/claude-haiku-5-5":  llm.ReasoningAdaptive,
		"anthropic/claude-opus-4-7":   llm.ReasoningAdaptive,
		"anthropic/claude-sonnet-4-5": llm.ReasoningBudget,
		"anthropic/claude-opus-4-5":   llm.ReasoningBudget,
		"openai/gpt-5-mini":           llm.ReasoningEffort,
		"openai/o3-mini":              llm.ReasoningEffort,
		"openai/o1-mini":              llm.ReasoningNone,
		"openai/gpt-4.1":              llm.ReasoningNone,
		"deepseek/deepseek-chat":      llm.ReasoningNone,
	}
	for ref, want := range cases {
		if got := r.ModelInfo(ref).ReasoningStyle; got != want {
			t.Errorf("%s ReasoningStyle = %q, want %q", ref, got, want)
		}
	}
}

func TestProviderErrorsForUnknownID(t *testing.T) {
	isolate(t)
	r := New(config.Config{})
	if _, err := r.Provider("nope"); err == nil {
		t.Fatal("unknown provider returned no error")
	}
}

func keysOf(m map[string]ProviderInfo) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Ensure the on-disk key store is not written by read-only paths.
func TestReadOnlyPathsDoNotWriteStore(t *testing.T) {
	home := isolate(t)
	r := New(config.Config{})
	_ = r.List()
	_ = r.ModelInfo("openai/gpt-5.4")
	if _, _, err := r.Resolve("openai/gpt-5.4"); err == nil {
		t.Fatal("expected missing-key error")
	}
	if _, err := os.Stat(filepath.Join(home, "keys.json")); !os.IsNotExist(err) {
		t.Fatalf("read-only path created keys.json: %v", err)
	}
}
