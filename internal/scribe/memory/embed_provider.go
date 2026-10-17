package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// HashModel is the tag stored with the local hashed embedding. Rows written
// before embedding_model existed are migrated to this tag, so a row's tag
// always says which embedder produced its vector.
const HashModel = "hashbag-512"

// Embedder turns text into vectors from one provider model. Recall compares
// a query only with rows tagged with the same Model(), so two models' vectors
// are never mixed.
type Embedder interface {
	// Model is the "provider/model" id stored with each vector.
	Model() string
	// Embed returns one L2-normalised vector for text.
	Embed(ctx context.Context, text string) ([]float32, error)
	// EmbedBatch returns one L2-normalised vector per text, in input order.
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
}

const (
	// providerTimeout bounds one HTTP call to an embedding server.
	providerTimeout = 60 * time.Second
	// maxEmbedResponse bounds how much of a response is read. A batch of
	// embeddings is well under this; anything larger is a broken server.
	maxEmbedResponse = 64 << 20
	// defaultOllamaRoot is where Ollama listens when OLLAMA_HOST is unset.
	defaultOllamaRoot = "http://127.0.0.1:11434"
)

// ParseEmbeddingModel splits a "provider/model" id at the first slash, so
// model names that contain slashes (such as "hf.co/org/model") survive.
func ParseEmbeddingModel(id string) (provider, model string, err error) {
	provider, model, ok := strings.Cut(strings.TrimSpace(id), "/")
	if !ok || provider == "" || model == "" {
		return "", "", fmt.Errorf("embedding model %q must be provider/model, e.g. ollama/nomic-embed-text", id)
	}
	return provider, model, nil
}

// NewEmbedder builds the embedder named by id. An empty id means no provider,
// and the local hash embedding is used (nil, nil). The caller supplies the
// base URL and key, so this package never reads key files. For "ollama",
// baseURL is the server root and "" means the default. Any other provider is
// treated as OpenAI-compatible and needs baseURL.
func NewEmbedder(id, baseURL, apiKey string) (Embedder, error) {
	if strings.TrimSpace(id) == "" {
		return nil, nil
	}
	provider, model, err := ParseEmbeddingModel(id)
	if err != nil {
		return nil, err
	}
	if provider == "ollama" {
		// The registry hands out an OpenAI-style /v1 base; Ollama's native
		// API lives at the root, so OllamaRoot strips the suffix.
		return NewOllamaEmbedder(model, OllamaRoot(baseURL), nil), nil
	}
	if baseURL == "" {
		return nil, fmt.Errorf("embedding provider %q needs a base URL", provider)
	}
	return NewOpenAIEmbedder(provider, model, baseURL, apiKey, nil), nil
}

// OllamaRoot turns an OLLAMA_HOST value such as "127.0.0.1:11434" or
// "http://host:11434" into the server root, with no /v1 suffix. Empty means
// the default local server.
func OllamaRoot(host string) string {
	v := strings.TrimSpace(host)
	if v == "" {
		return defaultOllamaRoot
	}
	if !strings.Contains(v, "://") {
		v = "http://" + v
	}
	v = strings.TrimRight(v, "/")
	v = strings.Replace(v, "://0.0.0.0", "://127.0.0.1", 1)
	return strings.TrimSuffix(v, "/v1")
}

// OllamaEmbedder calls Ollama's native /api/embed.
type OllamaEmbedder struct {
	model  string
	root   string
	client *http.Client
}

// NewOllamaEmbedder returns an embedder for model on the Ollama server at
// root. A nil client gets a default with providerTimeout.
func NewOllamaEmbedder(model, root string, client *http.Client) *OllamaEmbedder {
	if root == "" {
		root = defaultOllamaRoot
	}
	return &OllamaEmbedder{model: model, root: strings.TrimRight(root, "/"), client: clientOr(client)}
}

// Model is "ollama/<model>".
func (e *OllamaEmbedder) Model() string { return "ollama/" + e.model }

// Embed embeds one text.
func (e *OllamaEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	return embedOne(ctx, e, text)
}

// EmbedBatch sends texts in one /api/embed request.
func (e *OllamaEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body := map[string]any{"model": e.model, "input": texts}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := postJSON(ctx, e.client, e.root+"/api/embed", "", body, &out); err != nil {
		return nil, err
	}
	return checkBatch(out.Embeddings, len(texts))
}

