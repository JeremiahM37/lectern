package tui

// The list-style menus of menus.go that rank by frecency or follow the query:
// the command palette, the session browser, the error log, the help screen and
// the stashed-drafts browser. Frecency lives in the shared session store, so a
// command run here, from the slash popup or from the palette ranks the same.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/session"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
)

// suggestedCommands are the palette's suggestions until frecency has enough
// history to say which commands the person actually runs.
var suggestedCommands = []string{"help", "mode", "router", "verbose-tab"}

// livePicker is a Picker whose blurb, footer hint, size and rows follow its
// state. refresh runs after every key, so the counts never go stale.
type livePicker struct {
	*Picker
	refresh func(p *Picker)
	size    func(cols, rows int) (int, int)
}

func newLivePicker(p *Picker, refresh func(*Picker)) *livePicker {
	lp := &livePicker{Picker: p, refresh: refresh}
	refresh(p)
	return lp
}

// Update implements Modal. It hands the key to the Picker, then refreshes.
func (lp *livePicker) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	_, cmd, done := lp.Picker.Update(msg)
	if lp.refresh != nil {
		lp.refresh(lp.Picker)
	}
	return lp, cmd, done
}

// Size implements Sized.
func (lp *livePicker) Size(cols, rows int) (int, int) {
	if lp.size != nil {
		return lp.size(cols, rows)
	}
	return lp.Picker.Size(cols, rows)
}

// ---------------------------------------------------------------- frecency

