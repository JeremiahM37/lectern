package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// SessionDeleter is optionally implemented by a Backend that can delete a
// saved session (session picker, Ctrl+D). Checked by type assertion so the
// Backend interface stays unchanged.
type SessionDeleter interface{ DeleteSession(id string) error }

// SessionRenamer is optionally implemented by a Backend that can rename any
// saved session, not only the current one (session picker, Ctrl+R).
type SessionRenamer interface{ RenameSession(id, title string) error }

// modelsMsg carries the models of one provider from a background fetch.
type modelsMsg struct {
	tab      int
	provider string
	models   []llm.ModelInfo
	err      error
}

// openInfo shows read-only text in a scrollable modal.
func (m *Model) openInfo(title, icon string, lines []string) {
	m.pushModal(NewInfoView(m.th, title, icon, lines))
}

// ---------------------------------------------------------------- palette

// openPalette is Ctrl+K: every command, grouped, filtered as you type.
func (m *Model) openPalette() {
	p := NewPicker(m.th, "Command Palette", "lightning")
	p.TypeToFilter = true
	p.Hint = "type to filter · ↑↓ move · Enter run · Esc clear / close"
	p.Empty = "No commands match — try a different query"
	cmds := allCommands()
	items := make([]PickItem, 0, len(cmds))
	for _, c := range cmds {
		label := "/" + c.Name
		if c.Args != "" {
			label += " " + c.Args
		}
		items = append(items, PickItem{Value: c.Name, Label: label, Desc: c.Desc, Group: c.Group})
	}
	p.Items = items
	p.refreshFilter()
	p.OnSelect = func(it PickItem) tea.Cmd {
		c, _ := findCommand(it.Value)
		if c == nil {
			return nil
		}
		if c.Args != "" {
			m.cur().in.setValue("/" + c.Name + " ")
			return nil
		}
		return m.runCommand("/" + c.Name)
	}
	m.pushModal(p)
}

// refreshFilter re-applies the picker's filter after Items changed.
func (p *Picker) refreshFilter() { p.refilter() }

// ---------------------------------------------------------------- help

// openHelp shows keys and commands.
func (m *Model) openHelp() {
	lines := []string{
		"Keys",
		"  Enter               send (queues a steering message while Scribe works)",
		"  Shift+Enter, Ctrl+J newline",
		"  Up / Down           history (on the first or last line)",
		"  Ctrl+R              search history",
		"  Alt+S / Alt+P       stash draft / pop it back",
		"  Tab, Right          accept a completion",
		"  @path               mention a file",
		"  !command            run a shell command",
		"  ?                   this help (on an empty input)",
		"",
		"Panels",
		"  Ctrl+K              command palette",
		"  Ctrl+L              model picker",
		"  Ctrl+P              sessions",
		"  Ctrl+G              git menu",
		"  Ctrl+S              skills",
		"  Ctrl+O              expand or collapse tool output and reasoning",
		"  Alt+R               error log",
		"",
		"Modes",
		"  Ctrl+D or Shift+Tab cycle: default, auto, plan, architect, socratic, challenge",
		"",
		"Control",
		"  Esc                 close the top window, else stop the running turn",
		"  Ctrl+X              stop the running turn",
		"  Ctrl+C              clear the input; press twice to quit",
		"  Ctrl+B / Ctrl+F     step through checkpoints",
		"  Ctrl+Z              undo the last turn",
		"  PgUp / PgDn         scroll the transcript; mouse wheel too",
		"",
		"Tabs",
		"  Ctrl+T new tab      Ctrl+W close      Alt+1..5 switch",
		"  Ctrl+[ / Ctrl+]     previous or next tab",
		"",
		"Commands",
	}
	for _, c := range allCommands() {
		name := "/" + c.Name
		if c.Args != "" {
			name += " " + c.Args
		}
		lines = append(lines, fmt.Sprintf("  %-28s %s", name, c.Desc))
	}
	m.openInfo("Scribe help", "info", lines)
}

// ---------------------------------------------------------------- model

// modelPicker is the Ctrl+L picker. It embeds the generic Picker and also
// receives model lists fetched in the background.
type modelPicker struct {
	*Picker
	m      *Model
	tab    *tab
	models map[string][]llm.ModelInfo
	errs   map[string]error
	loads  map[string]bool
	order  []string // providers in display order
}

