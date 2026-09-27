// Package terminal is one-click terminal attach: spawn a ttyd on the control
// plane that wraps `tmux attach` (locally, over ssh, or via pct) for a running
// attempt. Browser and desktop clients share the same persistent tmux sessions.
package terminal

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// DefaultMaxTerminals is how many terminals may run at once unless
// LECTERN_TERMINALS_MAX says otherwise (Manager.Max). Each is one ttyd
// process; past the limit an idle one is retired, and a terminal someone is
// viewing never is.
const DefaultMaxTerminals = 200

// TTYDArgs builds ttyd's command line.
//
//   - `-i <socket>`: a ttyd with no credential is an unauthenticated shell, so
//     it listens on a Unix socket in a directory only this user can enter, and
//     is reached through this service's own proxy. That is also what makes it
//     work from any hostname. It used to listen on a loopback TCP port from a
//     fixed range of 21: any local user could connect to it, two Lectern
//     instances on one host could pick the same port and serve each other's
//     terminals, and the range capped how many terminals could be open.
//   - `-b <base path>`: ttyd's asset and websocket URLs are absolute, so it must
//     be told the prefix it is mounted under or the page loads blank.
//   - NOT `--once`: that accepts a single client and exits when it disconnects,
//     so having the terminal open on a phone made the same terminal dead on a
//     desktop, and ttyd's own "reconnect" had nothing to reconnect to. A tmux
//     session is multi-client by design — that is most of the point of it — so
//     the terminal in front of it has to be too.
func TTYDArgs(socket, basePath string, argv []string) []string {
	return append([]string{"-i", socket, "-W", "-b", basePath}, argv...)
}

// BasePath is where a terminal is mounted on the control plane's own origin.
//
// Same-origin matters: the browser may have reached lectern through nginx, a
// tailnet name or an IP, and only the control plane knows where ttyd actually
// runs. Building the URL from the browser's hostname pointed the terminal at
// whichever machine served the page — the reverse proxy, usually, which runs no
// ttyd at all and simply refused the connection.
//
// It names the ATTACHMENT, not the port. A port is where a terminal happens to
// be right now: it changes when the process is retired to free the range, and
// every one of them dies when the control plane restarts. A page holding a port
// URL is then pointed at nothing forever — which is what made ttyd's own
// "reconnect" loop without ever succeeding. Named by attachment, the URL stays
// valid and the terminal behind it is respawned on demand.
func (a Attachment) BasePath() string {
	kind, id, ok := strings.Cut(a.Key, ":")
	if !ok {
		return "/term/" + a.Key
	}
	return "/term/" + kind + "/" + id
}

// SocketFor returns the live terminal's socket for an attachment, if there is one.
func (m *Manager) SocketFor(key string) (string, bool) {
	m.reap()
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.procs[key]
	if !ok || s.cmd == nil {
		return "", false
	}
	return s.socket, true
}

// ErrFull means the terminal limit is reached and every running terminal is
// open in a browser, so none can be retired to make room.
var ErrFull = errors.New("every web terminal is open somewhere; close one and try again")

// Attachment is whatever you want a terminal on: a task's attempt, or an
// interactive session. Both are just a tmux session on a target, which is why
// one manager serves both.
type Attachment struct {
	// Key is unique across kinds, e.g. "attempt:12" or "session:3", so an
	// attempt and a session can never share a ttyd by accident.
	Key         string
	TmuxSession string
	// SandboxVMID is set when the tmux session lives inside an ephemeral
	// container rather than on the target itself.
	SandboxVMID string
	// Workdir turns this into a plain shell in a directory rather than an attach
	// to an existing agent session — a way into the machine where the code
	// actually lives, to read something or make a change by hand. It is still a
	// tmux session, so it survives a closed tab and comes back where you left it.
	Workdir string
}

// IsShell reports whether this attachment is a shell rather than an agent.
func (a Attachment) IsShell() bool { return a.Workdir != "" }

