package main

// The screen of Lectern's own attach client (native_bare.go): each pane is a
// terminal opened the way the browser terminal opens it, read into a
// terminal emulator, and drawn into its rectangle of this terminal with the
// key bar on the last row. Only rows that changed are written again.

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/filelinks"
	"github.com/JeremiahM37/lectern/v2/internal/terminal/webterm"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// barePane is one terminal on screen: the agent, or a shell beside it.
type barePane struct {
	c     *bareClient
	conn  webterm.Conn
	agent bool
	name  string

	mu         sync.Mutex // guards emu and everything the emulator's callbacks set
	emu        *vt.Emulator
	x, y, w, h int
	modes      map[ansi.Mode]bool
	hidden     bool
	cursor     int // DECSCUSR style, -1 when the program never set one
	kitty      []int
	mok        int
	// scroll is how many lines the view is moved back into history; 0 is
	// the live screen.
	scroll int
	ended  chan struct{}
}

const bareHistory = 5000

func newBarePane(c *bareClient, conn webterm.Conn, agent bool, name string, w, h int) *barePane {
	p := &barePane{c: c, conn: conn, agent: agent, name: name, w: w, h: h,
		modes: map[ansi.Mode]bool{}, cursor: -1, ended: make(chan struct{})}
	p.emu = vt.NewEmulator(max(1, w), max(1, h))
	p.emu.SetScrollbackSize(bareHistory)
	p.emu.SetCallbacks(vt.Callbacks{
		EnableMode:       func(m ansi.Mode) { p.modes[m] = true; c.markDirty() },
		DisableMode:      func(m ansi.Mode) { delete(p.modes, m); c.markDirty() },
		CursorVisibility: func(visible bool) { p.hidden = !visible },
		CursorStyle:      func(style vt.CursorStyle, blink bool) { p.cursor = cursorNumber(style, blink) },
		Bell:             func() { c.bell() },
	})
	// A program copying (OSC 52) reaches this terminal's clipboard, as the
	// tmux path's set-clipboard does. A request to read the clipboard is
	// never passed on: the agent must not be able to read what you copied.
	p.emu.RegisterOscHandler(52, func(data []byte) bool {
		parts := bytes.SplitN(data, []byte{';'}, 3)
		if len(parts) == 3 && string(parts[2]) != "?" {
			c.passthrough("\x1b]" + string(data) + "\a")
		}
		return true
	})
	// Keyboard protocols the program asks for are asked of this terminal in
	// turn while its pane has the focus (syncModes).
	p.emu.RegisterCsiHandler(ansi.Command('>', 0, 'u'), func(params ansi.Params) bool {
		f, _, _ := params.Param(0, 0)
		p.kitty = append(p.kitty, f)
		c.markDirty()
		return true
	})
	p.emu.RegisterCsiHandler(ansi.Command('<', 0, 'u'), func(params ansi.Params) bool {
		n, _, _ := params.Param(0, 1)
		for ; n > 0 && len(p.kitty) > 0; n-- {
			p.kitty = p.kitty[:len(p.kitty)-1]
		}
		c.markDirty()
		return true
	})
	p.emu.RegisterCsiHandler(ansi.Command('=', 0, 'u'), func(params ansi.Params) bool {
		f, _, _ := params.Param(0, 0)
		mode, _, _ := params.Param(1, 1)
		cur := 0
		if len(p.kitty) > 0 {
			cur = p.kitty[len(p.kitty)-1]
		}
		switch mode {
		case 2:
			f |= cur
		case 3:
			f = cur &^ f
		}
		if len(p.kitty) == 0 {
			p.kitty = append(p.kitty, f)
		} else {
			p.kitty[len(p.kitty)-1] = f
		}
		c.markDirty()
		return true
	})
	p.emu.RegisterCsiHandler(ansi.Command('>', 0, 'm'), func(params ansi.Params) bool {
		if res, _, _ := params.Param(0, 0); res == 4 {
			p.mok, _, _ = params.Param(1, 0)
			c.markDirty()
		}
		return true
	})
	return p
}

