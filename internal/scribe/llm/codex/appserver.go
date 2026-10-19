package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/version"
)

// appServerArgs start the JSON-RPC server that speaks on stdin and stdout.
var appServerArgs = []string{"app-server", "--listen", "stdio://"}

// closeGrace is how long Close waits after SIGTERM before it sends SIGKILL.
const closeGrace = 2 * time.Second

// maxBufferedNotes caps the notifications kept for WaitNotification. The ones
// this package waits for arrive next to the request that causes them, so an
// old unclaimed one is safe to drop when a chatty server would otherwise grow
// the buffer without bound.
const maxBufferedNotes = 256

// rpcMessage is one line from the server. A response has an id and a result
// or an error; a notification has a method and no id.
type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type notification struct {
	method string
	params json.RawMessage
}

// rpcReply is what a waiting Request receives. dead means the server ended
// before it answered.
type rpcReply struct {
	result json.RawMessage
	err    *rpcError
	dead   bool
}

// Client is a running `codex app-server`. One goroutine reads its stdout and
// hands each response to the request waiting for it; notifications wait in a
// buffer until WaitNotification asks for them. Methods are safe to call from
// several goroutines.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *appStderr

	writeMu sync.Mutex // one line at a time on stdin

	mu      sync.Mutex // guards the fields below
	nextID  int64
	pending map[int64]chan rpcReply
	notes   []notification
	changed chan struct{} // closed and replaced whenever notes change or the server ends
	dead    bool          // stdout has ended: no more replies or notifications

	exited    chan struct{} // closed after the child has been waited for
	closeOnce sync.Once
}

// StartAppServer starts `<bin> app-server` and completes the initialize
// handshake. The child is stopped when ctx is cancelled, and by Close.
func StartAppServer(ctx context.Context, bin string) (*Client, error) {
	return startAppServer(ctx, ctx, bin)
}

// startAppServer takes two contexts. handshake bounds the start-up requests.
// lifetime decides when the child is stopped. Login passes a lifetime that
// outlives its own context, so it can send account/login/cancel before the
// child is closed.
func startAppServer(handshake, lifetime context.Context, bin string) (*Client, error) {
	if bin == "" {
		bin = DefaultBinary
	}
	cmd := exec.Command(bin, appServerArgs...)
	setProcGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &appStderr{max: 4096}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("could not start %s app-server: %w", bin, err)
	}
	c := &Client{
		cmd:     cmd,
		stdin:   stdin,
		stderr:  stderr,
		pending: map[int64]chan rpcReply{},
		changed: make(chan struct{}),
		exited:  make(chan struct{}),
	}
	go c.readLoop(stdout)
	// Stopping the child with its context is what keeps it from outliving a
	// cancelled operation. Close is idempotent, so the caller may close too.
	go func() {
		select {
		case <-lifetime.Done():
			_ = c.Close()
		case <-c.exited:
		}
	}()
	if err := c.handshake(handshake); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("codex app-server did not start: %w%s", err, stderr.suffix())
	}
	return c, nil
}

func (c *Client) handshake(ctx context.Context) error {
	info := map[string]string{"name": "lectern", "version": version.Version}
	if err := c.Request(ctx, "initialize", map[string]any{"clientInfo": info}, nil); err != nil {
		return err
	}
	return c.writeJSON(map[string]string{"jsonrpc": "2.0", "method": "initialized"})
}