// commandNamed returns the registered command with exactly this name.
func commandNamed(name string) *command {
	for _, c := range commandTable {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// frecentCommands returns up to n commands the person runs most, best first.
func frecentCommands(n int) []*command {
	migrateLegacyFrecency()
	names, err := session.TopPicks(session.PickCommand, 0)
	if err != nil {
		return nil
	}
	var out []*command
	for _, name := range names {
		if c := commandNamed(name); c != nil {
			out = append(out, c)
			if len(out) == n {
				break
			}
		}
	}
	return out
}

// modelRanks maps each model the person has picked to its rank, 0 the most
// frecent. Models not in the map keep the provider's own order.
func modelRanks() map[string]int {
	migrateLegacyFrecency()
	names, err := session.TopPicks(session.PickModel, 0)
	if err != nil {
		return nil
	}
	ranks := make(map[string]int, len(names))
	for i, n := range names {
		ranks[n] = i
	}
	return ranks
}

// rankModels returns ms ordered by frecency within one provider. The sort is
// stable, so models never picked stay in the provider's order.
func rankModels(provider string, ms []llm.ModelInfo, ranks map[string]int) []llm.ModelInfo {
	out := append([]llm.ModelInfo(nil), ms...)
	rank := func(mi llm.ModelInfo) int {
		if r, ok := ranks[provider+"/"+mi.ID]; ok {
			return r
		}
		return 1 << 30
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out
}

// modelDesc is the dim meta text of a model row: local, context window, FREE.
// FREE means the provider's own catalog prices it at zero.
func modelDesc(mi llm.ModelInfo, local bool) string {
	var parts []string
	if local {
		parts = append(parts, "local")
	}
	if mi.ContextWindow > 0 {
		parts = append(parts, render.HumanTokens(mi.ContextWindow)+" ctx")
	}
	if mi.CatalogPriced && mi.InputPerMTok == 0 && mi.OutputPerMTok == 0 {
		parts = append(parts, "FREE")
	}
	return strings.Join(parts, " · ")
}

// ---------------------------------------------------------------- palette

// categoryPrefix is the three-letter tag a palette row shows while filtering.
func categoryPrefix(group string) string {
	g := strings.ToLower(group)
	if len(g) > 3 {
		g = g[:3]
	}
	return g
}

// paletteLabel is the command as it is typed, with its argument hint.
func paletteLabel(c *command) string {
	l := "/" + c.Name
	if c.Args != "" {
		l += " " + c.Args
	}
	return l
}

// paletteItems builds the rows. With no query: a Suggested group first (the
// most frecent commands, or the static suggestions), then one group per
// category with the suggested ones removed. With a query: one flat Results
// group, ordered by the Picker's fuzzy score.
func paletteItems(cmds []*command, suggested []*command, q string) []PickItem {
	var items []PickItem
	if q != "" {
		for _, c := range cmds {
			items = append(items, PickItem{Value: c.Name, Label: paletteLabel(c),
				Desc: categoryPrefix(c.Group) + " · " + c.Desc, Group: "Results"})
		}
		return items
	}
	isSuggested := map[string]bool{}
	for _, c := range suggested {
		isSuggested[c.Name] = true
		items = append(items, PickItem{Value: c.Name, Label: paletteLabel(c), Desc: c.Desc,
			Group: "Suggested", Icon: icons.Icon("sparkle")})
	}
	for _, c := range cmds {
		if !isSuggested[c.Name] {
			items = append(items, PickItem{Value: c.Name, Label: paletteLabel(c), Desc: c.Desc, Group: c.Group})
		}
	}
	return items
}

// paletteSuggestions is the Suggested group: frecent commands when there are
// any, else the static list. Names with no command are skipped.
func paletteSuggestions() []*command {
	if got := frecentCommands(5); len(got) > 0 {
		return got
	}
	var out []*command
	for _, n := range suggestedCommands {
		if c := commandNamed(n); c != nil {
			out = append(out, c)
		}
	}
	return out
}

// jumpGroupWrap moves the cursor to the first row of the next category, and
// wraps from the last category back to the first (Tab in the palette).
func jumpGroupWrap(p *Picker) {
	n := len(p.visible)
	if n == 0 {
		return
	}
	cur := p.Items[p.visible[p.cursor]].Group
	for step := 1; step <= n; step++ {
		i := (p.cursor + step) % n
		g := p.Items[p.visible[i]].Group
		if g == cur {
			continue
		}
		j := i
		for j-1 >= 0 && p.Items[p.visible[j-1]].Group == g {
			j--
		}
		p.cursor = j
		return
	}
}

// openPalette is Ctrl+K: every command, grouped, filtered as you type.
func (m *Model) openPalette() {
	p := NewPicker(m.th, "Command Palette", "lightning")
	p.TypeToFilter = true
	p.Empty = "No commands match — try a different query"
	cmds := allCommands()
	byName := make(map[string]*command, len(cmds))
	for _, c := range cmds {
		byName[c.Name] = c
	}
	suggested := paletteSuggestions()
	built := "\x00" // the query the rows were last built for
	p.OnSelect = func(it PickItem) tea.Cmd {
		c := byName[it.Value]
		if c == nil {
			return nil
		}
		bumpFrecency(c.Name)
		if c.Args != "" {
			m.cur().in.setValue("/" + c.Name + " ")
			return nil
		}
		return m.runCommand("/" + c.Name)
	}
	p.OnKey = func(key string) (tea.Cmd, bool) {
		if key != "tab" {
			return nil, false
		}
		if p.query != "" {
			return nil, true // Tab moves between categories only when unfiltered
		}
		jumpGroupWrap(p)
		return p.fireCursor(), true
	}
	lp := newLivePicker(p, func(p *Picker) {
		if p.query != built {
			built = p.query
			p.SetItems(paletteItems(cmds, suggested, p.query))
		}
		if p.query != "" {
			p.Hint = "↑↓ nav · Enter run · Backspace del · Esc clear"
			p.Blurb = fmt.Sprintf("%d matches", len(p.visible))
			return
		}
		p.Hint = "↑↓ nav · Tab next category · Enter run · Esc close"
		p.Blurb = fmt.Sprintf("%d commands · %d categories", len(cmds), len(commandGroups))
	})
	lp.size = func(cols, rows int) (int, int) {
		return min(84, max(72, cols*7/10)), min(32, max(18, rows-4))
	}
	m.pushModal(lp)
}

// ---------------------------------------------------------------- help

// openHelp shows keys, modes and commands.
func (m *Model) openHelp() {
	lines := keyHelpLines() // the key reference lives in keyhelp.go
	lines = append(lines, "", "Modes (switch with /mode <name>, or Shift+Tab to cycle):")
	for _, id := range modeOrder {
		mi := modeStyle(id, m.th)
		lines = append(lines, fmt.Sprintf("  %-28s %s", strings.ToLower(mi.label), mi.desc))
	}
	lines = append(lines, "", "Commands:")
	for _, c := range allCommands() {
		name := "/" + c.Name
		if c.Args != "" {
			name += " " + c.Args
		}
		lines = append(lines, fmt.Sprintf("  %-28s %s", name, c.Desc))
	}
	m.openInfo("Scribe help", "info", lines)
}

// ---------------------------------------------------------------- sessions

// sessionBlurb is the count under the session browser's title.
func sessionBlurb(p *Picker) string {
	switch {
	case p.query != "":
		return fmt.Sprintf("%d of %d sessions", len(p.visible), len(p.Items))
	case len(p.Items) == 0:
		return "No sessions yet"
	default:
		return fmt.Sprintf("%d sessions", len(p.Items))
	}
}

// openSessionPicker is Ctrl+P.
func (m *Model) openSessionPicker() {
	t := m.cur()
	p := NewPicker(m.th, "Sessions", "clock_alt")
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
		case "ctrl+x":
			if len(p.Items) == 0 {
				return nil, true
			}
			n := len(p.Items)
			m.pushModal(NewConfirm(m.th, "Clear all sessions?",
				fmt.Sprintf("%d sessions will be deleted from this project. This cannot be undone.", n), true, func() tea.Cmd {
					return m.clearAllSessions(t, p)
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
	lp := newLivePicker(p, func(p *Picker) {
		p.Hint = "Enter restore · ^R rename · ^D delete · ^X clear all · Esc close"
		p.Blurb = sessionBlurb(p)
	})
	m.pushModal(lp)
}

// clearAllSessions deletes every saved session of the tab's project (Ctrl+X).
// The first failure stops the sweep and is reported; what was deleted stays
// deleted.
func (m *Model) clearAllSessions(t *tab, p *Picker) tea.Cmd {
	del, can := t.be.(SessionDeleter)
	if !can {
		return flashCmd("info", "This backend cannot delete sessions")
	}
	n := 0
	for _, s := range t.be.Sessions() {
		if err := del.DeleteSession(s.ID); err != nil {
			p.SetItems(m.sessionItems())
			return errFlash(err)
		}
		n++
	}
	p.SetItems(m.sessionItems())
	noun := "sessions"
	if n == 1 {
		noun = "session"
	}
	t.chat.AddSystem("info", fmt.Sprintf("Cleared %d %s.", n, noun))
	return flashCmd("ok", fmt.Sprintf("Cleared %d %s", n, noun))
}

// ---------------------------------------------------------------- error log

// errorItems lists the session's errors, newest first. A row shows the time,
// the source and the text; a repeated fault shows how many times it happened.
func (m *Model) errorItems() []PickItem {
	var items []PickItem
	now := time.Now()
	for i := len(m.errLog) - 1; i >= 0; i-- {
		e := m.errLog[i]
		label := fmt.Sprintf("%-10s", e.Source) + " " + oneLine(e.Text) + "  " + relAge(e.At, now)
		desc := e.Tab
		if e.Count > 1 {
			desc += " ×" + strconv.Itoa(e.Count)
		}
		items = append(items, PickItem{
			Value: strconv.Itoa(i),
			Label: label,
			Desc:  desc,
			Icon:  icons.Icon("error"),
		})
	}
	return items
}

// openErrorLog is the error log (Alt+R, /errors). The list filters as you
// type, the side pane shows the whole error, and Ctrl+Y copies it.
func (m *Model) openErrorLog() {
	p := NewPicker(m.th, "Error log", "error")
	p.TypeToFilter = true
	p.Empty = "No errors this session."
	p.Items = m.errorItems()
	p.refilter()
	entry := func(it PickItem) (errEntry, bool) {
		i, err := strconv.Atoi(it.Value)
		if err != nil || i < 0 || i >= len(m.errLog) {
			return errEntry{}, false
		}
		return m.errLog[i], true
	}
	p.Preview = func(it PickItem) []string {
		e, ok := entry(it)
		if !ok {
			return nil
		}
		lines := []string{"When   " + relAge(e.At, time.Now()) + " ago (" + e.At.Format("15:04:05") + ")", "Source " + e.Source, "Tab    " + e.Tab}
		if e.Count > 1 {
			lines = append(lines, fmt.Sprintf("Repeat %d times, the same fault, shown once", e.Count))
		}
		lines = append(lines, "")
		if e.Args != "" {
			lines = append(lines, "── Args ──")
			lines = append(lines, strings.Split(render.Wrap(prettyArgs(e.Args), 40), "\n")...)
			lines = append(lines, "")
		}
		lines = append(lines, "── Error ──")
		return append(lines, strings.Split(render.Wrap(e.Text, 40), "\n")...)
	}
	p.OnKey = func(key string) (tea.Cmd, bool) {
		if key != "ctrl+y" {
			return nil, false
		}
		it, ok := p.Selected()
		if !ok {
			return nil, true
		}
		e, ok := entry(it)
		if !ok {
			return nil, true
		}
		text := e.Text
		if e.Args != "" {
			text = "Args:\n" + prettyArgs(e.Args) + "\n\n" + e.Text
		}
		copyToTerminal(text)
		return flashCmd("ok", "Copied the error"), true
	}
	lp := newLivePicker(p, func(p *Picker) {
		p.Hint = "↑↓ nav · ^Y copy · type to filter · Esc close"
		if p.query != "" {
			p.Blurb = fmt.Sprintf("%d of %d errors", len(p.visible), len(p.Items))
			return
		}
		p.Blurb = fmt.Sprintf("%d %s", len(p.Items), plural(len(p.Items), "error", "errors"))
	})
	m.pushModal(lp)
}

// plural picks the singular or plural noun for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// ---------------------------------------------------------------- stashes

// openStashPicker lists this folder's stashed drafts, newest first. Enter
// restores one into an empty prompt and removes it from the stash; d deletes
// one; X clears them all after a confirm that defaults to No.
func (m *Model) openStashPicker() {
	t := m.cur()
	p := NewPicker(m.th, "Stashed drafts", "bookmark")
	p.Empty = "No stashed drafts for this folder"
	build := func() []PickItem {
		var items []PickItem
		for i := len(t.in.stashes) - 1; i >= 0; i-- {
			s := t.in.stashes[i]
			lines := strings.Count(s, "\n") + 1
			items = append(items, PickItem{
				Value: strconv.Itoa(i),
				Label: firstStashLine(s),
				Desc:  fmt.Sprintf("%d %s", lines, plural(lines, "line", "lines")),
			})
		}
		return items
	}
	p.Items = build()
	p.refilter()
	p.Preview = func(it PickItem) []string {
		i, err := strconv.Atoi(it.Value)
		if err != nil || i < 0 || i >= len(t.in.stashes) {
			return nil
		}
		return strings.Split(t.in.stashes[i], "\n")
	}
	remove := func(i int) {
		t.in.stashes = append(t.in.stashes[:i], t.in.stashes[i+1:]...)
		m.saveStashes(t)
	}
	p.OnSelect = func(it PickItem) tea.Cmd {
		i, err := strconv.Atoi(it.Value)
		if err != nil || i < 0 || i >= len(t.in.stashes) {
			return nil
		}
		if !t.in.empty() {
			return flashCmd("info", "Clear the prompt first, then restore the draft")
		}
		text := t.in.stashes[i]
		remove(i)
		t.in.setValue(text)
		return nil
	}
	p.OnKey = func(key string) (tea.Cmd, bool) {
		if p.searching || p.query != "" {
			return nil, false
		}
		it, ok := p.Selected()
		if !ok {
			return nil, false
		}
		switch key {
		case "d":
			i, err := strconv.Atoi(it.Value)
			if err != nil || i < 0 || i >= len(t.in.stashes) {
				return nil, true
			}
			remove(i)
			p.SetItems(build())
			return flashCmd("ok", "Stash deleted"), true
		case "X":
			n := len(t.in.stashes)
			if n == 0 {
				return nil, true
			}
			m.pushModal(NewConfirm(m.th, "Clear all stashed drafts?",
				fmt.Sprintf("%d %s for this folder will be deleted. This cannot be undone.", n, plural(n, "draft", "drafts")),
				true, func() tea.Cmd {
					t.in.stashes = nil
					m.saveStashes(t)
					p.SetItems(build())
					return flashCmd("ok", fmt.Sprintf("Cleared %d %s", n, plural(n, "draft", "drafts")))
				}))
			return nil, true
		}
		return nil, false
	}
	lp := newLivePicker(p, func(p *Picker) {
		p.Hint = "Enter restore · d delete · X clear all · Esc close"
		p.Blurb = fmt.Sprintf("%d %s", len(t.in.stashes), plural(len(t.in.stashes), "draft", "drafts"))
	})
	m.pushModal(lp)
}

// firstStashLine is the first non-blank line of a stash, cut to a row's width.
func firstStashLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if r := []rune(l); len(r) > 72 {
				return string(r[:71]) + "…"
			}
			return l
		}
	}
	return "(blank)"
}
