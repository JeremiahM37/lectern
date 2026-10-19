package tui

// Wave 12 slash commands: /websearch, /budget. Both reach the backend through
// the existing setters: web tools use SetToolEnabled (saved per project), and
// the daily token budget is the daily_token_budget config key.
//
// Wave 17 adds /speed, /tier and /sendreasoning, which read and write the
// same keys as the settings screen. /pruning (wave 33) writes
// context_management.pruning_target the same way.
//
// Wave 25 adds /thinking, /effort, /toolstream, /seqtools and /perf on the
// performance.* keys. /codeexec and /computeruse are not registered: their
// settings do not exist in config or llm.Request, and a command for them would
// store a value nothing reads. /cleartools and /clearthinking are in
// commands_w40clear.go, over the context_management keys.

import (
	"fmt"
	"math"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
)

// webToolNames are the tools /websearch switches together.
var webToolNames = []string{"web_search", "fetch_page"}

func init() {
	registerCommands(
		&command{Name: "websearch", Args: "[on|off]", Desc: "Web search and page fetch tools on or off; no argument shows the state", Group: "Settings",
			Run: func(m *Model, args string) tea.Cmd { return websearchCommand(m, strings.TrimSpace(args)) }},
		&command{Name: "budget", Args: "[tokens|off]", Desc: "Daily token budget: show it, set it (1e6, 500k, 2m), or off for no limit", Group: "Settings",
			Run: func(m *Model, args string) tea.Cmd { return budgetCommand(m, strings.TrimSpace(args)) }},
		&command{Name: "speed", Args: "[off|standard|fast]", Desc: "Model speed tier (Claude Opus 4.6 only): show it, or set it", Group: "Settings",
			Run: func(m *Model, args string) tea.Cmd { return speedCommand(m, strings.TrimSpace(args)) }},
		&command{Name: "tier", Args: "[off|<tier>]", Desc: "Service tier for the current provider (Anthropic and OpenAI): show it, or set it", Group: "Settings",
			Run: func(m *Model, args string) tea.Cmd { return tierCommand(m, strings.TrimSpace(args)) }},
		&command{Name: "sendreasoning", Args: "[on|off]", Desc: "Send earlier reasoning back to the provider: show it, or turn it on or off", Group: "Settings",
			Run: func(m *Model, args string) tea.Cmd { return sendReasoningCommand(m, strings.TrimSpace(args)) }},
		&command{Name: "thinking", Args: "[default|off|adaptive|enabled]", Desc: "Claude thinking mode: show it, or set it", Group: "Settings",
			Run: func(m *Model, args string) tea.Cmd { return thinkingCommand(m, strings.TrimSpace(args)) }},
		&command{Name: "effort", Args: "[off|low|medium|high|xhigh|max]", Desc: "Claude reasoning effort: show it, or set it", Group: "Settings",
			Run: func(m *Model, args string) tea.Cmd { return effortCommand(m, strings.TrimSpace(args)) }},
		&command{Name: "toolstream", Args: "[on|off]", Desc: "Stream tool arguments as they are written (Claude): show it, or turn it on or off", Group: "Settings",
			Run: func(m *Model, args string) tea.Cmd { return toolStreamCommand(m, strings.TrimSpace(args)) }},
		&command{Name: "seqtools", Args: "[on|off]", Desc: "Run one tool at a time instead of several per turn: show it, or turn it on or off", Group: "Settings",
			Run: func(m *Model, args string) tea.Cmd { return seqToolsCommand(m, strings.TrimSpace(args)) }},
		&command{Name: "pruning", Args: "[none|main|subagents|both]", Desc: "Shorten old tool results in the live history, for the main agent, subagents or both: show it, or set it", Group: "Settings",
			Run: func(m *Model, args string) tea.Cmd { return pruningCommand(m, strings.TrimSpace(args)) }},
		&command{Name: "perf", Desc: "Show the performance settings in effect: thinking, effort, speed, tool and reasoning options", Group: "Settings",
			Run: func(m *Model, args string) tea.Cmd { return perfCommand(m, strings.TrimSpace(args)) }},
	)
}

