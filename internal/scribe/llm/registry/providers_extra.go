package registry

import (
	"os"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/codex"
)

// Providers added after the first table. They live in their own file so the
// built-in table in registry.go stays as it was: init appends them to
// builtins, which New copies into the registry like any other entry. Every
// one of them speaks the OpenAI-compatible Chat Completions wire format, so
// they reuse the openai adapter unchanged.
//
// Copilot exchanges its key for a bearer in its own package; Codex runs the
// codex CLI, which holds its own sign-in.

const (
	githubModelsBase  = "https://models.github.ai/inference"
	vercelGatewayBase = "https://ai-gateway.vercel.sh/v1"
	// opencodeZenBase is not given in the behavioural spec (only the env
	// var is). It is the OpenCode Zen OpenAI-compatible root as published by
	// OpenCode; verify before relying on it.
	opencodeZenBase      = "https://opencode.ai/zen/v1"
	proxyDefaultBase     = "http://127.0.0.1:8317/v1"
	copilotBaseURL       = "https://api.githubcopilot.com"
	bedrockRegionDefault = "us-east-1"
)

func init() {
	builtins = append(builtins,
		// Bedrock first (highest user demand), then GitHub Models.
		Definition{
			ID:        "bedrock",
			Name:      "AWS Bedrock",
			BaseURL:   bedrockBaseURL(awsRegionFromEnv()),
			EnvVars:   []string{"AWS_BEARER_TOKEN_BEDROCK"},
			NerdIcon:  "",
			ASCIIIcon: "br",
		},
		Definition{
			ID:        "github-models",
			Name:      "GitHub Models",
			BaseURL:   githubModelsBase,
			EnvVars:   []string{"GITHUB_TOKEN", "GITHUB_MODELS_API_KEY"},
			ASCIIIcon: "gh",
		},
		Definition{
			ID:        "vercel_gateway",
			Name:      "Vercel AI Gateway",
			BaseURL:   vercelGatewayBase,
			EnvVars:   []string{"AI_GATEWAY_API_KEY"},
			ASCIIIcon: "vc",
		},
		Definition{
			ID:        "opencode-zen",
			Name:      "OpenCode Zen",
			BaseURL:   opencodeZenBase,
			EnvVars:   []string{"OPENCODE_ZEN_API_KEY"},
			ASCIIIcon: "oz",
		},
		// GitHub Copilot: the editor's OAuth token is exchanged for a bearer
		// inside the copilot package, so the key is a plain store/env value.
		Definition{
			ID:        "copilot",
			Name:      "GitHub Copilot",
			BaseURL:   copilotBaseURL,
			EnvVars:   []string{"COPILOT_API_KEY"},
			ASCIIIcon: "cp",
		},
		// Codex: models from a ChatGPT subscription through the official codex
		// CLI, which holds the sign-in itself (/codex login). No key.
		Definition{
			ID:           codex.ProviderID,
			Name:         "Codex (ChatGPT login)",
			KeyOptional:  true,
			DefaultModel: "gpt-5.6-sol",
			ASCIIIcon:    "cx",
		},
		// The CLIProxyAPI sidecar. Its key is optional, and the base comes
		// from SCRIBE_PROXY_URL (a bare host:port gets /v1 appended, as for
		// OLLAMA_HOST).
		Definition{
			ID:          "proxy",
			Name:        "Proxy (CLIProxyAPI)",
			BaseURL:     proxyDefaultBase,
			BaseEnv:     "SCRIBE_PROXY_URL",
			EnvVars:     []string{"PROXY_API_KEY"},
			KeyOptional: true,
			ASCIIIcon:   "px",
		},
	)
}

// awsRegionFromEnv returns AWS_REGION, or us-east-1 when it is unset.
func awsRegionFromEnv() string {
	if r := strings.TrimSpace(os.Getenv("AWS_REGION")); r != "" {
		return r
	}
	return bedrockRegionDefault
}

// bedrockBaseURL is the Bedrock runtime's OpenAI-compatible root for a region.
func bedrockBaseURL(region string) string {
	if region == "" {
		region = bedrockRegionDefault
	}
	return "https://bedrock-runtime." + region + ".amazonaws.com/openai/v1"
}
