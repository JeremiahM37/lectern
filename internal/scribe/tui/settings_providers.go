package tui

import (
	"path"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// Per-provider reasoning options. Each list is the set of values the provider
// takes; the first entry is the provider default and is stored as "".
var (
	// verbosityOptions are the answer length levels (OpenAI GPT-5).
	verbosityOptions = []string{"", "low", "medium", "high"}
	// openRouterEffortOptions are OpenRouter's unified effort levels.
	openRouterEffortOptions = []string{"", "minimal", "low", "medium", "high", "xhigh", "none"}
	// openRouterBudgetOptions are the max_tokens values for the reasoning
	// object; 0 is the provider default.
	openRouterBudgetOptions = []int{0, 1024, 2048, 4096, 8192, 16384}
	// gatewayEffortOptions are the effort levels the LLM Gateway takes.
	gatewayEffortOptions = []string{"", "low", "medium", "high", "xhigh"}
	// deepSeekEffortOptions are the levels DeepSeek V4 takes (spec 02 §5.1.1).
	deepSeekEffortOptions = []string{"", "high", "max"}
	// geminiEffortOptions are the levels Gemini's compatible endpoint takes.
	geminiEffortOptions = []string{"", "minimal", "low", "medium", "high"}
	// deepSeekThinkingOptions are the DeepSeek V4 thinking switch states. ""
	// is the provider default, which is on, so the field is left out.
	deepSeekThinkingOptions = []string{"", "on", "off"}
)

// effortOptionsFor returns the effort levels an effort-style model takes on a
// provider. The LLM Gateway also takes xhigh; DeepSeek V4 takes only high and
// max; Gemini takes minimal through high.
func effortOptionsFor(provider string) []string {
	switch provider {
	case "llmgateway":
		return gatewayEffortOptions
	case "deepseek":
		return deepSeekEffortOptions
	case "gemini":
		return geminiEffortOptions
	}
	return effortOptions
}

// deepSeekThinkingModel reports whether a DeepSeek model id takes the thinking
// switch. DeepSeek documents it for the V4 family; other ids get no row.
func deepSeekThinkingModel(id string) bool {
	return strings.HasPrefix(strings.ToLower(id), "deepseek-v4")
}

// deepSeekThinkingRow is the Think tab's DeepSeek thinking switch: default
// (the provider's on), On or Off. It is saved under reasoning_by_provider's
// thinking key and sent as thinking.type only for V4 models.
func (s *settings) deepSeekThinkingRow(key string, rs config.Reasoning) setRow {
	return s.cycleRow("Thinking", key+"thinking", rs.Thinking, deepSeekThinkingOptions,
		"default is on; On sends thinking enabled, Off sends disabled")
}

// gpt5Model reports whether a model id is in the GPT-5 family, the only OpenAI
// family that takes the verbosity control. gpt-5-chat is excluded.
func gpt5Model(id string) bool {
	m := strings.ToLower(path.Base(id))
	return strings.HasPrefix(m, "gpt-5") && !strings.HasPrefix(m, "gpt-5-chat")
}

// openRouterRows are the OpenRouter tab's rows: unified effort, the token
// budget and the hide-reasoning switch. They are saved under the provider's
// reasoning_by_provider keys and sent in the reasoning object.
func (s *settings) openRouterRows(key string, rs config.Reasoning) []setRow {
	return []setRow{
		s.cycleRow("Effort", key+"effort", rs.Effort, openRouterEffortOptions, "unified effort; sent as reasoning.effort"),
		s.budgetRowOf("Max reasoning tokens", key+"budget", rs.Budget, openRouterBudgetOptions,
			"used when Effort is default; sent as reasoning.max_tokens"),
		s.reasoningFlagRow("Hide reasoning", key+"exclude", rs.Exclude,
			"the model still reasons; the reasoning text is left out"),
	}
}

// budgetRowOf is budgetRow over a provider's own list of token budgets.
func (s *settings) budgetRowOf(label, key string, cur int, opts []int, note string) setRow {
	shown := "default"
	if cur > 0 {
		shown = strconv.Itoa(cur)
	}
	return setRow{label: label, value: shown, note: note, key: key, raw: cur, act: func() tea.Cmd {
		idx := 0
		for i, v := range opts {
			if v == cur {
				idx = i
			}
		}
		return s.set(key, opts[(idx+1)%len(opts)])
	}}
}

// reasoningFlagRow is an on/off switch on a reasoning_by_provider key.
func (s *settings) reasoningFlagRow(label, key string, on bool, note string) setRow {
	return setRow{label: label, value: onOffWord(on), note: note, key: key, raw: on, toggle: true, on: on,
		act: func() tea.Cmd { return s.set(key, !on) }}
}

// claudeContextRows are the Claude tab's context management switches. They are
// saved under context_management and read on each Anthropic request. Both are
// off by default, so an unchanged config sends the same requests as before.
// Preserve thinking has no effect while Thinking is off, and the note says so.
func (s *settings) claudeContextRows(cm config.ContextManagement) []setRow {
	return []setRow{
		s.flagRow7("Preserve thinking", "context_management.clear_thinking", config.Bool(cm.ClearThinking, false),
			"keeps earlier thinking blocks; applies only while Thinking is on"),
		s.flagRow7("Clear old tool uses", "context_management.clear_tool_uses", config.Bool(cm.ClearToolUses, false),
			"clears old tool results past about 65% of the window; the prompt cache misses"),
	}
}

// claudeRuntimeRows are the Claude tab's request switches that have a config
// key already: Tool streaming and Send reasoning. The Anthropic adapter reads
// both. Speed is not here: each provider has its own row (providerSpeedRow).
// Tool streaming shows its default, on, because only an explicit false turns
// it off.
func (s *settings) claudeRuntimeRows(p config.Performance) []setRow {
	return []setRow{
		s.flagRow7("Tool streaming", "performance.tool_streaming", config.Bool(p.ToolStreaming, true),
			"streams tool arguments as they are generated"),
		s.flagRow7("Send reasoning", "performance.send_reasoning", config.Bool(p.SendReasoning, false),
			"sends earlier reasoning back to Claude"),
	}
}

// providerSpeedOptions are the values of a provider's Speed row. "inherit" is
// the default: it removes the provider's entry, so the global Speed applies.
// "standard" turns fast mode off for this provider only.
var providerSpeedOptions = []string{"inherit", "fast", "standard"}

// providerSpeedRow is the Speed row for one provider. It writes that provider's
// entry in performance.speed_by_provider and leaves every other provider's
// entry alone. The note names the global Speed the row inherits.
func (s *settings) providerSpeedRow(provider string, p config.Performance) setRow {
	cur := ""
	if v, ok := p.SpeedByProvider[provider]; ok {
		cur = v
		if cur == "" {
			cur = "standard"
		}
	}
	global := p.Speed
	if global == "" {
		global = "off"
	}
	return s.pickRow7("Speed", "performance.speed_by_provider."+provider, cur, providerSpeedOptions,
		"inherit uses the global speed ("+global+"); fast mode only on models that accept it")
}

// claudeFeatureRows are the Claude tab's server-side feature switches. Each
// adds one Anthropic feature to requests and is off by default. The
// notes say so, because none of them has been tested live against the API.
func (s *settings) claudeFeatureRows(p config.Performance) []setRow {
	return []setRow{
		s.flagRow7("Code execution", "performance.anthropic_code_execution", p.CodeExecutionOn(),
			"lets Claude run code on Anthropic's servers; off by default, not live-tested against the API"),
		s.flagRow7("Computer use", "performance.anthropic_computer_use", p.ComputerUseOn(),
			"adds the computer-use tool so Claude can ask for clicks and keystrokes; off by default, not live-tested against the API"),
		s.flagRow7("Server compaction", "performance.anthropic_server_compaction", p.ServerCompactionOn(),
			"Anthropic summarises long conversations on its side (200k+ windows); off by default, not live-tested against the API"),
	}
}

// openAIReasoningSummaryOptions are the values of the OpenAI reasoning summary
// row. The first entry, off, is stored as "", which sends nothing.
var openAIReasoningSummaryOptions = []string{"off", "auto", "concise", "detailed"}

// openAIReasoningSummaryRow is the OpenAI reasoning summary choice. It writes
// performance.openai_reasoning_summary through the same flat-key path as the
// other performance rows.
func (s *settings) openAIReasoningSummaryRow(p config.Performance) setRow {
	return s.pickRow7("Reasoning summary", "performance.openai_reasoning_summary", p.OpenAIReasoningSummary,
		openAIReasoningSummaryOptions,
		"OpenAI sends a summary of the model's reasoning: auto, concise or detailed; off by default, not live-tested against the API")
}