// openModelPicker is Ctrl+L.
func (m *Model) openModelPicker() tea.Cmd {
	t := m.cur()
	mp := &modelPicker{m: m, tab: t, models: map[string][]llm.ModelInfo{}, errs: map[string]error{}, loads: map[string]bool{}}
	mp.Picker = NewPicker(m.th, "Select Model", "model")
	mp.Picker.Hint = "↑↓ move · Enter select · k add key · / filter · Esc close"
	mp.Picker.Empty = "No models — check your provider keys with /keys"
	mp.Picker.OnKey = func(key string) (tea.Cmd, bool) {
		if key != "k" {
			return nil, false
		}
		it, ok := mp.Picker.Selected()
		if !ok {
			return nil, false
		}
		return mp.openKey(providerFromValue(it.Value)), true
	}
	mp.Picker.OnSelect = func(it PickItem) tea.Cmd {
		if strings.HasPrefix(it.Value, "key:") {
			return mp.openKey(strings.TrimPrefix(it.Value, "key:"))
		}
		return m.setModel(it.Value)
	}
	mp.rebuild()
	var cmds []tea.Cmd
	for _, p := range t.be.Providers() {
		mp.order = append(mp.order, p.ID)
		if p.HasKey || p.Local {
			cmds = append(cmds, mp.fetch(p.ID))
		}
	}
	mp.Picker.Loading = len(cmds) > 0
	m.pushModal(mp)
	return tea.Batch(cmds...)
}

// fetch loads one provider's models in the background.
func (mp *modelPicker) fetch(provider string) tea.Cmd {
	mp.loads[provider] = true
	be := mp.tab.be
	id := mp.tab.id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		ms, err := be.Models(ctx, provider)
		return modelsMsg{tab: id, provider: provider, models: ms, err: err}
	}
}

// openKey asks for an API key for provider and saves it.
func (mp *modelPicker) openKey(provider string) tea.Cmd {
	m := mp.m
	name := provider
	for _, p := range mp.tab.be.Providers() {
		if p.ID == provider {
			name = p.Name
		}
	}
	prompt := NewTextPrompt(m.th, "API key for "+name, "paste the key", true, func(key string) tea.Cmd {
		if err := mp.tab.be.SetKey(provider, key); err != nil {
			return errFlash(err)
		}
		delete(mp.errs, provider)
		mp.rebuild()
		return tea.Batch(mp.fetch(provider), flashCmd("ok", "Key saved for "+name))
	})
	prompt.icon = "key"
	m.pushModal(prompt)
	return nil
}

func providerFromValue(v string) string {
	if strings.HasPrefix(v, "key:") {
		return strings.TrimPrefix(v, "key:")
	}
	return providerOf(v)
}

// View refreshes the spinner, then draws the picker.
func (mp *modelPicker) View(w, h int) string {
	mp.Picker.Spin = render.Frame(render.Braille, mp.m.frame/3)
	return mp.Picker.View(w, h)
}

// Update intercepts model results and passes everything else to the picker.
func (mp *modelPicker) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	if r, ok := msg.(modelsMsg); ok && r.tab == mp.tab.id {
		delete(mp.loads, r.provider)
		if r.err != nil {
			mp.errs[r.provider] = r.err
		} else {
			mp.models[r.provider] = r.models
		}
		mp.rebuild()
		mp.Picker.Loading = len(mp.loads) > 0
		return mp, nil, false
	}
	_, cmd, done := mp.Picker.Update(msg)
	return mp, cmd, done
}

// rebuild recomputes the picker rows from what is known so far.
func (mp *modelPicker) rebuild() {
	t := mp.tab
	cur := t.be.Model()
	var items []PickItem
	provs := t.be.Providers()
	name := map[string]string{}
	for _, p := range provs {
		name[p.ID] = p.Name
	}
	for _, p := range provs {
		group := p.Name
		if !p.HasKey && !p.Local {
			items = append(items, PickItem{Value: "key:" + p.ID, Label: "add a key", Desc: "press k or Enter", Group: group, Icon: "key"})
			continue
		}
		if err, bad := mp.errs[p.ID]; bad {
			items = append(items, PickItem{Value: "err:" + p.ID, Label: "error: " + err.Error(), Group: group, Disabled: true})
			continue
		}
		ms, ok := mp.models[p.ID]
		if !ok {
			items = append(items, PickItem{Value: "wait:" + p.ID, Label: "loading models…", Group: group, Disabled: true})
			continue
		}
		for _, mi := range ms {
			ref := p.ID + "/" + mi.ID
			desc := ""
			if mi.ContextWindow > 0 {
				desc = render.HumanTokens(mi.ContextWindow) + " ctx"
			}
			if p.Local {
				desc = strings.TrimSpace("local " + desc)
			}
			label := mi.Name
			if label == "" {
				label = mi.ID
			}
			items = append(items, PickItem{Value: ref, Label: label, Desc: desc, Group: group, Current: ref == cur})
		}
	}
	mp.Picker.Items = items
	mp.Picker.refilter()
	mp.Picker.SelectValue(cur)
}

