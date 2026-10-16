package tui

// The memory browser (/memory): project and global memories, search, a
// preview pane, and an editor for create and update.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

func init() {
	overrideCommands(&command{Name: "memory", Desc: "Browse, edit and delete stored memories", Group: "Intelligence",
		Run: func(m *Model, _ string) tea.Cmd { m.openMemoryBrowser(); return nil }})
}

// memCategories are the categories the memory tool accepts.
var memCategories = []string{"fact", "decision", "convention", "bug", "preference", "todo"}

// memScopes are the two tabs, in order.
var memScopes = []string{"project", "global"}

// memCatWidth is the width of the category badge column.
const memCatWidth = 13

type memoryBrowser struct {
	m         *Model
	be        Backend
	th        theme.Theme
	all       []MemoryItem
	tab       int
	query     string
	searching bool
	cursor    int
	off       int
	wide      bool
	visible   []MemoryItem
	note      string
	noteKind  string
}

func (m *Model) openMemoryBrowser() {
	b := &memoryBrowser{m: m, be: m.cur().be, th: m.th}
	b.reload()
	m.pushModal(b)
}

// memScopeOf treats an empty scope as project, the default.
func memScopeOf(it MemoryItem) string {
	if it.Scope == "" {
		return "project"
	}
	return it.Scope
}

func (b *memoryBrowser) scope() string { return memScopes[b.tab] }

func (b *memoryBrowser) count(scope string) int {
	n := 0
	for _, it := range b.all {
		if memScopeOf(it) == scope {
			n++
		}
	}
	return n
}

// reload fetches every memory from the backend and re-applies the filter.
func (b *memoryBrowser) reload() {
	b.all = b.be.Memories("")
	b.apply()
}

// apply rebuilds the visible list: this tab's scope, the query, pinned first then newest.
func (b *memoryBrowser) apply() {
	q := strings.ToLower(strings.TrimSpace(b.query))
	var out []MemoryItem
	for _, it := range b.all {
		if memScopeOf(it) != b.scope() {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(it.Title+" "+it.Category+" "+it.Body), q) {
			continue
		}
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		return out[i].Updated.After(out[j].Updated)
	})
	b.visible = out
	b.cursor = clamp(b.cursor, 0, max(0, len(out)-1))
}

// selectID puts the cursor on the memory with id, if it is visible.
func (b *memoryBrowser) selectID(id string) {
	for i, it := range b.visible {
		if it.ID == id {
			b.cursor = i
			return
		}
	}
}

func (b *memoryBrowser) selected() (MemoryItem, bool) {
	if b.cursor < 0 || b.cursor >= len(b.visible) {
		return MemoryItem{}, false
	}
	return b.visible[b.cursor], true
}

func (b *memoryBrowser) move(d int) {
	b.cursor = clamp(b.cursor+d, 0, max(0, len(b.visible)-1))
}

func (b *memoryBrowser) say(kind, text string) {
	b.noteKind, b.note = kind, text
}

// Title implements Modal.
func (b *memoryBrowser) Title() string { return "Memory" }

// Icon implements Iconed.
func (b *memoryBrowser) Icon() string { return "memory" }

// Footer implements Footered.
func (b *memoryBrowser) Footer() string {
	if b.searching {
		return "type to search · Enter keep · Esc clear"
	}
	return "↑↓ move · Enter edit · n new · d delete · p pin · / search · Esc"
}

// Size implements Sized. It also records whether the preview goes beside the list.
func (b *memoryBrowser) Size(cols, rows int) (int, int) {
	b.wide = cols >= 100
	return min(cols-4, 120), min(rows-4, 36)
}

// Update implements Modal.
func (b *memoryBrowser) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return b, nil, false
	}
	key := km.String()
	switch key {
	case "up":
		b.move(-1)
		return b, nil, false
	case "down":
		b.move(1)
		return b, nil, false
	case "pgup":
		b.move(-8)
		return b, nil, false
	case "pgdown":
		b.move(8)
		return b, nil, false
	}
	if b.searching {
		switch key {
		case "esc":
			b.query, b.searching = "", false
			b.apply()
		case "enter":
			b.searching = false
		case "backspace":
			b.query = dropLastRune(b.query)
			b.apply()
		case "ctrl+u":
			b.query = ""
			b.apply()
		default:
			if s := printable(km); s != "" {
				b.query += s
				b.apply()
			}
		}
		return b, nil, false
	}
	switch key {
	case "esc":
		if b.query != "" {
			b.query = ""
			b.apply()
			return b, nil, false
		}
		return b, nil, true
	case "/":
		b.searching = true
	case "tab", "shift+tab":
		b.tab = 1 - b.tab
		b.query, b.cursor, b.off = "", 0, 0
		b.apply()
	case "j":
		b.move(1)
	case "k":
		b.move(-1)
	case "enter":
		if it, ok := b.selected(); ok {
			b.m.pushModal(b.editor(&it))
		}
	case "n":
		b.m.pushModal(b.editor(nil))
	case "d":
		if it, ok := b.selected(); ok {
			b.m.pushModal(b.confirmDelete(it))
		}
	case "p":
		b.togglePin()
	}
	return b, nil, false
}

