package main

// Lectern's own attach client: the attached-terminal experience without a
// local tmux (docs/terminal-client.md, docs/ptyhost.md).
//
// The tmux path (native_attach.go) wraps an attachment in a private tmux
// server for its key bar, its Ctrl+] menu and its mouse bindings. Where tmux
// is not installed — the default on macOS, Windows and minimal Linux now
// that sessions live on Lectern's PTY host — this client does the same work
// itself: it owns the keyboard and the screen, shows the session in an
// emulator above a key bar it draws on the last row, and keeps every key the
// tmux path has:
//
//	Ctrl+] then  m actions · y allow a waiting request · u send a file ·
//	             e pick a path or link · | shell right · - shell below ·
//	             o next pane · x close pane · [ scroll back · d leave ·
//	             ? every key · Ctrl+] a literal Ctrl+]
//	Ctrl+\       send a file. Always taken here: it is never delivered as
//	             SIGQUIT, which would end the agent.
//
// Double-click opens a path or link the agent printed, right-click offers
// its menu, a drag copies to the clipboard (OSC 52) and the wheel scrolls
// back — unless the program asked for the mouse itself, which then gets it.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/console"
	"github.com/JeremiahM37/lectern/v2/internal/filelinks"
	"github.com/JeremiahM37/lectern/v2/internal/terminal/webterm"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/cancelreader"
	"golang.org/x/term"
)

// bareEnv forces this client even where tmux is installed (tests, and anyone
// who prefers it).
const bareEnv = "LECTERN_NATIVE_BARE"

func bareForced() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(bareEnv))) {
	case "1", "on", "true", "yes":
		return true
	}
	return false
}

type bareClient struct {
	controls nativeControls
	self     string
	in       *os.File
	out      io.Writer
	outFd    int
	original *term.State

	mu         sync.Mutex
	panes      []*barePane
	focus      int
	dir        byte
	cols, rows int
	prefix     bool
	overlay    *bareOverlay
	hints      *bareHints
	sel        *bareSelection
	message    string
	messageEnd time.Time
	needsYou   string
	approvalID string
	paused     bool
	full       bool
	cache      map[*barePane][]string
	lastBar    string
	realModes  map[int]bool
	realKitty  int
	realMok    int
	click      struct {
		pane *barePane
		x, y int
		at   time.Time
	}

	writeMu  sync.Mutex
	dirty    chan struct{}
	done     chan string
	doneOnce sync.Once
	reader   cancelreader.CancelReader
	readerWG sync.WaitGroup
	pending  []byte
	pasting  bool
	links    *linkEnv
	linkDir  string
	pressed  bool
	// popup runs the Ctrl+] controls in place of the session; nil is the
	// real controls dashboard. Tests swap in their own.
	popup func(action string)
	// clipActive tells the clipboard bridge the person is typing (nil: off).
	clipActive func()
}

// runBareAttachment shows the attachment argv with Lectern's own key bar
// and controls until the person leaves or the session ends.
func runBareAttachment(argv []string, controls nativeControls, replace bool) error {
	c, err := newBareClient(controls)
	if err != nil {
		return err
	}
	return c.run(argv, replace)
}

func newBareClient(controls nativeControls) (*bareClient, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return &bareClient{controls: controls, self: self, in: os.Stdin, out: os.Stdout, outFd: int(os.Stdout.Fd()),
		cache: map[*barePane][]string{}, realModes: map[int]bool{},
		dirty: make(chan struct{}, 1), done: make(chan string, 1), dir: '|'}, nil
}

// run attaches to argv until the person leaves or the session ends.
func (c *bareClient) run(argv []string, replace bool) error {
	controls := c.controls
	self := c.self
	var err error
	c.cols, c.rows = 80, 24
	if w, h, err := term.GetSize(c.outFd); err == nil && w > 0 && h > 1 {
		c.cols, c.rows = w, h
	}
	conn, err := webterm.Open(argv, self, c.cols, max(1, c.rows-1))
	if err != nil {
		return err
	}
	if c.linkDir, err = os.MkdirTemp("", "lectern-attach-"); err == nil {
		_ = os.Chmod(c.linkDir, 0o700)
		defer os.RemoveAll(c.linkDir)
	}
	c.links = &linkEnv{kind: controls.Kind, id: controls.ID, base: strings.TrimRight(controls.Base, "/"),
		token: controls.Token, dir: c.linkDir, ui: c}
	agent := newBarePane(c, conn, true, "agent", c.cols, max(1, c.rows-1))
	c.panes = []*barePane{agent}

	if c.original, err = term.MakeRaw(int(c.in.Fd())); err != nil {
		conn.Close()
		return err
	}
	c.write("\x1b[?1049h\x1b[H\x1b[2J")
	c.full = true
	agent.start()
	if err := c.startInput(); err != nil {
		c.teardown()
		conn.Close()
		return err
	}
	var stopClip func()
	c.clipActive, stopClip = startClipboardBridge(controls, "", "")
	defer stopClip()
	stopWatch := make(chan struct{})
	go c.watchSize(stopWatch)
	go c.renderLoop(stopWatch)
	if controls.Kind == "session" && controls.Base != "" {
		go c.pollApprovals(stopWatch)
	}
	go func() {
		<-agent.ended
		c.finish("ended")
	}()
	reason := <-c.done
	close(stopWatch)
	c.stopInput()
	c.mu.Lock()
	c.paused = true
	panes := c.panes
	c.mu.Unlock()
	c.teardown()
	for _, p := range panes {
		p.close()
	}
	if replace && controls.Kind == "session" {
		if reason == "ended" {
			fmt.Fprintln(os.Stderr, "The session ended. `lectern` shows your sessions; r there brings an ended one back.")
		} else {
			fmt.Fprintln(os.Stderr, "Left the session; it keeps running. `lectern` shows all your sessions.")
		}
	}
	return nil
}

