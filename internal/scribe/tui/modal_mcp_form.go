package tui

// The MCP server form: add a server or edit one. Stdio and http servers
// have different fields. Environment and headers are KEY=VALUE lines.

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// MCPServerSpec is an MCP server as the editor and the backend see it.
type MCPServerSpec struct {
	Name, Scope, Transport string // Scope: global | project; Transport: stdio | http
	Command                string
	Args                   []string
	URL                    string
	Env, Headers           map[string]string
	Disabled               bool
}

// Field ids of the MCP form. Which ones show depends on the transport.
const (
	mcpFName = iota
	mcpFTransport
	mcpFCommand
	mcpFURL
	mcpFArgs
	mcpFHeaders
	mcpFEnv
	mcpFScope
)

// mcpForm edits one server. save writes it (see Backend.SaveMCPServer).
type mcpForm struct {
	th   theme.Theme
	was  *MCPServerSpec
	save func(MCPServerSpec, *MCPServerSpec) error

	name, command, url, args, headers, env []rune
	http                                   bool
	project                                bool
	focus                                  int
	hint                                   string
}

// newMCPForm opens the form for a new server (was nil) or an existing one.
func newMCPForm(th theme.Theme, was *MCPServerSpec, save func(MCPServerSpec, *MCPServerSpec) error) *mcpForm {
	f := &mcpForm{th: th, was: was, save: save, focus: mcpFName}
	if was == nil {
		return f
	}
	f.name = []rune(was.Name)
	f.http = was.Transport == "http"
	f.project = was.Scope == "project"
	f.command = []rune(was.Command)
	f.url = []rune(was.URL)
	f.args = []rune(strings.Join(was.Args, " "))
	f.headers = []rune(kvText(was.Headers))
	f.env = []rune(kvText(was.Env))
	return f
}

// kvText renders a map as KEY=VALUE lines in key order.
func kvText(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+"="+m[k])
	}
	return strings.Join(lines, "\n")
}

// parseKV reads KEY=VALUE lines. Blank lines and lines starting with # are
// skipped. It returns nil for no entries.
func parseKV(text string) (map[string]string, error) {
	var out map[string]string
	for i, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("line %d needs KEY=VALUE", i+1)
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = strings.TrimSpace(v)
	}
	return out, nil
}

// visible lists the field ids shown for the current transport, in order.
func (f *mcpForm) visible() []int {
	if f.http {
		return []int{mcpFName, mcpFTransport, mcpFURL, mcpFHeaders, mcpFEnv, mcpFScope}
	}
	return []int{mcpFName, mcpFTransport, mcpFCommand, mcpFArgs, mcpFEnv, mcpFScope}
}

// multiline reports whether a field takes several lines.
func multiline(id int) bool { return id == mcpFEnv || id == mcpFHeaders }

// buffer returns the text buffer of a text field.
func (f *mcpForm) buffer(id int) *[]rune {
	switch id {
	case mcpFName:
		return &f.name
	case mcpFCommand:
		return &f.command
	case mcpFURL:
		return &f.url
	case mcpFArgs:
		return &f.args
	case mcpFHeaders:
		return &f.headers
	case mcpFEnv:
		return &f.env
	}
	return nil
}

// move changes focus by delta within the visible fields, wrapping.
func (f *mcpForm) move(delta int) {
	vis := f.visible()
	idx := 0
	for i, id := range vis {
		if id == f.focus {
			idx = i
		}
	}
	f.focus = vis[(idx+delta+len(vis))%len(vis)]
}

// Title implements Modal.
func (f *mcpForm) Title() string {
	if f.was == nil {
		return "Add MCP server"
	}
	return "Edit MCP server"
}

// Icon implements Iconed.
func (f *mcpForm) Icon() string { return "plug" }

// Footer implements Footered.
func (f *mcpForm) Footer() string {
	return "tab field · ^T transport · ^G scope · ^S save · Esc cancel"
}

// Size implements Sized.
func (f *mcpForm) Size(cols, rows int) (int, int) {
	return min(cols-4, 84), min(rows-4, 24)
}

// Update implements Modal.
func (f *mcpForm) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return f, nil, false
	}
	switch key := km.String(); key {
	case "esc":
		return f, nil, true
	case "tab", "down":
		f.move(1)
	case "shift+tab", "up":
		f.move(-1)
	case "ctrl+t":
		f.http = !f.http
		f.hint = ""
		f.ensureVisibleFocus()
	case "ctrl+g":
		f.project = !f.project
	case "left", "right":
		switch f.focus {
		case mcpFTransport:
			f.http = !f.http
			f.ensureVisibleFocus()
		case mcpFScope:
			f.project = !f.project
		}
	case "ctrl+s":
		return f.submit()
	case "enter":
		if multiline(f.focus) {
			*f.buffer(f.focus) = append(*f.buffer(f.focus), '\n')
			break
		}
		vis := f.visible()
		if f.focus == vis[len(vis)-1] {
			return f.submit()
		}
		f.move(1)
	case "backspace":
		if buf := f.buffer(f.focus); buf != nil {
			*buf = dropRunes(*buf)
		}
	case "ctrl+u":
		if buf := f.buffer(f.focus); buf != nil {
			*buf = nil
		}
	default:
		s := printable(km)
		if s == "" {
			break
		}
		if buf := f.buffer(f.focus); buf != nil {
			if multiline(f.focus) {
				*buf = append(*buf, []rune(s)...)
			} else {
				*buf = append(*buf, []rune(strings.ReplaceAll(s, "\n", " "))...)
			}
		}
	}
	return f, nil, false
}

