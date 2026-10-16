package tui

// Modal framework.
//
// A Modal is an overlay drawn centred over the screen. While it is the top of
// the stack it receives every key and every message the Model does not handle
// itself. Modals are always pointers, so the stack can find and remove one by
// identity even after a callback has pushed another modal on top of it.
//
// Reusable components (all pointer types):
//
//   Picker    fuzzy-filtered, grouped list. NewPicker(th, title, icon) then
//             fill Items, OnSelect, OnCursor, OnCancel, Preview, etc. Set
//             TypeToFilter for palettes (letters always filter); otherwise
//             "/" starts a filter and j/k navigate.
//   Confirm   y/n dialog. NewConfirm(th, title, message, danger, onYes).
//   TextPrompt  single-line input. NewTextPrompt(th, title, placeholder, masked, onSubmit).
//   InfoView  scrollable read-only text. NewInfoView(th, title, icon, lines).
//
// Callbacks return a tea.Cmd and may push further modals with m.pushModal.
// The Model removes a modal only after its Update reports done.

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// Modal is an overlay. Update returns the modal to keep (normally itself) and
// done=true when it should close. View draws the body only; the frame (border,
// title, footer) is added by the framework.
type Modal interface {
	Update(msg tea.Msg) (Modal, tea.Cmd, bool)
	View(w, h int) string
	Title() string
}

// Footered is optionally implemented by a Modal to supply its key hints.
type Footered interface{ Footer() string }

// Sized is optionally implemented to choose the frame size for a terminal of
// cols x rows.
type Sized interface {
	Size(cols, rows int) (w, h int)
}

// Iconed is optionally implemented to supply a title icon.
type Iconed interface{ Icon() string }

// modalFrame renders md as a framed box for a terminal of cols x rows.
func modalFrame(md Modal, th theme.Theme, cols, rows int) []string {
	w, h := min(cols-4, 72), min(rows-4, 24)
	if s, ok := md.(Sized); ok {
		w, h = s.Size(cols, rows)
	}
	w = clamp(w, 30, cols-2)
	h = clamp(h, 6, rows-2)
	icon := ""
	if i, ok := md.(Iconed); ok {
		icon = icons.Icon(i.Icon())
	}
	footer := ""
	if f, ok := md.(Footered); ok {
		footer = f.Footer()
	}
	body := strings.Split(md.View(w-4, h-2), "\n")
	body = fitLines(body, w-4, h-2)
	for i := range body {
		body[i] = " " + body[i] + " "
	}
	border := th.BorderFocused
	if d, ok := md.(interface{ Danger() bool }); ok && d.Danger() {
		border = th.Error
	}
	return frame(md.Title(), icon, body, footer, w, border, th)
}

// pushModal puts md on top of the stack.
func (m *Model) pushModal(md Modal) {
	m.modals = append(m.modals, md)
}

// closeModal removes md from the stack wherever it is.
func (m *Model) closeModal(md Modal) {
	for i, x := range m.modals {
		if x == md {
			m.modals = append(m.modals[:i], m.modals[i+1:]...)
			return
		}
	}
}

// topModal returns the top modal or nil.
func (m *Model) topModal() Modal {
	if len(m.modals) == 0 {
		return nil
	}
	return m.modals[len(m.modals)-1]
}

// updateModal routes msg to the top modal and applies its result.
func (m *Model) updateModal(msg tea.Msg) tea.Cmd {
	md := m.topModal()
	if md == nil {
		return nil
	}
	next, cmd, done := md.Update(msg)
	if done {
		m.closeModal(md)
	} else if next != nil && next != md {
		for i, x := range m.modals {
			if x == md {
				m.modals[i] = next
			}
		}
	}
	return cmd
}

// ---------------------------------------------------------------- Picker

// PickItem is one row of a Picker.
type PickItem struct {
	Value    string // identity handed to the callbacks
	Label    string
	Desc     string // secondary text, shown dim after the label
	Group    string // rows with the same group are listed under one header
	Icon     string // optional glyph before the label
	Current  bool   // marked with a check
	Disabled bool   // shown dim, cannot be selected
	Color    lipgloss.TerminalColor
}