func (c *bareClient) finish(reason string) {
	c.doneOnce.Do(func() { c.done <- reason })
}

// teardown puts this terminal back the way it was found.
func (c *bareClient) teardown() {
	var b strings.Builder
	b.WriteString("\x1b[?2026l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?1004l\x1b[?2004l\x1b[?1l")
	if c.realKitty != 0 {
		b.WriteString("\x1b[<u")
	}
	if c.realMok != 0 {
		b.WriteString("\x1b[>4;0m")
	}
	b.WriteString("\x1b[0 q\x1b[0m\x1b[?25h\x1b[?1049l")
	c.write(b.String())
	if c.original != nil {
		_ = term.Restore(int(c.in.Fd()), c.original)
	}
}

func (c *bareClient) write(s string) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, _ = io.WriteString(c.out, s)
}

// passthrough writes a program's request (a clipboard copy) to this terminal
// unless another program has the screen.
func (c *bareClient) passthrough(s string) {
	if !c.paused {
		c.write(s)
	}
}

func (c *bareClient) bell() { c.passthrough("\a") }

func (c *bareClient) markDirty() {
	select {
	case c.dirty <- struct{}{}:
	default:
	}
}

func (c *bareClient) renderLoop(stop chan struct{}) {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-c.dirty:
			// Gather a burst of output into one frame.
			time.Sleep(8 * time.Millisecond)
			c.render()
		case <-tick.C:
			c.mu.Lock()
			expired := c.message != "" && time.Now().After(c.messageEnd)
			if expired {
				c.message = ""
			}
			c.mu.Unlock()
			if expired {
				c.render()
			}
		}
	}
}

// watchSize follows this terminal's size. A poll works the same on every
// platform, and a quarter second is quick enough for a person resizing.
func (c *bareClient) watchSize(stop chan struct{}) {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			w, h, err := term.GetSize(c.outFd)
			if err != nil || w <= 0 || h <= 1 {
				continue
			}
			c.mu.Lock()
			if w != c.cols || h != c.rows {
				c.cols, c.rows = w, h
				c.layout()
				c.mu.Unlock()
				c.markDirty()
				continue
			}
			c.mu.Unlock()
		}
	}
}

// say shows a message on the bar for a few seconds.
func (c *bareClient) say(message string) {
	c.mu.Lock()
	c.message = message
	c.messageEnd = time.Now().Add(5 * time.Second)
	c.mu.Unlock()
	c.markDirty()
}

// copy puts text on this terminal's clipboard with OSC 52, which works over
// SSH as well.
func (c *bareClient) copy(text string) error {
	c.write("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\a")
	return nil
}

// send types text into the agent's pane without pressing Enter.
func (c *bareClient) send(text string) error {
	if r, ok := firstInsertControl(text); ok {
		return fmt.Errorf("refusing to type text containing control character %s", strconv.QuoteRune(r))
	}
	c.mu.Lock()
	agent := c.panes[0]
	c.mu.Unlock()
	return agent.conn.Write([]byte(text))
}

// view shows a file in this terminal for a moment, in place of the session.
func (c *bareClient) view(link filelinks.Link) error {
	go c.takeover(func() {
		if err := c.links.view(link); err != nil {
			fmt.Println(err)
			fmt.Print("\nPress Enter to close.")
			_, _ = fmt.Scanln()
		}
	})
	return nil
}

func (c *bareClient) focused() *barePane {
	if c.focus < 0 || c.focus >= len(c.panes) {
		return nil
	}
	return c.panes[c.focus]
}

// ---- the key bar