// websearchCommand is /websearch [on|off]. It reports the state of the two web
// tools, or turns both on or off for this project.
func websearchCommand(m *Model, arg string) tea.Cmd {
	be := m.cur().be
	state := map[string]bool{}
	for _, t := range be.Tools() {
		state[t.Name] = t.Enabled
	}
	if arg == "" {
		var parts []string
		for _, n := range webToolNames {
			parts = append(parts, n+" "+onOff(state[n]))
		}
		return flashCmd("info", "Web tools: "+strings.Join(parts, ", ")+". /websearch on|off to change.")
	}
	var on bool
	switch strings.ToLower(arg) {
	case "on", "enable", "true", "yes":
		on = true
	case "off", "disable", "false", "no":
		on = false
	default:
		return cmdErrText("Usage: /websearch [on|off]. Tool-by-tool control is in /tools.")
	}
	for _, n := range webToolNames {
		be.SetToolEnabled(n, on)
	}
	return flashCmd("ok", "Web search and page fetch "+onOff(on)+" (this project)")
}

// budgetCommand is /budget [tokens|off]. With no argument it shows today's use
// against the cap. A number sets the cap; off or 0 removes it.
func budgetCommand(m *Model, arg string) tea.Cmd {
	t := m.cur()
	if arg == "" {
		limit := fields7(t.be).DailyBudget()
		d := t.be.ContextDetail()
		var text string
		if limit <= 0 {
			text = fmt.Sprintf("No daily token budget. Used today: %s. /budget 2m sets one.", render.HumanTokens(d.Today))
		} else {
			pct := int(math.Round(100 * float64(d.Today) / float64(limit)))
			text = fmt.Sprintf("Daily budget %s: %s used today (%d%%)", render.HumanTokens(limit), render.HumanTokens(d.Today), pct)
		}
		if cb, ok := chatBudgetOf(t.be); ok {
			text += " · " + chatBudgetShort(cb)
		}
		return flashCmd("info", text)
	}
	if rest, ok := strings.CutPrefix(strings.ToLower(arg), "chat"); ok {
		return chatBudgetCommand(m, strings.TrimSpace(rest))
	}
	n := 0
	switch strings.ToLower(arg) {
	case "off", "none", "no", "unlimited":
	default:
		v, err := parseTokenCount(arg)
		if err != nil {
			return cmdErrText("Usage: /budget [tokens|off], e.g. /budget 2m")
		}
		n = v
	}
	if err := t.be.SetConfig("daily_token_budget", n, false); err != nil {
		return cmdErr(err)
	}
	if n == 0 {
		return flashCmd("ok", "Daily token budget off")
	}
	return flashCmd("ok", "Daily token budget set to "+render.HumanTokens(n))
}

// chatBudgetShort is this chat's spend for a one-line flash.
func chatBudgetShort(cb ChatBudget) string {
	if cb.Limit > 0 {
		return fmt.Sprintf("this chat %s of %s", render.HumanTokens(cb.Today), render.HumanTokens(cb.Limit))
	}
	return fmt.Sprintf("this chat %s (no per-chat budget; /budget chat 500k sets one)", render.HumanTokens(cb.Today))
}

// chatBudgetCommand is /budget chat [tokens|off]: the per-chat daily budget.
func chatBudgetCommand(m *Model, arg string) tea.Cmd {
	cb, ok := m.cur().be.(ChatBudgeter)
	if !ok {
		return cmdErrText("this session has no per-chat budget")
	}
	if arg == "" {
		return flashCmd("info", "Per-chat daily budget: "+chatBudgetShort(cb.ChatBudget()))
	}
	n := 0
	switch arg {
	case "off", "none", "no", "unlimited":
	default:
		v, err := parseTokenCount(arg)
		if err != nil {
			return cmdErrText("Usage: /budget chat [tokens|off], e.g. /budget chat 500k")
		}
		n = v
	}
	if err := cb.SetChatBudget(n); err != nil {
		return cmdErr(err)
	}
	if n == 0 {
		return flashCmd("ok", "Per-chat daily budget off")
	}
	return flashCmd("ok", "Per-chat daily budget set to "+render.HumanTokens(n))
}

// speedChoices are the values of performance.speed. "off" is the provider
// default, stored as an empty value.
var speedChoices = []string{"off", "standard", "fast"}

// speedCommand is /speed [off|standard|fast].
func speedCommand(m *Model, arg string) tea.Cmd {
	const key = "performance.speed"
	cur := fields7(m.cur().be).Performance.Speed
	if arg == "" {
		return flashCmd("info", choiceStatus("Speed", orOff(cur), speedChoices, "/speed"))
	}
	choice, ok := matchChoice(arg, speedChoices)
	if !ok {
		return cmdHint(invalidChoice("/speed", arg, speedChoices))
	}
	value := choice
	if choice == "off" {
		value = ""
	}
	if err := m.saveFlatIn(key, value, false); err != nil {
		return cmdErr(err)
	}
	return flashCmd("ok", savedText("Speed set to "+choice, key))
}

