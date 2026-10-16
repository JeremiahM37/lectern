package tui

// The servers view: MCP servers and language servers, their state, and restart.

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

func init() {
	overrideCommands(
		&command{Name: "mcp", Desc: "MCP servers: state, tools, errors, restart", Group: "Intelligence",
			Run: func(m *Model, _ string) tea.Cmd { m.openServersModal("mcp"); return nil }},
		&command{Name: "lsp", Desc: "Language servers: state and errors", Group: "Intelligence",
			Run: func(m *Model, _ string) tea.Cmd { m.openServersModal("lsp"); return nil }},
		&command{Name: "servers", Desc: "MCP and language servers together", Group: "Intelligence",
			Run: func(m *Model, _ string) tea.Cmd { m.openServersModal(""); return nil }},
	)
}

// openServersModal opens the servers view. kind is "mcp", "lsp", or "" for both.
func (m *Model) openServersModal(kind string) {
	m.pushModal(&serversView{th: m.th, be: m.cur().be, kind: kind, m: m})
}

type serversView struct {
	m        *Model
	th       theme.Theme
	be       Backend
	kind     string // "" lists both kinds
	cursor   int
	off      int
	note     string
	noteKind string
}

// servers reads the current state; it is cheap and always fresh.
func (v *serversView) servers() []ServerStatus {
	var out []ServerStatus
	for _, s := range v.be.Servers() {
		if v.kind == "" || s.Kind == v.kind {
			out = append(out, s)
		}
	}
	return out
}

// Title implements Modal.
func (v *serversView) Title() string {
	switch v.kind {
	case "mcp":
		return "MCP servers"
	case "lsp":
		return "Language servers"
	}
	return "Servers"
}

// Icon implements Iconed.
func (v *serversView) Icon() string { return "plug" }

// Footer implements Footered.
func (v *serversView) Footer() string { return "↑↓ move · r restart · Esc close" }

// Size implements Sized.
func (v *serversView) Size(cols, rows int) (int, int) {
	return min(cols-4, 100), min(rows-4, 26)
}

// Update implements Modal.
func (v *serversView) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return v, nil, false
	}
	n := len(v.servers())
	switch km.String() {
	case "esc", "q":
		return v, nil, true
	case "up", "k":
		v.cursor = clamp(v.cursor-1, 0, max(0, n-1))
	case "down", "j":
		v.cursor = clamp(v.cursor+1, 0, max(0, n-1))
	case "r":
		return v, v.restart(), false
	}
	return v, nil, false
}

// restart runs RestartServer for the selected MCP server in the background.
func (v *serversView) restart() tea.Cmd {
	list := v.servers()
	if v.cursor < 0 || v.cursor >= len(list) {
		v.say("info", "Nothing selected.")
		return nil
	}
	s := list[v.cursor]
	if s.Kind != "mcp" {
		v.say("info", "Only MCP servers can be restarted from here.")
		return nil
	}
	be, name := v.be, s.Name
	v.say("info", "Restarting "+name+"…")
	return func() tea.Msg {
		if err := be.RestartServer(name); err != nil {
			return flashMsg{kind: "err", text: "Restart of " + name + " failed: " + err.Error()}
		}
		return flashMsg{kind: "ok", text: "Restarted " + name}
	}
}

func (v *serversView) say(kind, text string) { v.noteKind, v.note = kind, text }

// glyph is the state marker: ● ready, ◐ starting, ✗ failed, ○ anything else.
func serverGlyph(th theme.Theme, state string) string {
	switch state {
	case "ready":
		return fg(th.Success).Render("●")
	case "starting":
		return fg(th.Warning).Render("◐")
	case "failed":
		return fg(th.Error).Render("✗")
	}
	return fg(th.TextDim).Render("○")
}

// View implements Modal.
func (v *serversView) View(w, h int) string {
	th := v.th
	list := v.servers()
	v.cursor = clamp(v.cursor, 0, max(0, len(list)-1))

	footer := []string{"", fg(th.TextFaint).Render(v.configLine())}
	if v.note != "" {
		col := th.TextMuted
		switch v.noteKind {
		case "ok":
			col = th.Success
		case "err":
			col = th.Error
		}
		footer = append([]string{fg(col).Render(render.Truncate(v.note, w))}, footer...)
	}

	if len(list) == 0 {
		msg := v.emptyText()
		out := []string{fg(th.TextFaint).Render("  " + msg)}
		out = append(out, footer...)
		return strings.Join(fitLines(out, w, h), "\n")
	}

	// Build every row, remembering where each server starts so the cursor can be kept visible.
	var rows []string
	starts := make([]int, len(list))
	for i, s := range list {
		starts[i] = len(rows)
		rows = append(rows, v.header(s, i == v.cursor, w))
		if s.Error != "" {
			for _, l := range wrapLines(oneLine(s.Error), max(10, w-6)) {
				rows = append(rows, "      "+fg(th.Error).Render(l))
			}
		}
	}
	avail := max(1, h-len(footer))
	v.off = keepInView(starts[v.cursor], v.off, avail)
	if last := len(rows) - avail; v.off > last {
		v.off = max(0, last)
	}
	end := min(len(rows), v.off+avail)
	out := append([]string{}, rows[v.off:end]...)
	out = append(out, footer...)
	return strings.Join(fitLines(out, w, h), "\n")
}

// header draws the one-line summary of a server.
func (v *serversView) header(s ServerStatus, sel bool, w int) string {
	th := v.th
	marker := "  "
	name := fg(th.TextSecondary).Render(padRight(s.Name, 22))
	if sel {
		marker = fgb(th.BrandSecondary).Render("› ")
		name = fgb(th.TextPrimary).Render(padRight(s.Name, 22))
	}
	kind := fg(th.TextDim).Render(padRight(s.Kind, 5))
	state := fg(th.TextMuted).Render(padRight(s.State, 9))
	tools := fg(th.TextDim).Render(fmt.Sprintf("%d tools", s.Tools))
	line := marker + serverGlyph(th, s.State) + " " + name + " " + kind + " " + state + " " + tools
	return render.Truncate(line, w)
}

func (v *serversView) configLine() string {
	switch v.kind {
	case "mcp":
		return "Config: .scribe/config.json (\"mcp\") and .mcp.json"
	case "lsp":
		return "Language servers start when a matching file is opened."
	}
	return "MCP config: .scribe/config.json (\"mcp\") and .mcp.json"
}

func (v *serversView) emptyText() string {
	switch v.kind {
	case "mcp":
		return "No MCP servers configured. Add them under \"mcp\" in .scribe/config.json, or in .mcp.json."
	case "lsp":
		return "No language servers have started yet."
	}
	return "No MCP or language servers yet."
}
