package mcp

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const maxBody = 16 << 20

// httpTransport is the Streamable HTTP transport: every message is a POST
// to one URL, answered with JSON or an SSE stream. An optional GET opens a
// server-to-client SSE stream for notifications.
type httpTransport struct {
	c       *Client
	url     string
	headers map[string]string
	hc      *http.Client

	mu      sync.Mutex
	session string

	streamCtx    context.Context
	streamCancel context.CancelFunc
	closeOnce    sync.Once
}

func newHTTPTransport(c *Client, url string, headers map[string]string) *httpTransport {
	ctx, cancel := context.WithCancel(context.Background())
	// No overall client timeout: the caller's context bounds each request,
	// and the GET stream lives as long as the connection.
	return &httpTransport{c: c, url: url, headers: headers, hc: &http.Client{},
		streamCtx: ctx, streamCancel: cancel}
}

func (t *httpTransport) setHeaders(req *http.Request, accept string) {
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	t.mu.Lock()
	if t.session != "" {
		req.Header.Set("Mcp-Session-Id", t.session)
	}
	t.mu.Unlock()
}

func (t *httpTransport) send(ctx context.Context, msg []byte, expect string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(msg))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	t.setHeaders(req, "application/json, text/event-stream")
	resp, err := t.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		t.session = sid
		t.mu.Unlock()
	}
	if resp.StatusCode == http.StatusAccepted {
		return nil // notifications and responses are acknowledged with no body
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, t.url, strings.TrimSpace(string(snippet)))
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		err := readSSE(resp.Body, func(data []byte) bool { return t.c.deliverBody(data, expect) })
		if err != nil && err != io.EOF {
			return err
		}
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return err
	}
	t.c.deliverBody(body, expect)
	return nil
}

// open starts the GET stream. A server that does not offer one (405)
// simply gets no server-to-client channel.
func (t *httpTransport) open() {
	go func() {
		req, err := http.NewRequestWithContext(t.streamCtx, http.MethodGet, t.url, nil)
		if err != nil {
			return
		}
		t.setHeaders(req, "text/event-stream")
		resp, err := t.hc.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			return
		}
		_ = readSSE(resp.Body, func(data []byte) bool {
			t.c.deliverBody(data, "")
			return false
		})
	}()
}

func (t *httpTransport) close() error {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		sid := t.session
		t.mu.Unlock()
		if sid != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.url, nil)
			if err == nil {
				t.setHeaders(req, "application/json")
				if resp, err := t.hc.Do(req); err == nil {
					resp.Body.Close()
				}
			}
		}
		t.streamCancel()
	})
	return nil
}

// readSSE reads server-sent events and calls fn with the data of each one.
// fn returns true to stop reading early.
func readSSE(r io.Reader, fn func(data []byte) bool) error {
	br := bufio.NewReader(r)
	var data []byte
	has := false
	flush := func() bool {
		if !has {
			return false
		}
		stop := fn(data)
		data = nil
		has = false
		return stop
	}
	for {
		line, err := br.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if flush() {
				return nil
			}
		case strings.HasPrefix(line, ":"):
			// comment or keep-alive
		case strings.HasPrefix(line, "data:"):
			v := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			if has {
				data = append(data, '\n')
			}
			data = append(data, v...)
			has = true
		}
		if err != nil {
			flush()
			return err
		}
	}
}
