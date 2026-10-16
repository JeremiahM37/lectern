package tui

// Catalog commands from spec 02 section 2 that the registry did not have:
// /mode, /status, /diagnose, /changes, /checkpoint, /font nerd, the settings
// shortcuts (/provider-settings, /router), and the aliases that point at an
// existing command (/split, /repo-map, /wizard, /lsp status, /lsp restart).
// Everything here runs on the Backend the tab already has; no new backend
// methods are needed.

import (
	"fmt"
	"os"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
)

func init() {
	registerCommands(
		&command{Name: "mode", Args: "[name]", Desc: "Pick a forge mode, or switch to a named one", Group: "Settings",
			Run: func(m *Model, args string) tea.Cmd {
				args = strings.TrimSpace(args)
				if args == "" {
					m.openModePicker()
					return nil
				}
				if !isForgeMode(args) {
					return flashCmd("err", "Unknown mode "+args+". Modes: "+strings.Join(modeOrder, ", "))
				}
				return m.setMode(args)
			}},
		&command{Name: "status", Desc: "System status: model, mode, context, git, servers, jobs", Group: "System",
			Run: func(m *Model, _ string) tea.Cmd {
				m.openInfo("Status", "gauge", m.statusLines())
				return nil
			}},
		&command{Name: "diagnose", Desc: "Health check: code map, language and MCP servers, provider keys", Group: "Intelligence",
			Run: func(m *Model, _ string) tea.Cmd {
				m.openInfo("Diagnose", "bug", m.diagnoseLines())
				return nil
			}},
		&command{Name: "changes", Desc: "Files changed by this session's turns, and the working tree", Group: "Tabs",
			Run: func(m *Model, _ string) tea.Cmd {
				m.openInfo("Changes", "diff", m.changesLines())
				return nil
			}},
		&command{Name: "checkpoint", Args: "[list|undo|redo]", Desc: "Checkpoints: browse, undo or redo a turn", Group: "Session",
			Run: func(m *Model, args string) tea.Cmd {
				switch strings.TrimSpace(args) {
				case "", "list":
					m.openCheckpoints()
					return nil
				case "undo":
					m.confirmUndo()
					return nil
				case "redo":
					c, _ := findCommand("redo")
					return c.Run(m, "")
				case "live":
					return flashCmd("info", "Ctrl+F steps forward through checkpoints and returns to the live view")
				case "save":
					return flashCmd("info", "Checkpoints are kept in Scribe's own store; there is no git tag to save")
				}
				return flashCmd("err", "Usage: /checkpoint [list|undo|redo]")
			}},
		&command{Name: "font", Args: "[nerd]", Desc: "Terminal font info; nerd toggles Nerd Font icons", Group: "Settings",
			Run: func(m *Model, args string) tea.Cmd {
				t := m.cur()
				switch strings.TrimSpace(args) {
				case "":
					on, _ := t.be.Config()["nerd_font"].(bool)
					return flashCmd("info", "Nerd Font icons "+onOff(on)+". The terminal font itself is set in your terminal emulator")
				case "nerd":
					on, _ := t.be.Config()["nerd_font"].(bool)
					if err := t.be.SetConfig("nerd_font", !on, false); err != nil {
						return errFlash(err)
					}
					icons.SetNerd(!on)
					return flashCmd("ok", "Nerd Font icons "+onOff(!on))
				}
				return flashCmd("err", "Usage: /font [nerd]. The terminal font is set in your terminal emulator")
			}},
		&command{Name: "provider-settings", Aliases: []string{"providers", "provider"}, Desc: "Provider and model options: keys, thinking, effort", Group: "Models",
			Run: func(m *Model, _ string) tea.Cmd { return m.openSettingsTab(stProviders) }},
		&command{Name: "router", Desc: "Route models per task (explore, code, compact, fallback)", Group: "Models",
			Run: func(m *Model, _ string) tea.Cmd { return m.openSettingsTab(stRouter) }},
		&command{Name: "repo-map", Desc: "Code map: counts and rebuild (same as /map)", Group: "Intelligence",
			Run: func(m *Model, args string) tea.Cmd { return runAlias(m, "map", args) }},
		&command{Name: "wizard", Desc: "Re-run the first-run setup (same as /setup)", Group: "System",
			Run: func(m *Model, args string) tea.Cmd { return runAlias(m, "setup", args) }},
		&command{Name: "lsp status", Desc: "Language server status and errors", Group: "Intelligence",
			Run: func(m *Model, _ string) tea.Cmd { m.openServers("lsp"); return nil }},
		&command{Name: "lsp-status", Desc: "Same as /lsp status", Group: "Intelligence",
			Run: func(m *Model, args string) tea.Cmd { return runAlias(m, "lsp status", args) }},
		&command{Name: "lsp restart", Args: "[filter]", Desc: "Restart every language server, or those whose name contains filter", Group: "Intelligence",
			Run: func(m *Model, args string) tea.Cmd { return m.restartLSP(strings.TrimSpace(args)) }},
		&command{Name: "lsp-restart", Args: "[filter]", Desc: "Same as /lsp restart", Group: "Intelligence",
			Run: func(m *Model, args string) tea.Cmd { return runAlias(m, "lsp restart", args) }},
	)
}

