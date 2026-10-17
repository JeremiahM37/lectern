package registry

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// embedTestRegistry is a registry whose key store is a temp file, so the
// tests never read the user's real keys.
func embedTestRegistry(t *testing.T) *Registry {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", "")
	r := New(config.Config{})
	r.keysPath = filepath.Join(t.TempDir(), "keys.json")
	return r
}

func TestEmbeddingCredentialsOllamaRootWithoutV1(t *testing.T) {
	r := embedTestRegistry(t)
	t.Setenv("OLLAMA_HOST", "http://100.127.85.58:11434")
	base, key, err := r.EmbeddingCredentials("ollama")
	if err != nil {
		t.Fatalf("ollama: %v", err)
	}
	if base != "http://100.127.85.58:11434" {
		t.Fatalf("base = %q, want the server root with no /v1", base)
	}
	if key != "" {
		t.Fatalf("ollama is key-optional, got key %q", key)
	}
}

func TestEmbeddingCredentialsOpenAIUsesAPIBaseAndStoredKey(t *testing.T) {
	r := embedTestRegistry(t)
	if err := r.SetKey("openai", "sk-test-embed-key"); err != nil {
		t.Fatal(err)
	}
	base, key, err := r.EmbeddingCredentials("openai")
	if err != nil {
		t.Fatalf("openai: %v", err)
	}
	if base == "" {
		t.Fatal("openai base is empty")
	}
	if key != "sk-test-embed-key" {
		t.Fatalf("key not resolved from the store")
	}
}

func TestEmbeddingCredentialsMissingKeyNeverEchoesIt(t *testing.T) {
	r := embedTestRegistry(t)
	_, _, err := r.EmbeddingCredentials("openai")
	if err == nil {
		t.Fatal("openai without a key should fail")
	}
	if !strings.Contains(err.Error(), "no API key") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEmbeddingCredentialsRejectsUnknownAndAnthropic(t *testing.T) {
	r := embedTestRegistry(t)
	if _, _, err := r.EmbeddingCredentials("nope"); err == nil {
		t.Fatal("unknown provider should fail")
	}
	if _, _, err := r.EmbeddingCredentials("anthropic"); err == nil {
		t.Fatal("anthropic has no embeddings endpoint and should fail")
	}
}
