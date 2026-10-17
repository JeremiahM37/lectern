package tui

// Wave 12 slash commands: /websearch, /budget. Both reach the backend through
// the existing setters: web tools use SetToolEnabled (saved per project), and
// the daily token budget is the daily_token_budget config key.
//
// Wave 17 adds /speed, /tier and /sendreasoning, which read and write the
// same keys as the settings screen. /pruning has no config key to write (no
// tool-result pruning setting exists), so it is not registered.

import (
	"fmt"
	"math"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

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
		if limit <= 0 {
			return flashCmd("info", fmt.Sprintf("No daily token budget. Used today: %s. /budget 2m sets one.", render.HumanTokens(d.Today)))
		}
		pct := 0
		if limit > 0 {
			pct = int(math.Round(100 * float64(d.Today) / float64(limit)))
		}
		return flashCmd("info", fmt.Sprintf("Daily budget %s: %s used today (%d%%)", render.HumanTokens(limit), render.HumanTokens(d.Today), pct))
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
		return cmdErrText(invalidChoice("/speed", arg, speedChoices))
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

// tierCommand is /tier [off|<tier>]. Service tiers are per provider, so the
// command acts on the provider of the current model and lists that provider's
// tiers. "off" is the provider default, stored as an empty value.
func tierCommand(m *Model, arg string) tea.Cmd {
	t := m.cur()
	provider, _, _ := strings.Cut(t.be.Model(), "/")
	tiers, ok := serviceTiers[provider]
	if !ok {
		return cmdErrText(fmt.Sprintf("The current provider (%s) has no service tier setting. Tiers apply to anthropic and openai; /model switches provider.", provider))
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
		return cmdErrText(invalidChoice("/tier", arg, choices))
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
		return cmdErrText(invalidChoice("/sendreasoning", arg, choices))
	}
	if err := m.saveFlatIn(key, choice == "on", false); err != nil {
		return cmdErr(err)
	}
	return flashCmd("ok", savedText("Send reasoning "+choice, key))
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
	if v == "" {
		return "off"
	}
	return v
}