// isForgeMode reports whether id is one of the modes the Ctrl+D cycle offers.
func isForgeMode(id string) bool {
	for _, x := range modeOrder {
		if x == id {
			return true
		}
	}
	return false
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// runAlias runs the command called name with args, as if the user had typed it.
func runAlias(m *Model, name, args string) tea.Cmd {
	line := strings.TrimSpace(name + " " + args)
	c, rest := findCommand(line)
	if c == nil || c.Name != name {
		return flashCmd("err", "Command /"+name+" is not registered")
	}
	return c.Run(m, rest)
}

// openSettingsTab opens the settings screen on one of its tabs.
func (m *Model) openSettingsTab(tab int) tea.Cmd {
	cmd := m.openSettingsScreen()
	if s, ok := m.modals[len(m.modals)-1].(*settings); ok {
		s.tab = tab
	}
	return cmd
}

// statusLines is the body of /status.
func (m *Model) statusLines() []string {
	t := m.cur()
	used, win := t.be.Context()
	u, cost := t.be.Usage()
	g := t.be.Git()
	branch := g.Branch
	if branch == "" {
		branch = "(no branch)"
	}
	if g.Dirty {
		branch += fmt.Sprintf(" · %d changed", len(g.Changed))
	}
	var ready, failed, other int
	for _, s := range t.be.Servers() {
		switch s.State {
		case "ready":
			ready++
		case "failed":
			failed++
		default:
			other++
		}
	}
	running := "idle"
	if t.busy {
		running = "running a turn"
	}
	return []string{
		"Workdir    " + t.be.Workdir(),
		"Session    " + t.be.SessionID() + " " + t.be.SessionTitle(),
		"Model      " + t.be.Model(),
		"Mode       " + t.be.Mode() + " · reasoning " + t.be.Reasoning(),
		"Context    " + render.HumanTokens(used) + " of " + render.HumanTokens(win) + fmt.Sprintf(" (%d%%)", pct(used, win)),
		fmt.Sprintf("Tokens     %s in, %s out, $%.4f", render.HumanTokens(u.InputTokens), render.HumanTokens(u.OutputTokens), cost),
		"Git        " + branch,
		fmt.Sprintf("Servers    %d ready, %d failed, %d starting or stopped", ready, failed, other),
		fmt.Sprintf("Jobs       %d background", len(t.be.Jobs())),
		"State      " + running,
	}
}

// diagnoseLines is the body of /diagnose: the parts of Scribe that can be
// checked from the backend. Parser and semantic-index checks are not built.
func (m *Model) diagnoseLines() []string {
	t := m.cur()
	s := t.be.MapStats()
	lines := []string{"Code map", fmt.Sprintf("  %d files, %d symbols, %d edges", s.Files, s.Symbols, s.Edges)}
	if s.Building {
		lines = append(lines, fmt.Sprintf("  building, %d%%", int(s.Progress*100)))
	}
	lines = append(lines, "", "Servers")
	servers := t.be.Servers()
	if len(servers) == 0 {
		lines = append(lines, "  none configured")
	}
	for _, sv := range servers {
		line := fmt.Sprintf("  %-4s %-18s %s", sv.Kind, sv.Name, sv.State)
		if sv.Error != "" {
			line += " · " + oneLine(sv.Error)
		}
		lines = append(lines, line)
	}
	lines = append(lines, "", "Provider keys")
	for _, p := range t.be.Providers() {
		state := "no key"
		switch {
		case p.Local:
			state = "local, no key needed"
		case p.HasKey:
			state = "ok"
		}
		lines = append(lines, fmt.Sprintf("  %-18s %s", p.ID, state))
	}
	return lines
}

// changesLines is the body of /changes: files named by turns that were not
// undone, then whatever the working tree shows now.
func (m *Model) changesLines() []string {
	t := m.cur()
	seen := map[string]bool{}
	var turned []string
	for _, cp := range t.be.Checkpoints() {
		if cp.Undone {
			continue
		}
		for _, f := range cp.Files {
			if !seen[f] {
				seen[f] = true
				turned = append(turned, f)
			}
		}
	}
	sort.Strings(turned)
	lines := []string{"Changed by this session's turns"}
	if len(turned) == 0 {
		lines = append(lines, "  none")
	}
	for _, f := range turned {
		lines = append(lines, "  "+f)
	}
	lines = append(lines, "", "Working tree")
	wt := t.be.Git().Changed
	if len(wt) == 0 {
		lines = append(lines, "  clean")
	}
	for _, f := range wt {
		lines = append(lines, "  "+f)
	}
	return lines
}

// restartLSP restarts the language servers whose name contains filter (all
// when filter is empty). The restarts run off the UI goroutine.
func (m *Model) restartLSP(filter string) tea.Cmd {
	t := m.cur()
	var names []string
	for _, s := range t.be.Servers() {
		if s.Kind == "lsp" && (filter == "" || strings.Contains(s.Name, filter)) {
			names = append(names, s.Name)
		}
	}
	if len(names) == 0 {
		return flashCmd("info", "No language servers match")
	}
	be := t.be
	return func() tea.Msg {
		var failed []string
		for _, n := range names {
			if err := be.RestartServer(n); err != nil {
				failed = append(failed, n+": "+err.Error())
			}
		}
		if len(failed) > 0 {
			return flashMsg{kind: "err", text: "Restart failed: " + strings.Join(failed, "; ")}
		}
		return flashMsg{kind: "ok", text: fmt.Sprintf("Restarted %d language server(s)", len(names))}
	}
}

// copyExport copies the session as Markdown to the clipboard with OSC 52.
// Backends that can render the text directly do not write a file; the others
// export to a file and copy that file's text.
func (m *Model) copyExport() tea.Cmd {
	be := m.cur().be
	if md, ok := be.(MarkdownExporter); ok {
		text, err := md.ExportMarkdown()
		if err != nil {
			return errFlash(err)
		}
		copyToTerminal(text)
		return flashCmd("ok", "Copied the chat as markdown")
	}
	path, err := be.Export("markdown")
	if err != nil {
		return errFlash(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return errFlash(err)
	}
	copyToTerminal(string(data))
	return flashCmd("ok", "Copied the chat as markdown (file kept at "+path+")")
}
