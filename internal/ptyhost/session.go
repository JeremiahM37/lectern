package ptyhost

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/charmbracelet/x/xpty"
)

// DefaultHistory is how many lines of scrollback a session keeps unless
// LECTERN_PTY_HISTORY says otherwise.
const DefaultHistory = 5000

// Session is one program on one pseudo-terminal.
type Session struct {
	host     *Host
	name     string
	id       int
	pty      xpty.Pty
	cmd      *exec.Cmd
	startDir string
	env      []string // -e entries
	done     chan struct{}
	reaped   chan struct{} // closed once the program has been waited for

	writeMu sync.Mutex // serialises writes to the PTY

	mu       sync.Mutex // everything below
	emu      *vt.Emulator
	created  time.Time
	activity time.Time
	cols     int
	rows     int
	options  map[string]string
	clients  map[*attached]struct{}
	modes    map[ansi.Mode]bool // private modes the program has set
	hidden   bool               // cursor hidden
	kitty    []int              // kitty keyboard flag stack; last is current
	mok      int                // xterm modifyOtherKeys level
	oscCwd   string             // last directory the shell reported (OSC 7)
	exited   bool
	nclients atomic.Int32
}

// attached is one client streaming a session.
type attached struct {
	out  chan []byte
	gone chan struct{}
	once sync.Once
}

func (a *attached) close() { a.once.Do(func() { close(a.gone) }) }

// newSession starts argv on a fresh PTY.
func (h *Host) newSession(req Request) (*Session, error) {
	argv := req.Argv
	if len(argv) == 0 && req.Shell != "" {
		argv = append(defaultShell(), req.Shell)
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("no command")
	}
	cols, rows := req.Cols, req.Rows
	if cols <= 0 || rows <= 0 {
		cols, rows = 80, 24 // tmux's default-size
	}
	p, cmd, err := startOnPty(argv, sessionEnv(req.Env, req.SessionEnv, req.Name), req.Dir, cols, rows)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	s := &Session{
		host: h, name: req.Name, pty: p, cmd: cmd, startDir: req.Dir,
		env: append([]string(nil), req.SessionEnv...), done: make(chan struct{}), reaped: make(chan struct{}),
		created: now, activity: now, cols: cols, rows: rows,
		options: map[string]string{}, clients: map[*attached]struct{}{},
		modes: map[ansi.Mode]bool{},
	}
	if s.startDir == "" {
		s.startDir, _ = os.Getwd()
	}
	s.emu = s.newEmulator(cols, rows)
	go s.answerQueries()
	go s.pump()
	go s.wait()
	return s, nil
}

// startOnPty starts argv on a new pseudo-terminal of the given size.
func startOnPty(argv, env []string, dir string, cols, rows int) (xpty.Pty, *exec.Cmd, error) {
	path, err := lookPath(argv[0], env)
	if err != nil {
		return nil, nil, err
	}
	p, err := xpty.NewPty(cols, rows)
	if err != nil {
		return nil, nil, fmt.Errorf("open a pseudo-terminal: %w", err)
	}
	cmd := &exec.Cmd{Path: path, Args: argv, Env: env, Dir: dir}
	cmd.SysProcAttr = childAttr()
	if err := p.Start(cmd); err != nil {
		p.Close()
		return nil, nil, fmt.Errorf("start %s: %w", argv[0], err)
	}
	// Only the master side is kept. With the slave still open here, a
	// program that exits would never hang the terminal up.
	if u, ok := p.(*xpty.UnixPty); ok {
		u.Slave().Close()
	}
	return p, cmd, nil
}

// Process is one program on a pseudo-terminal of its own, with no screen
// model and no host: what the web terminal runs for an attachment that is
// not a PTY-host session (a `tmux attach`, an ssh to another machine).
type Process struct {
	pty    xpty.Pty
	cmd    *exec.Cmd
	reaped chan struct{}
	once   sync.Once
}

// StartProcess starts argv on a new pseudo-terminal, in this process's
// environment with TERM set for an xterm-compatible client.
func StartProcess(argv []string, cols, rows int) (*Process, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("no command")
	}
	if cols <= 0 || rows <= 0 {
		cols, rows = 80, 24
	}
	env := append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	p, cmd, err := startOnPty(argv, env, "", cols, rows)
	if err != nil {
		return nil, err
	}
	pr := &Process{pty: p, cmd: cmd, reaped: make(chan struct{})}
	go func() {
		_ = xpty.WaitProcess(context.Background(), cmd)
		close(pr.reaped)
	}()
	return pr, nil
}

// Read returns the program's output; io.EOF-like errors once it has exited.
func (p *Process) Read(b []byte) (int, error) { return p.pty.Read(b) }

// Write types into the program.
func (p *Process) Write(b []byte) (int, error) { return p.pty.Write(b) }