// Manager tracks one ttyd per attachment.
type Manager struct {
	mu    sync.Mutex
	procs map[string]*session
	// viewers counts open browser connections per attachment, kept apart
	// from procs so a viewer that connects while its ttyd is being respawned
	// is already counted.
	viewers    map[string]int
	lastViewed map[string]time.Time
	dir        string // private socket directory, made on first use
	seq        int

	// Max overrides DefaultMaxTerminals when positive.
	Max int
	// SocketDir overrides where the private socket directory is made.
	SocketDir string

	// Spawn is the process launcher. Tests replace it; production shells out.
	Spawn func(socket, basePath string, argv []string) (*exec.Cmd, error)
	// LookPath reports whether ttyd is installed. Tests override it.
	LookPath func(string) (string, error)
}

type session struct {
	socket  string
	cmd     *exec.Cmd
	started time.Time
	exited  chan struct{} // closed when cmd has exited; nil for a test double never started
}

// NewManager builds a terminal manager wired to the real ttyd binary.
func NewManager() *Manager {
	return &Manager{
		procs:   map[string]*session{},
		viewers: map[string]int{}, lastViewed: map[string]time.Time{},
		LookPath: exec.LookPath,
		Spawn: func(socket, basePath string, argv []string) (*exec.Cmd, error) {
			cmd := exec.Command("ttyd", TTYDArgs(socket, basePath, argv)...)
			if err := cmd.Start(); err != nil {
				return nil, err
			}
			return cmd, nil
		},
	}
}

// AttachArgv is the command a ttyd wraps, per target kind.
//
// It errors rather than falling back for a sandbox with no vmid: the default
// branch runs tmux on the control plane itself, so a missing vmid would quietly
// hand the operator a shell on the wrong machine.
func AttachArgv(a Attachment, target *store.Target) ([]string, error) {
	sess := a.TmuxSession
	// `new-session -A` attaches if it is already there and creates it otherwise,
	// so reopening a shell returns to the same one with its history and whatever
	// was half-typed, rather than starting over in a fresh directory.
	inner := []string{"tmux", "attach", "-t", sess}
	if a.IsShell() {
		// An empty command inherits tmux's default-command, which may launch an
		// agent. Resolve SHELL on the target, not on the control-plane host, and
		// pass a real shell explicitly for project and companion terminals.
		inner = []string{"tmux", "new-session", "-A", "-s", sess, "-c", a.Workdir,
			"--", "/bin/sh", "-c", `exec "${SHELL:-/bin/sh}" -i`}
	}
	// One pane has one grid, so one client decides its size. `latest` gives it
	// to the client that last attached, typed or resized, and the browser
	// re-announces its size whenever its terminal is shown or focused, so the
	// screen being used is drawn at its own size. `smallest` let a forgotten
	// client (a sleeping phone, a hidden tab, a split pane) pin every other
	// view to a sliver of Claude's input box padded with tmux's dots until the
	// session was restarted. Apply this to the attached window, never to the
	// user's global tmux options. Queue it after new-session so companion
	// shells are created before targeting.
	inner = append(inner, ";", "set-option", "-w", "-t", "="+sess+":", "window-size", "latest")

	switch {
	case target.Kind == "sandbox":
		if a.SandboxVMID == "" {
			return nil, errors.New("sandbox attachment has no container id — its sandbox is gone")
		}
		return append([]string{"sudo", "pct", "exec", a.SandboxVMID, "--"}, inner...), nil
	case target.Kind == "pct":
		return append([]string{"sudo", "pct", "exec", target.Host, "--"}, inner...), nil
	case target.Kind == "ssh":
		argv := []string{"ssh", "-tt", "-o", "StrictHostKeyChecking=accept-new"}
		if target.Port > 0 {
			argv = append(argv, "-p", strconv.Itoa(target.Port))
		}
		if target.KeyPath != "" {
			argv = append(argv, "-i", target.KeyPath)
		}
		user := target.User
		if user == "" {
			user = "root"
		}
		words := make([]string, len(inner))
		for i, word := range inner {
			words[i] = shellq.Quote(word)
		}
		command := strings.Join(words, " ")
		wrapper := target.CommandPrefix
		// Wrappers such as Windows SSH -> WSL consume stdin while decoding the
		// command. Give tmux its own Unix PTY and relay input from the outer tty.
		if wrapper != "" {
			command = "script -qefc " + shellq.Quote("env TERM=xterm-256color "+command) + " /dev/null </dev/tty"
		}
		switch {
		case strings.Contains(wrapper, "{b64}"):
			command = strings.ReplaceAll(wrapper, "{b64}", base64.StdEncoding.EncodeToString([]byte(command)))
		case strings.Contains(wrapper, "{cmd}"):
			command = strings.ReplaceAll(wrapper, "{cmd}", shellq.Quote(command))
		case wrapper != "":
			command = wrapper + " " + shellq.Quote(command)
		}
		return append(argv, user+"@"+target.Host, command), nil
	default:
		return inner, nil
	}
}

