package registry

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

func TestInferEmbeddingModelFromDirectProviders(t *testing.T) {
	cases := map[string]string{
		"openai/gpt-4.1":               "openai/text-embedding-3-small",
		"gemini/gemini-2.5-flash":      "google/text-embedding-004",
		"anthropic/claude-sonnet-5-5":  "",
		"xai/grok-4":                   "",
		"groq/llama-3.3-70b-versatile": "",
		"ollama/qwen2.5-coder:7b":      "",
		"none":                         "",
		"":                             "",
	}
	for chat, want := range cases {
		if got := InferEmbeddingModel(chat); got != want {
			t.Errorf("InferEmbeddingModel(%q) = %q, want %q", chat, got, want)
		}
	}
}

func TestInferEmbeddingModelFromGatewayVendor(t *testing.T) {
	cases := map[string]string{
		"llmgateway/openai/gpt-4.1":        "openai/text-embedding-3-small",
		"openrouter/google/gemini-2.5-pro": "google/text-embedding-004",
		"vercel_gateway/openai/gpt-5":      "openai/text-embedding-3-small",
		"openrouter/anthropic/claude":      "",
		"openrouter/meta-llama/llama-3":    "",
	}
	for chat, want := range cases {
		if got := InferEmbeddingModel(chat); got != want {
			t.Errorf("InferEmbeddingModel(%q) = %q, want %q", chat, got, want)
		}
	}
}

// The spec names Gemini "google"; the registry id is "gemini". Both must
// resolve to the same key and base, so google/text-embedding-004 works.
func TestEmbeddingCredentialsAcceptsGoogleAlias(t *testing.T) {
	r := embedTestRegistry(t)
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_GENERATIVE_AI_API_KEY", "")
	if err := r.SetKey("gemini", "gm-test-embed-key"); err != nil {
		t.Fatal(err)
	}
	baseG, keyG, err := r.EmbeddingCredentials("gemini")
	if err != nil {
		t.Fatalf("gemini: %v", err)
	}
	baseGoogle, keyGoogle, err := r.EmbeddingCredentials("google")
	if err != nil {
		t.Fatalf("google alias: %v", err)
	}
	if baseG != baseGoogle || keyG != keyGoogle {
		t.Fatalf("google alias differs from gemini: %q/%q vs %q/%q", baseGoogle, keyGoogle, baseG, keyG)
	}
	if keyGoogle != "gm-test-embed-key" {
		t.Fatal("alias did not resolve the stored gemini key")
	}
}

func TestGroupByVendorKeepsOrderWithinVendor(t *testing.T) {
	in := []llm.ModelInfo{
		{ID: "openai/gpt-5"},
		{ID: "anthropic/claude-a"},
		{ID: "openai/gpt-4.1"},
		{ID: "anthropic/claude-b"},
		{ID: "bare"},
	}
	got := groupByVendor(in)
	want := []string{"anthropic/claude-a", "anthropic/claude-b", "bare", "openai/gpt-5", "openai/gpt-4.1"}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("position %d = %q, want %q (all: %v)", i, got[i].ID, id, got)
		}
	}
}