// ensureVisibleFocus moves focus onto a field that exists for the transport.
func (f *mcpForm) ensureVisibleFocus() {
	for _, id := range f.visible() {
		if id == f.focus {
			return
		}
	}
	f.focus = mcpFTransport
}

// spec validates the form and builds the server it describes.
func (f *mcpForm) spec() (MCPServerSpec, error) {
	s := MCPServerSpec{Name: strings.TrimSpace(string(f.name)), Scope: "global", Transport: "stdio"}
	if f.project {
		s.Scope = "project"
	}
	if f.was != nil {
		s.Disabled = f.was.Disabled
	}
	if s.Name == "" {
		return s, errors.New("A name is needed.")
	}
	env, err := parseKV(string(f.env))
	if err != nil {
		return s, fmt.Errorf("Environment %v", err)
	}
	s.Env = env
	if f.http {
		s.Transport = "http"
		s.URL = strings.TrimSpace(string(f.url))
		if s.URL == "" {
			return s, errors.New("A URL is needed.")
		}
		headers, err := parseKV(string(f.headers))
		if err != nil {
			return s, fmt.Errorf("Headers %v", err)
		}
		s.Headers = headers
		return s, nil
	}
	s.Command = strings.TrimSpace(string(f.command))
	if s.Command == "" {
		return s, errors.New("A command is needed.")
	}
	s.Args = strings.Fields(string(f.args))
	return s, nil
}

// submit saves the server. Validation and backend errors stay in the form.
func (f *mcpForm) submit() (Modal, tea.Cmd, bool) {
	spec, err := f.spec()
	if err != nil {
		f.hint = err.Error()
		return f, nil, false
	}
	if err := f.save(spec, f.was); err != nil {
		f.hint = err.Error()
		return f, nil, false
	}
	return f, flashCmd("ok", fmt.Sprintf("Saved %s to the %s config.", spec.Name, spec.Scope)), true
}

// View implements Modal.
func (f *mcpForm) View(w, h int) string {
	th := f.th
	var rows []string
	for _, id := range f.visible() {
		rows = append(rows, f.rows(id, w)...)
	}
	footer := []string{""}
	switch {
	case f.hint != "":
		footer = append(footer, fg(th.Warning).Render(render.Truncate(f.hint, w)))
	case !f.project:
		footer = append(footer, fg(th.TextFaint).Render("Saved to the global config for every project."))
	default:
		footer = append(footer, fg(th.TextFaint).Render("Saved to .scribe/config.json. It starts only in a trusted project."))
	}
	avail := max(1, h-len(footer))
	if len(rows) > avail {
		rows = rows[:avail]
	}
	return strings.Join(fitLines(append(rows, footer...), w, h), "\n")
}

// rows draws one field: its label, then its value (several lines for
// environment and headers).
func (f *mcpForm) rows(id, w int) []string {
	th := f.th
	focused := id == f.focus
	label := func(s string) string {
		if focused {
			return fgb(th.Brand).Render(padRight(s, 11))
		}
		return fg(th.TextDim).Render(padRight(s, 11))
	}
	caret := ""
	if focused {
		caret = fg(th.Brand).Render("▌")
	}
	value := func(text, placeholder string) string {
		if text == "" && !focused {
			return fg(th.TextFaint).Render(placeholder)
		}
		if text == "" {
			return fg(th.TextFaint).Render(placeholder) + caret
		}
		return fg(th.TextPrimary).Render(render.Truncate(text, w-13)) + caret
	}
	switch id {
	case mcpFName:
		return []string{label("Name") + value(string(f.name), "unique id (e.g. github, postgres)")}
	case mcpFTransport:
		return []string{label("Transport") + f.choice(!f.http, "stdio", "http")}
	case mcpFCommand:
		return []string{label("Command") + value(string(f.command), "executable (e.g. npx, uvx, docker)")}
	case mcpFURL:
		return []string{label("URL") + value(string(f.url), "https://mcp.example.com/mcp")}
	case mcpFArgs:
		return []string{label("Arguments") + value(string(f.args), "space-separated (e.g. -y @modelcontextprotocol/server-github)")}
	case mcpFScope:
		return []string{label("Scope") + f.choice(!f.project, "global", "project")}
	case mcpFEnv:
		return f.multi(label("Environment"), string(f.env), "KEY=VALUE, one per line", focused, caret)
	case mcpFHeaders:
		return f.multi(label("Headers"), string(f.headers), "Header=value, one per line", focused, caret)
	}
	return nil
}

// multi draws a KEY=VALUE block: the label on the first line, the entries
// indented under it.
func (f *mcpForm) multi(label, text, placeholder string, focused bool, caret string) []string {
	th := f.th
	if text == "" {
		line := label + fg(th.TextFaint).Render(placeholder)
		if focused {
			line += caret
		}
		return []string{line}
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		prefix := strings.Repeat(" ", 11)
		if i == 0 {
			prefix = label
		}
		suffix := ""
		if focused && i == len(lines)-1 {
			suffix = caret
		}
		out = append(out, prefix+fg(th.TextPrimary).Render(render.Truncate(l, 60))+suffix)
	}
	return out
}

// choice draws a two-way choice with the active one bold.
func (f *mcpForm) choice(first bool, a, b string) string {
	th := f.th
	on := func(s string, active bool) string {
		if active {
			return fgb(th.Brand).Render("● " + s)
		}
		return fg(th.TextMuted).Render("○ " + s)
	}
	return on(a, first) + "   " + on(b, !first)
}