func (b *memoryBrowser) togglePin() {
	it, ok := b.selected()
	if !ok {
		return
	}
	it.Pinned = !it.Pinned
	if err := b.be.SaveMemory(it); err != nil {
		b.say("err", err.Error())
		return
	}
	verb := "Unpinned"
	if it.Pinned {
		verb = "Pinned"
	}
	b.reload()
	b.selectID(it.ID)
	b.say("ok", verb+" "+shorten(it.Title, 40))
}

func (b *memoryBrowser) confirmDelete(it MemoryItem) Modal {
	id, title := it.ID, shorten(it.Title, 60)
	c := NewConfirm(b.th, "Delete memory", fmt.Sprintf("Delete %q? This cannot be undone.", title), true, func() tea.Cmd {
		if err := b.be.DeleteMemory(id); err != nil {
			b.say("err", err.Error())
		} else {
			b.say("ok", "Deleted "+title)
		}
		b.reload()
		return nil
	})
	return c.WithLabels("Yes, delete", "No")
}

// editor returns an editor for it, or for a new memory when it is nil.
func (b *memoryBrowser) editor(it *MemoryItem) *memoryEditor {
	e := &memoryEditor{th: b.th, scope: b.scope(), cat: 0, isNew: it == nil}
	if it != nil {
		e.id, e.scope, e.pinned = it.ID, memScopeOf(*it), it.Pinned
		e.title = []rune(it.Title)
		e.body = []rune(it.Body)
		e.cat = indexOf(memCategories, it.Category)
	}
	e.save = func(item MemoryItem) error {
		if err := b.be.SaveMemory(item); err != nil {
			return err
		}
		b.reload()
		if item.ID != "" {
			b.selectID(item.ID)
		} else {
			b.cursor = 0
		}
		if item.ID == "" {
			b.say("ok", "Saved "+shorten(item.Title, 40))
		} else {
			b.say("ok", "Updated "+shorten(item.Title, 40))
		}
		return nil
	}
	return e
}

// View implements Modal.
func (b *memoryBrowser) View(w, h int) string {
	th := b.th
	counts := []string{
		fmt.Sprintf("Project %d", b.count("project")),
		fmt.Sprintf("Global %d", b.count("global")),
	}
	caret := fg(th.Brand).Render("▌")
	search := icons.Icon("search") + " "
	switch {
	case b.searching:
		search += fg(th.TextPrimary).Render(b.query) + caret
	case b.query != "":
		search += fg(th.TextPrimary).Render(b.query)
	default:
		search += fg(th.TextDim).Render("/ to search")
	}
	count := ""
	if b.query != "" {
		count = fmt.Sprintf("%d / %d", len(b.visible), b.count(b.scope()))
	} else if n := len(b.visible); n > 0 {
		count = fmt.Sprintf("%d", n)
	}
	head := []string{
		modalTabs(th, counts, b.tab) + "  " + fg(th.TextFaint).Render("Tab switches"),
		spaceBetween(search, fg(th.TextFaint).Render(count), w),
		"",
	}
	noteRows := 0
	if b.note != "" {
		noteRows = 1
	}
	avail := max(1, h-len(head)-noteRows)

	var body []string
	if b.wide {
		listW := w * 46 / 100
		paneW := w - listW - 3
		left := fitLines(b.listRows(listW, avail), listW, avail)
		right := fitLines(b.preview(paneW), paneW, avail)
		for i := range left {
			body = append(body, left[i]+fg(th.BorderFocused).Render(" │ ")+right[i])
		}
	} else {
		listH := max(1, avail*55/100)
		prevH := avail - listH - 1
		body = append(body, fitLines(b.listRows(w, listH), w, listH)...)
		if prevH > 0 {
			body = append(body, fg(th.Border).Render(strings.Repeat("─", w)))
			body = append(body, fitLines(b.preview(w), w, prevH)...)
		}
	}
	out := append(head, body...)
	if b.note != "" {
		col := th.TextMuted
		switch b.noteKind {
		case "ok":
			col = th.Success
		case "err":
			col = th.Error
		}
		out = append(out, fg(col).Render(render.Truncate(b.note, w)))
	}
	return strings.Join(fitLines(out, w, h), "\n")
}