// Attach spawns (or reuses) a ttyd for an attachment and returns its socket.
func (m *Manager) Attach(ctx context.Context, a Attachment, target *store.Target) (string, error) {
	socket, _, err := m.AttachWithNotice(ctx, a, target)
	return socket, err
}

// AttachWithNotice is Attach, also naming the idle terminal it retired to make
// room, if any ("" when none), so the caller can tell the user.
func (m *Manager) AttachWithNotice(ctx context.Context, a Attachment, target *store.Target) (string, string, error) {
	m.reap()
	if _, err := m.LookPath("ttyd"); err != nil {
		return "", "", errors.New("ttyd is not installed on the control plane")
	}
	argv, err := AttachArgv(a, target)
	if err != nil {
		return "", "", err
	}

	// The reservation is taken under the same lock that reads the map, so two
	// simultaneous attaches of one attachment cannot start two ttyds.
	m.mu.Lock()
	if s, ok := m.procs[a.Key]; ok {
		socket := s.socket
		m.mu.Unlock()
		return socket, "", nil
	}
	retired, err := m.makeRoomLocked()
	if err != nil {
		m.mu.Unlock()
		return "", "", err
	}
	socket, err := m.socketPathLocked(a.Key)
	if err != nil {
		m.mu.Unlock()
		return "", "", err
	}
	m.procs[a.Key] = &session{socket: socket, started: time.Now()} // reserved, not yet running
	m.mu.Unlock()

	cmd, err := m.Spawn(socket, a.BasePath(), argv)
	if err != nil {
		m.release(a.Key)
		return "", "", fmt.Errorf("ttyd failed to start: %w", err)
	}
	var exited chan struct{}
	if cmd.Process != nil { // test doubles may hand back a command never started
		exited = make(chan struct{})
		go func() { _ = cmd.Wait(); close(exited) }()
	}
	// Wait until it is listening, for at most BindWait (this used to sleep a
	// fixed 300ms, nearly all of an attach's latency; ttyd listens in a few
	// milliseconds). An exit in the meantime means it could not start.
	if !waitListening(ctx, socket, exited, BindWait) {
		select {
		case <-exited:
			m.release(a.Key)
			return "", "", errors.New("ttyd exited immediately")
		default:
		}
	}
	m.mu.Lock()
	m.procs[a.Key] = &session{socket: socket, cmd: cmd, started: time.Now(), exited: exited}
	m.mu.Unlock()
	return socket, retired, nil
}

// BindWait is the longest Attach waits for a fresh ttyd to start listening.
var BindWait = 300 * time.Millisecond

// waitListening polls the socket until something accepts, the process exits,
// ctx ends, or limit passes. It reports whether the socket is accepting.
func waitListening(ctx context.Context, socket string, exited <-chan struct{}, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for {
		if conn, err := net.DialTimeout("unix", socket, 50*time.Millisecond); err == nil {
			conn.Close()
			return true
		}
		wait := min(10*time.Millisecond, time.Until(deadline))
		if wait <= 0 {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-exited:
			return false
		case <-time.After(wait):
		}
	}
}

