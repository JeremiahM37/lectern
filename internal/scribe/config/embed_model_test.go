package config

import (
	"encoding/json"
	"testing"
)

// The memory embedding model is opt-in: empty by default, read from the flat
// memory_embedding_model key, and overridable by a project config.
func TestMemoryEmbeddingModelDefaultsEmptyAndMerges(t *testing.T) {
	var base Config
	if base.MemoryEmbeddingModel != "" {
		t.Fatalf("default must be empty (hash only), got %q", base.MemoryEmbeddingModel)
	}

	var c Config
	if err := json.Unmarshal([]byte(`{"memory_embedding_model":"ollama/nomic-embed-text"}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.MemoryEmbeddingModel != "ollama/nomic-embed-text" {
		t.Fatalf("json key not read: %q", c.MemoryEmbeddingModel)
	}

	base.MemoryEmbeddingModel = "ollama/nomic-embed-text"
	if got := Merge(base, Config{}).MemoryEmbeddingModel; got != "ollama/nomic-embed-text" {
		t.Fatalf("an empty overlay must keep the base value, got %q", got)
	}
	if got := Merge(base, Config{MemoryEmbeddingModel: "ollama/other"}).MemoryEmbeddingModel; got != "ollama/other" {
		t.Fatalf("an overlay must replace it, got %q", got)
	}
}
