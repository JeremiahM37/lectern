package registry

import (
	"path"
	"strings"
)

// directFamily maps a provider id whose own API is one model family.
var directFamily = map[string]string{
	"anthropic": "claude",
	"openai":    "openai",
	"gemini":    "google",
	"xai":       "xai",
	"deepseek":  "deepseek",
	"moonshot":  "moonshot",
}

// Family derives a model family from the provider and model id. The provider
// id decides first, for providers that serve one family. A gateway falls
// through to the model id's prefix: claude*, gpt-*, o1, o3 and o4 (openai),
// gemini* (google), grok* (xai), deepseek* (deepseek), kimi* (kimi). The
// model is matched on its last path segment, so "vendor/claude-x" counts.
// It returns "" when neither rule applies.
func Family(provider, model string) string {
	if f, ok := directFamily[provider]; ok {
		return f
	}
	m := strings.ToLower(path.Base(model))
	switch {
	case strings.HasPrefix(m, "claude"):
		return "claude"
	case strings.HasPrefix(m, "gpt-"), m == "o1", m == "o3", strings.HasPrefix(m, "o3-"), strings.HasPrefix(m, "o4"):
		return "openai"
	case strings.HasPrefix(m, "gemini"):
		return "google"
	case strings.HasPrefix(m, "grok"):
		return "xai"
	case strings.HasPrefix(m, "deepseek"):
		return "deepseek"
	case strings.HasPrefix(m, "kimi"):
		return "kimi"
	case strings.HasPrefix(m, "moonshot"):
		return "moonshot"
	}
	return ""
}