// socketPathLocked names a fresh socket for an attachment in the private
// directory. Every spawn gets its own name, so a new ttyd never contends with
// a dying one for the same path. Caller holds m.mu.
func (m *Manager) socketPathLocked(key string) (string, error) {
	if m.dir == "" {
		parent := m.SocketDir
		if parent == "" {
			parent = os.Getenv("XDG_RUNTIME_DIR")
		}
		if parent == "" {
			parent = os.TempDir()
		}
		dir, err := os.MkdirTemp(parent, "lectern-term-")
		if err != nil {
			return "", fmt.Errorf("terminal socket directory: %w", err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", err
		}
		m.dir = dir
	}
	m.seq++
	name := strings.NewReplacer(":", "-", "/", "-").Replace(key)
	return filepath.Join(m.dir, fmt.Sprintf("%s.%d.sock", name, m.seq)), nil
}

// Dial connects to a terminal's socket; the proxy's transport uses it.
func Dial(ctx context.Context, socket string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", socket)
}

// release drops a reservation whose ttyd never came up.
func (m *Manager) release(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.procs[key]; ok && s.cmd == nil {
		delete(m.procs, key)
		os.Remove(s.socket)
	}
}

func (m *Manager) reap() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.procs {
		if s.exited == nil {
			continue
		}
		select {
		case <-s.exited:
			delete(m.procs, id)
			os.Remove(s.socket)
		default:
		}
	}
}

// makeRoomLocked keeps the number of running terminals under the limit. When
// it is reached it retires the least recently used terminal that nobody has
// open, and names it; the tmux session behind it is untouched and its URL
// respawns it on the next visit. A terminal someone is viewing is never
// retired: with every one viewed, the attach is refused instead.
func (m *Manager) makeRoomLocked() (string, error) {
	limit := m.Max
	if limit <= 0 {
		limit = DefaultMaxTerminals
	}
	if len(m.procs) < limit {
		return "", nil
	}
	key, s := m.idlestLocked()
	if s == nil {
		return "", ErrFull
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	delete(m.procs, key)
	os.Remove(s.socket)
	return key, nil
}

// idlestLocked returns the running terminal with no viewers that was used
// least recently. Caller holds m.mu.
func (m *Manager) idlestLocked() (string, *session) {
	var idleKey string
	var idle *session
	var idleAt time.Time
	for key, s := range m.procs {
		if s.cmd == nil || m.viewers[key] > 0 {
			continue // still starting, or someone has it open
		}
		at := s.started
		if seen := m.lastViewed[key]; seen.After(at) {
			at = seen
		}
		if idle == nil || at.Before(idleAt) {
			idleKey, idle, idleAt = key, s, at
		}
	}
	return idleKey, idle
}

// Viewing records one open browser connection to an attachment's terminal
// until the returned func is called. The proxy calls it for each websocket.
func (m *Manager) Viewing(key string) (done func()) {
	m.mu.Lock()
	if m.viewers == nil {
		m.viewers, m.lastViewed = map[string]int{}, map[string]time.Time{}
	}
	m.viewers[key]++
	m.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			if m.viewers[key]--; m.viewers[key] <= 0 {
				delete(m.viewers, key)
			}
			m.lastViewed[key] = time.Now()
			m.mu.Unlock()
		})
	}
}

// Viewers reports how many browser connections an attachment has open.
func (m *Manager) Viewers(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.viewers[key]
}

// Shutdown terminates every attached terminal and removes their sockets.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	procs, dir := m.procs, m.dir
	m.procs, m.dir = map[string]*session{}, ""
	m.mu.Unlock()
	for _, s := range procs {
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
	}
	if dir != "" {
		os.RemoveAll(dir)
	}
}
