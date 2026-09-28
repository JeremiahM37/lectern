// Package webterm is Lectern's own web terminal server. It speaks ttyd's
// WebSocket protocol, which the browser terminal already uses
// (frontend/src/terminal/engine.ts), so it replaces ttyd without a change on
// the page: GET <base>/token, then a WebSocket at <base>/ws with subprotocol
// "tty", whose first message is {"AuthToken","columns","rows"}; after it,
// input is "0"+bytes, a resize "1"+{"columns","rows"}, and "2"/"3" pause and
// resume output. Output goes back as "0"+bytes.
//
// Each connection runs the attachment's command on a pseudo-terminal of its
// own, as ttyd does — except a PTY-host attachment (`lectern pty …`), which is
// streamed straight from the host with no terminal in between.
package webterm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/JeremiahM37/lectern/v2/internal/ptyhost"
)

// Options configure one terminal server.
type Options struct {
	// Socket is the private Unix socket to listen on.
	Socket string
	// Base is the path the terminal is mounted under, e.g. /term/session/3.
	Base string
	// Argv is the command each connection runs.
	Argv []string
	// Self is this lectern binary, for PTY-host attachments.
	Self string
	Log  *slog.Logger
}

// Serve listens on opts.Socket until ctx ends.
func Serve(ctx context.Context, opts Options) error {
	if opts.Log == nil {
		opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	_ = os.Remove(opts.Socket)
	ln, err := net.Listen("unix", opts.Socket)
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(opts.Socket, 0o600)
	}
	srv := &http.Server{Handler: Handler(opts), ReadHeaderTimeout: 15 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Handler serves the terminal under opts.Base.
func Handler(opts Options) http.Handler {
	base := strings.TrimRight(opts.Base, "/")
	mux := http.NewServeMux()
	mux.HandleFunc(base+"/token", func(w http.ResponseWriter, _ *http.Request) {
		// ttyd's credential; access control is Lectern's proxy's job.
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"token": ""}`)
	})
	mux.HandleFunc(base+"/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWS(w, r, opts)
	})
	mux.HandleFunc(base+"/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "Lectern terminal endpoint: open it from Lectern.\n")
	})
	return mux
}

// terminal is what a connection streams: a program on its own PTY, or a
// PTY-host session.
type terminal interface {
	read() ([]byte, error)
	write([]byte) error
	resize(cols, rows int) error
	close()
}

type hello struct {
	AuthToken string `json:"AuthToken"`
	Columns   int    `json:"columns"`
	Rows      int    `json:"rows"`
}

func serveWS(w http.ResponseWriter, r *http.Request, opts Options) {
	// The socket is private and reached only through Lectern's own proxy,
	// which has already authenticated the browser; the proxy rewrites Host,
	// so an Origin check here could only ever refuse legitimate pages.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"tty"}, InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ws.SetReadLimit(16 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer ws.CloseNow()
	_, first, err := ws.Read(ctx)
	if err != nil {
		return
	}
	var h hello
	_ = json.Unmarshal(first, &h)
	term, err := open(opts, h.Columns, h.Rows)
	if err != nil {
		opts.Log.Info("terminal could not start", "base", opts.Base, "err", err)
		_ = ws.Write(ctx, websocket.MessageBinary, append([]byte("0"), []byte("\r\n"+err.Error()+"\r\n")...))
		_ = ws.Close(websocket.StatusNormalClosure, "")
		return
	}
	defer term.close()
	bridge(ctx, cancel, ws, term)
	_ = ws.Close(websocket.StatusNormalClosure, "")
}

// bridge copies until either side ends. A paused browser (message "2") stops
// the output being read, so a flood waits in the program's own terminal
// buffer rather than in memory here.
func bridge(ctx context.Context, cancel context.CancelFunc, ws *websocket.Conn, term terminal) {
	var mu sync.Mutex
	paused := false
	resume := make(chan struct{}, 1)
	go func() {
		defer cancel()
		for {
			mu.Lock()
			p := paused
			mu.Unlock()
			if p {
				select {
				case <-resume:
					continue
				case <-ctx.Done():
					return
				}
			}
			b, err := term.read()
			if len(b) > 0 {
				if ws.Write(ctx, websocket.MessageBinary, append([]byte("0"), b...)) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		_, msg, err := ws.Read(ctx)
		if err != nil || len(msg) == 0 {
			return
		}
		switch msg[0] {
		case '0':
			if term.write(msg[1:]) != nil {
				return
			}
		case '1':
			var size struct {
				Columns int `json:"columns"`
				Rows    int `json:"rows"`
			}
			if json.Unmarshal(msg[1:], &size) == nil && size.Columns > 0 && size.Rows > 0 {
				_ = term.resize(size.Columns, size.Rows)
			}
		case '2':
			mu.Lock()
			paused = true
			mu.Unlock()
		case '3':
			mu.Lock()
			paused = false
			mu.Unlock()
			select {
			case resume <- struct{}{}:
			default:
			}
		}
	}
}

// open starts what the attachment's command names.
func open(opts Options, cols, rows int) (terminal, error) {
	if isPtyCommand(opts.Argv) {
		return openHost(opts, cols, rows)
	}
	p, err := ptyhost.StartProcess(opts.Argv, cols, rows)
	if err != nil {
		return nil, err
	}
	t := &process{p: p, out: make(chan []byte)}
	go t.pump()
	return t, nil
}

// isPtyCommand recognises `<lectern> pty …`, a PTY-host attachment on this
// machine.
func isPtyCommand(argv []string) bool {
	return len(argv) > 2 && argv[1] == "pty"
}

// process is a program on its own pseudo-terminal.
type process struct {
	p   *ptyhost.Process
	out chan []byte
}

// pump reads the terminal; while the browser has paused output nobody
// receives, and the program's own terminal buffer holds the rest.
func (t *process) pump() {
	defer close(t.out)
	buf := make([]byte, 32<<10)
	for {
		n, err := t.p.Read(buf)
		if n > 0 {
			t.out <- append([]byte(nil), buf[:n]...)
		}
		if err != nil {
			return
		}
	}
}

func (t *process) read() ([]byte, error) {
	select {
	case b, ok := <-t.out:
		if !ok {
			return nil, io.EOF
		}
		return b, nil
	case <-t.p.Done():
		// A pseudoconsole's output does not end when its program does;
		// Done is what says it has. Pass on what is still in flight.
		select {
		case b, ok := <-t.out:
			if ok {
				return b, nil
			}
		case <-time.After(200 * time.Millisecond):
		}
		return nil, io.EOF
	}
}

func (t *process) write(b []byte) error        { _, err := t.p.Write(b); return err }
func (t *process) resize(cols, rows int) error { return t.p.Resize(cols, rows) }
func (t *process) close()                      { _ = t.p.Close() }

// openHost runs a `lectern pty …` attachment in this process: the same
// command-line client, whose attach hands the host's stream to the browser.
func openHost(opts Options, cols, rows int) (terminal, error) {
	socket, err := ptyhost.SocketPath()
	if err != nil {
		return nil, err
	}
	self := opts.Self
	if self == "" {
		if self, err = os.Executable(); err != nil {
			return nil, err
		}
	}
	streams := make(chan *ptyhost.Stream, 1)
	finished := make(chan struct{})
	var stderr strings.Builder
	cli := &ptyhost.CLI{Socket: socket, Bin: self, Stdout: io.Discard, Stderr: &stderr,
		Attach: func(c *ptyhost.Client, name string) error {
			st, err := c.Attach(name, cols, rows)
			if err != nil {
				return err
			}
			streams <- st
			<-finished // the connection is the browser's until it leaves
			return nil
		}}
	result := make(chan int, 1)
	go func() { result <- cli.Run(opts.Argv[2:]) }()
	select {
	case st := <-streams:
		return &hostStream{st: st, finished: finished}, nil
	case code := <-result:
		close(finished)
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = "the session is not available"
		}
		return nil, fmt.Errorf("%s (exit %d)", msg, code)
	}
}

type hostStream struct {
	st       *ptyhost.Stream
	finished chan struct{}
	once     sync.Once
}

func (t *hostStream) read() ([]byte, error)       { return t.st.Read() }
func (t *hostStream) write(b []byte) error        { return t.st.Write(b) }
func (t *hostStream) resize(cols, rows int) error { return t.st.Resize(cols, rows) }
func (t *hostStream) close() {
	t.once.Do(func() {
		_ = t.st.Close()
		close(t.finished)
	})
}