// barText is the last row. Caller holds c.mu.
func (c *bareClient) barText() string {
	width := c.cols
	text := ""
	p := c.focused()
	switch {
	case c.message != "":
		text = " " + c.message
	case c.overlay != nil:
		text = " ↑↓ choose · Enter or the key runs it · Esc close"
	case c.hints != nil:
		text = " Type a label to open it · Shift+label for its menu · Esc cancel"
	case p != nil && p.scroll > 0:
		text = " Scrolled back · ↑↓ PgUp PgDn move · q or Esc back to live"
	case c.prefix:
		word := "leave"
		if c.controls.TabView {
			word = "close tab"
		}
		text = " \x1b[7m Ctrl+] then \x1b[27m m actions · d " + word + " · u send file · | shell right · - shell below · e open a link · ? all keys"
		if c.needsYou != "" {
			text = " \x1b[7m Ctrl+] then \x1b[27m y allow once · m answer or more · d " + word + " · ? all keys"
		}
	default:
		leave := "\x1b[1mCtrl+] d\x1b[22m leave"
		if c.controls.TabView {
			leave = "\x1b[1mCtrl+] d\x1b[22m close tab"
		}
		text = " \x1b[1mCtrl+]\x1b[22m menu · " + leave
		if width >= 60 {
			text += " · \x1b[1mCtrl+\\\x1b[22m send file · \x1b[1mdouble-click\x1b[22m opens paths"
		}
		if len(c.panes) > 1 && p != nil {
			text = fmt.Sprintf(" [%s %d/%d · Ctrl+] o next]", p.name, c.focus+1, len(c.panes)) + text
		}
		if c.needsYou != "" {
			// The keys come first, so a narrow terminal cuts the request's
			// text, never the way to answer it.
			what := strings.TrimPrefix(c.needsYou, "⏸ Needs you: ")
			text = " \x1b[1;38;5;214m⏸ Needs you · Ctrl+] y allow · Ctrl+] m more\x1b[22;38;5;252m · " + what
		}
	}
	if ansi.StringWidth(text) > width {
		text = ansi.Truncate(text, width, "")
	}
	return text + strings.Repeat(" ", max(0, width-ansi.StringWidth(text)))
}

// ---- keyboard and mouse

func (c *bareClient) startInput() error {
	r, err := newInputReader(c.in)
	if err != nil {
		return err
	}
	c.reader = r
	c.readerWG.Add(1)
	go func() {
		defer c.readerWG.Done()
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				c.input(buf[:n])
			}
			if err != nil {
				if !errors.Is(err, cancelreader.ErrCanceled) {
					c.finish("input")
				}
				return
			}
		}
	}()
	return nil
}

func (c *bareClient) stopInput() {
	if c.reader == nil {
		return
	}
	c.reader.Cancel()
	c.readerWG.Wait()
	_ = c.reader.Close()
	c.reader = nil
}

// input splits what the terminal sent into keys, mouse reports and pastes.
func (c *bareClient) input(data []byte) {
	if c.clipActive != nil {
		go c.clipActive()
	}
	buf := append(c.pending, data...)
	c.pending = nil
	var forward []byte
	flush := func() {
		if len(forward) > 0 {
			c.forward(forward)
			forward = nil
		}
	}
	for len(buf) > 0 {
		if c.pasting {
			if i := strings.Index(string(buf), "\x1b[201~"); i >= 0 {
				forward = append(forward, buf[:i+6]...)
				buf = buf[i+6:]
				c.pasting = false
			} else {
				forward = append(forward, buf...)
				buf = nil
			}
			continue
		}
		tok, rest, ok := nextInputToken(buf)
		if !ok {
			c.pending = append([]byte(nil), buf...)
			break
		}
		buf = rest
		if string(tok) == "\x1b[200~" {
			c.pasting = true
			forward = append(forward, tok...)
			continue
		}
		if c.handleToken(tok) {
			flush()
			continue
		}
		forward = append(forward, tok...)
	}
	flush()
}

// nextInputToken returns one key, escape sequence or run of plain text.
func nextInputToken(b []byte) (tok, rest []byte, complete bool) {
	if b[0] != 0x1b {
		// One byte at a time: a key typed after Ctrl+] may arrive in the
		// same read. The caller still writes a run to the pane at once.
		return b[:1], b[1:], true
	}
	if len(b) == 1 {
		return b, nil, true
	}
	switch b[1] {
	case '[':
		if len(b) >= 3 && b[2] == 'M' {
			// An X10 mouse report: three bytes follow.
			if len(b) < 6 {
				return nil, b, false
			}
			return b[:6], b[6:], true
		}
		for i := 2; i < len(b); i++ {
			if b[i] >= 0x40 && b[i] <= 0x7e {
				return b[:i+1], b[i+1:], true
			}
		}
		return nil, b, false
	case 'O':
		if len(b) < 3 {
			return nil, b, false
		}
		return b[:3], b[3:], true
	}
	return b[:2], b[2:], true
}

