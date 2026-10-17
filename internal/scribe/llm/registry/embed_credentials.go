package registry

import (
	"fmt"
	"strings"
)

// EmbeddingCredentials returns what the memory embedder needs to reach the
// provider id: its base URL and key. For Ollama the base is the server root
// (OLLAMA_HOST without the /v1 the chat base carries), because Ollama's
// embedding API lives at the root. Other providers return their API base as
// the chat path uses it; the embedder accepts a root or a /v1 base.
//
// The key is returned only to the caller that builds the embedder. Errors
// never include it, and the registry does not log it.
func (r *Registry) EmbeddingCredentials(id string) (baseURL, key string, err error) {
	i, ok := r.byID[id]
	if !ok {
		return "", "", fmt.Errorf("unknown provider %q for memory embeddings", id)
	}
	d := r.defs[i]
	if d.Anthropic {
		return "", "", fmt.Errorf("%s has no embeddings endpoint; pick another provider for memory.embedding_model", id)
	}
	key, _, err = r.key(d)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", id, err)
	}
	if key == "" && !d.KeyOptional {
		return "", "", fmt.Errorf("%s: no API key for memory embeddings; set %s or store one with: lectern code --set-key %s KEY", id, strings.Join(d.EnvVars, " or "), id)
	}
	baseURL = r.baseURL(d)
	if d.ID == "ollama" {
		baseURL = strings.TrimSuffix(baseURL, "/v1")
	}
	return baseURL, key, nil
}
