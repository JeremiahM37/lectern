package tui

// The settings screen (/settings): five tabs over the config. Every change is
// written through Backend.SetConfig, so the Global or Project file is chosen
// with g / p and the backend decides what applies now.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

func init() {
	overrideCommands(&command{Name: "settings", Desc: "View and change settings", Group: "Settings",
		Run: func(m *Model, _ string) tea.Cmd { m.openSettingsScreen(); return nil }})
}

const (
	stGeneral = iota
	stProviders
	stRouter
	stAppearance
	stPermissions
	stTabCount
)

var stTabNames = []string{"General", "Providers", "Router", "Appearance", "Permissions"}

// setRow is one line of a settings tab. Rows are rebuilt from the backend
// on every draw, so they always show what is saved.
type setRow struct {
	label  string
	value  string
	note   string
	header bool   // section heading (Permissions); Enter and a add to it
	id     string // theme name on the Appearance tab
	act    func() tea.Cmd
	del    func() tea.Cmd
	add    func() tea.Cmd
}

// settings is the modal. It reads and writes through the backend of the tab
// that opened it.
type settings struct {
	m        *Model
	be       Backend
	tab      int
	cur      [stTabCount]int
	off      int
	project  bool   // save to the project config instead of the global one
	saved    string // theme kept; Esc reverts the live preview to it
	note     string
	noteKind string // ok | err | info
}

// openSettingsScreen opens the settings modal on the current tab's backend.
func (m *Model) openSettingsScreen() {
	s := &settings{m: m, be: m.cur().be, saved: m.themeName}
	for i, n := range theme.Names() {
		if n == m.themeName {
			s.cur[stAppearance] = i
		}
	}
	m.pushModal(s)
}

// Title implements Modal.
func (s *settings) Title() string { return "Settings" }

// Icon implements Iconed.
func (s *settings) Icon() string { return "gear" }

// Footer implements Footered.
func (s *settings) Footer() string {
	return "Tab tabs · ↑↓ move · Enter change · a add · d delete · g global · p project · Esc close"
}

// Size implements Sized.
func (s *settings) Size(cols, rows int) (int, int) { return min(84, cols-4), min(rows-4, 26) }

// Update implements Modal.
func (s *settings) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return s, nil, false
	}
	rows := s.rows()
	cur := s.cur[s.tab]
	switch km.String() {
	case "esc", "q":
		s.revertTheme()
		return s, nil, true
	case "tab":
		s.switchTab(1)
	case "shift+tab":
		s.switchTab(-1)
	case "up", "k":
		s.moveCursor(-1, len(rows))
		s.previewTheme()
	case "down", "j":
		s.moveCursor(1, len(rows))
		s.previewTheme()
	case "enter", " ":
		if cur < len(rows) && rows[cur].act != nil {
			return s, rows[cur].act(), false
		}
	case "a":
		if cur < len(rows) && rows[cur].add != nil {
			return s, rows[cur].add(), false
		}
	case "d":
		if cur < len(rows) && rows[cur].del != nil {
			return s, rows[cur].del(), false
		}
	case "g":
		s.project = false
		s.say("info", "Changes now save to the global config")
	case "p":
		s.project = true
		s.say("info", "Changes now save to this project's config")
	}
	return s, nil, false
}

func (s *settings) switchTab(d int) {
	s.tab = (s.tab + d + stTabCount) % stTabCount
	s.off = 0
	s.previewTheme()
}

func (s *settings) moveCursor(d, n int) {
	if n == 0 {
		return
	}
	s.cur[s.tab] = clamp(s.cur[s.tab]+d, 0, n-1)
}

// previewTheme applies the theme under the cursor on the Appearance tab.
func (s *settings) previewTheme() {
	if s.tab != stAppearance {
		return
	}
	rows := s.rows()
	if c := s.cur[s.tab]; c < len(rows) && rows[c].id != "" {
		if th, ok := theme.Lookup(rows[c].id); ok {
			s.m.setTheme(rows[c].id, th)
		}
	}
}

// revertTheme drops an uncommitted preview.
func (s *settings) revertTheme() {
	if s.m.themeName == s.saved {
		return
	}
	if th, ok := theme.Lookup(s.saved); ok {
		s.m.setTheme(s.saved, th)
	}
}