// listRows renders the visible memories into at most h rows, keeping the cursor on screen.
func (b *memoryBrowser) listRows(w, h int) []string {
	th := b.th
	if len(b.visible) == 0 {
		msg := "No memories yet. Press n to add one."
		switch {
		case b.query != "":
			msg = "No memories match “" + b.query + "”."
		case b.tab == 1:
			msg = "No global memories yet. Press n to add one."
		case b.tab == 0:
			msg = "No project memories yet. Press n to add one."
		}
		var out []string
		for _, l := range wrapLines(msg, max(8, w-2)) {
			out = append(out, fg(th.TextFaint).Render("  "+l))
		}
		return out
	}
	b.off = keepInView(b.cursor, b.off, h)
	if b.off > max(0, len(b.visible)-h) {
		b.off = max(0, len(b.visible)-h)
	}
	now := time.Now()
	end := min(len(b.visible), b.off+h)
	rows := make([]string, 0, end-b.off)
	for i := b.off; i < end; i++ {
		rows = append(rows, b.row(b.visible[i], i == b.cursor, w, now))
	}
	return rows
}

// row draws one memory line: marker, pin star, category badge, title, age.
func (b *memoryBrowser) row(it MemoryItem, sel bool, w int, now time.Time) string {
	th := b.th
	marker := "  "
	if sel {
		marker = fgb(th.BrandSecondary).Render("› ")
	}
	star := "  "
	if it.Pinned {
		star = fg(th.Warning).Render("★ ")
	}
	badge := padRight(memBadge(th, it.Category), memCatWidth)
	age := fg(th.TextDim).Render(relAge(it.Updated, now))
	avail := w - ansi.StringWidth(marker) - ansi.StringWidth(star) - memCatWidth - ansi.StringWidth(age) - 1
	if avail < 4 {
		avail = 4
	}
	title := render.Truncate(oneLine(it.Title), avail)
	titleStyle := fg(th.TextSecondary)
	if sel {
		titleStyle = fgb(th.TextPrimary)
	}
	gap := max(1, avail-ansi.StringWidth(title)+1)
	return render.Truncate(marker+star+badge+titleStyle.Render(title)+strings.Repeat(" ", gap)+age, w)
}

// preview is the body pane for the highlighted memory.
func (b *memoryBrowser) preview(w int) []string {
	th := b.th
	it, ok := b.selected()
	if !ok {
		return []string{fg(th.TextFaint).Render("Nothing selected.")}
	}
	meta := []string{memBadge(th, it.Category), fg(th.TextDim).Render(memScopeOf(it)), fg(th.TextDim).Render("updated " + relAge(it.Updated, time.Now()) + " ago")}
	if it.Pinned {
		meta = append(meta, fg(th.Warning).Render("★ pinned"))
	}
	lines := []string{
		fgb(th.TextPrimary).Render(render.Truncate(oneLine(it.Title), w)),
		strings.Join(meta, fg(th.TextFaint).Render(" · ")),
		"",
	}
	if strings.TrimSpace(it.Body) == "" {
		return append(lines, fg(th.TextFaint).Render("(no body)"))
	}
	for _, l := range strings.Split(it.Body, "\n") {
		if l == "" {
			lines = append(lines, "")
			continue
		}
		for _, wl := range wrapLines(l, w) {
			lines = append(lines, fg(th.TextPrimary).Render(wl))
		}
	}
	return lines
}

// memBadge is the category label, coloured by category.
func memBadge(th theme.Theme, cat string) string {
	if cat == "" {
		cat = "fact"
	}
	col := th.Info
	switch cat {
	case "decision":
		col = th.BrandSecondary
	case "convention":
		col = th.Brand
	case "bug":
		col = th.Error
	case "preference":
		col = th.Success
	case "todo":
		col = th.Warning
	}
	return lipgloss.NewStyle().Foreground(col).Render("[" + cat + "]")
}