// Picker is a fuzzy-filtered, grouped list.
type Picker struct {
	th    theme.Theme
	title string
	icon  string
	Items []PickItem
	Empty string // text when nothing matches
	Hint  string // footer override
	// TypeToFilter makes printable keys filter immediately (palettes).
	TypeToFilter bool
	// KeepOpen keeps the picker on screen after OnSelect.
	KeepOpen bool
	// Loading shows a spinner line while data arrives asynchronously.
	Loading bool
	Spin    string
	Blurb   string
	// Preview returns extra lines for the highlighted item (side pane when wide).
	Preview func(PickItem) []string

	OnSelect func(PickItem) tea.Cmd
	OnCursor func(PickItem) tea.Cmd
	OnCancel func()
	// OnKey lets a caller handle extra keys. Return handled=true to consume.
	OnKey func(key string) (tea.Cmd, bool)

	query     string
	searching bool
	cursor    int // index into visible
	visible   []int
	off       int
}

// NewPicker makes an empty picker.
func NewPicker(th theme.Theme, title, icon string) *Picker {
	p := &Picker{th: th, title: title, icon: icon, Empty: "No matches"}
	p.refilter()
	return p
}

// SetTheme swaps the palette used to draw the picker (theme live preview).
func (p *Picker) SetTheme(th theme.Theme) { p.th = th }

// SetItems replaces the items and keeps the cursor on the same value when possible.
func (p *Picker) SetItems(items []PickItem) {
	prev := ""
	if it, ok := p.Selected(); ok {
		prev = it.Value
	}
	p.Items = items
	p.refilter()
	for i, idx := range p.visible {
		if p.Items[idx].Value == prev {
			p.cursor = i
		}
	}
	p.clampCursor()
}

// Selected returns the highlighted item.
func (p *Picker) Selected() (PickItem, bool) {
	if p.cursor < 0 || p.cursor >= len(p.visible) {
		return PickItem{}, false
	}
	return p.Items[p.visible[p.cursor]], true
}

// SelectValue moves the cursor onto the item with value v, if present.
func (p *Picker) SelectValue(v string) {
	for i, idx := range p.visible {
		if p.Items[idx].Value == v {
			p.cursor = i
			return
		}
	}
}

// Title implements Modal.
func (p *Picker) Title() string { return p.title }

// Icon implements Iconed.
func (p *Picker) Icon() string { return p.icon }

// Footer implements Footered.
func (p *Picker) Footer() string {
	if p.Hint != "" {
		return p.Hint
	}
	if p.searching || p.TypeToFilter {
		return "type filter · ↑↓ move · Enter select · Esc clear"
	}
	return "↑↓ move · Enter select · / filter · Tab group · Esc close"
}

// Size implements Sized.
func (p *Picker) Size(cols, rows int) (int, int) {
	w := min(64, cols-4)
	if p.Preview != nil && cols >= 100 {
		w = min(110, int(float64(cols)*0.8))
	}
	h := min(rows-4, 28)
	return w, h
}

func (p *Picker) refilter() {
	p.visible = p.visible[:0]
	type scored struct {
		idx, score int
	}
	var sc []scored
	for i, it := range p.Items {
		if it.Disabled && p.query != "" {
			continue
		}
		if p.query == "" {
			p.visible = append(p.visible, i)
			continue
		}
		best, ok := -1<<30, false
		for _, f := range []string{it.Label, it.Value, it.Desc, it.Group} {
			if s, hit := fuzzyScore(f, p.query); hit && s > best {
				best, ok = s, true
			}
		}
		if ok {
			sc = append(sc, scored{i, best})
		}
	}
	if p.query != "" {
		// stable insertion sort, best first
		for i := 1; i < len(sc); i++ {
			for j := i; j > 0 && sc[j].score > sc[j-1].score; j-- {
				sc[j], sc[j-1] = sc[j-1], sc[j]
			}
		}
		for _, s := range sc {
			p.visible = append(p.visible, s.idx)
		}
	}
	p.clampCursor()
}

