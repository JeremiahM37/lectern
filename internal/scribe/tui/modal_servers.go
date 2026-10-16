package tui

// The servers view: MCP servers and language servers, their state, and restart.

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

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
	lsp      map[string]LSPSession
	pings    map[string]mcpPingResult
	cursor   int
	off      int
	note     string
	noteKind string
	width    int // the frame width Size chose; the footer follows it
}

// ServerToolLister is optionally implemented by a Backend that can list the
// tools an MCP server registered. The servers view opens them with v.
type ServerToolLister interface {
	ServerTools(name string) ([]string, error)
}

// LSPSession describes one language server, for the servers view. Command is
// empty until the binary is found; LastError is set when a start failed.
type LSPSession struct {
	Language, Command, Root, State, LastError string
	PID, OpenDocs                             int
	Since                                     time.Time
}

// LSPSessioner is optionally implemented by a Backend that can list its
// language servers. The servers view shows them alongside the MCP servers.
type LSPSessioner interface {
	LSPSessions() []LSPSession
}

// MCPPinger is optionally implemented by a Backend that can ping an MCP
// server. p in the servers view pings the highlighted one; the details show
// the last answer.
type MCPPinger interface {
	PingMCP(name string) (time.Duration, error)
}

// mcpPingDone is the result of one ping, returned to the view in the background.
type mcpPingDone struct {
	name string
	took time.Duration
	err  error
}

// mcpPingResult is the last answer the view saw for a server.
type mcpPingResult struct {
	at   time.Time
	took time.Duration
	err  error
}

// lspName is the row name of a language server: its language, plus the
// project folder when the same language runs in more than one root.
func lspName(s LSPSession) string {
	return s.Language + " " + filepath.Base(s.Root)
}

// lspState maps a language server's state onto the row glyphs the view uses.
func lspState(state string) string {
	switch state {
	case "running":
		return "ready"
	case "starting", "failed":
		return state
	}
	return "stopped"
}