// ---------------------------------------------------------------- keys

// openKeyPicker lists providers that need a key.
func (m *Model) openKeyPicker() {
	p := NewPicker(m.th, "Provider keys", "key")
	p.Hint = "↑↓ move · Enter set key · Esc close"
	var items []PickItem
	for _, pv := range m.cur().be.Providers() {
		desc := "key set"
		if !pv.HasKey {
			desc = "no key"
		}
		if pv.Local {
			desc = "local, no key needed"
		}
		items = append(items, PickItem{Value: pv.ID, Label: pv.Name, Desc: desc, Current: pv.HasKey})
	}
	p.Items = items
	p.refilter()
	be := m.cur().be
	p.OnSelect = func(it PickItem) tea.Cmd {
		name := it.Label
		tp := NewTextPrompt(m.th, "API key for "+name, "paste the key", true, func(key string) tea.Cmd {
			if err := be.SetKey(it.Value, key); err != nil {
				return errFlash(err)
			}
			return flashCmd("ok", "Key saved for "+name)
		})
		tp.icon = "key"
		m.pushModal(tp)
		return nil
	}
	m.pushModal(p)
}

// ---------------------------------------------------------------- mode

// openModePicker lists the forge modes.
func (m *Model) openModePicker() {
	p := NewPicker(m.th, "Mode", "cog")
	cur := m.cur().mode
	for _, id := range modeOrder {
		mi := modeStyle(id, m.th)
		p.Items = append(p.Items, PickItem{Value: id, Label: strings.ToLower(mi.label), Desc: mi.desc, Current: id == cur, Color: mi.color})
	}
	p.refilter()
	p.OnSelect = func(it PickItem) tea.Cmd { return m.setMode(it.Value) }
	m.pushModal(p)
}

// ---------------------------------------------------------------- sessions

// sessionItems lists saved sessions, newest first.
func (m *Model) sessionItems() []PickItem {
	t := m.cur()
	now := time.Now()
	var items []PickItem
	list := t.be.Sessions()
	sort.SliceStable(list, func(i, j int) bool { return list[i].Updated.After(list[j].Updated) })
	for _, s := range list {
		title := s.Title
		if title == "" {
			title = short(s.ID)
		}
		desc := fmt.Sprintf("%d turns · $%.2f · %s", s.Turns, s.CostUSD, render.RelTime(s.Updated, now))
		items = append(items, PickItem{Value: s.ID, Label: title, Desc: desc, Current: s.ID == t.be.SessionID()})
	}
	return items
}

// openSessionPicker is Ctrl+P.
func (m *Model) openSessionPicker() {
	t := m.cur()
	p := NewPicker(m.th, "Sessions", "clock_alt")
	p.Hint = "Enter resume · ^R rename · ^D delete · Esc close"
	p.Empty = "No sessions yet — start chatting!"
	p.Items = m.sessionItems()
	p.refilter()
	p.Preview = func(it PickItem) []string {
		for _, s := range t.be.Sessions() {
			if s.ID == it.Value {
				lines := []string{s.Title, "", "id       " + s.ID, "model    " + s.Model, "turns    " + fmt.Sprint(s.Turns), ""}
				if s.Preview != "" {
					lines = append(lines, "first message:", render.Wrap(s.Preview, 40))
				}
				return lines
			}
		}
		return nil
	}
	p.OnSelect = func(it PickItem) tea.Cmd { return m.resumeSession(it.Value) }
	p.OnKey = func(key string) (tea.Cmd, bool) {
		it, ok := p.Selected()
		if !ok {
			return nil, false
		}
		switch key {
		case "ctrl+d":
			del, can := t.be.(SessionDeleter)
			if !can {
				return flashCmd("info", "This backend cannot delete sessions"), true
			}
			m.pushModal(NewConfirm(m.th, "Delete session?", "“"+it.Label+"” will be removed. This cannot be undone.", true, func() tea.Cmd {
				if err := del.DeleteSession(it.Value); err != nil {
					return errFlash(err)
				}
				p.SetItems(m.sessionItems())
				return flashCmd("ok", "Deleted "+it.Label)
			}))
			return nil, true
		case "ctrl+r":
			m.pushModal(NewTextPrompt(m.th, "Rename session", "New title", false, func(v string) tea.Cmd {
				if rn, can := t.be.(SessionRenamer); can {
					if err := rn.RenameSession(it.Value, v); err != nil {
						return errFlash(err)
					}
				} else if it.Value == t.be.SessionID() {
					if err := t.be.Rename(v); err != nil {
						return errFlash(err)
					}
				} else {
					return flashCmd("info", "This backend cannot rename other sessions")
				}
				p.SetItems(m.sessionItems())
				return flashCmd("ok", "Renamed")
			}).WithValue(it.Label))
			return nil, true
		}
		return nil, false
	}
	m.pushModal(p)
}