// pruningCommand is /pruning [none|main|subagents|both]. "none" is the
// default and is stored as an empty value.
func pruningCommand(m *Model, arg string) tea.Cmd {
	const key = "context_management.pruning_target"
	cur := fields7(m.cur().be).ContextManagement.PruningTarget
	if strings.EqualFold(strings.TrimSpace(arg), "off") {
		arg = config.PruningNone // the word people type for "none"
	}
	return choiceCommand(m, "/pruning", "Tool result pruning", key, cur, config.PruningNone, config.PruningTargets, arg)
}

// tierCommand is /tier [off|<tier>]. Service tiers are per provider, so the
// command acts on the provider of the current model and lists that provider's
// tiers. "off" is the provider default, stored as an empty value.
func tierCommand(m *Model, arg string) tea.Cmd {
	t := m.cur()
	provider, _, _ := strings.Cut(t.be.Model(), "/")
	tiers, ok := serviceTiers[provider]
	if !ok {
		// Not an error: the setting simply does not exist here.
		return flashCmd("info", fmt.Sprintf("Service tier: not available for %s. Tiers apply to anthropic and openai; /model switches provider.", provider))
	}
	choices := []string{"off"}
	for _, c := range tiers {
		if c != "" {
			choices = append(choices, c)
		}
	}
	key := reasoningKeyPrefix + provider + ".service_tier"
	cur := fields7(t.be).ReasoningByProvider[provider].ServiceTier
	if arg == "" {
		return flashCmd("info", choiceStatus("Service tier ("+provider+")", orOff(cur), choices, "/tier"))
	}
	choice, ok := matchChoice(arg, choices)
	if !ok {
		return cmdHint(invalidChoice("/tier", arg, choices))
	}
	value := choice
	if choice == "off" {
		value = ""
	}
	if err := t.be.SetConfig(key, value, false); err != nil {
		return cmdErr(err)
	}
	return flashCmd("ok", savedText("Service tier for "+provider+" set to "+choice, key))
}

// sendReasoningCommand is /sendreasoning [on|off]. It sends the model's
// earlier reasoning back on later requests; off is the default.
func sendReasoningCommand(m *Model, arg string) tea.Cmd {
	const key = "performance.send_reasoning"
	choices := []string{"on", "off"}
	on := false
	if p := fields7(m.cur().be).Performance.SendReasoning; p != nil {
		on = *p
	}
	if arg == "" {
		return flashCmd("info", choiceStatus("Send reasoning", onOff(on), choices, "/sendreasoning"))
	}
	choice, ok := matchChoice(arg, choices)
	if !ok {
		return cmdHint(invalidChoice("/sendreasoning", arg, choices))
	}
	if err := m.saveFlatIn(key, choice == "on", false); err != nil {
		return cmdErr(err)
	}
	return flashCmd("ok", savedText("Send reasoning "+choice, key))
}

// thinkingChoices are the values of performance.thinking. "default" stores an
// empty value, which leaves the provider's own choice in place. adaptive and
// enabled take effect only on models of the matching reasoning style; the
// settings screen shows the same rule.
var thinkingChoices = []string{"default", "off", "adaptive", "enabled"}

// thinkingCommand is /thinking [default|off|adaptive|enabled].
func thinkingCommand(m *Model, arg string) tea.Cmd {
	cur := fields7(m.cur().be).Performance.Thinking
	return choiceCommand(m, "/thinking", "Thinking", "performance.thinking", cur, "default", thinkingChoices, arg)
}

// effortChoices are the values of performance.effort, as the Claude settings
// rows show them. "off" stores an empty value, the provider default.
var effortChoices = []string{"off", "low", "medium", "high", "xhigh", "max"}

// effortCommand is /effort [off|low|medium|high|xhigh|max].
func effortCommand(m *Model, arg string) tea.Cmd {
	cur := fields7(m.cur().be).Performance.Effort
	return choiceCommand(m, "/effort", "Effort", "performance.effort", cur, "off", effortChoices, arg)
}

// toolStreamCommand is /toolstream [on|off]. An unset value reads as on, the
// default (llm/anthropic/request.go).
func toolStreamCommand(m *Model, arg string) tea.Cmd {
	cur := config.Bool(fields7(m.cur().be).Performance.ToolStreaming, true)
	return toggleCommand(m, "/toolstream", "Tool streaming", "performance.tool_streaming", cur, arg)
}

// seqToolsCommand is /seqtools [on|off]. On means one tool call per turn: it
// writes performance.disable_parallel_tool_use, so the value is the same as
// the settings row "One tool at a time".
func seqToolsCommand(m *Model, arg string) tea.Cmd {
	cur := config.Bool(fields7(m.cur().be).Performance.DisableParallelToolUse, false)
	return toggleCommand(m, "/seqtools", "One tool at a time", "performance.disable_parallel_tool_use", cur, arg)
}