// start reads the terminal into the emulator, and the emulator's answers to
// the program's queries (cursor position, device attributes) back to it.
func (p *barePane) start() {
	go func() {
		defer close(p.ended)
		for {
			b, err := p.conn.Read()
			if len(b) > 0 {
				p.mu.Lock()
				_, _ = p.emu.Write(b)
				p.mu.Unlock()
				p.c.markDirty()
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := p.emu.Read(buf)
			if n > 0 {
				_ = p.conn.Write(append([]byte(nil), buf[:n]...))
			}
			if err != nil {
				return
			}
		}
	}()
}

func (p *barePane) close() {
	p.conn.Close()
	_ = p.emu.Close()
}

func (p *barePane) resize(x, y, w, h int) {
	p.mu.Lock()
	changed := w != p.w || h != p.h
	p.x, p.y, p.w, p.h = x, y, w, h
	if changed {
		p.emu.Resize(max(1, w), max(1, h))
	}
	p.mu.Unlock()
	if changed {
		_ = p.conn.Resize(max(1, w), max(1, h))
	}
}

func (p *barePane) contains(col, row int) bool {
	return col >= p.x && col < p.x+p.w && row >= p.y && row < p.y+p.h
}

// wantsMouse reports a program that reads mouse events itself.
func (p *barePane) wantsMouse() bool {
	return p.modes[ansi.ModeMouseNormal] || p.modes[ansi.ModeMouseButtonEvent] || p.modes[ansi.ModeMouseAnyEvent]
}

// line is row r of what the pane shows: the live screen, or history while
// scrolled back. Caller holds p.mu.
func (p *barePane) line(r int) uv.Line {
	line := make(uv.Line, p.w)
	history := 0
	if p.scroll > 0 {
		history = p.emu.ScrollbackLen()
	}
	index := history - p.scroll + r
	for x := 0; x < p.w; x++ {
		var c *uv.Cell
		if index < history {
			c = p.emu.ScrollbackCellAt(x, index)
		} else {
			c = p.emu.CellAt(x, index-history)
		}
		if c != nil {
			line[x] = *c
		} else {
			line[x] = uv.EmptyCell
		}
	}
	return line
}

// rows is the pane's text, for finding links and copying. A full row
// followed by another is taken to continue onto it, as an agent's wrapped
// path does.
func (p *barePane) rows() []filelinks.Row {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]filelinks.Row, p.h)
	for r := 0; r < p.h; r++ {
		text := p.line(r).String()
		out[r] = filelinks.Row{Text: text}
		if r > 0 && displayWidth(out[r-1].Text) >= p.w && strings.TrimSpace(text) != "" {
			out[r].Wrapped = true
		}
	}
	return out
}

// hyperlinkAt is the OSC 8 address of the cell, if the program set one.
func (p *barePane) hyperlinkAt(x, y int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if y < 0 || y >= p.h || x < 0 || x >= p.w {
		return ""
	}
	return p.line(y)[x].Link.URL
}

func displayWidth(s string) int { return ansi.StringWidth(s) }

func cursorNumber(style vt.CursorStyle, blink bool) int {
	n := 2*int(style) + 1
	if !blink {
		n++
	}
	return n
}

// ---- layout

// layout gives every pane its rectangle above the key bar: one pane fills
// it; more sit side by side (|) or stacked (-) with a one-cell rule between.
// Caller holds c.mu.
func (c *bareClient) layout() {
	w, h := max(1, c.cols), max(1, c.rows-1)
	n := len(c.panes)
	if n == 0 {
		return
	}
	pos := 0
	for i, p := range c.panes {
		if n == 1 {
			p.resize(0, 0, w, h)
			continue
		}
		if c.dir == '|' {
			avail := max(n, w-(n-1))
			size := avail / n
			if i < avail%n {
				size++
			}
			p.resize(pos, 0, size, h)
			pos += size + 1
		} else {
			avail := max(n, h-(n-1))
			size := avail / n
			if i < avail%n {
				size++
			}
			p.resize(0, pos, w, size)
			pos += size + 1
		}
	}
	c.full = true
}

// ---- drawing

var barStyle = "\x1b[0;38;5;252;48;5;236m"