// keyOf names a token for the controls: Ctrl+] and Ctrl+\ in every encoding
// a terminal uses for them (plain, kitty's CSI u, xterm's modifyOtherKeys),
// and a plain character.
func keyOf(tok []byte) string {
	s := string(tok)
	switch s {
	case "\x1d", "\x1b[93;5u", "\x1b[27;5;93~":
		return "C-]"
	case "\x1c", "\x1b[92;5u", "\x1b[27;5;92~":
		return `C-\`
	case "\x1b":
		return "Esc"
	case "\x03":
		return "C-c"
	case "\r", "\x1b[13u":
		return "Enter"
	case "\x1b[A", "\x1bOA":
		return "Up"
	case "\x1b[B", "\x1bOB":
		return "Down"
	case "\x1b[C", "\x1bOC":
		return "Right"
	case "\x1b[D", "\x1bOD":
		return "Left"
	case "\x1b[5~":
		return "PgUp"
	case "\x1b[6~":
		return "PgDn"
	case "\x1b[27u":
		return "Esc"
	}
	if strings.HasPrefix(s, "\x1b[") && strings.HasSuffix(s, "u") {
		// kitty: CSI code[;mods] u
		params := strings.Split(strings.TrimSuffix(strings.TrimPrefix(s, "\x1b["), "u"), ";")
		code, err := strconv.Atoi(strings.Split(params[0], ":")[0])
		mods := 1
		if len(params) > 1 {
			mods, _ = strconv.Atoi(strings.Split(params[1], ":")[0])
		}
		if err == nil && code >= 0x20 && code < 0x7f && (mods == 1 || mods == 2) {
			ch := rune(code)
			if mods == 2 && ch >= 'a' && ch <= 'z' {
				ch -= 'a' - 'A'
			}
			return string(ch)
		}
	}
	if len(tok) == 1 && tok[0] >= 0x20 && tok[0] < 0x7f {
		return s
	}
	return ""
}

// handleToken acts on a token that belongs to the client and reports
// whether it did; anything else goes on to the focused pane.
func (c *bareClient) handleToken(tok []byte) bool {
	if strings.HasPrefix(string(tok), "\x1b[<") {
		c.mouse(string(tok))
		return true
	}
	if strings.HasPrefix(string(tok), "\x1b[M") && len(tok) == 6 {
		return true // an X10 report: this client asked for SGR ones
	}
	key := keyOf(tok)
	c.mu.Lock()
	overlay, hints, prefix := c.overlay, c.hints, c.prefix
	p := c.focused()
	scrolling := p != nil && p.scroll > 0
	c.mu.Unlock()
	switch {
	case overlay != nil:
		c.overlayKey(key)
		return true
	case hints != nil:
		c.hintsKey(key)
		return true
	case prefix:
		c.mu.Lock()
		c.prefix = false
		c.mu.Unlock()
		c.markDirty()
		if key == "C-]" {
			c.forward([]byte{0x1d})
			return true
		}
		c.command(key)
		return true
	case scrolling:
		c.scrollKey(p, key)
		return true
	case key == "C-]":
		c.mu.Lock()
		c.prefix = true
		c.mu.Unlock()
		c.markDirty()
		return true
	case key == `C-\`:
		// Never SIGQUIT: in a terminal it ends the agent, and the screen
		// a dead agent leaves behind used to show its launch command.
		c.controlsPopup("upload")
		return true
	case string(tok) == "\x1b[I" || string(tok) == "\x1b[O":
		if p != nil {
			p.mu.Lock()
			wants := p.modes[ansi.ModeFocusEvent]
			p.mu.Unlock()
			if !wants {
				return true
			}
		}
	}
	return false
}

// forward types into the focused pane.
func (c *bareClient) forward(b []byte) {
	c.mu.Lock()
	p := c.focused()
	c.sel = nil
	c.mu.Unlock()
	if p != nil {
		_ = p.conn.Write(append([]byte(nil), b...))
	}
}

// command runs what follows Ctrl+].
func (c *bareClient) command(key string) {
	switch key {
	case "d":
		c.finish("leave")
	case "m":
		c.controlsPopup("")
	case "u":
		c.controlsPopup("upload")
	case "y":
		c.allowOnce()
	case "e":
		c.startHints()
	case "|", "%":
		c.split('|')
	case "-", `"`:
		c.split('-')
	case "c":
		c.split(0)
	case "o", "Right", "Down":
		c.cycleFocus(1)
	case "Left", "Up":
		c.cycleFocus(-1)
	case "x":
		c.closeFocused()
	case "[":
		c.mu.Lock()
		if p := c.focused(); p != nil {
			p.mu.Lock()
			if !p.emu.IsAltScreen() && p.emu.ScrollbackLen() > 0 {
				p.scroll = min(p.emu.ScrollbackLen(), max(1, p.h/2))
			}
			p.mu.Unlock()
		}
		c.mu.Unlock()
		c.markDirty()
	case "?", " ":
		c.showKeys()
	case "Esc", "C-c", "":
	default:
		c.say("Ctrl+] " + key + " does nothing here · Ctrl+] ? lists every key")
	}
}

