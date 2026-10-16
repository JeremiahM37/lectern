package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"
)

// peer is the server end of an in-process connection. It reads what the
// client writes from in and writes frames to out.
type peer struct {
	t   *testing.T
	r   *bufio.Reader
	out io.Writer
}

func newPeer(t *testing.T, in io.Reader, out io.Writer) *peer {
	return &peer{t: t, r: bufio.NewReader(in), out: out}
}

func (p *peer) read() map[string]json.RawMessage {
	p.t.Helper()
	body, err := readFrame(p.r)
	if err != nil {
		p.t.Fatalf("peer read: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		p.t.Fatalf("peer decode %q: %v", body, err)
	}
	return m
}

func (p *peer) write(v any) {
	p.t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		p.t.Fatal(err)
	}
	if _, err := io.WriteString(p.out, "Content-Length: "+strconv.Itoa(len(body))+"\r\n\r\n"); err != nil {
		p.t.Fatal(err)
	}
	if _, err := p.out.Write(body); err != nil {
		p.t.Fatal(err)
	}
}

// pipes returns a client wired to a peer through io.Pipe, and the peer's ends.
func pipes(t *testing.T) (*Client, *peer, func()) {
	t.Helper()
	clientIn, serverOut := io.Pipe() // server writes serverOut, client reads clientIn
	serverIn, clientOut := io.Pipe() // client writes clientOut, server reads serverIn
	c := NewClient(clientIn, clientOut)
	p := newPeer(t, serverIn, serverOut)
	return c, p, func() {
		_ = clientOut.Close()
		_ = serverOut.Close()
	}
}

func TestReadFrameVariants(t *testing.T) {
	// Lower-case header name, an extra header, and two frames back to back.
	stream := "content-length: 5\r\nX-Other: y\r\n\r\nhello" +
		"Content-Length: 7\r\n\r\n{\"a\":1}"
	r := bufio.NewReader(strings.NewReader(stream))
	got, err := readFrame(r)
	if err != nil || string(got) != "hello" {
		t.Fatalf("first frame: %q, %v", got, err)
	}
	got, err = readFrame(r)
	if err != nil || string(got) != `{"a":1}` {
		t.Fatalf("second frame: %q, %v", got, err)
	}
	if _, err := readFrame(r); !errors.Is(err, io.EOF) {
		t.Fatalf("end of stream: want io.EOF, got %v", err)
	}
}

func TestReadFrameErrors(t *testing.T) {
	cases := map[string]string{
		"no length":  "X-Foo: 1\r\n\r\nbody",
		"bad length": "Content-Length: nope\r\n\r\nbody",
		"truncated":  "Content-Length: 10\r\n\r\nshort",
		"malformed":  "not a header\r\n\r\n",
	}
	for name, in := range cases {
		if _, err := readFrame(bufio.NewReader(strings.NewReader(in))); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestCallWithServerRequestsAndDiagnostics(t *testing.T) {
	c, p, done := pipes(t)
	defer done()
	const uri = "file:///tmp/x/a.go"
	errCh := make(chan error, 1)

	go func() {
		req := p.read()
		id := req["id"]
		if string(req["method"]) != `"ping"` {
			errCh <- errors.New("expected ping, got " + string(req["method"]))
			return
		}
		// The server asks us for configuration, then publishes diagnostics,
		// then answers the ping.
		p.write(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "workspace/configuration",
			"params": map[string]any{"items": []any{map[string]any{"section": "a"}, map[string]any{"section": "b"}}}})
		p.write(map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics",
			"params": map[string]any{"uri": uri, "diagnostics": []any{map[string]any{
				"range":    map[string]any{"start": map[string]any{"line": 2, "character": 3}, "end": map[string]any{"line": 2, "character": 5}},
				"severity": 1, "message": "boom 😀",
			}}}})
		p.write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": map[string]any{"ok": "héllo 😀"}})

		// The reply to the configuration request must be an array of nulls.
		reply := p.read()
		if string(reply["id"]) != "7" {
			errCh <- errors.New("reply id mismatch: " + string(reply["id"]))
			return
		}
		if string(reply["result"]) != "[null,null]" {
			errCh <- errors.New("configuration reply: " + string(reply["result"]))
			return
		}
		errCh <- nil
	}()

	var out struct {
		OK string `json:"ok"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Call(ctx, "ping", map[string]any{"x": 1}, &out); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.OK != "héllo 😀" {
		t.Fatalf("result = %q", out.OK)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	c.waitSettled(ctx, uri, 0, 50*time.Millisecond, 2*time.Second)
	diags := c.Diagnostics(uri)
	if len(diags) != 1 || diags[0].Message != "boom 😀" || diags[0].Range.Start.Line != 2 {
		t.Fatalf("diagnostics = %+v", diags)
	}
}

func TestCallCancellationSendsCancelRequest(t *testing.T) {
	c, p, done := pipes(t)
	defer done()
	cancelled := make(chan string, 1)
	go func() {
		req := p.read() // never answered
		id := string(req["id"])
		note := p.read()
		if string(note["method"]) != `"$/cancelRequest"` {
			cancelled <- "unexpected " + string(note["method"])
			return
		}
		cancelled <- id + " " + string(note["params"])
	}()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Call(ctx, "slow", nil, nil) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("Call returned %v, want context.Canceled", err)
	}
	got := <-cancelled
	if got != `1 {"id":1}` {
		t.Fatalf("cancel notification: %q", got)
	}
}

func TestPendingCallsFailWhenServerExits(t *testing.T) {
	c, p, done := pipes(t)
	defer done()
	go func() {
		p.read()
		// Close the server's output: the client sees EOF.
		if closer, ok := p.out.(io.Closer); ok {
			_ = closer.Close()
		}
	}()
	err := c.Call(context.Background(), "hang", nil, nil)
	if !errors.Is(err, ErrServerExited) {
		t.Fatalf("Call error = %v, want ErrServerExited", err)
	}
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("Done not closed after exit")
	}
	if err := c.Call(context.Background(), "after", nil, nil); !errors.Is(err, ErrServerExited) {
		t.Fatalf("Call after exit = %v", err)
	}
}