// ---------------------------------------------------------------- theme

// openThemePicker previews each theme as the cursor moves.
func (m *Model) openThemePicker() {
	original := m.themeName
	p := NewPicker(m.th, "Theme", "lightning")
	p.Hint = "↑↓ preview · Enter keep · Esc revert"
	for _, name := range theme.Names() {
		p.Items = append(p.Items, PickItem{Value: name, Label: name, Current: name == original})
	}
	p.refilter()
	p.SelectValue(original)
	p.OnCursor = func(it PickItem) tea.Cmd {
		if th, ok := theme.Lookup(it.Value); ok {
			m.setTheme(it.Value, th)
			p.SetTheme(th)
		}
		return nil
	}
	p.OnSelect = func(it PickItem) tea.Cmd {
		m.cur().be.SetTheme(it.Value)
		return flashCmd("ok", "Theme: "+it.Value)
	}
	p.OnCancel = func() {
		if th, ok := theme.Lookup(original); ok {
			m.setTheme(original, th)
		}
	}
	m.pushModal(p)
}

// ---------------------------------------------------------------- tabs

// openTabPicker lists open tabs.
func (m *Model) openTabPicker() {
	p := NewPicker(m.th, "Tabs", "tabs")
	for i, t := range m.tabs {
		desc := t.mode
		if t.busy {
			desc += " · working"
		}
		p.Items = append(p.Items, PickItem{Value: itoa(i), Label: fmt.Sprintf("%d  %s", i+1, t.name), Desc: desc, Current: i == m.active})
	}
	p.refilter()
	p.OnSelect = func(it PickItem) tea.Cmd {
		n := 0
		fmt.Sscanf(it.Value, "%d", &n)
		m.switchTab(n)
		return nil
	}
	m.pushModal(p)
}

// ---------------------------------------------------------------- git

// openGitMenu offers status, diff and commit.
func (m *Model) openGitMenu() {
	t := m.cur()
	p := NewPicker(m.th, "Git", "git")
	p.Hint = "↑↓ move · Enter select · Esc close"
	st := t.be.Git()
	branch := st.Branch
	if branch == "" {
		branch = "(not a git repository)"
	}
	dirty := "clean"
	if st.Dirty {
		dirty = fmt.Sprintf("%d changed", len(st.Changed))
	}
	p.Items = []PickItem{
		{Value: "status", Label: "Status", Desc: branch + " · " + dirty, Icon: "git"},
		{Value: "diff", Label: "Diff", Desc: "working tree", Icon: "diff_add"},
		{Value: "commit", Label: "Commit…", Desc: "suggests a message", Icon: "check"},
	}
	p.refilter()
	p.OnSelect = func(it PickItem) tea.Cmd {
		switch it.Value {
		case "status":
			lines := []string{"Branch: " + branch}
			if st.Ahead > 0 || st.Behind > 0 {
				lines = append(lines, fmt.Sprintf("Ahead %d, behind %d", st.Ahead, st.Behind))
			}
			lines = append(lines, "")
			for _, f := range st.Changed {
				lines = append(lines, "  "+f)
			}
			if len(st.Changed) == 0 {
				lines = append(lines, "Nothing changed.")
			}
			m.openInfo("Git status", "git", lines)
		case "diff":
			d := t.be.GitDiff()
			if strings.TrimSpace(d) == "" {
				d = "No changes in the working tree."
			}
			m.openInfo("Git diff", "diff", strings.Split(d, "\n"))
		case "commit":
			return m.commitPrompt()
		}
		return nil
	}
	m.pushModal(p)
}

// openCommitPrompt opens the commit message box, pre-filled with a suggestion.
func (m *Model) openCommitPrompt(suggestion string) {
	be := m.cur().be
	tp := NewTextPrompt(m.th, "Commit message", "describe the change", false, func(v string) tea.Cmd {
		return m.gitCommitWith(be, v)
	}).WithValue(suggestion)
	tp.icon = "git"
	tp.WithHint("Enter commits what is staged.")
	m.pushModal(tp)
}

