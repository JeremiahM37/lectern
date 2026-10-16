// Package mcp is Scribe's Model Context Protocol client. It connects to
// MCP servers over stdio or Streamable HTTP, lists their tools and exposes
// them to the agent as tools.Tool values named mcp__<server>__<tool>.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// ProtocolVersion is the MCP revision Scribe speaks.
const ProtocolVersion = "2025-06-18"

var supportedVersions = []string{ProtocolVersion, "2025-03-26", "2024-11-05"}

var errClosed = errors.New("connection closed")

// rpcError is a JSON-RPC error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type response struct {
	result json.RawMessage
	err    error
}

// transport moves JSON-RPC messages. Incoming messages are handed to the
// Client with handle/deliverBody.
type transport interface {
	// send writes one outgoing message. expect is the id of the request
	// it carries ("" for notifications).
	send(ctx context.Context, msg []byte, expect string) error
	// open starts any optional server-to-client channel after initialize.
	open()
	close() error
}

// ToolInfo is one tool advertised by a server.
type ToolInfo struct {
	Name        string
	Description string
	Schema      json.RawMessage
	ReadOnly    bool // annotations.readOnlyHint
}

// Client is one connection to one MCP server.
type Client struct {
	server   string
	version  string
	onNotify func(method string)
	tr       transport

	nextID atomic.Int64

	mu       sync.Mutex
	pending  map[string]chan response
	closed   bool
	closeErr error
	done     chan struct{}
}

func newClient(server, version string, onNotify func(string)) *Client {
	return &Client{
		server:   server,
		version:  version,
		onNotify: onNotify,
		pending:  map[string]chan response{},
		done:     make(chan struct{}),
	}
}

// connect starts the transport, runs the initialize handshake and lists
// the tools. On failure the connection is closed and the error returned.
func connect(ctx context.Context, name string, spec config.MCPServer, version string, onNotify func(string)) (*Client, []ToolInfo, error) {
	c := newClient(name, version, onNotify)
	var err error
	switch {
	case spec.URL != "":
		c.tr = newHTTPTransport(c, expandString(spec.URL), expandMap(spec.Headers))
	case spec.Command != "":
		c.tr, err = newStdioTransport(c, spec)
	default:
		err = errors.New("server needs a command (stdio) or a url (http)")
	}
	if err != nil {
		return nil, nil, err
	}
	if err := c.initialize(ctx); err != nil {
		c.Close()
		return nil, nil, err
	}
	tools, err := c.listTools(ctx)
	if err != nil {
		c.Close()
		return nil, nil, err
	}
	c.tr.open()
	return c, tools, nil
}

func (c *Client) initialize(ctx context.Context) error {
	raw, err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "scribe", "version": c.version},
	})
	if err != nil {
		return err
	}
	var res struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("initialize: bad result: %w", err)
	}
	if !contains(supportedVersions, res.ProtocolVersion) {
		return fmt.Errorf("server speaks MCP %q; Scribe supports %v", res.ProtocolVersion, supportedVersions)
	}
	return c.notify(ctx, "notifications/initialized", nil)
}

// listTools follows nextCursor until the server has no more pages.
func (c *Client) listTools(ctx context.Context) ([]ToolInfo, error) {
	var out []ToolInfo
	cursor := ""
	for page := 0; page < 200; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.request(ctx, "tools/list", params)
		if err != nil {
			return nil, fmt.Errorf("tools/list: %w", err)
		}
		var res struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
				Annotations struct {
					ReadOnlyHint bool `json:"readOnlyHint"`
				} `json:"annotations"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("tools/list: bad result: %w", err)
		}
		for _, t := range res.Tools {
			out = append(out, ToolInfo{
				Name:        t.Name,
				Description: t.Description,
				Schema:      t.InputSchema,
				ReadOnly:    t.Annotations.ReadOnlyHint,
			})
		}
		if res.NextCursor == "" {
			return out, nil
		}
		if res.NextCursor == cursor {
			return nil, errors.New("tools/list: server returned the same cursor twice")
		}
		cursor = res.NextCursor
	}
	return nil, errors.New("tools/list: too many pages")
}

// callResult is the tools/call result.
type callResult struct {
	Content           []contentItem   `json:"content"`
	IsError           bool            `json:"isError"`
	StructuredContent json.RawMessage `json:"structuredContent"`
}

func (c *Client) callTool(ctx context.Context, name string, args json.RawMessage) (callResult, error) {
	if len(bytes.TrimSpace(args)) == 0 || string(bytes.TrimSpace(args)) == "null" {
		args = json.RawMessage(`{}`)
	}
	raw, err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return callResult{}, err
	}
	var res callResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return callResult{}, fmt.Errorf("tools/call: bad result: %w", err)
	}
	return res, nil
}

// request sends a JSON-RPC request and waits for its response.
func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	key := strconv.FormatInt(id, 10)
	ch := make(chan response, 1)
	c.mu.Lock()
	if c.closed {
		err := c.closeErr
		c.mu.Unlock()
		return nil, err
	}
	c.pending[key] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, key)
		c.mu.Unlock()
	}()

	msg, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	if err := c.tr.send(ctx, msg, key); err != nil {
		return nil, fmt.Errorf("%s: %w", method, err)
	}
	select {
	case r := <-ch:
		return r.result, r.err
	case <-ctx.Done():
		return nil, fmt.Errorf("%s: %w", method, ctx.Err())
	case <-c.done:
		c.mu.Lock()
		err := c.closeErr
		c.mu.Unlock()
		return nil, fmt.Errorf("%s: %w", method, err)
	}
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	m := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		m["params"] = params
	}
	msg, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return c.tr.send(ctx, msg, "")
}

// handle processes one incoming message. It returns the id of the response
// it delivered, or "" for anything else.
func (c *Client) handle(raw []byte) string {
	var m rpcMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return "" // a stray log line on stdout, for example
	}
	hasID := len(m.ID) > 0 && string(m.ID) != "null"
	switch {
	case m.Method != "" && hasID:
		// A request from the server. Scribe offers no client features, so
		// refuse politely instead of leaving the server waiting.
		reply, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      m.ID,
			"error":   map[string]any{"code": -32601, "message": "scribe does not support " + m.Method},
		})
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = c.tr.send(ctx, reply, "")
		}()
		return ""
	case m.Method != "":
		if c.onNotify != nil {
			c.onNotify(m.Method)
		}
		return ""
	case hasID:
		key := string(m.ID)
		c.mu.Lock()
		ch := c.pending[key]
		c.mu.Unlock()
		if ch != nil {
			r := response{result: m.Result}
			if m.Error != nil {
				r.err = m.Error
			}
			select {
			case ch <- r:
			default:
			}
		}
		return key
	}
	return ""
}

// deliverBody handles a message or a JSON batch. It reports whether the
// response to expect was among them.
func (c *Client) deliverBody(raw []byte, expect string) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return false
	}
	var msgs []json.RawMessage
	if raw[0] == '[' {
		if err := json.Unmarshal(raw, &msgs); err != nil {
			return false
		}
	} else {
		msgs = []json.RawMessage{raw}
	}
	found := false
	for _, m := range msgs {
		if c.handle(m) == expect && expect != "" {
			found = true
		}
	}
	return found
}

// fail marks the connection dead and wakes every waiting request. The
// first reason wins.
func (c *Client) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.closeErr = err
	close(c.done)
}

// Close shuts the connection down. It is safe to call more than once.
func (c *Client) Close() error {
	c.fail(errClosed)
	if c.tr != nil {
		return c.tr.close()
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