// servers reads the current state; it is cheap and always fresh. The lsp
// view and the combined view also list language servers.
func (v *serversView) servers() []ServerStatus {
	var out []ServerStatus
	for _, s := range v.be.Servers() {
		if v.kind == "" || v.kind == "lsp" || s.Kind == v.kind {
			out = append(out, s)
		}
	}
	v.lsp = map[string]LSPSession{}
	if ls, ok := v.be.(LSPSessioner); ok && (v.kind == "" || v.kind == "lsp") {
		for _, s := range ls.LSPSessions() {
			name := lspName(s)
			v.lsp[name] = s
			out = append(out, ServerStatus{Name: name, Kind: "lsp", State: lspState(s.State), Error: s.LastError})
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
func (v *serversView) Footer() string {
	if v.kind == "lsp" {
		return "↑↓ move · Enter details · r restart · v tools · Esc close"
	}
	if v.width < 100 {
		// a narrow frame drops the restart and details hints, which the
		// wide one lists, so the close hint is not cut off
		return "↑↓ · a add · e edit · t on/off · d remove · v tools · Esc close"
	}
	return "↑↓ · a add · e edit · t on/off · v tools · ⏎ details · d remove · r restart · Esc close"
}

// Size implements Sized.
func (v *serversView) Size(cols, rows int) (int, int) {
	v.width = min(cols-4, 100)
	return v.width, min(rows-4, 26)
}

// Update implements Modal.
func (v *serversView) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	if done, ok := msg.(mcpPingDone); ok {
		if v.pings == nil {
			v.pings = map[string]mcpPingResult{}
		}
		v.pings[done.name] = mcpPingResult{at: time.Now(), took: done.took, err: done.err}
		if done.err != nil {
			v.say("err", done.name+" did not answer: "+done.err.Error())
		} else {
			v.say("ok", fmt.Sprintf("%s answered in %s", done.name, done.took.Round(time.Millisecond)))
		}
		return v, nil, false
	}
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return v, nil, false
	}
	n := len(v.servers())
	switch km.String() {
	case "p":
		return v, v.ping(), false
	case "esc", "q":
		return v, nil, true
	case "up", "k":
		v.cursor = clamp(v.cursor-1, 0, max(0, n-1))
	case "down", "j":
		v.cursor = clamp(v.cursor+1, 0, max(0, n-1))
	case "r":
		return v, v.restart(), false
	case "a":
		if v.kind != "lsp" {
			v.m.pushModal(newMCPForm(v.th, nil, v.be.SaveMCPServer))
		}
	case "e":
		if sp, ok := v.selectedMCP(); ok {
			v.m.pushModal(newMCPForm(v.th, &sp, v.be.SaveMCPServer))
		}
	case "t":
		return v, v.toggle(), false
	case "d":
		v.confirmRemove()
	case "v":
		v.showTools()
	case "enter":
		v.showDetails()
	}
	return v, nil, false
}

// showDetails opens the highlighted language server's details: command, root,
// state, process and open documents, and the last error. Read only.
func (v *serversView) showDetails() {
	list := v.servers()
	if v.cursor >= 0 && v.cursor < len(list) && list[v.cursor].Kind == "mcp" {
		v.showMCPDetails(list[v.cursor])
		return
	}
	if v.cursor < 0 || v.cursor >= len(list) || list[v.cursor].Kind != "lsp" {
		return
	}
	s, ok := v.lsp[list[v.cursor].Name]
	if !ok {
		return
	}
	cmd := s.Command
	if cmd == "" {
		cmd = "not found on PATH"
	}
	pid := "none"
	if s.PID > 0 {
		pid = fmt.Sprint(s.PID)
	}
	items := []PickItem{
		{Value: "command", Label: "Command", Desc: cmd},
		{Value: "root", Label: "Root", Desc: s.Root},
		{Value: "state", Label: "State", Desc: lspState(s.State)},
		{Value: "pid", Label: "Process", Desc: pid},
		{Value: "docs", Label: "Open documents", Desc: fmt.Sprint(s.OpenDocs)},
	}
	if s.LastError != "" {
		items = append(items, PickItem{Value: "error", Label: "Last error", Desc: oneLine(s.LastError)})
	}
	p := NewPicker(v.th, "Language server · "+s.Language, "lsp")
	p.SetItems(items)
	v.m.pushModal(p)
}

// ping asks the highlighted MCP server to answer a ping, in the background.
func (v *serversView) ping() tea.Cmd {
	list := v.servers()
	if v.cursor < 0 || v.cursor >= len(list) || list[v.cursor].Kind != "mcp" {
		v.say("info", "Select an MCP server first.")
		return nil
	}
	pg, ok := v.be.(MCPPinger)
	if !ok {
		v.say("info", "This build cannot ping MCP servers.")
		return nil
	}
	name := list[v.cursor].Name
	v.say("info", "Pinging "+name+"…")
	return func() tea.Msg {
		took, err := pg.PingMCP(name)
		return mcpPingDone{name: name, took: took, err: err}
	}
}

// showMCPDetails shows what the config says about an MCP server and what
// the running state is. Nothing here changes anything; Esc closes it.
func (v *serversView) showMCPDetails(st ServerStatus) {
	items := []PickItem{{Value: "state", Label: "State", Desc: st.State}}
	if sp, ok := mcpSpecNamed(v.be.MCPServers(), st.Name); ok {
		where := sp.Command
		if sp.Transport == "http" {
			where = sp.URL
		} else if len(sp.Args) > 0 {
			where += " " + strings.Join(sp.Args, " ")
		}
		items = append(items,
			PickItem{Value: "scope", Label: "Scope", Desc: sp.Scope},
			PickItem{Value: "transport", Label: "Transport", Desc: sp.Transport},
			PickItem{Value: "run", Label: "Runs", Desc: where},
		)
		if sp.Disabled {
			items = append(items, PickItem{Value: "off", Label: "Turned off", Desc: "press t to turn it on"})
		}
	}
	items = append(items, PickItem{Value: "tools", Label: "Tools", Desc: fmt.Sprint(st.Tools)})
	if _, ok := v.be.(MCPPinger); ok {
		items = append(items, PickItem{Value: "ping", Label: "Last ping", Desc: v.pingText(st.Name)})
	}
	if st.Error != "" {
		items = append(items, PickItem{Value: "error", Label: "Last error", Desc: oneLine(st.Error)})
	}
	p := NewPicker(v.th, "MCP · "+st.Name, "plug")
	p.SetItems(items)
	v.m.pushModal(p)
}

// mcpSpecNamed finds the configured spec for an MCP server by name.
func mcpSpecNamed(specs []MCPServerSpec, name string) (MCPServerSpec, bool) {
	for _, sp := range specs {
		if sp.Name == name {
			return sp, true
		}
	}
	return MCPServerSpec{}, false
}

// showTools lists the tools the highlighted MCP server registered. Nothing
// in the list changes anything, so it is a plain picker that Esc closes.
func (v *serversView) showTools() {
	list := v.servers()
	if v.cursor < 0 || v.cursor >= len(list) || list[v.cursor].Kind != "mcp" {
		v.say("info", "Select an MCP server first.")
		return
	}
	s := list[v.cursor]
	lister, ok := v.be.(ServerToolLister)
	if !ok {
		v.say("info", "This build cannot list server tools.")
		return
	}
	names, err := lister.ServerTools(s.Name)
	if err != nil {
		v.say("err", err.Error())
		return
	}
	if len(names) == 0 {
		v.say("info", fmt.Sprintf("%s has no tools (state: %s).", s.Name, s.State))
		return
	}
	p := NewPicker(v.th, "Tools · "+s.Name, "tools")
	items := make([]PickItem, 0, len(names))
	for _, n := range names {
		items = append(items, PickItem{Value: n, Label: n})
	}
	p.SetItems(items)
	v.m.pushModal(p)
}

// selectedMCP returns the definition of the highlighted MCP server.
func (v *serversView) selectedMCP() (MCPServerSpec, bool) {
	list := v.servers()
	if v.cursor < 0 || v.cursor >= len(list) || list[v.cursor].Kind != "mcp" {
		v.say("info", "Select an MCP server first.")
		return MCPServerSpec{}, false
	}
	name := list[v.cursor].Name
	for _, sp := range v.be.MCPServers() {
		if sp.Name == name {
			return sp, true
		}
	}
	v.say("err", name+" is not in any MCP config.")
	return MCPServerSpec{}, false
}

// toggle turns the highlighted MCP server on or off and saves it. The
// change applies to the next request.
func (v *serversView) toggle() tea.Cmd {
	sp, ok := v.selectedMCP()
	if !ok {
		return nil
	}
	was := sp
	sp.Disabled = !sp.Disabled
	if err := v.be.SaveMCPServer(sp, &was); err != nil {
		return flashCmd("err", "Could not change "+sp.Name+": "+err.Error())
	}
	if sp.Disabled {
		return flashCmd("ok", sp.Name+" disabled. Its tools leave the next request.")
	}
	return flashCmd("ok", sp.Name+" enabled. It is connecting.")
}

// confirmRemove asks before removing the highlighted server from its config.
func (v *serversView) confirmRemove() {
	sp, ok := v.selectedMCP()
	if !ok {
		return
	}
	be := v.be
	v.m.pushModal(NewConfirm(v.th, "Remove MCP server",
		fmt.Sprintf("Remove %s from the %s config? Its tools leave the next request.", sp.Name, sp.Scope), true,
		func() tea.Cmd {
			if err := be.RemoveMCPServer(sp.Name, sp.Scope); err != nil {
				return flashCmd("err", "Could not remove "+sp.Name+": "+err.Error())
			}
			return flashCmd("ok", "Removed "+sp.Name+".")
		}).WithLabels("Remove", "Cancel"))
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
	if s.Kind == "lsp" {
		tools = fg(th.TextDim).Render(v.lspDetail(s.Name))
	}
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

// lspDetail is the short tail of a language server's row: its process and
// open document count, or nothing when the row has no session.
func (v *serversView) lspDetail(name string) string {
	ls, ok := v.lsp[name]
	if !ok {
		return ""
	}
	if ls.PID > 0 {
		return fmt.Sprintf("pid %d · %d open", ls.PID, ls.OpenDocs)
	}
	return fmt.Sprintf("%d open", ls.OpenDocs)
}

// pingText is the Last ping row: the last answer and how long ago it came,
// or a hint when this view has not pinged the server yet.
func (v *serversView) pingText(name string) string {
	r, ok := v.pings[name]
	if !ok {
		return "not pinged yet; press p on the server in the list"
	}
	ago := relAge(r.at, time.Now())
	if r.err != nil {
		return "no answer " + ago + " ago: " + oneLine(r.err.Error())
	}
	return fmt.Sprintf("%s, %s ago", r.took.Round(time.Millisecond), ago)
}
