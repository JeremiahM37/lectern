package registry

import (
	"os"
	"strings"
)

// Providers added after the first table. They live in their own file so the
// built-in table in registry.go stays as it was: init appends them to
// builtins, which New copies into the registry like any other entry. Every
// one of them speaks the OpenAI-compatible Chat Completions wire format, so
// they reuse the openai adapter unchanged.
//
// Copilot (token exchange) and Codex (CLI subprocess) are not here: they
// need an auth flow the static-key Provider path cannot express.

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
