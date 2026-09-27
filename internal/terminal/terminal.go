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
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Port range handed out to attached terminals.
const (
	PortLo = 7710
	PortHi = 7730
)

// TTYDArgs builds ttyd's command line.
//
//   - `-i lo`: a ttyd with no credential is an unauthenticated shell, so it
//     listens on loopback only and is reached through this service's own proxy.
//     That is also what makes it work from any hostname.
//   - `-b <base path>`: ttyd's asset and websocket URLs are absolute, so it must
//     be told the prefix it is mounted under or the page loads blank.
//   - NOT `--once`: that accepts a single client and exits when it disconnects,
//     so having the terminal open on a phone made the same terminal dead on a
//     desktop, and ttyd's own "reconnect" had nothing to reconnect to. A tmux
//     session is multi-client by design — that is most of the point of it — so
//     the terminal in front of it has to be too.
func TTYDArgs(port int, basePath string, argv []string) []string {
	return append([]string{
		"-p", strconv.Itoa(port), "-i", "lo", "-W",
		"-b", basePath}, argv...)
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

// PortFor returns the live terminal for an attachment, if there is one.
func (m *Manager) PortFor(key string) (int, bool) {
	m.reap()
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.procs[key]
	if !ok || s.cmd == nil {
		return 0, false
	}
	return s.port, true
}

// ErrNoPorts means every terminal port in the range is taken.
var ErrNoPorts = errors.New("no free terminal ports")

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

	// Spawn is the process launcher. Tests replace it; production shells out.
	Spawn func(port int, basePath string, argv []string) (*exec.Cmd, error)
	// LookPath reports whether ttyd is installed. Tests override it.
	LookPath func(string) (string, error)
}

type session struct {
	port    int
	cmd     *exec.Cmd
	started time.Time
}

// NewManager builds a terminal manager wired to the real ttyd binary.
func NewManager() *Manager {
	return &Manager{
		procs:    map[string]*session{},
		LookPath: exec.LookPath,
		Spawn: func(port int, basePath string, argv []string) (*exec.Cmd, error) {
			cmd := exec.Command("ttyd", TTYDArgs(port, basePath, argv)...)
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

// Attach spawns (or reuses) a ttyd for an attachment and returns its port.
func (m *Manager) Attach(ctx context.Context, a Attachment, target *store.Target) (int, error) {
	m.reap()
	if _, err := m.LookPath("ttyd"); err != nil {
		return 0, errors.New("ttyd is not installed on the control plane")
	}
	argv, err := AttachArgv(a, target)
	if err != nil {
		return 0, err
	}

	// The reservation is taken under the same lock that reads the map, so two
	// simultaneous attaches cannot be handed the same port. Doing this in two
	// steps is what let the board's "attach" buttons collide: both callers saw
	// the port free, both spawned on it, and the loser's ttyd could not bind.
	//
	// That lock only covers this process. Another program on the machine —
	// a second Lectern (the service beside a `lectern local` runtime, or
	// parallel test servers) scanning the same range — can take the port
	// between the free check and ttyd's bind. That ttyd exits at once, so the
	// attach retries on another port instead of failing with a 503.
	var cmd *exec.Cmd
	var port int
	skip := map[int]bool{}
	for attempt := 0; ; attempt++ {
		m.mu.Lock()
		if s, ok := m.procs[a.Key]; ok {
			port := s.port
			m.mu.Unlock()
			return port, nil
		}
		port, err = m.freePortSkippingLocked(skip)
		if err != nil {
			m.mu.Unlock()
			return 0, err
		}
		m.procs[a.Key] = &session{port: port, started: time.Now()} // reserved, not yet running
		m.mu.Unlock()

		cmd, err = m.Spawn(port, a.BasePath(), argv)
		if err != nil {
			m.release(a.Key)
			return 0, fmt.Errorf("ttyd failed to start: %w", err)
		}
		exited := make(chan struct{})
		if cmd.Process != nil { // test doubles may hand back a command never started
			go func() { _ = cmd.Wait(); close(exited) }()
		}
		// Wait until it is listening, for at most the fixed 300ms this used to
		// sleep unconditionally: that sleep was nearly all of an attach's latency,
		// and ttyd binds in a few milliseconds. An exit in the meantime means it
		// could not start (a port taken after all, a bad argument).
		if !waitListening(ctx, port, exited, BindWait) {
			select {
			case <-exited:
				m.release(a.Key)
				if attempt+1 < attachAttempts && ctx.Err() == nil {
					skip[port] = true
					continue
				}
				return 0, errors.New("ttyd exited immediately")
			default:
			}
		}
		break
	}
	m.mu.Lock()
	m.procs[a.Key] = &session{port: port, cmd: cmd, started: time.Now()}
	m.mu.Unlock()
	return port, nil
}

// BindWait is the longest Attach waits for a fresh ttyd to start listening.
var BindWait = 300 * time.Millisecond

// waitListening polls the loopback port until something accepts, the process
// exits, ctx ends, or limit passes. It reports whether the port is accepting.
func waitListening(ctx context.Context, port int, exited <-chan struct{}, limit time.Duration) bool {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(limit)
	for {
		if conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond); err == nil {
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

// release drops a reservation whose ttyd never came up, returning its port.
func (m *Manager) release(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.procs[key]; ok && s.cmd == nil {
		delete(m.procs, key)
	}
}

func (m *Manager) reap() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.procs {
		if s.cmd != nil && s.cmd.ProcessState != nil {
			delete(m.procs, id)
		}
	}
}

// freePortLocked picks a port not already reserved here and not answering on
// the loopback interface. The caller must hold m.mu, which is what makes the
// choice and the reservation atomic.
// attachAttempts bounds how many ports one attach tries when ttyd cannot bind.
const attachAttempts = 5

func (m *Manager) freePortLocked() (int, error) { return m.freePortSkippingLocked(nil) }

// freePortSkippingLocked is freePortLocked, passing over ports this attach
// already failed to bind.
func (m *Manager) freePortSkippingLocked(skip map[int]bool) (int, error) {
	used := map[int]bool{}
	for port := range skip {
		used[port] = true
	}
	for _, s := range m.procs {
		used[s.port] = true
	}
	for port := PortLo; port <= PortHi; port++ {
		if used[port] {
			continue
		}
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
			200*time.Millisecond)
		if err != nil {
			return port, nil // nothing listening: the port is ours
		}
		conn.Close()
	}
	// Terminals no longer exit on their own when the last viewer leaves, so the
	// range can fill with ones nobody is looking at. Rather than refuse to
	// attach, retire the oldest: re-attaching to it costs one click and the tmux
	// session behind it is untouched either way.
	if key, s := m.oldestLocked(); s != nil {
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		delete(m.procs, key)
		return s.port, nil
	}
	return 0, ErrNoPorts
}

// oldestLocked returns the longest-running terminal. Caller holds m.mu.
func (m *Manager) oldestLocked() (string, *session) {
	var oldestKey string
	var oldest *session
	for key, s := range m.procs {
		if oldest == nil || s.started.Before(oldest.started) {
			oldestKey, oldest = key, s
		}
	}
	return oldestKey, oldest
}

// Shutdown terminates every attached terminal.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	procs := m.procs
	m.procs = map[string]*session{}
	m.mu.Unlock()
	for _, s := range procs {
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
	}
}