func (c *bareClient) cycleFocus(delta int) {
	c.mu.Lock()
	if n := len(c.panes); n > 1 {
		c.focus = (c.focus + delta + n) % n
		c.full = true
	}
	c.mu.Unlock()
	c.markDirty()
}

// showKeys is Ctrl+] ?: every key, each runnable from the list.
func (c *bareClient) showKeys() {
	c.mu.Lock()
	leave := "Leave (the session keeps running)"
	if c.controls.TabView {
		leave = "Close this tab (the session keeps running)"
	}
	items := []bareItem{{"Lectern actions for this session", "m", func() { c.controlsPopup("") }}}
	if c.needsYou != "" {
		items = append(items, bareItem{"Allow the waiting request once", "y", c.allowOnce})
	}
	items = append(items,
		bareItem{`Send a file to the agent (also Ctrl+\)`, "u", func() { c.controlsPopup("upload") }},
		bareItem{"Pick a path or link on screen", "e", c.startHints},
		bareItem{"Shell to the right", "|", func() { c.split('|') }},
		bareItem{"Shell below", "-", func() { c.split('-') }},
	)
	if len(c.panes) > 1 {
		items = append(items, bareItem{"Next pane", "o", func() { c.cycleFocus(1) }})
		if p := c.focused(); p != nil && !p.agent {
			items = append(items, bareItem{"Close this shell pane", "x", c.closeFocused})
		}
	}
	items = append(items,
		bareItem{"Scroll back (q stops)", "[", func() { c.command("[") }},
		bareItem{leave, "d", func() { c.finish("leave") }},
		bareItem{"Send Ctrl+] to the agent", "C-]", func() { c.forward([]byte{0x1d}) }},
	)
	c.overlay = &bareOverlay{title: " Attach keys ", items: items}
	c.mu.Unlock()
	c.markDirty()
}

func (c *bareClient) overlayKey(key string) {
	c.mu.Lock()
	o := c.overlay
	if o == nil {
		c.mu.Unlock()
		return
	}
	var run func()
	switch key {
	case "Esc", "q", "C-c", "C-]":
		c.overlay = nil
		c.full = true
	case "Up", "k":
		o.index = (o.index - 1 + len(o.items)) % len(o.items)
	case "Down", "j":
		o.index = (o.index + 1) % len(o.items)
	case "Enter":
		run = o.items[o.index].run
	default:
		for _, it := range o.items {
			if it.key == key {
				run = it.run
			}
		}
	}
	if run != nil {
		c.overlay = nil
		c.full = true
	}
	c.mu.Unlock()
	c.markDirty()
	if run != nil {
		run()
	}
}

func (c *bareClient) scrollKey(p *barePane, key string) {
	p.mu.Lock()
	limit := p.emu.ScrollbackLen()
	switch key {
	case "Up", "k":
		p.scroll++
	case "Down", "j":
		p.scroll--
	case "PgUp", "b":
		p.scroll += max(1, p.h-1)
	case "PgDn", " ", "f":
		p.scroll -= max(1, p.h-1)
	case "g":
		p.scroll = limit
	case "G", "q", "Esc", "C-c", "Enter":
		p.scroll = 0
	}
	p.scroll = max(0, min(limit, p.scroll))
	p.mu.Unlock()
	c.markDirty()
}