func (p *Picker) clampCursor() {
	if p.cursor >= len(p.visible) {
		p.cursor = len(p.visible) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

// move steps the cursor, skipping disabled rows; it does not wrap.
func (p *Picker) move(delta int) {
	if len(p.visible) == 0 {
		return
	}
	i := p.cursor
	for step := 0; step < len(p.visible); step++ {
		i += delta
		if i < 0 || i >= len(p.visible) {
			return
		}
		if !p.Items[p.visible[i]].Disabled {
			p.cursor = i
			return
		}
	}
}

// jumpGroup moves to the first item of the next (or previous) group.
func (p *Picker) jumpGroup(dir int) {
	if len(p.visible) == 0 {
		return
	}
	cur := p.Items[p.visible[p.cursor]].Group
	for i := p.cursor + dir; i >= 0 && i < len(p.visible); i += dir {
		if p.Items[p.visible[i]].Group != cur {
			// first item of that group, walking back to its start
			g := p.Items[p.visible[i]].Group
			j := i
			for j-1 >= 0 && p.Items[p.visible[j-1]].Group == g {
				j--
			}
			p.cursor = j
			return
		}
	}
}

func (p *Picker) fireCursor() tea.Cmd {
	if p.OnCursor == nil {
		return nil
	}
	if it, ok := p.Selected(); ok {
		return p.OnCursor(it)
	}
	return nil
}

// Update implements Modal.
func (p *Picker) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return p, nil, false
	}
	key := km.String()
	if p.OnKey != nil {
		if cmd, handled := p.OnKey(key); handled {
			return p, cmd, false
		}
	}
	text := printable(km)
	switch {
	case key == "esc":
		if p.query != "" {
			p.query = ""
			p.searching = false
			p.refilter()
			return p, p.fireCursor(), false
		}
		if p.searching {
			p.searching = false
			return p, nil, false
		}
		if p.OnCancel != nil {
			p.OnCancel()
		}
		return p, nil, true
	case key == "enter":
		if p.searching {
			p.searching = false
			return p, nil, false
		}
		it, ok := p.Selected()
		if !ok || it.Disabled || p.OnSelect == nil {
			return p, nil, false
		}
		cmd := p.OnSelect(it)
		return p, cmd, !p.KeepOpen
	case key == "up":
		p.move(-1)
		return p, p.fireCursor(), false
	case key == "down":
		p.move(1)
		return p, p.fireCursor(), false
	case key == "pgup":
		for i := 0; i < 8; i++ {
			p.move(-1)
		}
		return p, p.fireCursor(), false
	case key == "pgdown":
		for i := 0; i < 8; i++ {
			p.move(1)
		}
		return p, p.fireCursor(), false
	case key == "home":
		p.cursor = 0
		p.move(1)
		return p, p.fireCursor(), false
	case key == "end":
		p.cursor = len(p.visible) - 1
		p.move(-1)
		return p, p.fireCursor(), false
	case key == "tab":
		p.jumpGroup(1)
		return p, p.fireCursor(), false
	case key == "shift+tab":
		p.jumpGroup(-1)
		return p, p.fireCursor(), false
	case key == "backspace":
		if r := []rune(p.query); len(r) > 0 {
			p.query = string(r[:len(r)-1])
			p.refilter()
			return p, p.fireCursor(), false
		}
		return p, nil, false
	case key == "ctrl+u":
		p.query = ""
		p.refilter()
		return p, p.fireCursor(), false
	case key == "/" && !p.searching && !p.TypeToFilter:
		p.searching = true
		return p, nil, false
	case !p.TypeToFilter && !p.searching && (key == "j" || key == "k"):
		if key == "j" {
			p.move(1)
		} else {
			p.move(-1)
		}
		return p, p.fireCursor(), false
	case text != "" && (p.TypeToFilter || p.searching):
		p.query += text
		p.refilter()
		return p, p.fireCursor(), false
	case text != "" && !p.TypeToFilter:
		// typing outside search mode starts a filter, like "/"
		p.searching = true
		p.query += text
		p.refilter()
		return p, p.fireCursor(), false
	}
	return p, nil, false
}

