package registry

import (
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/anthropic"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/openai"
)

// defaultContext is the window for models the table does not know.
const defaultContext = 128000

// defaultPrice is the Sonnet-class tier (USD per million tokens) used for a
// model id the table does not know. The result is marked Estimated.
var defaultPrice = priceEntry{in: 3, out: 15, cacheRd: 0.3, cacheWr: 3.75}

// priceEntry holds USD per million tokens and the context window. Keys are
// matched as a prefix of the model id or of any "/"- or ":"-separated segment,
// and the longest matching key wins.
//
// TODO: the OpenAI, Gemini, DeepSeek, Groq, Qwen and xAI rows are from
// memory, not from the provider price pages. Rows marked "unverified" must be
// checked before cost numbers are trusted. Claude rows come from
// anthropic.KnownModels() first and only fall back here.
type priceEntry struct {
	key                       string
	in, out, cacheRd, cacheWr float64
	ctx                       int
}

var priceTable = []priceEntry{
	// Anthropic fallbacks (prefix match; exact ids come from anthropic.KnownModels).
	{"claude-opus-4-1", 15, 75, 1.5, 18.75, 200000},
	{"claude-opus-4-5", 5, 25, 0.5, 6.25, 200000},
	{"claude-opus", 5, 25, 0.5, 6.25, 1000000},
	{"claude-sonnet-4-6", 3, 15, 0.3, 3.75, 1000000},
	{"claude-sonnet", 3, 15, 0.3, 3.75, 200000},
	{"claude-haiku-4-5", 1, 5, 0.1, 1.25, 200000},
	{"claude-haiku", 1, 5, 0.1, 1.25, 200000},

	// OpenAI (gpt-5.4 unverified).
	{"gpt-5.4", 1.25, 10, 0.125, 0, 400000},
	{"gpt-5-nano", 0.05, 0.4, 0.005, 0, 400000},
	{"gpt-5-mini", 0.25, 2, 0.025, 0, 400000},
	{"gpt-5", 1.25, 10, 0.125, 0, 400000},
	{"gpt-4.1-nano", 0.1, 0.4, 0.025, 0, 1047576},
	{"gpt-4.1-mini", 0.4, 1.6, 0.1, 0, 1047576},
	{"gpt-4.1", 2, 8, 0.5, 0, 1047576},
	{"o4-mini", 1.1, 4.4, 0.275, 0, 200000},
	{"o3-mini", 1.1, 4.4, 0.55, 0, 200000},
	{"o3", 2, 8, 0.5, 0, 200000},
	{"o1-mini", 1.1, 4.4, 0.55, 0, 128000},
	{"o1", 15, 60, 7.5, 0, 200000},

	// DeepSeek (deepseek-flash unverified).
	{"deepseek-chat", 0.27, 1.1, 0.07, 0, 128000},
	{"deepseek-reasoner", 0.55, 2.19, 0.14, 0, 128000},
	{"deepseek-flash", 0.14, 0.28, 0.028, 0, 128000},

	// Gemini (gemini-3 rows unverified).
	{"gemini-2.5-pro", 1.25, 10, 0.31, 0, 1048576},
	{"gemini-2.5-flash-lite", 0.1, 0.4, 0.025, 0, 1048576},
	{"gemini-2.5-flash", 0.3, 2.5, 0.075, 0, 1048576},
	{"gemini-3-pro", 2, 12, 0.2, 0, 1048576},
	{"gemini-3-flash", 0.5, 3, 0.05, 0, 1048576},

	// Groq-hosted Llama (prices as listed for Groq).
	{"llama-3.3-70b", 0.59, 0.79, 0, 0, 131072},
	{"llama-3.1-8b", 0.05, 0.08, 0, 0, 131072},
	{"llama-4-scout", 0.11, 0.34, 0, 0, 131072},

	// Qwen (unverified).
	{"qwen3-coder", 0.22, 1, 0, 0, 262144},
	{"qwen3", 0.2, 0.6, 0, 0, 131072},

	// xAI.
	{"grok-4", 3, 15, 0.75, 0, 256000},
}

// lookupModel returns the table entry for a provider/model pair. Local
// providers are free. A model the table does not know is priced at the
// Sonnet-class default and marked Estimated, except ":free" and "-free"
// variants, which are free.
func lookupModel(provider, model string) llm.ModelInfo {
	info := llm.ModelInfo{ID: model, Name: model, ContextWindow: defaultContext}
	if provider == "ollama" || provider == "lmstudio" {
		return info
	}
	info.ReasoningStyle = reasoningStyle(model)
	if provider == "anthropic" || strings.HasPrefix(model, "claude-") {
		for _, k := range anthropic.KnownModels() {
			if k.ID == model {
				k.Name = info.Name
				return k
			}
		}
	}
	e, ok := matchPrice(model)
	if !ok {
		if isFree(model) {
			return info
		}
		e = defaultPrice
		info.Estimated = true
	}
	info.InputPerMTok = e.in
	info.OutputPerMTok = e.out
	info.CacheReadPerMTok = e.cacheRd
	info.CacheWritePerMTok = e.cacheWr
	if e.ctx > 0 {
		info.ContextWindow = e.ctx
	}
	return info
}

// reasoningStyle is the model's reasoning control style. The family rules
// live with the adapters that send the controls.
func reasoningStyle(model string) string {
	if strings.HasPrefix(model, "claude-") {
		return anthropic.ReasoningStyle(model)
	}
	return openai.ReasoningStyle(model)
}

// isFree reports the ":free" and "-free" model variants of gateways.
func isFree(model string) bool {
	m := strings.ToLower(model)
	return strings.HasSuffix(m, ":free") || strings.HasSuffix(m, "-free")
}

// matchPrice finds the longest table key that starts a segment of model.
func matchPrice(model string) (priceEntry, bool) {
	m := strings.ToLower(model)
	best := -1
	for i, e := range priceTable {
		if !segmentPrefix(m, e.key) {
			continue
		}
		if best < 0 || len(e.key) > len(priceTable[best].key) {
			best = i
		}
	}
	if best < 0 {
		return priceEntry{}, false
	}
	return priceTable[best], true
}

// segmentPrefix reports whether key starts m or starts a segment after "/" or ":".
func segmentPrefix(m, key string) bool {
	if strings.HasPrefix(m, key) {
		return true
	}
	for i := 0; i < len(m); i++ {
		if (m[i] == '/' || m[i] == ':') && strings.HasPrefix(m[i+1:], key) {
			return true
		}
	}
	return false
}
