// Package lsp is Scribe's Language Server Protocol client: a JSON-RPC 2.0
// connection over stdio with Content-Length framing, a catalog of known
// servers, and a Manager that runs one server per (language, project root).
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrServerExited is returned for requests that were pending when the server
// went away, and for requests made after that.
var ErrServerExited = errors.New("language server exited")

const maxFrameBytes = 256 << 20

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("server error %d: %s", e.Code, e.Message) }

// incoming is any message read from the server.
type incoming struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type outRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type outNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type outResult struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result"`
}

type outError struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   *rpcError       `json:"error"`
}

// Client is one JSON-RPC connection. It is safe for concurrent use.
type Client struct {
	w   io.WriteCloser
	wmu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan incoming
	exitErr error

	dmu     sync.Mutex
	diags   map[string][]Diagnostic
	diagSeq map[string]uint64
	diagCh  chan struct{} // closed and replaced on every publishDiagnostics

	// done is closed when the read loop ends (the server closed stdout,
	// died, or sent a frame we could not read).
	done chan struct{}
}

// NewClient starts reading from r and writing to w. The caller owns the
// underlying process or pipes.
func NewClient(r io.Reader, w io.WriteCloser) *Client {
	c := &Client{
		w:       w,
		pending: map[int64]chan incoming{},
		diags:   map[string][]Diagnostic{},
		diagSeq: map[string]uint64{},
		diagCh:  make(chan struct{}),
		done:    make(chan struct{}),
	}
	go c.readLoop(bufio.NewReaderSize(r, 1<<16))
	return c
}

// Done is closed once the connection is gone.
func (c *Client) Done() <-chan struct{} { return c.done }

func (c *Client) readLoop(r *bufio.Reader) {
	var err error
	for {
		var body []byte
		body, err = readFrame(r)
		if err != nil {
			break
		}
		var msg incoming
		if json.Unmarshal(body, &msg) != nil {
			continue
		}
		c.dispatch(msg)
	}
	c.mu.Lock()
	c.exitErr = fmt.Errorf("%w (%v)", ErrServerExited, err)
	close(c.done)
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
}

func (c *Client) dispatch(msg incoming) {
	hasID := len(msg.ID) > 0 && string(msg.ID) != "null"
	switch {
	case msg.Method != "" && hasID:
		// Answer off the read loop: a write can block while the server
		// is itself blocked writing to us.
		go c.answer(msg)
	case msg.Method != "":
		c.notified(msg)
	case hasID:
		var id int64
		if json.Unmarshal(msg.ID, &id) != nil {
			return
		}
		c.mu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ch != nil {
			ch <- msg // buffered, and this is the only send for id
		}
	}
}

// answer replies to a request the server made of us.
func (c *Client) answer(msg incoming) {
	switch msg.Method {
	case "workspace/configuration":
		var p struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		_ = c.send(outResult{JSONRPC: "2.0", ID: msg.ID, Result: make([]any, len(p.Items))})
	case "window/workDoneProgress/create", "client/registerCapability", "client/unregisterCapability":
		_ = c.send(outResult{JSONRPC: "2.0", ID: msg.ID, Result: nil})
	default:
		_ = c.send(outError{JSONRPC: "2.0", ID: msg.ID, Error: &rpcError{
			Code: -32601, Message: "method not supported by client: " + msg.Method,
		}})
	}
}

func (c *Client) notified(msg incoming) {
	if msg.Method != "textDocument/publishDiagnostics" {
		return
	}
	var p struct {
		URI         string       `json:"uri"`
		Diagnostics []Diagnostic `json:"diagnostics"`
	}
	if json.Unmarshal(msg.Params, &p) != nil || p.URI == "" {
		return
	}
	c.dmu.Lock()
	c.diags[p.URI] = p.Diagnostics
	c.diagSeq[p.URI]++
	close(c.diagCh)
	c.diagCh = make(chan struct{})
	c.dmu.Unlock()
}

// Call sends a request and decodes its result into result (which may be nil).
// If ctx ends first, a $/cancelRequest is sent and ctx.Err() is returned.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	if c.exitErr != nil {
		err := c.exitErr
		c.mu.Unlock()
		return err
	}
	c.nextID++
	id := c.nextID
	ch := make(chan incoming, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.send(outRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		c.drop(id)
		return err
	}
	select {
	case msg, ok := <-ch:
		if !ok {
			c.mu.Lock()
			err := c.exitErr
			c.mu.Unlock()
			return err
		}
		if msg.Error != nil {
			return fmt.Errorf("%s: %w", method, msg.Error)
		}
		if result != nil && len(msg.Result) > 0 {
			if err := json.Unmarshal(msg.Result, result); err != nil {
				return fmt.Errorf("%s: decoding result: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.drop(id)
		_ = c.Notify("$/cancelRequest", map[string]any{"id": id})
		return ctx.Err()
	}
}

func (c *Client) drop(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// Notify sends a notification. params may be nil.
func (c *Client) Notify(method string, params any) error {
	return c.send(outNotification{JSONRPC: "2.0", Method: method, Params: params})
}

// Close closes the write side, which tells a well-behaved server to exit.
func (c *Client) Close() error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.w.Close()
}

func (c *Client) send(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	frame := make([]byte, 0, len(body)+32)
	frame = append(frame, "Content-Length: "...)
	frame = strconv.AppendInt(frame, int64(len(body)), 10)
	frame = append(frame, "\r\n\r\n"...)
	frame = append(frame, body...)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.w.Write(frame)
	return err
}

// diagState returns the publish count for uri and a channel that is closed
// on the next publishDiagnostics for any URI.
func (c *Client) diagState(uri string) (uint64, <-chan struct{}) {
	c.dmu.Lock()
	defer c.dmu.Unlock()
	return c.diagSeq[uri], c.diagCh
}

// Diagnostics returns the last diagnostics the server published for uri.
func (c *Client) Diagnostics(uri string) []Diagnostic {
	c.dmu.Lock()
	defer c.dmu.Unlock()
	return append([]Diagnostic(nil), c.diags[uri]...)
}

// waitSettled waits for a publish for uri newer than after, then for quiet
// time with no further publish for uri. It gives up after max or when ctx or
// the connection ends.
func (c *Client) waitSettled(ctx context.Context, uri string, after uint64, quiet, max time.Duration) {
	deadline := time.NewTimer(max)
	defer deadline.Stop()
	var quietT *time.Timer
	var quietC <-chan time.Time
	defer func() {
		if quietT != nil {
			quietT.Stop()
		}
	}()
	seen := after
	for {
		seq, ch := c.diagState(uri)
		if seq > seen {
			seen = seq
			if quietT != nil {
				quietT.Stop()
			}
			quietT = time.NewTimer(quiet)
			quietC = quietT.C
		}
		select {
		case <-ch:
		case <-quietC:
			return
		case <-deadline.C:
			return
		case <-ctx.Done():
			return
		case <-c.done:
			return
		}
	}
}

// readFrame reads one Content-Length framed message body.
func readFrame(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && line != "" {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("malformed header %q", line)
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || n < 0 || n > maxFrameBytes {
				return nil, fmt.Errorf("bad Content-Length %q", value)
			}
			length = n
		}
	}
	if length < 0 {
		return nil, errors.New("frame has no Content-Length header")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}