// mouse handles an SGR report: CSI < b ; x ; y M (press) or m (release).
func (c *bareClient) mouse(report string) {
	body := strings.TrimPrefix(report, "\x1b[<")
	release := strings.HasSuffix(body, "m")
	parts := strings.Split(body[:len(body)-1], ";")
	if len(parts) != 3 {
		return
	}
	b, _ := strconv.Atoi(parts[0])
	col, _ := strconv.Atoi(parts[1])
	row, _ := strconv.Atoi(parts[2])
	col, row = col-1, row-1
	button, motion, wheel := b&3, b&32 != 0, b&64 != 0

	c.mu.Lock()
	if o := c.overlay; o != nil {
		c.mu.Unlock()
		if !release && !motion && button == 0 && !wheel {
			if i := o.itemAt(col, row); i >= 0 {
				c.mu.Lock()
				run := o.items[i].run
				c.overlay, c.full = nil, true
				c.mu.Unlock()
				c.markDirty()
				run()
				return
			}
			c.overlayKey("Esc")
		}
		return
	}
	if row == c.rows-1 {
		c.mu.Unlock()
		// The bar itself: a click there lists every key.
		if !release && !motion && button == 0 && !wheel {
			c.showKeys()
		}
		return
	}
	var p *barePane
	for i, q := range c.panes {
		if q.contains(col, row) {
			p = q
			if !release && !motion && !wheel && c.focus != i {
				c.focus, c.full = i, true
			}
		}
	}
	c.mu.Unlock()
	if p == nil {
		return
	}
	x, y := col-p.x, row-p.y
	p.mu.Lock()
	programMouse := p.wantsMouse() && p.scroll == 0 && b&4 == 0
	sgr := p.modes[ansi.ModeMouseExtSgr]
	anyMotion := p.modes[ansi.ModeMouseAnyEvent]
	dragMotion := p.modes[ansi.ModeMouseButtonEvent]
	alt := p.emu.IsAltScreen()
	p.mu.Unlock()
	if programMouse {
		if motion && !anyMotion && !(dragMotion && c.pressed) {
			return
		}
		if !motion && !wheel {
			c.pressed = !release
		}
		var out string
		if sgr {
			end := "M"
			if release {
				end = "m"
			}
			out = fmt.Sprintf("\x1b[<%d;%d;%d%s", b, x+1, y+1, end)
		} else {
			code := b
			if release {
				code = 3 | b&^3
			}
			out = "\x1b[M" + string(rune(32+code)) + string(rune(32+min(x+1, 222))) + string(rune(32+min(y+1, 222)))
		}
		_ = p.conn.Write([]byte(out))
		return
	}
	switch {
	case wheel:
		if alt {
			return
		}
		p.mu.Lock()
		if b&1 == 0 {
			p.scroll = min(p.emu.ScrollbackLen(), p.scroll+3)
		} else {
			p.scroll = max(0, p.scroll-3)
		}
		p.mu.Unlock()
		c.markDirty()
	case button == 0 && !release && !motion:
		c.mu.Lock()
		c.sel = &bareSelection{pane: p, start: [2]int{x, y}, end: [2]int{x, y}}
		c.mu.Unlock()
		c.markDirty()
	case button == 0 && motion:
		c.mu.Lock()
		if c.sel != nil && c.sel.pane == p {
			c.sel.end = [2]int{max(0, min(p.w-1, x)), max(0, min(p.h-1, y))}
			c.sel.moved = true
		}
		c.mu.Unlock()
		c.markDirty()
	case release && button == 0:
		c.mu.Lock()
		sel := c.sel
		double := c.click.pane == p && c.click.x == x && c.click.y == y && time.Since(c.click.at) < 450*time.Millisecond
		c.click.pane, c.click.x, c.click.y, c.click.at = p, x, y, time.Now()
		if sel != nil && !sel.moved {
			c.sel = nil
		}
		c.mu.Unlock()
		if sel != nil && sel.moved {
			text := sel.text()
			_ = c.copy(text)
			c.say(fmt.Sprintf("Copied %d characters", len([]rune(text))))
			return
		}
		c.markDirty()
		if double {
			go c.openAt(p, x, y)
		}
	case button == 2 && !release && !motion:
		go c.linkMenuAt(p, x, y)
	}
}

// ---- links

func (c *bareClient) linkAt(p *barePane, x, y int) (filelinks.Link, bool) {
	rows := p.rows()
	return c.links.detectIn(rows, p.w, x, y, p.hyperlinkAt(x, y))
}

func (c *bareClient) openAt(p *barePane, x, y int) {
	link, ok := c.linkAt(p, x, y)
	if !ok {
		return
	}
	if err := c.links.act("open", link); err != nil {
		c.say(failure("open", err))
	}
}

func (c *bareClient) linkMenuAt(p *barePane, x, y int) {
	link, ok := c.linkAt(p, x, y)
	if !ok {
		c.say("No path or link there · Ctrl+] e labels the ones on screen")
		return
	}
	c.linkMenu(link)
}

