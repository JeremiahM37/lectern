package registry

import "github.com/JeremiahM37/lectern/v2/internal/scribe/llm"

// InferEmbeddingModel returns the memory embedding model that goes with the
// chat model chatRef ("provider/model"), or "" when no embedder matches. It is
// the default used when memory.embedding_model is not set, so it only names a
// model; the caller must still resolve credentials for it and stay on the
// local hash when they are missing.
//
// Direct providers map to their own embedder. Gateways map by the vendor in
// the model id (vercel_gateway/openai/gpt-4.1 -> openai), because the vendor's
// embedder is the one whose model family matches the chat model.
func InferEmbeddingModel(chatRef string) string {
	provider, model := llm.SplitModelRef(chatRef)
	switch provider {
	case "openai":
		return openAIEmbedModel
	case "gemini":
		return googleEmbedModel
	case "vercel_gateway", "llmgateway", "openrouter":
		vendor, _ := llm.SplitModelRef(model)
		switch vendor {
		case "openai":
			return openAIEmbedModel
		case "google":
			return googleEmbedModel
		}
	}
	return ""
}

const (
	openAIEmbedModel = "openai/text-embedding-3-small"
	googleEmbedModel = "google/text-embedding-004"
)