// OpenAIEmbedder calls an OpenAI-compatible /v1/embeddings endpoint. OpenAI
// itself and most gateways speak this shape.
type OpenAIEmbedder struct {
	provider string
	model    string
	url      string
	apiKey   string
	client   *http.Client
}

// NewOpenAIEmbedder returns an embedder for model behind baseURL. baseURL may
// be the API root ("https://api.openai.com") or already end in /v1. apiKey
// may be empty for a gateway that does not need one.
func NewOpenAIEmbedder(provider, model, baseURL, apiKey string, client *http.Client) *OpenAIEmbedder {
	return &OpenAIEmbedder{
		provider: provider,
		model:    model,
		url:      embeddingsURL(baseURL),
		apiKey:   apiKey,
		client:   clientOr(client),
	}
}

// Model is "<provider>/<model>".
func (e *OpenAIEmbedder) Model() string { return e.provider + "/" + e.model }

// Embed embeds one text.
func (e *OpenAIEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	return embedOne(ctx, e, text)
}

// EmbedBatch sends texts in one /v1/embeddings request. The response carries
// an index per item, and the vectors are put back in input order.
func (e *OpenAIEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body := map[string]any{"model": e.model, "input": texts}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := postJSON(ctx, e.client, e.url, e.apiKey, body, &out); err != nil {
		return nil, err
	}
	vecs := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("%s: embedding index %d out of range", e.Model(), d.Index)
		}
		vecs[d.Index] = d.Embedding
	}
	return checkBatch(vecs, len(texts))
}

// embeddingsURL accepts a root or a root ending in /v1 and returns the
// /embeddings endpoint.
func embeddingsURL(base string) string {
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	switch {
	case strings.HasSuffix(b, "/embeddings"):
		return b
	case strings.HasSuffix(b, "/v1"):
		return b + "/embeddings"
	default:
		return b + "/v1/embeddings"
	}
}

func clientOr(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{Timeout: providerTimeout}
}

// embedOne is the single-text path shared by both providers.
func embedOne(ctx context.Context, e Embedder, text string) ([]float32, error) {
	vecs, err := e.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

// checkBatch confirms the server returned one usable vector per input, all
// the same length, and normalises them.
func checkBatch(vecs [][]float32, want int) ([][]float32, error) {
	if len(vecs) != want {
		return nil, fmt.Errorf("embedding server returned %d vectors for %d inputs", len(vecs), want)
	}
	dim := 0
	for i, v := range vecs {
		if len(v) == 0 {
			return nil, fmt.Errorf("embedding server returned an empty vector for input %d", i)
		}
		if dim == 0 {
			dim = len(v)
		}
		if len(v) != dim {
			return nil, errors.New("embedding server returned vectors of different lengths")
		}
		vecs[i] = normalize(v)
	}
	return vecs, nil
}

// normalize scales v to unit length in place, so cosine is a dot product. A
// zero vector is returned as is.
func normalize(v []float32) []float32 {
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		return v
	}
	inv := float32(1 / math.Sqrt(norm))
	for i := range v {
		v[i] *= inv
	}
	return v
}

// postJSON posts body and decodes the reply into out. The key, when set, goes
// in the Authorization header only; it never appears in an error.
func postJSON(ctx context.Context, client *http.Client, url, apiKey string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("embedding request to %s failed: %w", hostOf(url), err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxEmbedResponse))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		// A gateway may echo the request back, so the key is scrubbed from
		// the part of the body that goes into the error.
		msg := snippet(data)
		if apiKey != "" {
			msg = strings.ReplaceAll(msg, apiKey, "[redacted]")
		}
		return fmt.Errorf("embedding request to %s returned %d: %s", hostOf(url), resp.StatusCode, msg)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("embedding response from %s is not valid JSON: %w", hostOf(url), err)
	}
	return nil
}

// hostOf names the server in an error without its path or query.
func hostOf(url string) string {
	_, rest, ok := strings.Cut(url, "://")
	if !ok {
		rest = url
	}
	host, _, _ := strings.Cut(rest, "/")
	return host
}

func snippet(b []byte) string {
	const n = 200
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		s = s[:n] + "..."
	}
	return s
}
