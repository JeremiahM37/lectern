package registry

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/codex"
)

// groupedProviders are gateways whose model list spans many vendors. Their
// listed ids are matched against the requested id by suffix.
var groupedProviders = map[string]bool{
	"llmgateway":     true,
	"vercel_gateway": true,
}

// SetKeyPriority chooses which source wins when both the environment and the
// key store hold a key: "env" (the default) or "app". Any other value is
// treated as "env".
func (r *Registry) SetKeyPriority(p string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p == "app" {
		r.keyPriority = "app"
		return
	}
	r.keyPriority = "env"
}

// SetContextOverrides replaces the user context windows, keyed by
// "provider/model". They win over every other source.
func (r *Registry) SetContextOverrides(m map[string]int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.contextOverrides = make(map[string]int, len(m))
	for k, v := range m {
		r.contextOverrides[k] = v
	}
}

// InvalidateModels drops the cached model list of one provider, so the next
// ListModels asks the provider again.
func (r *Registry) InvalidateModels(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cache, id)
}

// Prewarm lists the models of every provider that can be used now (keyed
// providers with a key, and keyless ones), in parallel. Errors are ignored:
// a failed listing just leaves the provider uncached. It returns when every
// listing has finished or ctx is done, so call it from a goroutine at boot.
func (r *Registry) Prewarm(ctx context.Context) {
	var wg sync.WaitGroup
	for _, pi := range r.List() {
		if pi.NeedsKey && !pi.HasKey {
			continue
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, _ = r.ListModels(ctx, id)
		}(pi.ID)
	}
	wg.Wait()
}

// ModelInfoAsync is ModelInfo after a metadata fetch for the provider, with a
// five-second limit. A provider that does not answer in time falls back to
// the table without an error.
func (r *Registry) ModelInfoAsync(ctx context.Context, ref string) llm.ModelInfo {
	pid, _ := llm.SplitModelRef(ref)
	if _, ok := r.byID[pid]; ok {
		c, cancel := context.WithTimeout(ctx, metadataTimeout)
		defer cancel()
		_, _ = r.ListModels(c, pid)
	}
	return r.ModelInfo(ref)
}

// cachedInfo answers from the last model listing of provider when it has the
// model. For a grouped gateway the listed id may be a suffix of the requested
// one ("openai/gpt-4.1" lists as "gpt-4.1"), and the longest such id wins.
func (r *Registry) cachedInfo(provider, model string) (llm.ModelInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.cache[provider]
	if !ok {
		return llm.ModelInfo{}, false
	}
	for _, m := range c.models {
		if m.ID == model {
			return m, true
		}
	}
	if !groupedProviders[provider] {
		return llm.ModelInfo{}, false
	}
	best := -1
	for i, m := range c.models {
		if m.ID == "" || !strings.HasSuffix(model, m.ID) {
			continue
		}
		// Match on a segment boundary, so "gpt-4o" does not match "xgpt-4o".
		if cut := len(model) - len(m.ID); cut > 0 && model[cut-1] != '/' {
			continue
		}
		if best < 0 || len(m.ID) > len(c.models[best].ID) {
			best = i
		}
	}
	if best < 0 {
		return llm.ModelInfo{}, false
	}
	return c.models[best], true
}

// Available reports whether a provider can be used now. A custom provider
// with no key variable is available when its base URL answers (2xx, 401 or
// 403) within two seconds. A keyless built-in is always available. Anything
// else needs a key that resolves.
func (r *Registry) Available(ctx context.Context, id string) bool {
	i, ok := r.byID[id]
	if !ok {
		return false
	}
	d := r.defs[i]
	if d.Custom && len(d.EnvVars) == 0 {
		return r.probe(ctx, r.baseURL(d))
	}
	if d.ID == codex.ProviderID {
		return codex.Available(ctx, os.Getenv(codexBinEnv))
	}
	if d.KeyOptional {
		return true
	}
	key, _, err := r.key(d)
	return err == nil && key != ""
}

// probe is GET base with a two-second limit. The server counts as up when it
// answers 2xx, 401 or 403.
func (r *Registry) probe(ctx context.Context, base string) bool {
	ctx, cancel := context.WithTimeout(ctx, customModelsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
	if err != nil {
		return false
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode/100 == 2, resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return true
	}
	return false
}
