package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// openWithMemoryEmbedding opens a session whose config names memoryModel
// (empty leaves the field unset). The fake Ollama server counts embed calls,
// so a test can prove nothing was sent when the field is unset.
func openWithMemoryEmbedding(t *testing.T, memoryModel string) (*App, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only the embedding endpoint counts: the provider's model-list
		// prewarm also reaches this server.
		if r.URL.Path == "/api/embed" {
			calls.Add(1)
		}
		var in struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		vecs := make([][]float32, len(in.Input))
		for i := range vecs {
			vecs[i] = []float32{1, 0, 0}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OLLAMA_HOST", srv.URL)

	home := t.TempDir()
	t.Setenv("SCRIBE_HOME", home)
	cfg := map[string]any{
		"model": "fake/test-model",
		"providers": []any{map[string]any{
			"id": "fake", "name": "Fake", "base_url": srv.URL + "/v1", "models": []string{"test-model"},
		}},
	}
	if memoryModel != "" {
		cfg["memory_embedding_model"] = memoryModel
	}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(home, "config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := Open(context.Background(), Options{Workdir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if a.Memory != nil {
			a.Memory.Close()
		}
	})
	return a, &calls
}

func TestMemoryEmbeddingUnsetStaysOnHashWithoutNetwork(t *testing.T) {
	a, calls := openWithMemoryEmbedding(t, "")
	if a.Memory == nil {
		t.Fatal("memory should be open")
	}
	if got := a.Memory.Project; got == nil {
		t.Fatal("project store should be open")
	}
	if calls.Load() != 0 {
		t.Fatalf("no embedding call expected with the field unset, got %d", calls.Load())
	}
}

func TestMemoryEmbeddingSetActivatesProvider(t *testing.T) {
	a, calls := openWithMemoryEmbedding(t, "ollama/nomic-embed-text")
	if a.Memory == nil {
		t.Fatal("memory should be open")
	}
	if calls.Load() == 0 {
		t.Fatal("the smoke test should have called the provider")
	}
	if strings.Contains(a.ConfigWarning, "memory embeddings off") {
		t.Fatalf("a reachable provider should not warn, got %q", a.ConfigWarning)
	}
}

func TestMemoryEmbeddingBadProviderWarnsAndKeepsHash(t *testing.T) {
	a, _ := openWithMemoryEmbedding(t, "nosuch/some-model")
	if a.Memory == nil {
		t.Fatal("a bad embedding setting must not stop memory")
	}
	if !strings.Contains(a.ConfigWarning, "memory embeddings off") {
		t.Fatalf("the failure should be reported once at start, got %q", a.ConfigWarning)
	}
}