func (s *settings) say(kind, text string) { s.note, s.noteKind = text, kind }

func (s *settings) scope() string {
	if s.project {
		return "project"
	}
	return "global"
}

// set writes one setting to the chosen scope and applies it to the open tab.
func (s *settings) set(key string, value any) tea.Cmd {
	if err := s.be.SetConfig(key, value, s.project); err != nil {
		s.say("err", err.Error())
		return nil
	}
	s.afterSet(key, value)
	s.say("ok", fmt.Sprintf("%s saved to %s config", key, s.scope()))
	return nil
}

// afterSet applies the settings that the open tab shows straight away.
func (s *settings) afterSet(key string, value any) {
	t := s.m.cur()
	switch key {
	case "model":
		s.m.syncModel()
	case "mode":
		t.mode = t.be.Mode()
	case "verbose", "show_reasoning":
		b, _ := value.(bool)
		if key == "verbose" {
			t.copt.Verbose = b
		} else {
			t.copt.ShowReasoning = b
		}
		t.chat = rebuildTranscript(t.chat, t.copt, s.m.width, s.m.chatRows())
	case "nerd_font":
		if b, ok := value.(bool); ok {
			icons.SetNerd(b)
		}
	}
}

// syncModel refreshes the open tab after the model changed.
func (m *Model) syncModel() {
	t := m.cur()
	t.model = t.be.Model()
	t.local = isLocalModel(t.model)
}

// rows returns the current tab's rows.
func (s *settings) rows() []setRow {
	switch s.tab {
	case stProviders:
		return s.providerRows()
	case stRouter:
		return s.routerRows()
	case stAppearance:
		return s.appearanceRows()
	case stPermissions:
		return s.permissionRows()
	}
	return s.generalRows()
}

// ---------------------------------------------------------------- General

func (s *settings) generalRows() []setRow {
	c := s.be.Config()
	model := fmt.Sprint(c["model"])
	return []setRow{
		{label: "Default model", value: model, note: "picker",
			act: func() tea.Cmd {
				return s.pickModel("Default model", model, false, func(ref string) tea.Cmd { return s.set("model", ref) })
			}},
		s.choiceRow("Mode", "mode", fmt.Sprint(c["mode"]), agent.Modes, ""),
		s.choiceRow("Reasoning", "reasoning", reasoningOf(c), []string{"off", "low", "medium", "high"}, ""),
		s.intRow("Max tokens per response", "max_tokens", intOf(c, "max_tokens"), "0 = model default"),
		s.boolRow("Auto-compact", "auto_compact", c, true, "summarise when context fills"),
		s.floatRow("Compact at", "compact_at", floatOf(c, "compact_at", 0.8)),
		s.boolRow("Checkpoints", "checkpoints", c, true, "undo file edits per turn"),
		s.boolRow("Memory", "memory", c, true, "recall and the memory tool"),
		s.boolRow("Code map", "repo_map", c, true, "build the symbol map"),
		s.boolRow("Verbose tool rows", "verbose", c, false, "every tool call, in order"),
		s.boolRow("Show reasoning", "show_reasoning", c, true, ""),
		s.choiceRow("Chat style", "chat_style", fmt.Sprint(c["chat_style"]), []string{"accent", "bubble"}, ""),
		s.boolRow("Nerd Font icons", "nerd_font", c, false, "needs a Nerd Font in the terminal"),
		s.boolRow("Vim keys in the input", "vim", c, false, ""),
	}
}

// boolRow is an on/off setting that Enter flips.
func (s *settings) boolRow(label, key string, c map[string]any, def bool, note string) setRow {
	v := def
	if b, ok := c[key].(bool); ok {
		v = b
	}
	val := "off"
	if v {
		val = "on"
	}
	return setRow{label: label, value: val, note: note, act: func() tea.Cmd { return s.set(key, !v) }}
}

// choiceRow cycles through opts on Enter. Reasoning stores "" for off.
func (s *settings) choiceRow(label, key, cur string, opts []string, note string) setRow {
	shown := cur
	if shown == "" && key == "reasoning" {
		shown = "off"
	}
	return setRow{label: label, value: shown, note: note, act: func() tea.Cmd {
		next := opts[(listIndex(opts, shown)+1)%len(opts)]
		if key == "reasoning" && next == "off" {
			next = ""
		}
		return s.set(key, next)
	}}
}

