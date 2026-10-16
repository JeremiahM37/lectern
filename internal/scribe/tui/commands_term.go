package tui

// Slash commands for the panes. They replace the placeholders in commands.go
// (overrideCommands): /terminal is the floating shell, not a one-off command
// (use "!command" for that).

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/nvim"
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
		&command{Name: "terminals", Args: "[list|new|show|hide]", Desc: "List the floating terminals, or show, hide or start one", Group: "Tabs",
			Run: func(m *Model, args string) tea.Cmd {
				switch strings.TrimSpace(args) {
				case "", "list":
					m.openTerminalList()
					return nil
				case "new":
					return m.newFloat()
				case "show":
					return m.showFloatOrNew()
				case "hide":
					m.hideFloat()
					return nil
				}
				return flashCmd("err", "Usage: /terminals [list|new|show|hide]")
			}},
		&command{Name: "editor", Args: "[file[:line]|open <file[:line]>|split|close|access [on|when-open|off]]",
			Desc: "Open the editor beside the chat (Ctrl+E) at a file and line; split changes its width; access sets what the agent may do in it", Group: "Tabs",
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
				case "open":
					return flashCmd("err", "Usage: /editor open <file[:line]>")
				case "access":
					m.setFlash("info", "Agent editor access: "+nvim.GetAccess().Label()+" (on, when-open or off)")
					return nil
				}
				if val, ok := strings.CutPrefix(args, "access "); ok {
					a, err := nvim.ParseAccess(val)
					if err != nil {
						return flashCmd("err", "Usage: /editor access [on|when-open|off]")
					}
					nvim.SetAccess(a)
					m.setFlash("info", "Agent editor access: "+a.Label())
					return nil
				}
				if file, ok := strings.CutPrefix(args, "open "); ok {
					f, line := splitFileLine(file)
					return m.openEditorAt(f, line)
				}
				f, line := splitFileLine(args)
				return m.openEditorAt(f, line)
			}},
		&command{Name: "split", Desc: "Cycle the editor/chat split (same as /editor split)", Group: "Tabs",
			Run: func(m *Model, _ string) tea.Cmd {
				m.cycleSplit()
				m.resizePanes()
				return nil
			}},
	)
}
