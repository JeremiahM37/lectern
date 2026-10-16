// Package registry is Scribe's provider table: the built-in providers, the
// user's custom OpenAI-compatible providers from config, the key store, model
// listing with a short cache, and "provider/model" resolution.
package registry

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/anthropic"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/openai"
)

// modelCacheTTL bounds how long ListModels results are reused.
const modelCacheTTL = 10 * time.Minute

// Definition describes one provider, built-in or custom.
type Definition struct {
	ID           string
	Name         string
	BaseURL      string
	BaseEnv      string            // env var overriding BaseURL (ollama: OLLAMA_HOST)
	EnvVars      []string          // first non-empty wins; the store is consulted after
	KeyOptional  bool              // the provider works without a key
	Anthropic    bool              // native Anthropic Messages API, else OpenAI-compatible
	DefaultModel string            // used by the UI when nothing else is set
	NerdIcon     string            // Nerd Font glyph
	ASCIIIcon    string            // fallback when Nerd Fonts are off
	Headers      map[string]string // extra request headers
	Models       []string          // static model list (custom providers)
	Custom       bool
}

// ProviderInfo is the public view of a provider.
type ProviderInfo struct {
	ID           string
	Name         string
	BaseURL      string
	DefaultModel string
	NerdIcon     string
	ASCIIIcon    string
	EnvVars      []string
	Custom       bool
	// NeedsKey is false for providers that work without a key.
	NeedsKey bool
	// HasKey reports whether a key resolves from the environment or the store.
	HasKey bool
	// KeySource is "env:NAME", "store", or "" when no key resolves.
	KeySource string
}