// Resize changes the terminal's size.
func (p *Process) Resize(cols, rows int) error { return p.pty.Resize(cols, rows) }

// Done closes when the program has exited.
func (p *Process) Done() <-chan struct{} { return p.reaped }

// Close hangs the terminal up, as a closed terminal window does, and kills
// whatever is left of the program a moment later.
func (p *Process) Close() error {
	p.once.Do(func() {
		signalGroup(p.cmd.Process)
		_ = p.pty.Close()
		go func() {
			select {
			case <-p.reaped:
			case <-time.After(3 * time.Second):
				killGroup(p.cmd.Process)
			}
		}()
	})
	return nil
}

// sessionEnv is the program's environment: the caller's, with the terminal
// Lectern's clients actually are, and without the markers of an outer tmux
// that would make a program think it runs inside one.
func sessionEnv(base, extra []string, name string) []string {
	drop := map[string]bool{"TMUX": true, "TMUX_PANE": true, "TERM": true, "COLORTERM": true, "LECTERN_PTY_SESSION": true}
	for _, kv := range extra {
		k, _, _ := strings.Cut(kv, "=")
		drop[k] = true
	}
	env := make([]string, 0, len(base)+len(extra)+3)
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if runtime.GOOS == "windows" {
			k = strings.ToUpper(k)
		}
		if !drop[k] {
			env = append(env, kv)
		}
	}
	env = append(env, extra...)
	return append(env, "TERM=xterm-256color", "COLORTERM=truecolor", "LECTERN_PTY_SESSION="+name)
}

