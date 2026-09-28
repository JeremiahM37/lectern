package ptyhost

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"regexp"
	"runtime"
	"sort"
	"sync"
	"time"
)

// Options configure a host.
type Options struct {
	Socket string
	// History is the scrollback per session; 0 means DefaultHistory.
	History int
	// Idle is how long a host with no sessions and no clients waits before
	// exiting; 0 means ten seconds.
	Idle  time.Duration
	Build string
	Log   *slog.Logger
}

// Host owns the sessions of one user on one machine.
type Host struct {
	opts    Options
	history int
	log     *slog.Logger

	mu       sync.Mutex
	sessions map[string]*Session
	seq      int
	conns    int
	quiet    time.Time // when the host last became empty
	ln       net.Listener
	stopped  chan struct{}
	stopOnce sync.Once
}

// ErrRunning means another host already owns the socket.
var ErrRunning = errors.New("a PTY host is already running for this socket")

var errMissing = errors.New("can't find session")

// validName is the session names the host accepts: tmux's own rule forbids
// '.' and ':' (they are target syntax), and control characters are refused.
var validName = regexp.MustCompile(`^[^.:\x00-\x1f\x7f]{1,200}$`)

// Serve runs a host until it is idle or told to stop. It returns ErrRunning
// without touching the socket when another host holds the lock.
func Serve(opts Options) error {
	if opts.Socket == "" {
		p, err := SocketPath()
		if err != nil {
			return err
		}
		opts.Socket = p
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if err := ensureDir(opts.Socket); err != nil {
		return err
	}
	lock, err := os.OpenFile(lockPath(opts.Socket), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	held, err := tryLock(lock)
	if err != nil {
		return err
	}
	if !held {
		return ErrRunning
	}
	// Holding the lock makes the name ours: whatever socket file is there
	// was left by a host that is gone.
	_ = os.Remove(opts.Socket)
	ln, err := net.Listen("unix", opts.Socket)
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(opts.Socket, 0o600)
	}
	h := &Host{opts: opts, history: opts.History, log: opts.Log,
		sessions: map[string]*Session{}, quiet: time.Now(), ln: ln, stopped: make(chan struct{})}
	if h.history <= 0 {
		h.history = DefaultHistory
	}
	h.log.Info("pty host listening", "socket", opts.Socket, "pid", os.Getpid(), "protocol", Protocol)
	go h.watchIdle()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-h.stopped:
				h.log.Info("pty host stopped")
				_ = os.Remove(opts.Socket)
				return nil
			default:
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		h.mu.Lock()
		h.conns++
		h.mu.Unlock()
		go h.serve(conn)
	}
}

func (h *Host) stop() {
	h.stopOnce.Do(func() {
		close(h.stopped)
		_ = h.ln.Close()
	})
}

// watchIdle ends a host that holds no session and serves nobody, as a tmux
// server exits with its last session. The quiet period starts when the last
// session ends (or the host starts); a status check or a poll that happens to
// be connected delays the exit, but does not restart the period. The next start then runs whatever
// lectern binary is current, which is how the host is upgraded without ever
// ending a live session.
func (h *Host) watchIdle() {
	idle := h.opts.Idle
	if idle <= 0 {
		idle = 10 * time.Second
	}
	t := time.NewTicker(idle / 4)
	defer t.Stop()
	for {
		select {
		case <-h.stopped:
			return
		case <-t.C:
		}
		h.mu.Lock()
		empty := len(h.sessions) == 0 && h.conns == 0 && time.Since(h.quiet) >= idle
		h.mu.Unlock()
		if empty {
			h.log.Info("pty host idle; exiting")
			h.stop()
			return
		}
	}
}

func (h *Host) remove(s *Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessions[s.name] == s {
		delete(h.sessions, s.name)
		h.log.Info("session ended", "name", s.name)
	}
	if len(h.sessions) == 0 {
		h.quiet = time.Now()
	}
}

func (h *Host) get(name string) (*Session, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[name]
	return s, ok && s != nil // nil: a name reserved while its program starts
}

func (h *Host) serve(conn net.Conn) {
	defer func() {
		conn.Close()
		h.mu.Lock()
		h.conns--
		h.mu.Unlock()
	}()
	var hi hello
	if err := readJSON(conn, &hi); err != nil || hi.Hello != helloName {
		return
	}
	reply := hello{Hello: helloName, Protocol: Protocol, Min: MinProtocol, Ops: hostOps, Build: h.opts.Build, PID: os.Getpid()}
	if hi.Protocol < MinProtocol || hi.Min > Protocol {
		reply.Error = fmt.Sprintf("this PTY host speaks protocol %d-%d, the client %d-%d", MinProtocol, Protocol, hi.Min, hi.Protocol)
		_ = writeJSON(conn, reply)
		return
	}
	if err := writeJSON(conn, reply); err != nil {
		return
	}
	for {
		var req Request
		if err := readJSON(conn, &req); err != nil {
			return
		}
		if req.Op == "attach" {
			h.attach(conn, req)
			return
		}
		res := h.handle(req)
		if err := writeJSON(conn, res); err != nil {
			return
		}
		if req.Op == "shutdown" && res.OK {
			h.stop()
			return
		}
	}
}

func fail(err error) Response {
	if errors.Is(err, errMissing) {
		return Response{Error: err.Error(), Missing: true}
	}
	return Response{Error: err.Error()}
}

func (h *Host) session(name string) (*Session, error) {
	if s, ok := h.get(name); ok {
		return s, nil
	}
	return nil, errMissing
}

