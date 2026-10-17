package memory

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseEmbeddingModel(t *testing.T) {
	p, m, err := ParseEmbeddingModel("ollama/nomic-embed-text")
	if err != nil || p != "ollama" || m != "nomic-embed-text" {
		t.Fatalf("got %q %q %v", p, m, err)
	}
	// Only the first slash splits, so a model path survives.
	p, m, err = ParseEmbeddingModel("ollama/hf.co/org/model")
	if err != nil || p != "ollama" || m != "hf.co/org/model" {
		t.Fatalf("slashed model: got %q %q %v", p, m, err)
	}
	for _, bad := range []string{"nomic", "/x", "ollama/", " "} {
		if _, _, err := ParseEmbeddingModel(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func TestNewEmbedderSelection(t *testing.T) {
	if e, err := NewEmbedder("", "", ""); e != nil || err != nil {
		t.Fatalf("empty id should mean hash (nil, nil), got %v, %v", e, err)
	}
	e, err := NewEmbedder("ollama/nomic-embed-text", "", "")
	if err != nil || e.Model() != "ollama/nomic-embed-text" {
		t.Fatalf("ollama: %v %v", e, err)
	}
	e, err = NewEmbedder("ollama/nomic-embed-text", "http://host:11434/v1", "")
	if oe, ok := e.(*OllamaEmbedder); !ok || err != nil || oe.root != "http://host:11434" {
		t.Fatalf("ollama with registry /v1 base: root not normalised: %+v %v", e, err)
	}
	e, err = NewEmbedder("openai/text-embedding-3-small", "https://api.openai.com", "k")
	if err != nil || e.Model() != "openai/text-embedding-3-small" {
		t.Fatalf("openai: %v %v", e, err)
	}
	if _, err := NewEmbedder("openai/text-embedding-3-small", "", "k"); err == nil {
		t.Error("openai without a base URL should fail")
	}
	if _, err := NewEmbedder("nomodel", "http://x", ""); err == nil {
		t.Error("malformed id should fail")
	}
}

func TestOllamaRoot(t *testing.T) {
	cases := map[string]string{
		"":                     "http://127.0.0.1:11434",
		"127.0.0.1:11434":      "http://127.0.0.1:11434",
		"http://host:11434/":   "http://host:11434",
		"http://host:11434/v1": "http://host:11434",
		"http://0.0.0.0:11434": "http://127.0.0.1:11434",
	}
	for in, want := range cases {
		if got := OllamaRoot(in); got != want {
			t.Errorf("OllamaRoot(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEmbeddingsURL(t *testing.T) {
	cases := map[string]string{
		"https://api.openai.com":           "https://api.openai.com/v1/embeddings",
		"https://gw.example/v1":            "https://gw.example/v1/embeddings",
		"https://gw.example/v1/":           "https://gw.example/v1/embeddings",
		"https://gw.example/v1/embeddings": "https://gw.example/v1/embeddings",
	}
	for in, want := range cases {
		if got := embeddingsURL(in); got != want {
			t.Errorf("embeddingsURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOllamaEmbedderCallsAPIEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" || r.Method != http.MethodPost {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body.Model != "nomic-embed-text" || len(body.Input) != 2 {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		// Unnormalised on purpose: the client must normalise.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"embeddings": [][]float32{{3, 4, 0}, {0, 0, 2}},
		})
	}))
	defer srv.Close()

	e := NewOllamaEmbedder("nomic-embed-text", srv.URL+"/", nil)
	vecs, err := e.EmbedBatch(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(vecs) != 2 || math.Abs(float64(vecs[0][0])-0.6) > 1e-6 || math.Abs(float64(vecs[0][1])-0.8) > 1e-6 {
		t.Fatalf("vectors not normalised: %v", vecs)
	}
	if e.Model() != "ollama/nomic-embed-text" {
		t.Errorf("Model() = %q", e.Model())
	}
	one, err := e.Embed(context.Background(), "a")
	if err == nil && len(one) == 3 {
		// The fixed server answers two vectors for one input, which must
		// be rejected rather than silently taking the first.
		t.Fatalf("mismatched batch size should be an error")
	}
}

func TestOpenAIEmbedderOrdersByIndexAndSendsKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		// Items come back out of order; the client must sort them by index.
		_, _ = w.Write([]byte(`{"data":[
			{"index":1,"embedding":[0,1]},
			{"index":0,"embedding":[1,0]}]}`))
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder("openai", "text-embedding-3-small", srv.URL, "sk-test", nil)
	vecs, err := e.EmbedBatch(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if vecs[0][0] != 1 || vecs[1][1] != 1 {
		t.Fatalf("order lost: %v", vecs)
	}
	if e.Model() != "openai/text-embedding-3-small" {
		t.Errorf("Model() = %q", e.Model())
	}
}

func TestProviderErrorNeverCarriesTheKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A gateway that echoes the header back must not leak it into errors
		// that reach the log or the agent.
		http.Error(w, "bad key "+r.Header.Get("Authorization"), http.StatusUnauthorized)
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder("openai", "m", srv.URL, "sk-secret-123", nil)
	_, err := e.Embed(context.Background(), "x")
	if err == nil {
		t.Fatal("want error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should name the status: %v", err)
	}
	// The body echo is in the error, so the key itself must not be.
	if strings.Contains(err.Error(), "sk-secret-123") {
		t.Errorf("key leaked into error: %v", err)
	}
}
