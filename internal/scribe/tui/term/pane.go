// Package term runs a program on a pseudo-terminal and keeps its screen in a
// terminal emulator, so the TUI can draw the program inside a pane. It is
// pure Go on every platform (xpty uses ConPTY on Windows).
package term

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/charmbracelet/x/xpty"
)

// Scrollback is how many lines of history the emulator keeps.
const Scrollback = 2000

// frameGap throttles redraws requested by output: Updates wakes at most this
// often, which is about 30 frames a second.
const frameGap = 33 * time.Millisecond

// Pane is a program on a pseudo-terminal with its screen in an emulator.
// Methods are safe to call from any goroutine.
type Pane struct {
	argv []string

	pty    xpty.Pty
	cmd    *exec.Cmd
	closed atomic.Bool

	// emu, the fields below and everything the emulator's callbacks touch
	// are guarded by mu. mu is never held across a write to the pty: the
	// reader needs it to drain the program's output, and a program that is
	// blocked writing output cannot read its input.
	mu           sync.Mutex
	emu          *vt.Emulator
	w, h         int
	scroll       int // lines moved back into history; 0 is the live screen
	focused      bool
	cursorHidden bool
	appCursor    bool // DECCKM: cursor keys send SS3 forms
	bracketed    bool // bracketed paste is on
	title        string
	exited       bool
	code         int

	notify   chan struct{} // capacity 1; wakeups coalesce
	done     chan struct{} // closed when the process has exited
	readDone chan struct{} // closed when readLoop returns
}

// New starts cmd (argv[0] plus arguments) in dir on a new pseudo-terminal of
// w x h cells. The environment is inherited, with TERM set for an
// xterm-compatible program.
func New(cmd []string, dir string, w, h int) (*Pane, error) {
	if len(cmd) == 0 || cmd[0] == "" {
		return nil, errors.New("no command to run")
	}
	w, h = max(1, w), max(1, h)
	c := exec.Command(cmd[0], cmd[1:]...)
	if c.Err != nil {
		return nil, c.Err
	}
	c.Dir = dir
	c.Env = append(os.Environ(), "TERM=xterm-256color")
	pt, err := xpty.NewPty(w, h)
	if err != nil {
		return nil, fmt.Errorf("open a pseudo-terminal: %w", err)
	}
	if err := pt.Start(c); err != nil {
		_ = pt.Close()
		return nil, fmt.Errorf("start %s: %w", cmd[0], err)
	}
	// Only the master side is kept, so the program's exit hangs up the pty.
	if u, ok := pt.(*xpty.UnixPty); ok {
		_ = u.Slave().Close()
	}

	p := &Pane{
		argv:     append([]string(nil), cmd...),
		pty:      pt,
		cmd:      c,
		emu:      vt.NewEmulator(w, h),
		w:        w,
		h:        h,
		notify:   make(chan struct{}, 1),
		done:     make(chan struct{}),
		readDone: make(chan struct{}),
	}
	p.emu.SetScrollbackSize(Scrollback)
	p.emu.SetCallbacks(vt.Callbacks{
		Title:            func(s string) { p.title = s },
		CursorVisibility: func(visible bool) { p.cursorHidden = !visible },
		EnableMode:       func(m ansi.Mode) { p.setMode(m, true) },
		DisableMode:      func(m ansi.Mode) { p.setMode(m, false) },
	})
	go p.readLoop()
	go p.replyLoop()
	go p.waitLoop()
	return p, nil
}

func (p *Pane) setMode(m ansi.Mode, on bool) {
	switch m {
	case ansi.ModeCursorKeys:
		p.appCursor = on
	case ansi.ModeBracketedPaste:
		p.bracketed = on
	}
}

// readLoop feeds the program's output to the emulator.
func (p *Pane) readLoop() {
	defer close(p.readDone)
	buf := make([]byte, 32*1024)
	for {
		n, err := p.pty.Read(buf)
		if n > 0 {
			p.mu.Lock()
			_, _ = p.emu.Write(buf[:n])
			p.mu.Unlock()
			p.wake()
		}
		if err != nil {
			return // the pty is closed once the program exits
		}
	}
}