func (s *settings) intRow(label, key string, cur int, note string) setRow {
	val := strconv.Itoa(cur)
	if cur == 0 {
		val = "default"
	}
	return setRow{label: label, value: val, note: note, act: func() tea.Cmd {
		return s.prompt(label, "a whole number", val, func(v string) tea.Cmd {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 0 {
				s.say("err", "enter a whole number, 0 for the default")
				return nil
			}
			return s.set(key, n)
		})
	}}
}

func (s *settings) floatRow(label, key string, cur float64) setRow {
	return setRow{label: label, value: fmt.Sprintf("%.0f%%", cur*100), note: "fraction of the window", act: func() tea.Cmd {
		return s.prompt(label, "0.5 to 0.95", strconv.FormatFloat(cur, 'f', -1, 64), func(v string) tea.Cmd {
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil || f < 0.3 || f > 0.95 {
				s.say("err", "enter a fraction between 0.3 and 0.95")
				return nil
			}
			return s.set(key, f)
		})
	}}
}

// prompt opens a single-line input over the settings screen.
func (s *settings) prompt(title, placeholder, value string, onSubmit func(string) tea.Cmd) tea.Cmd {
	p := NewTextPrompt(s.m.th, title, placeholder, false, onSubmit).WithValue(value)
	s.m.pushModal(p)
	return nil
}

func reasoningOf(c map[string]any) string {
	if v := fmt.Sprint(c["reasoning"]); v != "" && v != "<nil>" {
		return v
	}
	return ""
}

func intOf(c map[string]any, key string) int {
	n, _ := c[key].(int)
	return n
}

func floatOf(c map[string]any, key string, def float64) float64 {
	if f, ok := c[key].(float64); ok && f != 0 {
		return f
	}
	return def
}

// ---------------------------------------------------------------- Providers

func (s *settings) providerRows() []setRow {
	var rows []setRow
	for _, p := range s.be.Providers() {
		p := p
		rows = append(rows, setRow{label: p.Name, value: keyStatus(p), note: p.ID, act: func() tea.Cmd { return s.setKey(p) }})
	}
	return append(rows, setRow{label: "+ Add a custom OpenAI-compatible provider", note: "id, base URL, key variable",
		act: s.addCustomProvider})
}

// keyStatus is the Providers tab's status column.
func keyStatus(p ProviderStatus) string {
	switch {
	case p.Local:
		return "no key needed"
	case p.HasKey && strings.HasPrefix(p.KeySource, "env:"):
		return "✓ " + p.KeySource
	case p.HasKey && p.KeySource == "store":
		return "✓ stored"
	case p.HasKey:
		return "✓ set"
	}
	return "✗ no key"
}

// setKey asks for a provider's key. The input is masked; the key is never shown.
func (s *settings) setKey(p ProviderStatus) tea.Cmd {
	if p.Local {
		s.say("info", p.Name+" runs on this machine and needs no key")
		return nil
	}
	prompt := NewTextPrompt(s.m.th, "API key for "+p.Name, "paste the key", true, func(key string) tea.Cmd {
		if err := s.be.SetKey(p.ID, key); err != nil {
			s.say("err", err.Error())
			return nil
		}
		s.say("ok", "key saved for "+p.Name)
		return nil
	})
	prompt.icon = "key"
	s.m.pushModal(prompt)
	return nil
}

// addCustomProvider asks for the three fields of an OpenAI-compatible provider.
func (s *settings) addCustomProvider() tea.Cmd {
	var id, base string
	env := NewTextPrompt(s.m.th, "Custom provider: key variable", "e.g. MYPROXY_API_KEY, or blank for none", false, func(v string) tea.Cmd {
		if err := s.be.SetConfig("provider.add", map[string]any{"id": id, "base_url": base, "api_key_env": v}, s.project); err != nil {
			s.say("err", err.Error())
			return nil
		}
		s.say("ok", "provider "+id+" added; set its key on this tab")
		return nil
	}).AllowEmpty()
	url := NewTextPrompt(s.m.th, "Custom provider: base URL", "https://host/v1", false, func(v string) tea.Cmd {
		base = v
		s.m.pushModal(env)
		return nil
	})
	first := NewTextPrompt(s.m.th, "Custom provider: id", "short name, e.g. litellm", false, func(v string) tea.Cmd {
		id = v
		s.m.pushModal(url)
		return nil
	})
	s.m.pushModal(first)
	return nil
}