// linkMenu offers what can be done with one link, as the tmux path's
// right-click menu does.
func (c *bareClient) linkMenu(link filelinks.Link) {
	act := func(action string) func() {
		return func() {
			go func() {
				if err := c.links.act(action, link); err != nil {
					c.say(failure(action, err))
				}
			}()
		}
	}
	_, local := localOpener()
	var items []bareItem
	title := link.URL
	if link.Kind == "url" {
		if local {
			items = append(items, bareItem{"Open in browser", "o", act("open")})
		} else {
			items = append(items, bareItem{"Copy link to open in your browser", "o", act("open")})
		}
		items = append(items, bareItem{"Copy link", "c", act("copy")})
	} else {
		title = link.Path
		if local {
			items = append(items, bareItem{"Open on this machine", "o", act("open")}, bareItem{"Download to ~/Downloads", "d", act("download")})
		} else {
			items = append(items, bareItem{"View here", "v", act("view")})
		}
		items = append(items, bareItem{"Copy path", "c", act("copy")}, bareItem{"Send path to the agent", "s", act("send")},
			bareItem{"Open in web viewer", "w", act("web")})
	}
	if len([]rune(title)) > 50 {
		title = "…" + string([]rune(title)[len([]rune(title))-49:])
	}
	c.mu.Lock()
	c.overlay = &bareOverlay{title: " " + path.Base(title) + " ", items: items}
	if link.Kind == "url" {
		c.overlay.title = " " + title + " "
	}
	c.mu.Unlock()
	c.markDirty()
}

// startHints is Ctrl+] e: label every path and link on the focused pane.
func (c *bareClient) startHints() {
	c.mu.Lock()
	p := c.focused()
	c.mu.Unlock()
	if p == nil {
		return
	}
	c.say("Looking for paths and links…")
	go func() {
		rows := p.rows()
		found := c.links.paneHints(rows, p.w)
		if len(found) == 0 {
			c.say("No paths or links on screen")
			return
		}
		c.mu.Lock()
		c.hints = &bareHints{pane: p, rows: rows, hints: found}
		c.message = ""
		c.full = true
		c.mu.Unlock()
		c.markDirty()
	}()
}

func (c *bareClient) hintsKey(key string) {
	c.mu.Lock()
	h := c.hints
	if h == nil {
		c.mu.Unlock()
		return
	}
	if key == "Esc" || key == "C-c" || key == "C-]" || len(key) != 1 {
		if len(key) != 1 {
			c.hints, c.full = nil, true
		}
		c.mu.Unlock()
		c.markDirty()
		return
	}
	ch := key[0]
	if ch >= 'A' && ch <= 'Z' {
		h.actions = true
		ch += 'a' - 'A'
	}
	if ch < 'a' || ch > 'z' {
		c.mu.Unlock()
		return
	}
	h.typed += string(ch)
	var matched *hint
	prefix := false
	for i := range h.hints {
		if h.hints[i].label == h.typed {
			matched = &h.hints[i]
		} else if strings.HasPrefix(h.hints[i].label, h.typed) {
			prefix = true
		}
	}
	if matched == nil && !prefix {
		h.typed, h.actions = "", false
	}
	if matched != nil {
		c.hints, c.full = nil, true
	}
	actions := h.actions
	c.mu.Unlock()
	c.markDirty()
	if matched != nil {
		link := matched.link
		if actions {
			c.linkMenu(link)
			return
		}
		go func() {
			if err := c.links.act("open", link); err != nil {
				c.say(failure("open", err))
			}
		}()
	}
}

// ---- shells beside the agent

// split opens a shell on the session's machine, in the agent's directory,
// as a new pane beside (|) or below (-) — a new tracked shell session, as
// with the tmux path's Ctrl+] |.
func (c *bareClient) split(dir byte) {
	c.mu.Lock()
	if len(c.panes) >= 4 {
		c.mu.Unlock()
		c.say("Four panes is the most here · Ctrl+] x closes a shell")
		return
	}
	if dir == 0 {
		dir = '|'
	}
	if len(c.panes) == 1 {
		c.dir = dir
	} else if c.dir != dir {
		dir = c.dir
	}
	w, h := c.cols, max(1, c.rows-1)
	c.mu.Unlock()
	c.say("Opening a shell where the agent is…")
	go func() {
		argv, err := splitShellArgv(c.controls.Kind, c.controls.ID, c.controls.Base, c.controls.Token, os.Getenv("LECTERN_ATTACH_HOST"), "agent")
		if err != nil {
			c.say("Couldn't open a shell: " + err.Error())
			return
		}
		conn, err := webterm.Open(argv, c.self, max(1, w/2), h)
		if err != nil {
			c.say("Couldn't open a shell: " + err.Error())
			return
		}
		pane := newBarePane(c, conn, false, "shell", max(1, w/2), h)
		c.mu.Lock()
		c.panes = append(c.panes, pane)
		c.focus = len(c.panes) - 1
		c.layout()
		c.message = ""
		c.mu.Unlock()
		pane.start()
		go func() {
			<-pane.ended
			c.removePane(pane)
		}()
		c.markDirty()
	}()
}