// shorten cuts s to n runes with an ellipsis, after collapsing whitespace.
func shorten(s string, n int) string {
	s = oneLine(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func indexOf(list []string, v string) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return 0
}

// ---------------------------------------------------------------- editor

// memoryEditor creates or updates one memory: title, category and body.
type memoryEditor struct {
	th        theme.Theme
	id, scope string
	pinned    bool
	isNew     bool
	title     []rune
	body      []rune
	cat       int
	focus     int // 0 title, 1 category, 2 body
	hint      string
	save      func(MemoryItem) error
}

// Title implements Modal.
func (e *memoryEditor) Title() string {
	if e.isNew {
		return "New memory"
	}
	return "Edit memory"
}

// Icon implements Iconed.
func (e *memoryEditor) Icon() string { return "pencil" }

// Footer implements Footered.
func (e *memoryEditor) Footer() string {
	return "tab field · ←→ category · Ctrl+S save · Esc cancel"
}

// Size implements Sized.
func (e *memoryEditor) Size(cols, rows int) (int, int) {
	return min(cols-4, 84), min(rows-4, 24)
}

// Update implements Modal.
func (e *memoryEditor) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return e, nil, false
	}
	switch key := km.String(); key {
	case "esc":
		return e, nil, true
	case "tab":
		e.focus = (e.focus + 1) % 3
	case "shift+tab":
		e.focus = (e.focus + 2) % 3
	case "ctrl+s":
		return e.submit()
	case "enter":
		if e.focus == 0 {
			e.focus = 2
		} else if e.focus == 2 {
			e.body = append(e.body, '\n')
		} else {
			e.focus = 2
		}
	case "left":
		if e.focus == 1 {
			e.cat = (e.cat + len(memCategories) - 1) % len(memCategories)
		}
	case "right":
		if e.focus == 1 {
			e.cat = (e.cat + 1) % len(memCategories)
		}
	case "backspace":
		switch e.focus {
		case 0:
			e.title = dropRunes(e.title)
		case 2:
			e.body = dropRunes(e.body)
		}
	case "ctrl+u":
		switch e.focus {
		case 0:
			e.title = nil
		case 2:
			e.body = nil
		}
	default:
		s := printable(km)
		if s == "" {
			break
		}
		switch e.focus {
		case 0:
			e.title = append(e.title, []rune(strings.ReplaceAll(s, "\n", " "))...)
		case 2:
			e.body = append(e.body, []rune(s)...)
		}
	}
	return e, nil, false
}

func dropRunes(r []rune) []rune {
	if len(r) == 0 {
		return r
	}
	return r[:len(r)-1]
}

func (e *memoryEditor) submit() (Modal, tea.Cmd, bool) {
	title := strings.TrimSpace(string(e.title))
	if title == "" {
		e.hint, e.focus = "A title is needed.", 0
		return e, nil, false
	}
	item := MemoryItem{
		ID: e.id, Scope: e.scope, Category: memCategories[e.cat],
		Title: title, Body: strings.TrimSpace(string(e.body)), Pinned: e.pinned,
	}
	if err := e.save(item); err != nil {
		e.hint = err.Error()
		return e, nil, false
	}
	return e, nil, true
}

// View implements Modal.
func (e *memoryEditor) View(w, h int) string {
	th := e.th
	label := func(s string, focused bool) string {
		if focused {
			return fgb(th.Brand).Render(s)
		}
		return fg(th.TextDim).Render(s)
	}
	caret := func(on bool) string {
		if on {
			return fg(th.Brand).Render("▌")
		}
		return ""
	}
	var head []string
	head = append(head, label("Title", e.focus == 0))
	if len(e.title) == 0 && e.focus != 0 {
		head = append(head, fg(th.TextDim).Render("A short summary, e.g. Tests live next to the code"))
	} else {
		head = append(head, fg(th.TextPrimary).Render(render.Truncate(string(e.title), w-2))+caret(e.focus == 0))
	}
	head = append(head, "", label("Category", e.focus == 1))
	chips := make([]string, 0, len(memCategories))
	for i, c := range memCategories {
		if i == e.cat {
			chips = append(chips, lipgloss.NewStyle().Bold(true).Foreground(th.BgApp).Background(memCatColor(th, c)).Render(" "+c+" "))
		} else {
			chips = append(chips, fg(th.TextMuted).Render(" "+c+" "))
		}
	}
	head = append(head, render.Truncate(strings.Join(chips, " "), w))
	head = append(head, "", label("Body", e.focus == 2))

	footer := []string{""}
	if e.hint != "" {
		footer = append(footer, fg(th.Warning).Render(e.hint))
	} else {
		footer = append(footer, fg(th.TextFaint).Render("Saved to "+e.scope+" memory · Enter adds a line"))
	}
	avail := max(1, h-len(head)-len(footer))
	var bodyLines []string
	for _, l := range strings.Split(string(e.body), "\n") {
		bodyLines = append(bodyLines, wrapLines(l, w)...)
	}
	if len(bodyLines) == 0 {
		bodyLines = []string{""}
	}
	if len(bodyLines) > avail {
		bodyLines = bodyLines[len(bodyLines)-avail:]
	}
	last := len(bodyLines) - 1
	if e.focus == 2 {
		bodyLines[last] += caret(true)
	}
	for i := range bodyLines {
		bodyLines[i] = fg(th.TextPrimary).Render(bodyLines[i])
	}
	out := append(head, bodyLines...)
	out = append(out, footer...)
	return strings.Join(fitLines(out, w, h), "\n")
}

func memCatColor(th theme.Theme, cat string) lipgloss.TerminalColor {
	switch cat {
	case "decision":
		return th.BrandSecondary
	case "convention":
		return th.Brand
	case "bug":
		return th.Error
	case "preference":
		return th.Success
	case "todo":
		return th.Warning
	}
	return th.Info
}