// View implements Modal.
func (p *Picker) View(w, h int) string {
	th := p.th
	var lines []string
	// search row
	caret := fg(th.Brand).Render("▌")
	search := icons.Icon("search") + " "
	if p.query == "" && !p.searching && !p.TypeToFilter {
		search += fg(th.TextDim).Render("/ to filter")
	} else if p.query == "" {
		search += fg(th.TextDim).Render("type to filter") + caret
	} else {
		search += fg(th.TextPrimary).Render(p.query) + caret
	}
	count := ""
	if p.query != "" {
		count = itoa(len(p.visible)) + " / " + itoa(len(p.Items))
	} else if len(p.Items) > 0 {
		count = itoa(len(p.Items))
	}
	lines = append(lines, spaceBetween(search, fg(th.TextFaint).Render(count), w))
	if p.Blurb != "" {
		lines = append(lines, fg(th.TextMuted).Render(p.Blurb))
	}
	lines = append(lines, "")
	listW := w
	var prev []string
	if p.Preview != nil && w >= 80 {
		listW = w * 45 / 100
		if it, ok := p.Selected(); ok {
			prev = p.Preview(it)
		}
	}
	rowsAvail := h - len(lines)
	if p.Loading {
		rowsAvail--
	}
	if rowsAvail < 1 {
		rowsAvail = 1
	}
	rows := p.renderRows(listW)
	if len(p.visible) == 0 {
		msg := p.Empty
		if p.Loading {
			msg = p.Spin + " loading…"
		}
		rows = []string{fg(th.TextFaint).Render("  " + msg)}
	}
	// keep the cursor row inside the viewport
	cursorRow := p.cursorRow(rows)
	if cursorRow < p.off {
		p.off = cursorRow
	}
	if cursorRow >= p.off+rowsAvail {
		p.off = cursorRow - rowsAvail + 1
	}
	if p.off > len(rows)-rowsAvail {
		p.off = max(0, len(rows)-rowsAvail)
	}
	end := min(len(rows), p.off+rowsAvail)
	body := append([]string{}, rows[p.off:end]...)
	if p.Loading {
		body = append(body, fg(th.TextDim).Render("  "+p.Spin+" loading…"))
	}
	if prev != nil {
		pane := make([]string, 0, len(prev))
		for _, l := range prev {
			pane = append(pane, render.Truncate(l, w-listW-3))
		}
		left := fitLines(body, listW, rowsAvail)
		right := fitLines(pane, w-listW-3, rowsAvail)
		body = body[:0]
		for i := range left {
			body = append(body, left[i]+fg(th.BorderFocused).Render(" │ ")+right[i])
		}
	}
	lines = append(lines, body...)
	return strings.Join(fitLines(lines, w, h), "\n")
}

// renderRows builds the list rows (group headers included) for the visible items.
func (p *Picker) renderRows(w int) []string {
	th := p.th
	var rows []string
	group := "\x00"
	for i, idx := range p.visible {
		it := p.Items[idx]
		if p.query == "" && it.Group != group && it.Group != "" {
			rows = append(rows, fgb(th.Brand).Render(render.Truncate(it.Group, w)))
		}
		group = it.Group
		sel := i == p.cursor
		marker := "  "
		if sel {
			marker = fgb(th.BrandSecondary).Render("› ")
		}
		labelStyle := fg(th.TextPrimary)
		if it.Color != nil {
			labelStyle = fg(it.Color)
		}
		if it.Disabled {
			labelStyle = fg(th.TextDim)
		} else if sel {
			labelStyle = fgb(th.TextPrimary)
		}
		label := it.Label
		if it.Icon != "" {
			label = it.Icon + " " + label
		}
		line := marker + labelStyle.Render(label)
		if it.Desc != "" {
			line += "  " + fg(th.TextDim).Render(it.Desc)
		}
		if it.Current {
			line += " " + fg(th.Success).Render("✓")
		}
		rows = append(rows, render.Truncate(line, w))
	}
	return rows
}

// cursorRow maps the cursor (an index into visible) to a row index, counting group headers.
func (p *Picker) cursorRow(rows []string) int {
	if len(p.visible) == 0 {
		return 0
	}
	n := 0
	group := "\x00"
	for i, idx := range p.visible {
		it := p.Items[idx]
		if p.query == "" && it.Group != group && it.Group != "" {
			n++
		}
		group = it.Group
		if i == p.cursor {
			return n
		}
		n++
	}
	return n
}

// spaceBetween places left and right on one line of width w.
func spaceBetween(left, right string, w int) string {
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 1 {
		return render.Truncate(left, w)
	}
	return left + strings.Repeat(" ", gap) + right
}

// ---------------------------------------------------------------- Confirm