// builtins is the provider table in display order.
var builtins = []Definition{
	{ID: "anthropic", Name: "Anthropic", BaseURL: "https://api.anthropic.com", EnvVars: []string{"ANTHROPIC_API_KEY"}, Anthropic: true, DefaultModel: "claude-sonnet-5-5", NerdIcon: "", ASCIIIcon: "an"},
	{ID: "openai", Name: "OpenAI", BaseURL: "https://api.openai.com/v1", EnvVars: []string{"OPENAI_API_KEY"}, DefaultModel: "gpt-5.4", NerdIcon: "", ASCIIIcon: "oa"},
	{ID: "deepseek", Name: "DeepSeek", BaseURL: "https://api.deepseek.com/v1", EnvVars: []string{"DEEPSEEK_API_KEY"}, DefaultModel: "deepseek-chat", NerdIcon: "", ASCIIIcon: "ds"},
	{ID: "openrouter", Name: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1", EnvVars: []string{"OPENROUTER_API_KEY"}, DefaultModel: "anthropic/claude-sonnet-4.5", NerdIcon: "", ASCIIIcon: "or"},
	{ID: "groq", Name: "Groq", BaseURL: "https://api.groq.com/openai/v1", EnvVars: []string{"GROQ_API_KEY"}, DefaultModel: "llama-3.3-70b-versatile", NerdIcon: "", ASCIIIcon: "gq"},
	{ID: "together", Name: "Together AI", BaseURL: "https://api.together.xyz/v1", EnvVars: []string{"TOGETHER_API_KEY"}, DefaultModel: "meta-llama/Llama-3.3-70B-Instruct-Turbo", NerdIcon: "", ASCIIIcon: "tg"},
	{ID: "mistral", Name: "Mistral", BaseURL: "https://api.mistral.ai/v1", EnvVars: []string{"MISTRAL_API_KEY"}, DefaultModel: "mistral-large-latest", NerdIcon: "", ASCIIIcon: "mi"},
	{ID: "xai", Name: "xAI", BaseURL: "https://api.x.ai/v1", EnvVars: []string{"XAI_API_KEY"}, DefaultModel: "grok-4", NerdIcon: "", ASCIIIcon: "xa"},
	{ID: "gemini", Name: "Gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", EnvVars: []string{"GEMINI_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY"}, DefaultModel: "gemini-2.5-flash", NerdIcon: "", ASCIIIcon: "gm"},
	{ID: "fireworks", Name: "Fireworks", BaseURL: "https://api.fireworks.ai/inference/v1", EnvVars: []string{"FIREWORKS_API_KEY"}, DefaultModel: "accounts/fireworks/models/llama-v3p3-70b-instruct", NerdIcon: "", ASCIIIcon: "fw"},
	{ID: "cerebras", Name: "Cerebras", BaseURL: "https://api.cerebras.ai/v1", EnvVars: []string{"CEREBRAS_API_KEY"}, DefaultModel: "llama-3.3-70b", NerdIcon: "", ASCIIIcon: "cb"},
	{ID: "nvidia", Name: "NVIDIA NIM", BaseURL: "https://integrate.api.nvidia.com/v1", EnvVars: []string{"NVIDIA_API_KEY"}, DefaultModel: "meta/llama-3.3-70b-instruct", NerdIcon: "", ASCIIIcon: "nv"},
	{ID: "ollama", Name: "Ollama", BaseURL: "http://localhost:11434/v1", BaseEnv: "OLLAMA_HOST", KeyOptional: true, DefaultModel: "qwen2.5-coder:7b", NerdIcon: "", ASCIIIcon: "ol"},
	{ID: "lmstudio", Name: "LM Studio", BaseURL: "http://localhost:1234/v1", BaseEnv: "LM_STUDIO_URL", EnvVars: []string{"LM_API_TOKEN"}, KeyOptional: true, NerdIcon: "", ASCIIIcon: "lm"},
	{ID: "llmgateway", Name: "LLM Gateway", BaseURL: "https://api.llmgateway.io/v1", EnvVars: []string{"LLM_GATEWAY_API_KEY"}, DefaultModel: "openai/gpt-4.1", NerdIcon: "", ASCIIIcon: "lg"},
	{ID: "zai", Name: "Z.AI", BaseURL: "https://api.z.ai/api/paas/v4", EnvVars: []string{"ZAI_API_KEY"}, DefaultModel: "glm-4.6", NerdIcon: "", ASCIIIcon: "zi"},
	{ID: "moonshot", Name: "Moonshot", BaseURL: "https://api.moonshot.ai/v1", EnvVars: []string{"MOONSHOT_API_KEY"}, DefaultModel: "kimi-k2-0905-preview", NerdIcon: "", ASCIIIcon: "mo"},
	{ID: "minimax", Name: "MiniMax", BaseURL: "https://api.minimax.io/v1", EnvVars: []string{"MINIMAX_API_KEY"}, DefaultModel: "MiniMax-M2", NerdIcon: "", ASCIIIcon: "mm"},
}

type cachedModels struct {
	at     time.Time
	models []llm.ModelInfo
}

// Registry holds every provider Scribe can call. It is safe for concurrent use.
type Registry struct {
	mu       sync.Mutex
	defs     []Definition
	byID     map[string]int
	keysPath string
	client   *http.Client
	now      func() time.Time
	cache    map[string]cachedModels
}

// New builds the registry from the built-in table plus cfg.Providers. Custom
// ids that collide with a built-in or an earlier custom id are renamed to
// <id>-custom, then <id>-custom-2, and so on; built-ins are never replaced.
// Keys live in config.Home()/keys.json.
func New(cfg config.Config) *Registry {
	r := &Registry{
		byID:     map[string]int{},
		keysPath: keysFile(),
		client:   &http.Client{},
		now:      time.Now,
		cache:    map[string]cachedModels{},
	}
	for _, d := range builtins {
		r.add(d)
	}
	for _, p := range cfg.Providers {
		if p.ID == "" || p.BaseURL == "" {
			continue
		}
		id := p.ID
		if _, taken := r.byID[id]; taken {
			id = p.ID + "-custom"
			for n := 2; ; n++ {
				if _, taken := r.byID[id]; !taken {
					break
				}
				id = fmt.Sprintf("%s-custom-%d", p.ID, n)
			}
		}
		name := p.Name
		if name == "" {
			name = id
		}
		r.add(Definition{
			ID:          id,
			Name:        name,
			BaseURL:     p.BaseURL,
			EnvVars:     nonEmpty(p.APIKeyEnv),
			KeyOptional: p.APIKeyEnv == "",
			Headers:     p.Headers,
			Models:      p.Models,
			ASCIIIcon:   "*",
			Custom:      true,
		})
	}
	return r
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

func (r *Registry) add(d Definition) {
	r.byID[d.ID] = len(r.defs)
	r.defs = append(r.defs, d)
}

// List returns every provider in display order: built-ins, then customs.
func (r *Registry) List() []ProviderInfo {
	out := make([]ProviderInfo, 0, len(r.defs))
	for _, d := range r.defs {
		out = append(out, r.info(d))
	}
	return out
}

// Lookup returns one provider by id.
func (r *Registry) Lookup(id string) (ProviderInfo, bool) {
	i, ok := r.byID[id]
	if !ok {
		return ProviderInfo{}, false
	}
	return r.info(r.defs[i]), true
}

func (r *Registry) info(d Definition) ProviderInfo {
	pi := ProviderInfo{
		ID:           d.ID,
		Name:         d.Name,
		BaseURL:      r.baseURL(d),
		DefaultModel: d.DefaultModel,
		NerdIcon:     d.NerdIcon,
		ASCIIIcon:    d.ASCIIIcon,
		EnvVars:      d.EnvVars,
		Custom:       d.Custom,
		NeedsKey:     !d.KeyOptional,
	}
	if key, src, err := r.key(d); err == nil && key != "" {
		pi.HasKey = true
		pi.KeySource = src
	}
	return pi
}

func (r *Registry) baseURL(d Definition) string {
	if d.BaseEnv != "" {
		if v := strings.TrimSpace(os.Getenv(d.BaseEnv)); v != "" {
			return localBase(v)
		}
	}
	return d.BaseURL
}

// localBase turns a host setting such as "127.0.0.1:11434" or
// "http://host:1234" into an OpenAI-compatible root ending in /v1.
func localBase(v string) string {
	if !strings.Contains(v, "://") {
		v = "http://" + v
	}
	v = strings.TrimRight(v, "/")
	v = strings.Replace(v, "://0.0.0.0", "://127.0.0.1", 1)
	if !strings.HasSuffix(v, "/v1") {
		v += "/v1"
	}
	return v
}

// key resolves a provider's key: the environment first, then the key store.
func (r *Registry) key(d Definition) (key, source string, err error) {
	for _, e := range d.EnvVars {
		if v := strings.TrimSpace(os.Getenv(e)); v != "" {
			return v, "env:" + e, nil
		}
	}
	keys, err := readKeys(r.keysPath)
	if err != nil {
		return "", "", err
	}
	if v := keys[d.ID]; v != "" {
		return v, "store", nil
	}
	return "", "", nil
}

// Provider returns a ready llm.Provider for id. It fails when the provider is
// unknown or needs a key that does not resolve.
func (r *Registry) Provider(id string) (llm.Provider, error) {
	i, ok := r.byID[id]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", id)
	}
	d := r.defs[i]
	key, _, err := r.key(d)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	if key == "" && !d.KeyOptional {
		if len(d.EnvVars) > 0 {
			return nil, fmt.Errorf("%s: no API key; set %s or store one with SetKey", id, strings.Join(d.EnvVars, " or "))
		}
		return nil, fmt.Errorf("%s: no API key stored; use SetKey", id)
	}
	base := r.baseURL(d)
	if d.Anthropic {
		return anthropic.New(anthropic.Options{
			ID:           id,
			APIKey:       key,
			BaseURL:      base,
			HTTPClient:   r.client,
			ExtraHeaders: d.Headers,
		}), nil
	}
	return openai.New(openai.Options{
		ID:         id,
		BaseURL:    base,
		APIKey:     key,
		HTTPClient: r.client,
		Headers:    d.Headers,
		Models:     d.Models,
	}), nil
}

// Resolve splits "provider/model" and returns the provider and model id. The
// model part may contain further slashes (gateways do).
func (r *Registry) Resolve(ref string) (llm.Provider, string, error) {
	if ref == "" || ref == "none" {
		return nil, "", fmt.Errorf("no model selected; use provider/model, e.g. anthropic/claude-sonnet-5-5")
	}
	pid, model := llm.SplitModelRef(ref)
	if pid == "" {
		return nil, "", fmt.Errorf("model %q has no provider prefix; use provider/model", ref)
	}
	if model == "" || model == "none" {
		return nil, "", fmt.Errorf("no model selected for provider %q", pid)
	}
	p, err := r.Provider(pid)
	if err != nil {
		return nil, "", err
	}
	return p, model, nil
}

// SetKey stores a key for a provider. An environment variable of the same
// provider still takes precedence when it is set.
func (r *Registry) SetKey(id, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("empty key for %s; use RemoveKey to delete one", id)
	}
	if _, ok := r.byID[id]; !ok {
		return fmt.Errorf("unknown provider %q", id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	keys, err := readKeys(r.keysPath)
	if err != nil {
		return err
	}
	keys[id] = key
	return writeKeys(r.keysPath, keys)
}

// RemoveKey deletes the stored key for a provider. Environment variables are
// not touched. Removing a key that is not stored is not an error.
func (r *Registry) RemoveKey(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	keys, err := readKeys(r.keysPath)
	if err != nil {
		return err
	}
	if _, ok := keys[id]; !ok {
		return nil
	}
	delete(keys, id)
	return writeKeys(r.keysPath, keys)
}

// ListModels returns the provider's models, cached in memory for ten minutes.
// Errors are not cached. When the provider returns a partial list with an
// error, that list is returned with the error.
func (r *Registry) ListModels(ctx context.Context, id string) ([]llm.ModelInfo, error) {
	r.mu.Lock()
	if c, ok := r.cache[id]; ok && r.now().Sub(c.at) < modelCacheTTL {
		r.mu.Unlock()
		return append([]llm.ModelInfo(nil), c.models...), nil
	}
	r.mu.Unlock()

	p, err := r.Provider(id)
	if err != nil {
		return nil, err
	}
	models, err := p.Models(ctx)
	for i := range models {
		models[i] = r.enrich(id, models[i])
	}
	if err != nil {
		return models, err
	}
	r.mu.Lock()
	r.cache[id] = cachedModels{at: r.now(), models: models}
	r.mu.Unlock()
	return append([]llm.ModelInfo(nil), models...), nil
}

// enrich fills in pricing and context window from the static table where the
// provider did not report them.
func (r *Registry) enrich(provider string, m llm.ModelInfo) llm.ModelInfo {
	known := lookupModel(provider, m.ID)
	if m.ContextWindow == 0 {
		m.ContextWindow = known.ContextWindow
	}
	if m.InputPerMTok == 0 && m.OutputPerMTok == 0 {
		m.InputPerMTok, m.OutputPerMTok = known.InputPerMTok, known.OutputPerMTok
		m.CacheReadPerMTok, m.CacheWritePerMTok = known.CacheReadPerMTok, known.CacheWritePerMTok
	}
	if m.Name == "" {
		m.Name = m.ID
	}
	return m
}

// ModelInfo describes "provider/model" from the pricing table. Unknown models
// have zero prices and a 128000-token context window.
func (r *Registry) ModelInfo(ref string) llm.ModelInfo {
	pid, model := llm.SplitModelRef(ref)
	return lookupModel(pid, model)
}