// pickModel opens a model chooser over every provider that has a key.
func (s *settings) pickModel(title, current string, inherit bool, pick func(string) tea.Cmd) tea.Cmd {
	mc, cmd := newModelChooser(s.m, title, "", inherit, current, pick)
	s.m.pushModal(mc)
	return cmd
}

// ---------------------------------------------------------------- Router

func (s *settings) routerRows() []setRow {
	c := s.be.Config()
	slot := func(label, key string) setRow {
		cur, _ := c[key].(string)
		val := cur
		if val == "" {
			val = "inherit default"
		}
		return setRow{label: label, value: val, note: "picker, d clears", id: "",
			act: func() tea.Cmd {
				return s.pickModel(label, cur, true, func(ref string) tea.Cmd { return s.set(key, ref) })
			},
			del: func() tea.Cmd { return s.set(key, "") }}
	}
	fallback, _ := c["router.fallback"].([]string)
	fb := strings.Join(fallback, ", ")
	if fb == "" {
		fb = "none"
	}
	return []setRow{
		slot("Explore agents (read-only)", "router.explore"),
		slot("Code agents (editing)", "router.code"),
		slot("Web search", "router.web_search"),
		slot("Compaction", "router.compact"),
		slot("Session titles and commits", "router.title"),
		{label: "Fallback chain", value: fb, note: "comma separated, tried in order",
			act: func() tea.Cmd {
				return s.prompt("Fallback chain", "provider/model, provider/model", strings.Join(fallback, ", "), func(v string) tea.Cmd {
					return s.set("router.fallback", v)
				})
			}},
		{label: "Concurrent agents", value: strconv.Itoa(maxAgents(c)), note: "2 to 8",
			act: func() tea.Cmd {
				return s.prompt("Concurrent agents", "2 to 8", strconv.Itoa(maxAgents(c)), func(v string) tea.Cmd {
					n, err := strconv.Atoi(strings.TrimSpace(v))
					if err != nil || n < 2 || n > 8 {
						s.say("err", "enter a number from 2 to 8")
						return nil
					}
					return s.set("router.max_agents", n)
				})
			}},
	}
}

func maxAgents(c map[string]any) int {
	if n := intOf(c, "router.max_agents"); n > 0 {
		return n
	}
	return 3
}

// ---------------------------------------------------------------- Appearance

func (s *settings) appearanceRows() []setRow {
	var rows []setRow
	for _, name := range theme.Names() {
		name := name
		val := ""
		if name == s.saved {
			val = "current"
		}
		rows = append(rows, setRow{label: name, value: val, id: name,
			act: func() tea.Cmd {
				s.be.SetTheme(name)
				s.saved = name
				s.say("ok", "theme saved: "+name)
				return nil
			}})
	}
	return rows
}

// ---------------------------------------------------------------- Permissions

func (s *settings) permissionRows() []setRow {
	c := s.be.Config()
	allow, _ := c["allow"].([]string)
	deny, _ := c["deny"].([]string)
	rows := s.listRows("Allow without asking", "allow", allow, "command prefix, e.g. go test")
	return append(rows, s.listRows("Always refuse", "deny", deny, "command prefix, e.g. rm -rf")...)
}

// listRows is one permissions section. The heading adds an entry; d deletes one.
func (s *settings) listRows(title, key string, items []string, placeholder string) []setRow {
	add := func() tea.Cmd {
		return s.prompt(title, placeholder, "", func(v string) tea.Cmd {
			v = strings.TrimSpace(v)
			if v == "" || listIndex(items, v) >= 0 {
				return nil
			}
			return s.set(key, append(append([]string(nil), items...), v))
		})
	}
	rows := []setRow{{label: title, value: strconv.Itoa(len(items)), header: true, act: add, add: add}}
	for i := range items {
		i := i
		rows = append(rows, setRow{label: items[i], value: key, add: add,
			del: func() tea.Cmd {
				return s.set(key, append(append([]string(nil), items[:i]...), items[i+1:]...))
			}})
	}
	return rows
}