// Confirm is a yes/no dialog.
type Confirm struct {
	th      theme.Theme
	title   string
	message string
	danger  bool
	yes     bool // selected button
	yesLbl  string
	noLbl   string
	onYes   func() tea.Cmd
	onNo    func()
}

// NewConfirm makes a yes/no dialog. danger colours it red and defaults to No.
func NewConfirm(th theme.Theme, title, message string, danger bool, onYes func() tea.Cmd) *Confirm {
	c := &Confirm{th: th, title: title, message: message, danger: danger, yes: !danger, yesLbl: "Yes", noLbl: "No", onYes: onYes}
	if danger {
		c.yesLbl = "Yes, do it"
	}
	return c
}

// WithLabels replaces the button labels.
func (c *Confirm) WithLabels(yes, no string) *Confirm {
	c.yesLbl, c.noLbl = yes, no
	return c
}

// OnNo sets a callback for a No/Esc answer.
func (c *Confirm) OnNo(f func()) *Confirm { c.onNo = f; return c }

// Title implements Modal.
func (c *Confirm) Title() string { return c.title }

// Icon implements Iconed.
func (c *Confirm) Icon() string {
	if c.danger {
		return "warning"
	}
	return "question"
}

// Danger reports whether the frame should be red.
func (c *Confirm) Danger() bool { return c.danger }

// Footer implements Footered.
func (c *Confirm) Footer() string { return "y/Enter confirm · n/Esc cancel · ←→ switch" }

// Size implements Sized.
func (c *Confirm) Size(cols, rows int) (int, int) { return min(64, cols-4), 9 }

// Update implements Modal.
func (c *Confirm) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return c, nil, false
	}
	switch km.String() {
	case "y", "Y":
		return c, c.yesCmd(), true
	case "n", "N", "esc":
		if c.onNo != nil {
			c.onNo()
		}
		return c, nil, true
	case "left", "right", "tab", "shift+tab", "h", "l":
		c.yes = !c.yes
	case "enter":
		if c.yes {
			return c, c.yesCmd(), true
		}
		if c.onNo != nil {
			c.onNo()
		}
		return c, nil, true
	}
	return c, nil, false
}

func (c *Confirm) yesCmd() tea.Cmd {
	if c.onYes == nil {
		return nil
	}
	return c.onYes()
}

// View implements Modal.
func (c *Confirm) View(w, h int) string {
	th := c.th
	btn := func(label string, sel bool, col lipgloss.TerminalColor) string {
		if sel {
			return lipgloss.NewStyle().Bold(true).Foreground(th.BgApp).Background(col).Padding(0, 1).Render(label)
		}
		return fg(th.TextMuted).Render("  " + label + "  ")
	}
	yc := th.Success
	if c.danger {
		yc = th.Error
	}
	msg := fg(th.TextPrimary).Render(render.Wrap(c.message, w))
	row := btn(c.yesLbl, c.yes, yc) + "  " + btn(c.noLbl, !c.yes, th.TextSecondary)
	return msg + "\n\n" + row
}

// ---------------------------------------------------------------- TextPrompt

// TextPrompt is a single-line text input in a modal.
type TextPrompt struct {
	th          theme.Theme
	title       string
	icon        string
	placeholder string
	value       []rune
	masked      bool
	allowEmpty  bool
	hint        string
	onSubmit    func(string) tea.Cmd
	onCancel    func()
}

// NewTextPrompt makes a single-line input. masked hides the text (API keys).
func NewTextPrompt(th theme.Theme, title, placeholder string, masked bool, onSubmit func(string) tea.Cmd) *TextPrompt {
	return &TextPrompt{th: th, title: title, icon: "pencil", placeholder: placeholder, masked: masked, onSubmit: onSubmit}
}

// WithValue prefills the input.
func (t *TextPrompt) WithValue(v string) *TextPrompt { t.value = []rune(v); return t }

// AllowEmpty lets an empty value submit.
func (t *TextPrompt) AllowEmpty() *TextPrompt { t.allowEmpty = true; return t }

// WithHint sets a line shown under the input.
func (t *TextPrompt) WithHint(h string) *TextPrompt { t.hint = h; return t }

// OnCancel sets a callback for Esc.
func (t *TextPrompt) OnCancel(f func()) *TextPrompt { t.onCancel = f; return t }