// perfCommand is /perf. It reads the performance block and changes nothing:
// the spec names the command but gives no view for it, so this opens a panel
// listing what is in effect and the command that changes each value. The
// panel has one value per line, because a flash line is cut at the terminal
// width and a 120-column line would still lose the last values.
func perfCommand(m *Model, arg string) tea.Cmd {
	if arg != "" {
		return cmdErrText("Usage: /perf takes no argument. It shows the performance settings; change them with /thinking, /effort, /speed, /toolstream, /seqtools and /sendreasoning.")
	}
	p := fields7(m.cur().be).Performance
	m.openInfo("Performance", "gauge", perfLines(p))
	return flashCmd("info", "Performance settings are open. Esc closes.")
}

// perfLines is the /perf panel: each setting in effect on its own line, with
// the command that changes it.
func perfLines(p config.Performance) []string {
	row := func(label, value, cmd string) string {
		return padRight(label, 20) + padRight(value, 9) + cmd
	}
	return []string{
		row("thinking", orWord(p.Thinking, "default"), "/thinking"),
		row("effort", orOff(p.Effort), "/effort"),
		row("speed", orOff(p.Speed), "/speed"),
		row("cache lifetime", orWord(p.CacheTTL, "default"), "set in /settings"),
		row("tool streaming", onOff(config.Bool(p.ToolStreaming, true)), "/toolstream"),
		row("send reasoning", onOff(config.Bool(p.SendReasoning, false)), "/sendreasoning"),
		row("one tool at a time", onOff(config.Bool(p.DisableParallelToolUse, false)), "/seqtools"),
	}
}

// choiceCommand is the shared body of the choice-valued commands. With no
// argument it shows the stored value and the choices. An argument must be one
// of choices (any case); blank names the choice that stores an empty value.
func choiceCommand(m *Model, cmd, label, key, cur, blank string, choices []string, arg string) tea.Cmd {
	if arg == "" {
		return flashCmd("info", choiceStatus(label, orWord(cur, blank), choices, cmd))
	}
	choice, ok := matchChoice(arg, choices)
	if !ok {
		return cmdHint(invalidChoice(cmd, arg, choices))
	}
	value := choice
	if choice == blank {
		value = ""
	}
	if err := m.saveFlatIn(key, value, false); err != nil {
		return cmdErr(err)
	}
	return flashCmd("ok", savedText(label+" set to "+choice, key))
}

// toggleCommand is the shared body of the on/off commands. cur is the value in
// effect, already resolved against the setting's default.
func toggleCommand(m *Model, cmd, label, key string, cur bool, arg string) tea.Cmd {
	choices := []string{"on", "off"}
	if arg == "" {
		return flashCmd("info", choiceStatus(label, onOff(cur), choices, cmd))
	}
	choice, ok := matchChoice(arg, choices)
	if !ok {
		return cmdHint(invalidChoice(cmd, arg, choices))
	}
	if err := m.saveFlatIn(key, choice == "on", false); err != nil {
		return cmdErr(err)
	}
	return flashCmd("ok", savedText(label+" set to "+choice, key))
}

// matchChoice finds arg among choices, ignoring case, and returns the
// choice as written in the list.
func matchChoice(arg string, choices []string) (string, bool) {
	for _, c := range choices {
		if strings.EqualFold(arg, c) {
			return c, true
		}
	}
	return "", false
}

// invalidChoice names the command, what was typed and what it accepts.
func invalidChoice(cmd, arg string, choices []string) string {
	return fmt.Sprintf("%s: %q is not a choice. Valid: %s.", cmd, arg, strings.Join(choices, ", "))
}

// choiceStatus is the no-argument reply: the current value, then the choices.
func choiceStatus(label, cur string, choices []string, cmd string) string {
	return fmt.Sprintf("%s: %s. Choices: %s. %s <choice> to change.", label, cur, strings.Join(choices, ", "), cmd)
}

// savedText is the confirmation line. Performance keys are read when the
// next session starts, so the line says so, as the settings screen does.
func savedText(msg, key string) string {
	if appliesLater(key) {
		msg += "; applies to the next session"
	}
	return msg
}

// orOff shows an empty stored value as the provider default.
func orOff(v string) string {
	return orWord(v, "off")
}

// orWord shows an empty stored value as word, the name the choices give it.
func orWord(v, word string) string {
	if v == "" {
		return word
	}
	return v
}