// ---------------------------------------------------------------- drawing

// View implements Modal.
func (s *settings) View(w, h int) string {
	th := s.m.th
	rows := s.rows()
	cur := clamp(s.cur[s.tab], 0, max(0, len(rows)-1))
	avail := max(1, h-4) // tab bar, scope line, blank line, note line
	s.off = keepInView(cur, s.off, avail)
	s.off = clamp(s.off, 0, max(0, len(rows)-avail))

	var out []string
	out = append(out, ansi.Truncate(modalTabs(th, stTabNames, s.tab), w, ""))
	out = append(out, s.scopeLine(th))
	out = append(out, "")
	end := min(len(rows), s.off+avail)
	for i := s.off; i < end; i++ {
		out = append(out, s.rowLine(rows[i], i == cur, w))
	}
	if len(rows) == 0 {
		out = append(out, fg(th.TextFaint).Render("Nothing here yet."))
	}
	out = fitLines(out, w, h-1)
	return strings.Join(append(out, s.noteLine(w, len(rows), avail)), "\n")
}

// scopeLine shows where changes are saved.
func (s *settings) scopeLine(th theme.Theme) string {
	global, project := fg(th.TextFaint), fg(th.TextFaint)
	if s.project {
		project = lipgloss.NewStyle().Bold(true).Foreground(th.Brand)
	} else {
		global = lipgloss.NewStyle().Bold(true).Foreground(th.Brand)
	}
	return fg(th.TextMuted).Render("Saving to ") + global.Render("[ Global ]") + " " + project.Render("[ Project ]") +
		fg(th.TextFaint).Render("   g / p to switch")
}

// rowLine draws one row: marker, label, and value right-aligned.
func (s *settings) rowLine(r setRow, sel bool, w int) string {
	th := s.m.th
	mark := "  "
	if sel {
		mark = fg(th.Brand).Render("› ")
	}
	var label string
	switch {
	case r.header:
		label = lipgloss.NewStyle().Bold(true).Foreground(th.TextMuted).Render(r.label)
	case sel:
		label = fgb(th.TextPrimary).Render(r.label)
	default:
		label = fg(th.TextSecondary).Render(r.label)
	}
	val := ""
	if r.value != "" {
		color := th.TextPrimary
		switch {
		case strings.HasPrefix(r.value, "✗"):
			color = th.TextFaint
		case strings.HasPrefix(r.value, "✓"):
			color = th.Success
		}
		val = fg(color).Render(r.value)
	}
	if r.note != "" {
		val += fg(th.TextFaint).Render("  " + r.note)
	}
	return spaceBetween(mark+label, val, w)
}

// noteLine is the last line: the last result, or a scroll position.
func (s *settings) noteLine(w, n, avail int) string {
	th := s.m.th
	if s.note != "" {
		color := th.TextSecondary
		switch s.noteKind {
		case "ok":
			color = th.Success
		case "err":
			color = th.Error
		}
		return fitWidth(fg(color).Render(s.note), w)
	}
	if n > avail {
		return fitWidth(fg(th.TextFaint).Render(fmt.Sprintf("%d–%d of %d", s.off+1, min(n, s.off+avail), n)), w)
	}
	return fitWidth("", w)
}

// ---------------------------------------------------------------- model chooser

// chooserMsg carries one provider's model list to the chooser that asked.
type chooserMsg struct {
	ch       *modelChooser
	provider string
	models   []llm.ModelInfo
	err      error
}

// modelChooser is a Picker over the models of every provider with a key (or
// of one provider when only is set). It is used for the default model, the
// router slots and the wizard's model step.
type modelChooser struct {
	*Picker
	m         *Model
	be        Backend
	only      string
	inherit   bool
	current   string
	preselect bool
	pick      func(ref string) tea.Cmd
	order     []ProviderStatus
	models    map[string][]llm.ModelInfo
	errs      map[string]error
	loads     map[string]bool
}