// Request sends one JSON-RPC request and decodes its result into out. A
// JSON-RPC error becomes a Go error that carries the server's message. A nil
// params is sent as no params at all. out may be nil when the result is not
// needed.
func (c *Client) Request(ctx context.Context, method string, params, out any) error {
	req := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int64  `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{JSONRPC: "2.0", Method: method, Params: params}

	ch := make(chan rpcReply, 1)
	c.mu.Lock()
	if c.dead {
		c.mu.Unlock()
		return c.deadError()
	}
	c.nextID++
	req.ID = c.nextID
	c.pending[req.ID] = ch
	c.mu.Unlock()

	if err := c.writeJSON(req); err != nil {
		c.forget(req.ID)
		return fmt.Errorf("%s: %w", method, err)
	}
	select {
	case r := <-ch:
		if r.dead {
			return c.deadError()
		}
		if r.err != nil {
			return fmt.Errorf("%s: %s", method, r.err.Message)
		}
		if out != nil && len(r.result) > 0 {
			if err := json.Unmarshal(r.result, out); err != nil {
				return fmt.Errorf("%s: unexpected reply: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.forget(req.ID)
		return ctx.Err()
	}
}

// WaitNotification returns the params of the first notification named method
// for which match is true (nil match accepts any). A notification that arrived
// before the call is still returned. It fails when ctx ends or the server
// exits first.
func (c *Client) WaitNotification(ctx context.Context, method string, match func(params json.RawMessage) bool) (json.RawMessage, error) {
	for {
		c.mu.Lock()
		for i, n := range c.notes {
			if n.method == method && (match == nil || match(n.params)) {
				c.notes = append(c.notes[:i], c.notes[i+1:]...)
				c.mu.Unlock()
				return n.params, nil
			}
		}
		if c.dead {
			c.mu.Unlock()
			return nil, c.deadError()
		}
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Close stops the child. It closes stdin, sends SIGTERM to the child's process
// group, waits up to closeGrace for it to exit, then sends SIGKILL and waits
// for the exit. Calling Close again does nothing.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.writeMu.Lock()
		_ = c.stdin.Close()
		c.writeMu.Unlock()
		select {
		case <-c.exited:
			return
		default:
		}
		_ = terminateGroup(c.cmd)
		select {
		case <-c.exited:
		case <-time.After(closeGrace):
			_ = killGroup(c.cmd)
			<-c.exited
		}
	})
	return nil
}

// readLoop owns stdout. When the output ends the server is gone: pending
// requests fail, waiters wake, and the child is reaped. Waiting is done here
// because os/exec requires every read to finish before Wait.
func (c *Client) readLoop(stdout io.Reader) {
	r := bufio.NewReader(stdout)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			c.dispatch(line)
		}
		if err != nil {
			break
		}
	}
	c.markDead()
	_ = c.cmd.Wait()
	close(c.exited)
}

func (c *Client) dispatch(line []byte) {
	var m rpcMessage
	// The CLI may print plain text on stdout around its JSON. Skip what is not
	// a message rather than failing the whole session.
	if json.Unmarshal(line, &m) != nil {
		return
	}
	switch {
	case m.Method == "" && len(m.ID) > 0:
		c.deliver(m)
	case m.Method != "" && len(m.ID) == 0:
		c.addNote(m)
	}
	// A server-to-client request (method and id) is not part of anything
	// Lectern asks for, so it is dropped.
}

func (c *Client) deliver(m rpcMessage) {
	var id int64
	if err := json.Unmarshal(m.ID, &id); err != nil {
		return
	}
	c.mu.Lock()
	ch, ok := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ok {
		ch <- rpcReply{result: m.Result, err: m.Error}
	}
}

func (c *Client) addNote(m rpcMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notes = append(c.notes, notification{method: m.Method, params: m.Params})
	if over := len(c.notes) - maxBufferedNotes; over > 0 {
		c.notes = c.notes[over:]
	}
	c.wakeLocked()
}

func (c *Client) markDead() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dead = true
	for id, ch := range c.pending {
		ch <- rpcReply{dead: true}
		delete(c.pending, id)
	}
	c.wakeLocked()
}

// wakeLocked wakes every WaitNotification. Caller holds c.mu.
func (c *Client) wakeLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *Client) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) deadError() error {
	return fmt.Errorf("codex app-server exited%s", c.stderr.suffix())
}

func (c *Client) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(append(b, '\n'))
	return err
}

// appStderr keeps the last max bytes of a stream, so a failed start can say
// what the CLI printed to stderr without holding all of it.
type appStderr struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *appStderr) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = t.buf[over:]
	}
	return len(p), nil
}

// suffix returns the last non-empty line of the stream as ": text", or "".
func (t *appStderr) suffix() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := strings.TrimSpace(string(t.buf))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if s == "" {
		return ""
	}
	return ": " + s
}
