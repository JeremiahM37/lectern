package tui

// Slash commands for the panes. They replace the placeholders in commands.go
// (overrideCommands): /terminal is the floating shell, not a one-off command
// (use "!command" for that).

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
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
		&command{Name: "terminals", Args: "[list|new|show|hide|close [n]|rename [n] <name>]", Desc: "List the floating terminals, or show, hide, start, close or rename one", Group: "Tabs",
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
				if rest, ok := strings.CutPrefix(strings.TrimSpace(args), "close"); ok {
					m.closeFloatCmd(rest)
					return nil
				}
				if rest, ok := strings.CutPrefix(strings.TrimSpace(args), "rename"); ok {
					m.renameFloatCmd(rest)
					return nil
				}
				return cmdErrText("Usage: /terminals [list|new|show|hide|close [n]|rename [n] <name>]")
			}},
		&command{Name: "editor", Args: "[file[:line]|open <file[:line]>|split|close|access [on|when-open|off]|sync [on|off]]",
			Desc: "Open the editor beside the chat (Ctrl+E) at a file and line; split changes its width; access sets what the agent may do in it; sync sets whether its edits follow into the buffer", Group: "Tabs",
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
					return cmdErrText("Usage: /editor open <file[:line]>")
				case "access":
					m.setFlash("info", "Agent editor access: "+nvim.GetAccess().Label()+" (on, when-open or off)")
					return nil
				case "sync":
					m.setFlash("info", syncLabel(nvim.SyncOnEdit()))
					return nil
				}
				if val, ok := strings.CutPrefix(args, "sync "); ok {
					on, err := parseOnOff(val)
					if err != nil {
						return cmdErrText("Usage: /editor sync [on|off]")
					}
					nvim.SetSyncOnEdit(on)
					m.setFlash("info", syncLabel(on)+" (saved)")
					if err := saveEditorSetting(func(c *config.Config) { c.EditorSync = &on }); err != nil {
						m.setFlash("err", "Could not save the editor setting: "+err.Error())
					}
					return nil
				}
				if val, ok := strings.CutPrefix(args, "access "); ok {
					a, err := nvim.ParseAccess(val)
					if err != nil {
						return cmdErrText("Usage: /editor access [on|when-open|off]")
					}
					nvim.SetAccess(a)
					m.setFlash("info", "Agent editor access: "+a.Label()+" (saved)")
					if err := saveEditorSetting(func(c *config.Config) { c.EditorAccess = a.String() }); err != nil {
						m.setFlash("err", "Could not save the editor setting: "+err.Error())
					}
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