// newModelChooser builds the chooser and returns the command that loads the
// models. pick receives the chosen ref ("" for inherit).
func newModelChooser(m *Model, title, only string, inherit bool, current string, pick func(string) tea.Cmd) (*modelChooser, tea.Cmd) {
	be := m.cur().be
	mc := &modelChooser{m: m, be: be, only: only, inherit: inherit, current: current, preselect: true,
		pick: pick, models: map[string][]llm.ModelInfo{}, errs: map[string]error{}, loads: map[string]bool{}}
	if mc.pick == nil {
		mc.pick = func(string) tea.Cmd { return nil }
	}
	mc.Picker = NewPicker(m.th, title, "model")
	mc.Picker.Hint = "↑↓ move · Enter select · / filter · Esc close"
	mc.Picker.Empty = "No models — add a key on the Providers tab"
	mc.Picker.OnSelect = func(it PickItem) tea.Cmd { return mc.pick(it.Value) }
	for _, p := range be.Providers() {
		if only == "" || p.ID == only {
			mc.order = append(mc.order, p)
		}
	}
	var cmds []tea.Cmd
	for _, p := range mc.order {
		if p.HasKey || p.Local {
			cmds = append(cmds, mc.fetch(p.ID))
		}
	}
	mc.Picker.Loading = len(cmds) > 0
	mc.rebuild()
	return mc, tea.Batch(cmds...)
}

func (mc *modelChooser) fetch(provider string) tea.Cmd {
	mc.loads[provider] = true
	be := mc.be
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		ms, err := be.Models(ctx, provider)
		return chooserMsg{ch: mc, provider: provider, models: ms, err: err}
	}
}

// Update implements Modal: model lists are absorbed, everything else goes to the Picker.
func (mc *modelChooser) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	if r, ok := msg.(chooserMsg); ok {
		if r.ch != mc {
			return mc, nil, false
		}
		delete(mc.loads, r.provider)
		if r.err != nil {
			mc.errs[r.provider] = r.err
		} else {
			mc.models[r.provider] = r.models
		}
		mc.rebuild()
		mc.Picker.Loading = len(mc.loads) > 0
		return mc, nil, false
	}
	_, cmd, done := mc.Picker.Update(msg)
	return mc, cmd, done
}

// View implements Modal.
func (mc *modelChooser) View(w, h int) string {
	mc.Picker.Spin = render.Frame(render.Braille, mc.m.frame/3)
	return mc.Picker.View(w, h)
}

// rebuild recomputes the rows from the lists fetched so far.
func (mc *modelChooser) rebuild() {
	var items []PickItem
	if mc.inherit {
		items = append(items, PickItem{Value: "", Label: "inherit the default model", Current: mc.current == ""})
	}
	for _, p := range mc.order {
		group := p.Name
		switch {
		case !p.HasKey && !p.Local:
			items = append(items, PickItem{Value: "key:" + p.ID, Label: "no key yet; add one on the Providers tab", Group: group, Disabled: true})
			continue
		case mc.errs[p.ID] != nil:
			items = append(items, PickItem{Value: "err:" + p.ID, Label: "error: " + mc.errs[p.ID].Error(), Group: group, Disabled: true})
			continue
		}
		ms, ok := mc.models[p.ID]
		if !ok {
			items = append(items, PickItem{Value: "wait:" + p.ID, Label: "loading models…", Group: group, Disabled: true})
			continue
		}
		for _, mi := range ms {
			ref := p.ID + "/" + mi.ID
			label := mi.Name
			if label == "" {
				label = mi.ID
			}
			desc := ""
			if mi.ContextWindow > 0 {
				desc = render.HumanTokens(mi.ContextWindow) + " ctx"
			}
			items = append(items, PickItem{Value: ref, Label: label, Desc: desc, Group: group, Current: ref == mc.current})
		}
	}
	mc.Picker.SetItems(items)
	if mc.preselect && mc.current != "" {
		if indexOfItem(items, mc.current) >= 0 {
			mc.Picker.SelectValue(mc.current)
			mc.preselect = false
		} else if len(mc.loads) == 0 {
			mc.preselect = false
		}
	}
}

func indexOfItem(items []PickItem, value string) int {
	for i, it := range items {
		if it.Value == value {
			return i
		}
	}
	return -1
}

// listIndex returns the position of v in list, or -1 when it is absent.
func listIndex(list []string, v string) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return -1
}