// render writes what changed since the last frame, inside a synchronized
// update so the terminal shows whole frames.
func (c *bareClient) render() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.paused || c.cols <= 0 || c.rows <= 0 {
		return
	}
	var b strings.Builder
	b.WriteString("\x1b[?2026h\x1b[?25l")
	if c.full {
		b.WriteString("\x1b[0m\x1b[H\x1b[2J")
		c.cache = map[*barePane][]string{}
		c.lastBar = ""
	}
	for _, p := range c.panes {
		p.mu.Lock()
		cache := c.cache[p]
		if len(cache) != p.h {
			cache = make([]string, p.h)
			for i := range cache {
				cache[i] = "\x00" // never matches a real row
			}
		}
		for r := 0; r < p.h; r++ {
			line := p.line(r)
			c.decorate(p, r, line)
			s := line.Render()
			if s == cache[r] {
				continue
			}
			cache[r] = s
			fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[0m%s\x1b[%d;%dH%s\x1b[0m", p.y+r+1, p.x+1, strings.Repeat(" ", p.w), p.y+r+1, p.x+1, s)
		}
		c.cache[p] = cache
		p.mu.Unlock()
	}
	if c.full && len(c.panes) > 1 {
		b.WriteString("\x1b[0;38;5;240m")
		for _, p := range c.panes[:len(c.panes)-1] {
			if c.dir == '|' {
				for r := 0; r < c.rows-1; r++ {
					fmt.Fprintf(&b, "\x1b[%d;%dH│", r+1, p.x+p.w+1)
				}
			} else {
				fmt.Fprintf(&b, "\x1b[%d;1H%s", p.y+p.h+1, strings.Repeat("─", c.cols))
			}
		}
		b.WriteString("\x1b[0m")
	}
	if c.overlay != nil {
		c.overlay.draw(&b, c.cols, c.rows-1)
		// Rows under the box are drawn again once it closes.
		c.cache = map[*barePane][]string{}
	}
	if bar := c.barText(); bar != c.lastBar {
		c.lastBar = bar
		fmt.Fprintf(&b, "\x1b[%d;1H%s%s\x1b[0m", c.rows, barStyle, bar)
	}
	c.syncModes(&b)
	if p := c.focused(); p != nil && c.overlay == nil && c.hints == nil {
		p.mu.Lock()
		if p.scroll == 0 && !p.hidden {
			pos := p.emu.CursorPosition()
			fmt.Fprintf(&b, "\x1b[%d;%dH", p.y+pos.Y+1, p.x+pos.X+1)
			if p.cursor >= 0 {
				fmt.Fprintf(&b, "\x1b[%d q", p.cursor)
			}
			b.WriteString("\x1b[?25h")
		}
		p.mu.Unlock()
	}
	b.WriteString("\x1b[?2026l")
	c.full = false
	c.write(b.String())
}

// decorate marks a selection and the hints of hints mode in a row before it
// is drawn. Caller holds c.mu and p.mu.
func (c *bareClient) decorate(p *barePane, r int, line uv.Line) {
	if s := c.sel; s != nil && s.pane == p {
		a, z := s.ordered()
		for x := range line {
			if (r > a[1] || r == a[1] && x >= a[0]) && (r < z[1] || r == z[1] && x <= z[0]) {
				line[x].Style.Attrs |= uv.AttrReverse
			}
		}
	}
	if h := c.hints; h != nil && h.pane == p {
		rows := h.rows
		for _, hint := range h.hints {
			if !strings.HasPrefix(hint.label, h.typed) {
				continue
			}
			for _, span := range hint.link.Spans {
				if span.Row != r {
					continue
				}
				text := []rune(rows[r].Text)
				for i := span.Start; i < span.End && i < len(text); i++ {
					if x := runeToCell(rows[r].Text, i); x < len(line) {
						line[x].Style.Attrs |= uv.AttrBold
						line[x].Style.Underline = uv.UnderlineSingle
					}
				}
			}
			first := hint.link.Spans[0]
			if first.Row != r {
				continue
			}
			x := runeToCell(rows[r].Text, first.Start)
			for _, ch := range hint.label[len(h.typed):] {
				if x >= len(line) {
					break
				}
				line[x] = uv.Cell{Content: string(ch), Width: 1, Style: uv.Style{Fg: ansi.Black, Bg: ansi.Yellow, Attrs: uv.AttrBold}}
				x++
			}
		}
	}
}

// runeToCell is the screen column of the i-th rune of text, the inverse of
// cellToRune.
func runeToCell(text string, i int) int {
	col := 0
	for n, r := range []rune(text) {
		if n == i {
			return col
		}
		col += max(1, ansi.StringWidth(string(r)))
	}
	return col
}

// syncModes asks this terminal for the modes the focused program asked its
// own terminal for, so keys, pastes and mouse reports arrive the way it
// expects. Mouse reporting for clicks, drags and the wheel is always on:
// the client needs them for links, selection and scrolling. Caller holds c.mu.
func (c *bareClient) syncModes(b *strings.Builder) {
	p := c.focused()
	want := map[int]bool{1002: true, 1006: true}
	kitty, mok := 0, 0
	if p != nil {
		p.mu.Lock()
		want[1] = p.modes[ansi.ModeCursorKeys]
		want[2004] = p.modes[ansi.ModeBracketedPaste]
		want[1004] = p.modes[ansi.ModeFocusEvent]
		want[1003] = p.modes[ansi.ModeMouseAnyEvent]
		if len(p.kitty) > 0 {
			kitty = p.kitty[len(p.kitty)-1]
		}
		mok = p.mok
		p.mu.Unlock()
	}
	for _, m := range []int{1, 1002, 1003, 1004, 1006, 2004} {
		if want[m] != c.realModes[m] {
			c.realModes[m] = want[m]
			if want[m] {
				b.WriteString("\x1b[?" + strconv.Itoa(m) + "h")
			} else {
				b.WriteString("\x1b[?" + strconv.Itoa(m) + "l")
			}
		}
	}
	if kitty != c.realKitty {
		if c.realKitty != 0 {
			b.WriteString("\x1b[<u")
		}
		if kitty != 0 {
			b.WriteString("\x1b[>" + strconv.Itoa(kitty) + "u")
		}
		c.realKitty = kitty
	}
	if mok != c.realMok {
		b.WriteString("\x1b[>4;" + strconv.Itoa(mok) + "m")
		c.realMok = mok
	}
}

// ---- selection

type bareSelection struct {
	pane       *barePane
	start, end [2]int // x, y in pane cells
	moved      bool
}

func (s *bareSelection) ordered() ([2]int, [2]int) {
	a, z := s.start, s.end
	if z[1] < a[1] || z[1] == a[1] && z[0] < a[0] {
		a, z = z, a
	}
	return a, z
}

// text is what the selection covers, one line per row.
func (s *bareSelection) text() string {
	rows := s.pane.rows()
	a, z := s.ordered()
	var out []string
	for y := a[1]; y <= z[1] && y < len(rows); y++ {
		runes := []rune(rows[y].Text)
		from, to := 0, len(runes)
		if y == a[1] {
			from = min(len(runes), cellToRune(rows[y].Text, a[0]))
		}
		if y == z[1] {
			to = min(len(runes), cellToRune(rows[y].Text, z[0])+1)
		}
		if from > to {
			from = to
		}
		out = append(out, strings.TrimRight(string(runes[from:to]), " "))
	}
	return strings.Join(out, "\n")
}

// ---- menus drawn over the panes

type bareItem struct {
	label, key string
	run        func()
}

type bareOverlay struct {
	title string
	items []bareItem
	index int
	// top and left of the box as last drawn, for mouse clicks.
	top, left, width int
}

func (o *bareOverlay) draw(b *strings.Builder, cols, rows int) {
	width := ansi.StringWidth(o.title) + 4
	for _, it := range o.items {
		width = max(width, ansi.StringWidth(it.label)+ansi.StringWidth(it.key)+8)
	}
	width = min(width, max(10, cols-2))
	height := len(o.items) + 2
	o.left = max(0, (cols-width)/2)
	o.top = max(0, (rows-height)/2)
	o.width = width
	inner := width - 2
	title := ansi.Truncate(o.title, inner-2, "…")
	pad := inner - ansi.StringWidth(title)
	fmt.Fprintf(b, "\x1b[%d;%dH\x1b[0;38;5;252;48;5;236m┌%s%s%s┐", o.top+1, o.left+1,
		strings.Repeat("─", pad/2), title, strings.Repeat("─", pad-pad/2))
	for i, it := range o.items {
		style := "\x1b[0;38;5;252;48;5;236m"
		if i == o.index {
			style = "\x1b[0;38;5;255;48;5;60m"
		}
		label := ansi.Truncate(it.label, inner-ansi.StringWidth(it.key)-5, "…")
		gap := inner - 2 - ansi.StringWidth(label) - ansi.StringWidth(it.key) - 2
		fmt.Fprintf(b, "\x1b[%d;%dH\x1b[0;38;5;252;48;5;236m│%s %s%s(%s) \x1b[0;38;5;252;48;5;236m│",
			o.top+2+i, o.left+1, style, label, strings.Repeat(" ", max(1, gap)), it.key)
	}
	fmt.Fprintf(b, "\x1b[%d;%dH└%s┘\x1b[0m", o.top+height, o.left+1, strings.Repeat("─", inner))
}

// itemAt is the item drawn at a screen cell, or -1.
func (o *bareOverlay) itemAt(col, row int) int {
	i := row - o.top - 1
	if col <= o.left || col >= o.left+o.width-1 || i < 0 || i >= len(o.items) {
		return -1
	}
	return i
}

// ---- hints mode

type bareHints struct {
	pane    *barePane
	rows    []filelinks.Row
	hints   []hint
	typed   string
	actions bool
}
