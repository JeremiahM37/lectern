// Package browser drives a real Chromium on a target over the Chrome DevTools
// Protocol, for the session Browser pane, Design Mode and the agent browser
// tools (docs/browser.md).
//
// The browser runs where the session's agent runs, so it sees that machine's
// localhost exactly as the agent does. Its DevTools port is bound to the
// target's loopback and reached through the same executor dialer that live port
// forwards use; nothing new listens on the target's network.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Dial opens a TCP connection to addr as the machine running the browser sees it.
type Dial func(ctx context.Context, addr string) (net.Conn, error)

// cdpError is an error the browser itself returned for one command.
type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data"`
}

func (e *cdpError) Error() string {
	if e.Data != "" {
		return e.Message + ": " + e.Data
	}
	return e.Message
}

type cdpMessage struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpError       `json:"error,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

// Event is one DevTools event, with the flattened session it belongs to.
type Event struct {
	Method    string
	Params    json.RawMessage
	SessionID string
}

// Conn is one DevTools WebSocket to a browser. Commands are correlated by id;
// events go to the handler of the page session they belong to, which must not
// block for long.
type Conn struct {
	ws       *websocket.Conn
	next     atomic.Int64
	mu       sync.Mutex
	pending  map[int64]chan cdpMessage
	handlers map[string]func(Event)
	closed   chan struct{}
	err      error
	once     sync.Once
}

// ErrClosed means the browser connection is gone.
var ErrClosed = errors.New("the browser has closed")

// connect dials the browser-level DevTools endpoint at 127.0.0.1:port+path.
func connect(ctx context.Context, dial Dial, port int, path string) (*Conn, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx, addr) },
	}}
	ws, _, err := websocket.Dial(ctx, "ws://"+addr+path, &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		return nil, fmt.Errorf("could not reach the browser's DevTools endpoint: %w", err)
	}
	// Full-page screenshots and large DOM snapshots arrive as one message.
	ws.SetReadLimit(64 << 20)
	c := &Conn{ws: ws, pending: map[int64]chan cdpMessage{}, handlers: map[string]func(Event){}, closed: make(chan struct{})}
	go c.read()
	return c, nil
}

func (c *Conn) read() {
	for {
		_, data, err := c.ws.Read(context.Background())
		if err != nil {
			c.shutdown(err)
			return
		}
		var msg cdpMessage
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		if msg.ID != 0 {
			c.mu.Lock()
			ch := c.pending[msg.ID]
			delete(c.pending, msg.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
			continue
		}
		if msg.Method != "" {
			c.mu.Lock()
			h := c.handlers[msg.SessionID]
			c.mu.Unlock()
			if h != nil {
				h(Event{Method: msg.Method, Params: msg.Params, SessionID: msg.SessionID})
			}
		}
	}
}

func (c *Conn) shutdown(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.mu.Unlock()
		close(c.closed)
		_ = c.ws.CloseNow()
	})
}

func (c *Conn) handle(session string, h func(Event)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h == nil {
		delete(c.handlers, session)
	} else {
		c.handlers[session] = h
	}
}

// Close ends the connection. The browser process is stopped separately.
func (c *Conn) Close() { c.shutdown(ErrClosed) }

// Done is closed when the connection ends.
func (c *Conn) Done() <-chan struct{} { return c.closed }

// Call sends one command and waits for its result. sessionID is the flattened
// page session, or "" for the browser itself.
func (c *Conn) Call(ctx context.Context, sessionID, method string, params any, result any) error {
	id := c.next.Add(1)
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if sessionID != "" {
		msg["sessionId"] = sessionID
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	ch := make(chan cdpMessage, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return ErrClosed
	}
	c.pending[id] = ch
	c.mu.Unlock()
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	if err := c.ws.Write(ctx, websocket.MessageText, raw); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("%s: %w", method, err)
	}
	select {
	case reply, ok := <-ch:
		if !ok {
			return ErrClosed
		}
		if reply.Error != nil {
			return fmt.Errorf("%s: %w", method, reply.Error)
		}
		if result != nil && len(reply.Result) > 0 {
			return json.Unmarshal(reply.Result, result)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("%s: %w", method, ctx.Err())
	}
}
