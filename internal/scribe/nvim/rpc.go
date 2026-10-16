package nvim

// A msgpack-RPC client for Neovim (see :help msgpack-rpc). Requests are
// [0, msgid, method, args], responses [1, msgid, error, result] and
// notifications [2, method, args]. The client only issues requests and
// listens for notifications; Neovim's requests to a client are refused.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// ErrClosed is returned by calls on a client that has been closed or whose
// connection has ended.
var ErrClosed = errors.New("neovim connection closed")

// Notify handles one notification from Neovim. It runs on the read loop,
// so it must not block for long and must not call Client.Call.
type Notify func(method string, args []any)

type response struct {
	result any
	err    error
}

// Client is a connection to a Neovim RPC socket. Methods are safe for
// concurrent use.
type Client struct {
	conn   io.ReadWriteCloser
	nextID atomic.Uint32

	wmu sync.Mutex // serialises writes

	mu      sync.Mutex
	pending map[uint32]chan response
	closed  bool
	notify  Notify

	done chan struct{} // closed when the read loop ends
}

// NewClient runs the RPC protocol over conn. notify may be nil.
func NewClient(conn io.ReadWriteCloser, notify Notify) *Client {
	c := &Client{
		conn:    conn,
		pending: map[uint32]chan response{},
		notify:  notify,
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// Dial connects to a Neovim listening on the socket path.
func Dial(path string, notify Notify) (*Client, error) {
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return nil, err
	}
	return NewClient(conn, notify), nil
}

// Close ends the connection. Pending calls fail with ErrClosed.
func (c *Client) Close() error {
	err := c.conn.Close()
	<-c.done
	return err
}

// Done is closed when the connection has ended.
func (c *Client) Done() <-chan struct{} { return c.done }

func (c *Client) readLoop() {
	defer close(c.done)
	defer c.failAll()
	r := &decoder{r: bufio.NewReader(c.conn)}
	for {
		v, err := r.decode(0)
		if err != nil {
			return
		}
		msg, ok := v.([]any)
		if !ok || len(msg) < 3 {
			continue
		}
		kind, _ := msg[0].(int64)
		switch kind {
		case 1: // response
			if len(msg) != 4 {
				continue
			}
			id, _ := msg[1].(int64)
			c.deliver(uint32(id), response{result: msg[3], err: rpcError(msg[2])})
		case 2: // notification
			method, _ := msg[1].(string)
			args, _ := msg[2].([]any)
			if c.notify != nil {
				c.notify(method, args)
			}
		}
	}
}

// rpcError turns Neovim's error value, [type, message], into an error.
func rpcError(v any) error {
	if v == nil {
		return nil
	}
	if arr, ok := v.([]any); ok && len(arr) == 2 {
		if s, ok := arr[1].(string); ok {
			return errors.New(s)
		}
	}
	return fmt.Errorf("neovim error: %v", v)
}

func (c *Client) deliver(id uint32, r response) {
	c.mu.Lock()
	ch, ok := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ok {
		ch <- r
	}
}

func (c *Client) failAll() {
	c.mu.Lock()
	c.closed = true
	for id, ch := range c.pending {
		ch <- response{err: ErrClosed}
		delete(c.pending, id)
	}
	c.mu.Unlock()
}

// Call invokes method with args and returns its result. The context bounds
// the wait; a call that times out may still run in Neovim.
func (c *Client) Call(ctx context.Context, method string, args ...any) (any, error) {
	if args == nil {
		args = []any{}
	}
	id := c.nextID.Add(1)
	ch := make(chan response, 1)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	c.pending[id] = ch
	c.mu.Unlock()

	data, err := Pack([]any{int64(0), int64(id), method, args})
	if err == nil {
		c.wmu.Lock()
		_, err = c.conn.Write(data)
		c.wmu.Unlock()
	}
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	select {
	case r := <-ch:
		return r.result, r.err
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("%s: %w", method, ctx.Err())
	}
}

// Send sends a notification with no reply.
func (c *Client) Send(method string, args ...any) error {
	if args == nil {
		args = []any{}
	}
	data, err := Pack([]any{int64(2), method, args})
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.conn.Write(data)
	return err
}
