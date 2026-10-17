package registry

// providerText is the user-facing text for each built-in provider, keyed by
// provider id. The setup screens show it next to the provider name.
//
// KeyURL is set only where the provider's own key page is known exactly. The
// behavioural spec names no key pages, so these come from the provider docs;
// a guessed URL sends a user to the wrong place. Leave a provider empty until
// its page is confirmed. The TUI shows the link only when it is set.
//
// Descriptions follow the spec's provider table (spec 03, section 4.2) and
// say what the provider is, not how it is priced. Providers the spec table
// does not list get a plain description of their service.
var providerText = map[string]struct{ KeyURL, Description string }{
	"anthropic":      {KeyURL: "https://console.anthropic.com/settings/keys", Description: "Claude models through the Anthropic API."},
	"openai":         {KeyURL: "https://platform.openai.com/api-keys", Description: "GPT and o-series models through the OpenAI API."},
	"xai":            {Description: "Grok models from xAI, with reasoning."},
	"gemini":         {KeyURL: "https://aistudio.google.com/apikey", Description: "Gemini models through Google's Generative AI API."},
	"groq":           {KeyURL: "https://console.groq.com/keys", Description: "Fast hosted Llama, Qwen and GPT-OSS models."},
	"deepseek":       {KeyURL: "https://platform.deepseek.com/api_keys", Description: "DeepSeek chat and reasoning models."},
	"mistral":        {Description: "Mistral's hosted models."},
	"fireworks":      {Description: "Hosted open-weight models with tiered per-token pricing."},
	"together":       {Description: "Open-weight models hosted by Together AI."},
	"cerebras":       {Description: "Llama models on Cerebras inference hardware."},
	"nvidia":         {Description: "NVIDIA NIM hosted models through an OpenAI-compatible API."},
	"zai":            {Description: "GLM models from Z.AI."},
	"moonshot":       {Description: "Kimi models from Moonshot AI."},
	"minimax":        {Description: "MiniMax models through the MiniMax API."},
	"bedrock":        {Description: "Claude and other models through AWS Bedrock, using your AWS account."},
	"llmgateway":     {Description: "Models from many vendors behind one key, through LLM Gateway."},
	"openrouter":     {KeyURL: "https://openrouter.ai/keys", Description: "Models from many vendors behind one key, with live pricing and context sizes."},
	"vercel_gateway": {Description: "Models from many vendors through the Vercel AI Gateway."},
	"github-models":  {Description: "Models through GitHub Models, billed by per-token multipliers."},
	"copilot":        {Description: "Your GitHub Copilot subscription, through its OpenAI-compatible endpoint. Unofficial."},
	"opencode-zen":   {Description: "Curated coding models through OpenCode Zen."},
	"ollama":         {Description: "Models running on this machine through Ollama. No cost."},
	"lmstudio":       {Description: "Models running on this machine through LM Studio. No cost."},
	"proxy":          {Description: "Models through a local CLIProxyAPI sidecar."},
}