func (h *Host) handle(req Request) Response {
	switch req.Op {
	case "new":
		if !validName.MatchString(req.Name) {
			return Response{Error: fmt.Sprintf("invalid session name: %q", req.Name)}
		}
		h.mu.Lock()
		if _, dup := h.sessions[req.Name]; dup {
			h.mu.Unlock()
			return Response{Error: "duplicate session: " + req.Name}
		}
		// Reserve the name while the program starts.
		h.sessions[req.Name] = nil
		h.mu.Unlock()
		s, err := h.newSession(req)
		h.mu.Lock()
		if err != nil {
			delete(h.sessions, req.Name)
			if len(h.sessions) == 0 {
				h.quiet = time.Now()
			}
			h.mu.Unlock()
			return Response{Error: err.Error()}
		}
		h.seq++
		s.id = h.seq
		h.sessions[req.Name] = s
		h.mu.Unlock()
		h.log.Info("session started", "name", req.Name, "argv", req.Argv, "shell", req.Shell)
		in := s.info()
		return Response{OK: true, Info: &in}
	case "list":
		h.mu.Lock()
		all := make([]*Session, 0, len(h.sessions))
		for _, s := range h.sessions {
			if s != nil {
				all = append(all, s)
			}
		}
		h.mu.Unlock()
		sort.Slice(all, func(i, j int) bool { return all[i].id < all[j].id })
		res := Response{OK: true, Sessions: []Info{}}
		for _, s := range all {
			res.Sessions = append(res.Sessions, s.info())
		}
		return res
	case "info":
		s, err := h.session(req.Name)
		if err != nil {
			return fail(err)
		}
		in := s.info()
		return Response{OK: true, Info: &in}
	case "kill":
		s, err := h.session(req.Name)
		if err != nil {
			return fail(err)
		}
		s.kill()
		return Response{OK: true}
	case "write":
		s, err := h.session(req.Name)
		if err != nil {
			return fail(err)
		}
		switch {
		case req.Paste:
			err = s.paste(req.Data)
		case len(req.Names) > 0:
			err = s.keys(req.Names)
		default:
			err = s.input(req.Data)
		}
		if err != nil {
			return fail(err)
		}
		return Response{OK: true}
	case "capture":
		s, err := h.session(req.Name)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Text: s.capture(req.Lines)}
	case "resize":
		s, err := h.session(req.Name)
		if err != nil {
			return fail(err)
		}
		if err := s.resize(req.Cols, req.Rows); err != nil {
			return fail(err)
		}
		return Response{OK: true}
	case "set-option":
		s, err := h.session(req.Name)
		if err != nil {
			return fail(err)
		}
		s.mu.Lock()
		_, exists := s.options[req.Key]
		set := !(req.OnlyIfUnset && exists)
		if set {
			s.options[req.Key] = req.Value
		}
		s.mu.Unlock()
		if req.OnlyIfUnset && exists {
			return Response{Error: "already set: " + req.Key}
		}
		return Response{OK: true, Set: set}
	case "probe":
		res := Response{OK: true}
		for _, name := range req.Names {
			p := Probe{Name: name}
			if s, ok := h.get(name); ok && s != nil {
				in := s.info()
				p.Found, p.Current = true, in.Current
				p.RootArgs, p.TTYArgs = probeArgs(s, in.PID)
			}
			res.Probes = append(res.Probes, p)
		}
		return res
	case "shutdown":
		h.mu.Lock()
		n := 0
		var all []*Session
		for _, s := range h.sessions {
			if s != nil {
				n++
				all = append(all, s)
			}
		}
		h.mu.Unlock()
		if n > 0 && !req.Force {
			return Response{Error: fmt.Sprintf("%d sessions are running; stopping the host ends them", n)}
		}
		for _, s := range all {
			s.kill()
		}
		return Response{OK: true}
	}
	return Response{Error: "unsupported operation: " + req.Op}
}

// attach turns the connection into a stream of one session's terminal.
func (h *Host) attach(conn net.Conn, req Request) {
	s, err := h.session(req.Name)
	if err != nil {
		_ = writeJSON(conn, fail(err))
		return
	}
	c, snap, err := s.attach(req.Cols, req.Rows)
	if err != nil {
		_ = writeJSON(conn, fail(err))
		return
	}
	defer s.detach(c)
	if err := writeJSON(conn, Response{OK: true}); err != nil {
		return
	}
	var wmu sync.Mutex
	send := func(kind byte, b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		return writeFrame(conn, kind, b)
	}
	if err := send(frameOutput, snap); err != nil {
		return
	}
	go func() {
		defer conn.Close()
		for {
			select {
			case b := <-c.out:
				if send(frameOutput, b) != nil {
					return
				}
			case <-c.gone:
				// Flush what was queued before the end, then say so.
			flush:
				for {
					select {
					case b := <-c.out:
						if send(frameOutput, b) != nil {
							return
						}
					default:
						break flush
					}
				}
				select {
				case <-s.done:
					_ = send(frameExit, nil)
				default:
				}
				return
			}
		}
	}()
	for {
		kind, payload, err := readFrame(conn)
		if err != nil {
			return
		}
		switch kind {
		case frameInput:
			if s.input(payload) != nil {
				return
			}
		case frameResize:
			var r resize
			if json.Unmarshal(payload, &r) == nil {
				_ = s.resize(r.Cols, r.Rows)
			}
		}
	}
}