func (c *bareClient) removePane(p *barePane) {
	c.mu.Lock()
	for i, q := range c.panes {
		if q == p && !q.agent {
			c.panes = append(c.panes[:i], c.panes[i+1:]...)
			delete(c.cache, p)
			if c.focus >= len(c.panes) || c.focus == i {
				c.focus = 0
			}
			c.layout()
			break
		}
	}
	c.mu.Unlock()
	p.close()
	c.markDirty()
}

// closeFocused leaves a shell pane; its shell session keeps running, like a
// closed tmux pane's Lectern shell.
func (c *bareClient) closeFocused() {
	c.mu.Lock()
	p := c.focused()
	c.mu.Unlock()
	if p == nil || p.agent {
		c.say("The agent's pane stays · Ctrl+] d leaves the session")
		return
	}
	c.removePane(p)
}

// ---- Lectern actions, approvals and files

// takeover gives this terminal to fn (the controls dashboard, a pager) and
// takes it back afterwards, drawing the session again.
func (c *bareClient) takeover(fn func()) {
	c.mu.Lock()
	if c.paused {
		c.mu.Unlock()
		return
	}
	c.paused = true
	c.prefix = false
	c.mu.Unlock()
	c.stopInput()
	c.write("\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?1004l\x1b[?2004l\x1b[?1l\x1b[0 q\x1b[0m\x1b[?25h\x1b[H\x1b[2J")
	if c.realKitty != 0 {
		c.write("\x1b[<u")
	}
	if c.original != nil {
		_ = term.Restore(int(c.in.Fd()), c.original)
	}
	fn()
	_, _ = term.MakeRaw(int(c.in.Fd()))
	c.mu.Lock()
	c.paused = false
	c.full = true
	c.realModes = map[int]bool{}
	c.realKitty, c.realMok = 0, 0
	c.mu.Unlock()
	c.write("\x1b[?1049h")
	if err := c.startInput(); err != nil {
		c.finish("input")
		return
	}
	c.markDirty()
	if c.controls.Kind == "session" && c.controls.Base != "" {
		go c.checkApprovals()
	}
}

// controlsPopup runs the same controls the tmux path's Ctrl+] m popup runs,
// in this process, in place of the session until it closes. Text it types
// (an uploaded file's path) goes straight to the agent's pane.
func (c *bareClient) controlsPopup(action string) {
	if c.popup != nil {
		go c.takeover(func() { c.popup(action) })
		return
	}
	go c.takeover(func() {
		opts := console.DashboardOptions{Popup: true, FocusKind: c.controls.Kind, FocusID: c.controls.ID,
			Action: action, Insert: c.send}
		if err := console.RunControls(console.New(c.controls.Base, c.controls.Token), c.in, os.Stdout, opts); err != nil {
			fmt.Fprintln(c.out, "\r\nlectern: "+err.Error())
			time.Sleep(2 * time.Second)
		}
	})
}

// pollApprovals keeps the "Needs you" note on the bar current.
func (c *bareClient) pollApprovals(stop chan struct{}) {
	c.checkApprovals()
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			c.checkApprovals()
		}
	}
}

func (c *bareClient) checkApprovals() {
	cl := console.New(c.controls.Base, c.controls.Token)
	cl.HTTP.Timeout = 3 * time.Second
	data, err := cl.JSON("GET", "/approvals?status=pending", nil)
	if err != nil {
		return
	}
	note, approval := needsYouNote(data, c.controls.ID), pendingApprovalID(data, c.controls.ID)
	c.mu.Lock()
	changed := note != c.needsYou
	c.needsYou, c.approvalID = note, approval
	c.mu.Unlock()
	if changed {
		c.markDirty()
	}
}

func pendingApprovalID(data []byte, sessionID string) string {
	var rows []struct {
		ID        int64 `json:"id"`
		SessionID int64 `json:"session_id"`
	}
	if json.Unmarshal(data, &rows) != nil {
		return ""
	}
	for _, r := range rows {
		if strconv.FormatInt(r.SessionID, 10) == sessionID {
			return strconv.FormatInt(r.ID, 10)
		}
	}
	return ""
}

// allowOnce is Ctrl+] y: allow the request the session is waiting on, once.
func (c *bareClient) allowOnce() {
	c.mu.Lock()
	id, note := c.approvalID, c.needsYou
	c.mu.Unlock()
	if id == "" {
		c.say("Nothing is waiting for you · Ctrl+] m has the other actions")
		return
	}
	go func() {
		cl := console.New(c.controls.Base, c.controls.Token)
		if _, err := cl.JSON("POST", "/approvals/"+id+"/decision", map[string]any{"decision": "approved"}); err != nil {
			c.say("Couldn't allow it: " + err.Error())
			return
		}
		c.say("Allowed once: " + strings.TrimPrefix(note, "⏸ Needs you: "))
		c.checkApprovals()
	}()
}
