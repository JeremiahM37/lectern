package tui

// Wave 12 slash commands: /websearch, /budget. Both reach the backend through
// the existing setters: web tools use SetToolEnabled (saved per project), and
// the daily token budget is the daily_token_budget config key.

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
