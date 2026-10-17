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
)

// effortOptionsFor returns the effort levels an effort-style model takes on a
// provider. The LLM Gateway also takes xhigh; DeepSeek V4 takes only high and
// max.
func effortOptionsFor(provider string) []string {
	switch provider {
	case "llmgateway":
		return gatewayEffortOptions
	case "deepseek":
		return deepSeekEffortOptions
	}
	return effortOptions
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
