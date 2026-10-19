package tui

// Wave 40 slash commands: /cleartools and /clearthinking. They read and write
// the context_management.clear_tool_uses and context_management.clear_thinking
// keys, the same ones as the settings screen. Both default to off, as the
// config type does (a nil value is off).

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

func init() {
	registerCommands(
		&command{Name: "cleartools", Args: "[on|off]", Desc: "Clear old tool results on the server (Anthropic): show it, or turn it on or off", Group: "Settings",
			Run: func(m *Model, args string) tea.Cmd { return clearToolsCommand(m, strings.TrimSpace(args)) }},
		&command{Name: "clearthinking", Args: "[on|off]", Desc: "Keep earlier thinking blocks on the server (Anthropic): show it, or turn it on or off", Group: "Settings",
			Run: func(m *Model, args string) tea.Cmd { return clearThinkingCommand(m, strings.TrimSpace(args)) }},
	)
}

// clearToolsCommand is /cleartools [on|off].
func clearToolsCommand(m *Model, arg string) tea.Cmd {
	cur := config.Bool(fields7(m.cur().be).ContextManagement.ClearToolUses, false)
	return toggleCommand(m, "/cleartools", "Clear old tool uses", "context_management.clear_tool_uses", cur, arg)
}

// clearThinkingCommand is /clearthinking [on|off]. On keeps every earlier
// thinking block; it only matters while thinking is on for the request.
func clearThinkingCommand(m *Model, arg string) tea.Cmd {
	cur := config.Bool(fields7(m.cur().be).ContextManagement.ClearThinking, false)
	return toggleCommand(m, "/clearthinking", "Preserve thinking (on keeps earlier thinking blocks)", "context_management.clear_thinking", cur, arg)
}