// replyLoop sends the emulator's replies (device status, attributes) back to
// the program. The emulator blocks on a reply nobody reads, so this runs for
// the life of the pane.
func (p *Pane) replyLoop() {
	buf := make([]byte, 1024)
	for {
		n, err := p.emu.Read(buf)
		if n > 0 && !p.closed.Load() {
			_, _ = p.pty.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// waitLoop records the exit status once the program has ended.
func (p *Pane) waitLoop() {
	_ = xpty.WaitProcess(context.Background(), p.cmd)
	// Let the reader take the output the program wrote before it exited. A
	// background job can hold the pty open, so the wait is bounded.
	select {
	case <-p.readDone:
	case <-time.After(500 * time.Millisecond):
	}
	p.mu.Lock()
	p.exited = true
	p.code = -1
	if st := p.cmd.ProcessState; st != nil {
		p.code = st.ExitCode()
	}
	p.mu.Unlock()
	close(p.done)
	p.wake()
}

func (p *Pane) wake() {
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

// Updates is signalled when the screen changed or the program exited.
// Wakeups coalesce, so a receiver sees at most one pending signal.
func (p *Pane) Updates() <-chan struct{} { return p.notify }

// FrameGap is the minimum time a receiver should wait between redraws.
func FrameGap() time.Duration { return frameGap }

// Done is closed when the program has exited.
func (p *Pane) Done() <-chan struct{} { return p.done }

// Write sends raw bytes to the program. It does not move the scrollback.
func (p *Pane) Write(keys []byte) error {
	if p.closed.Load() {
		return io.ErrClosedPipe
	}
	if len(keys) == 0 {
		return nil
	}
	_, err := p.pty.Write(keys)
	return err
}

// SendKey translates a key press and sends it. Typing returns the view to
// the live screen. A paste is wrapped in bracketed-paste markers when the
// program has turned that mode on.
func (p *Pane) SendKey(km tea.KeyMsg) error {
	p.mu.Lock()
	app, bracketed := p.appCursor, p.bracketed
	p.scroll = 0
	p.mu.Unlock()
	if km.Paste && bracketed {
		text := string(km.Runes)
		return p.Write([]byte(ansi.BracketedPasteStart + text + ansi.BracketedPasteEnd))
	}
	return p.Write(Translate(km, app))
}

// Wheel handles a mouse wheel step of lines rows. A program on the alternate
// screen (an editor, a pager) gets cursor keys, as a terminal does; otherwise
// the scrollback moves.
func (p *Pane) Wheel(up bool, lines int) {
	if lines < 1 {
		lines = 1
	}
	p.mu.Lock()
	alt := p.emu.IsAltScreen()
	app := p.appCursor
	if !alt {
		delta := lines
		if !up {
			delta = -lines
		}
		p.scroll = clamp(p.scroll+delta, 0, p.emu.ScrollbackLen())
		p.mu.Unlock()
		p.wake()
		return
	}
	p.mu.Unlock()
	key := tea.KeyDown
	if up {
		key = tea.KeyUp
	}
	for i := 0; i < lines; i++ {
		_ = p.Write(Translate(tea.KeyMsg{Type: key}, app))
	}
}

// ScrollToBottom returns the view to the live screen.
func (p *Pane) ScrollToBottom() {
	p.mu.Lock()
	p.scroll = 0
	p.mu.Unlock()
}

// Resize changes the emulator and the pty to w x h cells.
func (p *Pane) Resize(w, h int) error {
	w, h = max(1, w), max(1, h)
	p.mu.Lock()
	if w == p.w && h == p.h {
		p.mu.Unlock()
		return nil
	}
	p.w, p.h = w, h
	p.emu.Resize(w, h)
	p.scroll = clamp(p.scroll, 0, p.emu.ScrollbackLen())
	p.mu.Unlock()
	if p.closed.Load() {
		return io.ErrClosedPipe
	}
	return p.pty.Resize(w, h)
}

// Size is the pane's current size in cells.
func (p *Pane) Size() (w, h int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.w, p.h
}

// SetFocus marks the pane focused, which draws the cursor in reverse video.
func (p *Pane) SetFocus(on bool) {
	p.mu.Lock()
	p.focused = on
	p.mu.Unlock()
}

// Title is the program's window title, or its command name until it sets one.
func (p *Pane) Title() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.title != "" {
		return p.title
	}
	return filepath.Base(p.argv[0])
}

// Exited reports whether the program has ended, and its exit code (-1 when
// it was killed by a signal or the code is unknown).
func (p *Pane) Exited() (bool, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exited, p.code
}

// Pid is the program's process id.
func (p *Pane) Pid() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Close kills the program if it is still running, hangs up the pty and
// releases the emulator. It is safe to call more than once.
func (p *Pane) Close() error {
	if !p.closed.CompareAndSwap(false, true) {
		return nil
	}
	if exited, _ := p.Exited(); !exited && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	// Ending the emulator's reply pipe lets replyLoop return. The pipe is
	// closed directly: Emulator.Close writes a flag that replyLoop reads.
	if c, ok := p.emu.InputPipe().(io.Closer); ok {
		_ = c.Close()
	}
	// xpty also closes the slave we already let go of, so its error is
	// noise: the pty is gone either way.
	_ = p.pty.Close()
	p.wake()
	return nil
}

// View draws the pane as w x h cells of ANSI text, one line per row, with no
// trailing newline. The cursor is shown in reverse video when focused and
// the live screen is showing.
func (p *Pane) View() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	showCursor := p.focused && !p.cursorHidden && p.scroll == 0
	var cur uv.Position
	if showCursor {
		cur = p.emu.CursorPosition()
	}
	var b strings.Builder
	b.Grow(p.w * p.h * 2)
	for r := 0; r < p.h; r++ {
		if r > 0 {
			b.WriteByte('\n')
		}
		p.writeRow(&b, r, showCursor && r == cur.Y, cur.X)
	}
	return b.String()
}

// writeRow draws row r. Caller holds p.mu.
func (p *Pane) writeRow(b *strings.Builder, r int, cursorRow bool, curX int) {
	line := p.line(r)
	var last uv.Style
	styled := false
	cols := 0
	for x := 0; x < p.w && x < len(line); x++ {
		c := line[x]
		if c.Width == 0 {
			// second half of a wide character, already counted
			continue
		}
		style := c.Style
		if cursorRow && x == curX {
			style.Attrs |= uv.AttrReverse
		}
		if !styled || !style.Equal(&last) {
			b.WriteString(ansi.ResetStyle)
			b.WriteString(style.String())
			last = style
			styled = true
		}
		content := c.Content
		if content == "" {
			content = " "
		}
		b.WriteString(content)
		cols += max(c.Width, 1)
	}
	if styled {
		b.WriteString(ansi.ResetStyle)
	}
	if cols < p.w {
		b.WriteString(strings.Repeat(" ", p.w-cols))
	}
}

// line is row r of what the pane shows: the live screen, or history while
// scrolled back. Caller holds p.mu.
func (p *Pane) line(r int) uv.Line {
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

// Text is the visible screen as plain text, one line per row. It is for
// tests and diagnostics.
func (p *Pane) Text() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	rows := make([]string, p.h)
	for r := range rows {
		var b strings.Builder
		for _, c := range p.line(r) {
			if c.Width == 0 {
				continue
			}
			if c.Content == "" {
				b.WriteByte(' ')
			} else {
				b.WriteString(c.Content)
			}
		}
		rows[r] = strings.TrimRight(b.String(), " ")
	}
	return strings.Join(rows, "\n")
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