// lookPath resolves a program with the PATH the program itself will get,
// not the host's own, which may be years older than the caller's.
func lookPath(file string, env []string) (string, error) {
	if p := posixProgram(file); p != "" {
		return p, nil
	}
	if strings.ContainsAny(file, `/\`) {
		return file, nil
	}
	pathEnv := ""
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, "PATH") {
			pathEnv = v
		}
	}
	exts := []string{""}
	if runtime.GOOS == "windows" {
		exts = []string{"", ".exe", ".com", ".bat", ".cmd"}
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			dir = "."
		}
		for _, ext := range exts {
			p := filepath.Join(dir, file+ext)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() && (runtime.GOOS == "windows" || fi.Mode()&0o111 != 0) {
				return p, nil
			}
		}
	}
	if p, err := exec.LookPath(file); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%s: command not found", file)
}

// newEmulator builds the screen model and follows the state a client that
// attaches later must be told about: private modes, cursor visibility, the
// keyboard protocols and the shell's reported directory.
func (s *Session) newEmulator(cols, rows int) *vt.Emulator {
	e := vt.NewEmulator(cols, rows)
	e.SetScrollbackSize(s.host.history)
	e.SetCallbacks(vt.Callbacks{
		EnableMode:       func(m ansi.Mode) { s.modes[m] = true },
		DisableMode:      func(m ansi.Mode) { delete(s.modes, m) },
		CursorVisibility: func(visible bool) { s.hidden = !visible },
		WorkingDirectory: func(dir string) { s.oscCwd = dir },
	})
	// kitty keyboard protocol: CSI > flags u pushes, CSI < n u pops,
	// CSI = flags ; mode u sets. vt does not track it, and a browser that
	// attaches later must encode keys the way the program asked for.
	e.RegisterCsiHandler(ansi.Command('>', 0, 'u'), func(p ansi.Params) bool {
		f, _, _ := p.Param(0, 0)
		s.kitty = append(s.kitty, f)
		return true
	})
	e.RegisterCsiHandler(ansi.Command('<', 0, 'u'), func(p ansi.Params) bool {
		n, _, _ := p.Param(0, 1)
		for ; n > 0 && len(s.kitty) > 0; n-- {
			s.kitty = s.kitty[:len(s.kitty)-1]
		}
		return true
	})
	e.RegisterCsiHandler(ansi.Command('=', 0, 'u'), func(p ansi.Params) bool {
		f, _, _ := p.Param(0, 0)
		mode, _, _ := p.Param(1, 1)
		cur := 0
		if len(s.kitty) > 0 {
			cur = s.kitty[len(s.kitty)-1]
		}
		switch mode {
		case 2:
			f |= cur
		case 3:
			f = cur &^ f
		}
		if len(s.kitty) == 0 {
			s.kitty = append(s.kitty, f)
		} else {
			s.kitty[len(s.kitty)-1] = f
		}
		return true
	})
	// A query is answered for a program with nobody attached: the browser
	// Lectern serves speaks the protocol (frontend extended-keys.ts).
	e.RegisterCsiHandler(ansi.Command('?', 0, 'u'), func(ansi.Params) bool {
		if s.nclients.Load() == 0 {
			cur := 0
			if len(s.kitty) > 0 {
				cur = s.kitty[len(s.kitty)-1]
			}
			go s.write([]byte("\x1b[?" + strconv.Itoa(cur) + "u"))
		}
		return true
	})
	// xterm modifyOtherKeys: CSI > 4 ; n m.
	e.RegisterCsiHandler(ansi.Command('>', 0, 'm'), func(p ansi.Params) bool {
		if res, _, _ := p.Param(0, 0); res == 4 {
			s.mok, _, _ = p.Param(1, 0)
		}
		return true
	})
	return e
}

// answerQueries forwards what the emulator answers to a program's terminal
// queries (cursor position, device attributes) — but only while no client is
// attached. An attached terminal answers them itself, and a second answer
// would reach the program as typed input.
func (s *Session) answerQueries() {
	buf := make([]byte, 4096)
	for {
		n, err := s.emu.Read(buf)
		if n > 0 && s.nclients.Load() == 0 {
			s.write(append([]byte(nil), buf[:n]...))
		}
		if err != nil {
			return
		}
	}
}

// pump moves the program's output into the screen model and to every
// attached client. A client too slow to keep up is disconnected rather than
// allowed to stall the program; it reconnects to a fresh snapshot.
func (s *Session) pump() {
	buf := make([]byte, 32<<10)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			s.mu.Lock()
			_, _ = s.emu.Write(chunk)
			s.activity = time.Now()
			for c := range s.clients {
				select {
				case c.out <- chunk:
				default:
					delete(s.clients, c)
					s.nclients.Add(-1)
					c.close()
				}
			}
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// wait reaps the program and ends the session with it, as tmux does when a
// pane's program exits.
func (s *Session) wait() {
	_ = xpty.WaitProcess(context.Background(), s.cmd)
	close(s.reaped)
	s.finish()
}

// finish releases the session once. It is called when the program exits and
// when the session is killed.
func (s *Session) finish() {
	s.mu.Lock()
	if s.exited {
		s.mu.Unlock()
		return
	}
	s.exited = true
	clients := s.clients
	s.clients = map[*attached]struct{}{}
	s.nclients.Store(0)
	s.mu.Unlock()
	s.host.remove(s)
	// Closing the terminal is what hangs up anything still on it; on Windows
	// closing the pseudoconsole ends every process attached to it.
	_ = s.pty.Close()
	_ = s.emu.Close()
	for c := range clients {
		c.close()
	}
	close(s.done)
}

// kill ends the session's program and everything on its terminal.
func (s *Session) kill() {
	signalGroup(s.cmd.Process)
	s.finish()
	go func() {
		select {
		case <-time.After(3 * time.Second):
			killGroup(s.cmd.Process)
		case <-s.reaped:
		}
	}()
}

func (s *Session) write(b []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.pty.Write(b)
	return err
}

// input is typed keyboard input; it counts as activity, as in tmux.
func (s *Session) input(b []byte) error {
	s.mu.Lock()
	s.activity = time.Now()
	s.mu.Unlock()
	return s.write(b)
}

// paste types text the way tmux's paste-buffer -p does: newlines become
// carriage returns, and the whole is bracketed when the program asked for
// bracketed paste, so it arrives as one paste rather than as typed lines.
func (s *Session) paste(text []byte) error {
	body := strings.ReplaceAll(string(text), "\r\n", "\r")
	body = strings.ReplaceAll(body, "\n", "\r")
	s.mu.Lock()
	bracketed := s.modes[ansi.ModeBracketedPaste]
	s.mu.Unlock()
	if bracketed {
		body = "\x1b[200~" + body + "\x1b[201~"
	}
	return s.input([]byte(body))
}

// keys presses tmux-named keys.
func (s *Session) keys(names []string) error {
	s.mu.Lock()
	appCursor := s.modes[ansi.ModeCursorKeys]
	s.mu.Unlock()
	var b []byte
	for _, name := range names {
		seq, ok := keyBytes(name, appCursor)
		if !ok {
			return fmt.Errorf("unknown key: %s", name)
		}
		b = append(b, seq...)
	}
	return s.input(b)
}

func (s *Session) resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 || cols > 1000 || rows > 1000 {
		return fmt.Errorf("invalid size %dx%d", cols, rows)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cols == s.cols && rows == s.rows {
		return nil
	}
	s.cols, s.rows = cols, rows
	s.emu.Resize(cols, rows)
	return s.pty.Resize(cols, rows)
}

// capture renders the screen as plain text, preceded by up to lines lines of
// history, one row per line with trailing blanks trimmed — the shape of
// `tmux capture-pane -p`, blank rows below the cursor included.
func (s *Session) capture(lines int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	if lines > 0 && !s.emu.IsAltScreen() {
		if sb := s.emu.Scrollback(); sb != nil {
			n := sb.Len()
			for i := max(0, n-lines); i < n; i++ {
				out = append(out, lineText(sb.Line(i)))
			}
		}
	}
	for y := 0; y < s.rows; y++ {
		out = append(out, lineText(s.row(y)))
	}
	return strings.Join(out, "\n") + "\n"
}

func (s *Session) row(y int) uv.Line {
	line := make(uv.Line, s.cols)
	for x := 0; x < s.cols; x++ {
		if c := s.emu.CellAt(x, y); c != nil {
			line[x] = *c
		}
	}
	return line
}

func lineText(l uv.Line) string { return strings.TrimRight(l.String(), " ") }

// snapshot is what a client that attaches now needs to draw the session as it
// is: the screen with its styles, the cursor, and the modes and keyboard
// protocols the program has turned on. Caller holds s.mu.
func (s *Session) snapshot() []byte {
	var b strings.Builder
	b.WriteString("\x1b[0m")
	if s.emu.IsAltScreen() {
		b.WriteString("\x1b[?1049h")
	}
	b.WriteString("\x1b[H\x1b[2J")
	for y := 0; y < s.rows; y++ {
		fmt.Fprintf(&b, "\x1b[%d;1H", y+1)
		b.WriteString(s.row(y).Render())
		b.WriteString("\x1b[0m")
	}
	for _, m := range []ansi.Mode{ansi.ModeCursorKeys, ansi.ModeMouseNormal, ansi.ModeMouseButtonEvent,
		ansi.ModeMouseAnyEvent, ansi.ModeFocusEvent, ansi.ModeMouseExtSgr, ansi.ModeBracketedPaste} {
		if s.modes[m] {
			b.WriteString("\x1b[?" + strconv.Itoa(modeNumber(m)) + "h")
		}
	}
	if len(s.kitty) > 0 && s.kitty[len(s.kitty)-1] != 0 {
		fmt.Fprintf(&b, "\x1b[>%du", s.kitty[len(s.kitty)-1])
	}
	if s.mok > 0 {
		fmt.Fprintf(&b, "\x1b[>4;%dm", s.mok)
	}
	pos := s.emu.CursorPosition()
	fmt.Fprintf(&b, "\x1b[%d;%dH", pos.Y+1, pos.X+1)
	if s.hidden {
		b.WriteString("\x1b[?25l")
	} else {
		b.WriteString("\x1b[?25h")
	}
	return []byte(b.String())
}

func modeNumber(m ansi.Mode) int {
	if dm, ok := m.(ansi.DECMode); ok {
		return int(dm)
	}
	return 0
}

// attach registers a client. The snapshot and the registration happen under
// one lock, so the client sees every byte after the snapshot exactly once.
func (s *Session) attach(cols, rows int) (*attached, []byte, error) {
	if cols > 0 && rows > 0 {
		// The latest client to attach or resize decides the size, as with
		// tmux's window-size latest.
		if err := s.resize(cols, rows); err != nil {
			return nil, nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exited {
		return nil, nil, errMissing
	}
	c := &attached{out: make(chan []byte, 1024), gone: make(chan struct{})}
	s.clients[c] = struct{}{}
	s.nclients.Add(1)
	return c, s.snapshot(), nil
}

func (s *Session) detach(c *attached) {
	s.mu.Lock()
	if _, ok := s.clients[c]; ok {
		delete(s.clients, c)
		s.nclients.Add(-1)
	}
	s.mu.Unlock()
	c.close()
}

func (s *Session) info() Info {
	s.mu.Lock()
	in := Info{
		Name: s.name, ID: s.id, Created: s.created.Unix(), Activity: s.activity.Unix(),
		Cols: s.cols, Rows: s.rows, Clients: len(s.clients), StartDir: s.startDir,
		Options: map[string]string{}, Env: append([]string(nil), s.env...),
	}
	for k, v := range s.options {
		in.Options[k] = v
	}
	oscCwd := s.oscCwd
	s.mu.Unlock()
	if s.cmd.Process != nil {
		in.PID = s.cmd.Process.Pid
	}
	in.TTY = ttyName(s.pty)
	fg := foreground(s.pty, in.PID)
	in.Current = processName(fg)
	in.Cwd = processCwd(fg)
	if in.Cwd == "" {
		in.Cwd = oscDir(oscCwd)
	}
	if in.Cwd == "" {
		in.Cwd = s.startDir
	}
	return in
}

// oscDir turns an OSC 7 file:// URL into a path.
func oscDir(v string) string {
	if v == "" {
		return ""
	}
	if rest, ok := strings.CutPrefix(v, "file://"); ok {
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			rest = rest[i:]
		}
		if runtime.GOOS == "windows" && len(rest) > 2 && rest[0] == '/' && rest[2] == ':' {
			rest = rest[1:]
		}
		return rest
	}
	return v
}