func (m *Model) gitCommitWith(be Backend, msg string) tea.Cmd {
	return func() tea.Msg {
		if err := be.GitCommit(msg); err != nil {
			return flashMsg{kind: "err", text: "Commit failed: " + err.Error()}
		}
		return flashMsg{kind: "ok", text: "Committed"}
	}
}

// ---------------------------------------------------------------- info screens

// openSkills lists the skills the agent can use.
func (m *Model) openSkills() {
	names := m.cur().be.Skills()
	lines := []string{"Skills available to Scribe:", ""}
	for _, n := range names {
		lines = append(lines, "  "+n)
	}
	if len(names) == 0 {
		lines = append(lines, "  none installed")
	}
	m.openInfo("Skills", "skills", lines)
}

// openTools lists the agent's tools and lets you turn them on and off.
func (m *Model) openTools() {
	t := m.cur()
	p := NewPicker(m.th, "Tools", "wrench")
	p.Hint = "↑↓ move · Enter toggle · Esc close"
	p.KeepOpen = true
	build := func() []PickItem {
		var items []PickItem
		for _, ti := range t.be.Tools() {
			desc := ti.Source
			if ti.ReadOnly {
				desc += " · read only"
			}
			items = append(items, PickItem{Value: ti.Name, Label: ti.Name, Desc: desc + " · " + ti.Description, Group: ti.Source, Current: ti.Enabled})
		}
		return items
	}
	p.Items = build()
	p.refilter()
	p.OnSelect = func(it PickItem) tea.Cmd {
		t.be.SetToolEnabled(it.Value, !it.Current)
		p.SetItems(build())
		return nil
	}
	m.pushModal(p)
}

// openServers lists MCP servers or language servers.
func (m *Model) openServers(kind string) {
	var lines []string
	for _, s := range m.cur().be.Servers() {
		if s.Kind != kind {
			continue
		}
		line := fmt.Sprintf("%-20s %-9s tools %d", s.Name, s.State, s.Tools)
		if s.Error != "" {
			line += "  " + s.Error
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		lines = []string{"None configured."}
	}
	title := strings.ToUpper(kind)
	m.openInfo(title+" servers", kind, lines)
}

// openCheckpoints lists turns that changed files.
func (m *Model) openCheckpoints() {
	cps := m.cur().be.Checkpoints()
	var lines []string
	for _, cp := range cps {
		mark := "●"
		if cp.Undone {
			mark = "○"
		}
		lines = append(lines, fmt.Sprintf("%s turn %-3d %d files  %s", mark, cp.Turn, len(cp.Files), oneLine(cp.Prompt)))
	}
	if len(lines) == 0 {
		lines = []string{"No checkpoints yet. A turn that edits files creates one."}
	}
	m.openInfo("Checkpoints", "checkpoint", lines)
}

// openSettings lists the flat config map; booleans toggle, others open a prompt.
func (m *Model) openSettings() {
	t := m.cur()
	p := NewPicker(m.th, "Settings", "gear")
	p.KeepOpen = true
	p.Hint = "↑↓ move · Enter change · Esc close"
	build := func() []PickItem {
		cfg := t.be.Config()
		keys := make([]string, 0, len(cfg))
		for k := range cfg {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var items []PickItem
		for _, k := range keys {
			items = append(items, PickItem{Value: k, Label: k, Desc: fmt.Sprint(cfg[k])})
		}
		return items
	}
	p.Items = build()
	p.refilter()
	p.OnSelect = func(it PickItem) tea.Cmd {
		cfg := t.be.Config()
		if b, ok := cfg[it.Value].(bool); ok {
			if err := t.be.SetConfig(it.Value, !b, false); err != nil {
				return errFlash(err)
			}
			p.SetItems(build())
			return nil
		}
		m.pushModal(NewTextPrompt(m.th, it.Value, "new value", false, func(v string) tea.Cmd {
			if err := t.be.SetConfig(it.Value, v, false); err != nil {
				return errFlash(err)
			}
			p.SetItems(build())
			return flashCmd("ok", it.Value+" saved")
		}).WithValue(fmt.Sprint(cfg[it.Value])))
		return nil
	}
	m.pushModal(p)
}

// openErrorLog lists errors from this session, newest first.
func (m *Model) openErrorLog() {
	var lines []string
	for i := len(m.errLog) - 1; i >= 0; i-- {
		e := m.errLog[i]
		lines = append(lines, fmt.Sprintf("%s  [%s] %s", e.At.Format("15:04:05"), e.Tab, oneLine(e.Text)))
	}
	if len(lines) == 0 {
		lines = []string{"No errors this session."}
	}
	m.openInfo("Error log", "error", lines)
}