// Title implements Modal.
func (t *TextPrompt) Title() string { return t.title }

// Icon implements Iconed.
func (t *TextPrompt) Icon() string { return t.icon }

// Footer implements Footered.
func (t *TextPrompt) Footer() string { return "Enter confirm · Esc cancel" }

// Size implements Sized.
func (t *TextPrompt) Size(cols, rows int) (int, int) { return min(64, cols-4), 8 }

// Update implements Modal.
func (t *TextPrompt) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return t, nil, false
	}
	switch km.String() {
	case "esc":
		if t.onCancel != nil {
			t.onCancel()
		}
		return t, nil, true
	case "enter":
		v := strings.TrimSpace(string(t.value))
		if v == "" && !t.allowEmpty {
			t.hint = "Nothing entered."
			return t, nil, false
		}
		var cmd tea.Cmd
		if t.onSubmit != nil {
			cmd = t.onSubmit(v)
		}
		return t, cmd, true
	case "backspace":
		if len(t.value) > 0 {
			t.value = t.value[:len(t.value)-1]
		}
	case "ctrl+u":
		t.value = nil
	default:
		if s := printable(km); s != "" && len(t.value) < 400 {
			t.value = append(t.value, []rune(s)...)
		}
	}
	return t, nil, false
}

// View implements Modal.
func (t *TextPrompt) View(w, h int) string {
	th := t.th
	shown := string(t.value)
	if t.masked {
		shown = strings.Repeat("•", len(t.value))
	}
	var line string
	if len(t.value) == 0 {
		line = fg(th.Brand).Render("› ") + fg(th.TextDim).Render(t.placeholder) + fg(th.Brand).Render("▌")
	} else {
		line = fg(th.Brand).Render("› ") + fg(th.TextPrimary).Render(render.Truncate(shown, w-4)) + fg(th.Brand).Render("▌")
	}
	out := line
	if t.hint != "" {
		out += "\n\n" + fg(th.Warning).Render(t.hint)
	}
	return out
}

// ---------------------------------------------------------------- InfoView

// InfoView shows scrollable read-only text.
type InfoView struct {
	th    theme.Theme
	title string
	icon  string
	lines []string
	off   int
	last  int // visible rows last drawn, for paging
}

// NewInfoView makes a read-only text view.
func NewInfoView(th theme.Theme, title, icon string, lines []string) *InfoView {
	return &InfoView{th: th, title: title, icon: icon, lines: lines}
}

// Title implements Modal.
func (v *InfoView) Title() string { return v.title }

// Icon implements Iconed.
func (v *InfoView) Icon() string { return v.icon }

// Footer implements Footered.
func (v *InfoView) Footer() string { return "↑↓ scroll · PgUp/PgDn page · Esc close" }

// Size implements Sized.
func (v *InfoView) Size(cols, rows int) (int, int) {
	return min(88, int(float64(cols)*0.8)), min(rows-4, 30)
}

// Update implements Modal.
func (v *InfoView) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return v, nil, false
	}
	page := max(1, v.last-1)
	switch km.String() {
	case "esc", "q":
		return v, nil, true
	case "up", "k":
		v.off--
	case "down", "j":
		v.off++
	case "pgup":
		v.off -= page
	case "pgdown", " ":
		v.off += page
	case "home", "g":
		v.off = 0
	case "end", "G":
		v.off = len(v.lines)
	}
	v.off = clamp(v.off, 0, max(0, len(v.lines)-1))
	return v, nil, false
}

// View implements Modal.
func (v *InfoView) View(w, h int) string {
	v.last = h
	if len(v.lines) == 0 {
		return fg(v.th.TextFaint).Render("Nothing to show.")
	}
	var wrapped []string
	for _, l := range v.lines {
		wrapped = append(wrapped, strings.Split(render.Wrap(l, w), "\n")...)
	}
	end := min(len(wrapped), v.off+h)
	start := clamp(v.off, 0, max(0, len(wrapped)-1))
	out := append([]string{}, wrapped[start:end]...)
	if len(wrapped) > h {
		out = append(out, fg(v.th.TextFaint).Render(itoa(start+1)+"-"+itoa(end)+" / "+itoa(len(wrapped))))
	}
	return strings.Join(fitLines(out, w, h), "\n")
}

// itoa avoids importing strconv in every file.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
