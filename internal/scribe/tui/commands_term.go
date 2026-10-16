package tui

// Slash commands for the panes. They replace the placeholders in commands.go
// (overrideCommands): /terminal is the floating shell, not a one-off command
// (use "!command" for that).

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func init() {
	overrideCommands(
		&command{Name: "terminal", Aliases: []string{"term"}, Args: "[new]",
			Desc: "Show the floating terminal (new starts another; Esc hides it)", Group: "Tabs",
			Run: func(m *Model, args string) tea.Cmd {
				switch strings.TrimSpace(args) {
				case "":
					return m.showFloatOrNew()
				case "new":
					return m.newFloat()
				}
				m.setFlash("err", "Usage: /terminal [new]")
				return nil
			}},
		&command{Name: "terminals", Desc: "List the floating terminals and show one", Group: "Tabs",
			Run: func(m *Model, _ string) tea.Cmd {
				m.openTerminalList()
				return nil
			}},
		&command{Name: "editor", Args: "[file|split|close]",
			Desc: "Open the editor beside the chat (Ctrl+E); split changes its width", Group: "Tabs",
			Run: func(m *Model, args string) tea.Cmd {
				args = strings.TrimSpace(args)
				switch args {
				case "":
					return m.toggleEditor()
				case "split":
					m.cycleSplit()
					m.resizePanes()
					return nil
				case "close":
					m.closeEditor()
					return nil
				}
				return m.openEditor(args)
			}},
	)
}
