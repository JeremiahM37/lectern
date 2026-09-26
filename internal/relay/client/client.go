// Package client is a Go implementation of the phone's side of the relay
// (the browser's is frontend/src/relay). Lectern's tests drive the real relay
// and host with it; it is small enough to back a command-line client later.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/flynn/noise"

	"github.com/JeremiahM37/lectern/v2/internal/relay"
)

// Client is one authenticated tunnel to a Lectern host.
type Client struct {
	c       *websocket.Conn
	sess    *relay.Session
	Welcome relay.Welcome

	sendMu sync.Mutex
	mu     sync.Mutex
	next   uint32
	subs   map[uint32]chan relay.Frame
	err    error
	done   chan struct{}
}

// Options describe where and who to connect as.
type Options struct {
	RelayURL   string // ws:// or wss:// base
	Channel    string
	RouteToken string
	HostKey    []byte      // pinned X25519 public key
	Device     noise.DHKey // this device's static key
	Pair       *relay.PairRequest
}

// Dial connects, authenticates to the relay and runs the Noise handshake.
// A refused handshake returns the Welcome with its error.
func Dial(ctx context.Context, o Options) (*Client, error) {
	u := strings.TrimRight(o.RelayURL, "/") + "/v1/device?ch=" + o.Channel
	c, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(relay.MaxWireMessage)
	fail := func(err error) (*Client, error) { c.CloseNow(); return nil, err }
	auth, _ := json.Marshal(relay.Control{T: "auth", Token: o.RouteToken})
	if err := c.Write(ctx, websocket.MessageText, auth); err != nil {
		return fail(err)
	}
	typ, b, err := c.Read(ctx)
	if err != nil {
		return fail(fmt.Errorf("relay: %w", err))
	}
	var ok relay.Control
	if typ != websocket.MessageText || json.Unmarshal(b, &ok) != nil || ok.T != "ok" {
		return fail(fmt.Errorf("relay refused the device: %s", ok.Error))
	}
	ini, err := relay.NewInitiator(o.Device, o.HostKey, o.Channel)
	if err != nil {
		return fail(err)
	}
	hello, _ := json.Marshal(relay.Hello{V: relay.ProtocolVersion, Pair: o.Pair})
	m1, err := ini.Hello(hello)
	if err != nil {
		return fail(err)
	}
	if err := c.Write(ctx, websocket.MessageBinary, m1); err != nil {
		return fail(err)
	}
	typ, m2, err := c.Read(ctx)
	if err != nil || typ != websocket.MessageBinary {
		return fail(fmt.Errorf("no handshake reply: %v", err))
	}
	payload, sess, err := ini.Finish(m2)
	if err != nil {
		return fail(err)
	}
	cl := &Client{c: c, sess: sess, subs: map[uint32]chan relay.Frame{}, done: make(chan struct{})}
	if err := json.Unmarshal(payload, &cl.Welcome); err != nil {
		return fail(err)
	}
	if !cl.Welcome.OK {
		c.CloseNow()
		return cl, fmt.Errorf("host refused the device: %s", cl.Welcome.Error)
	}
	go cl.readLoop()
	return cl, nil
}

// Close ends the tunnel.
func (cl *Client) Close() { cl.c.CloseNow() }

// Done is closed when the tunnel ends; Err then says why.
func (cl *Client) Done() <-chan struct{} { return cl.done }

// Err is the reason the tunnel ended.
func (cl *Client) Err() error {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.err
}

func (cl *Client) readLoop() {
	defer close(cl.done)
	for {
		_, b, err := cl.c.Read(context.Background())
		if err == nil {
			var plain []byte
			if plain, err = cl.sess.Open(b); err == nil {
				var f relay.Frame
				if f, err = relay.DecodeFrame(plain); err == nil {
					cl.mu.Lock()
					ch := cl.subs[f.Stream]
					cl.mu.Unlock()
					if ch != nil {
						ch <- f
					}
					continue
				}
			}
		}
		cl.mu.Lock()
		cl.err = err
		for id, ch := range cl.subs {
			close(ch)
			delete(cl.subs, id)
		}
		cl.mu.Unlock()
		return
	}
}

// Send seals and writes one frame.
func (cl *Client) Send(t byte, stream uint32, payload []byte) error {
	plain, err := relay.EncodeFrame(relay.Frame{Type: t, Stream: stream, Payload: payload})
	if err != nil {
		return err
	}
	cl.sendMu.Lock()
	defer cl.sendMu.Unlock()
	ct, err := cl.sess.Seal(plain)
	if err != nil {
		return err
	}
	return cl.c.Write(context.Background(), websocket.MessageBinary, ct)
}

// WriteRaw sends bytes exactly as given, bypassing encryption: tests use it
// to replay or tamper with frames.
func (cl *Client) WriteRaw(b []byte) error {
	return cl.c.Write(context.Background(), websocket.MessageBinary, b)
}

// Seal encrypts a frame without sending it (for replay tests).
func (cl *Client) Seal(t byte, stream uint32, payload []byte) ([]byte, error) {
	plain, err := relay.EncodeFrame(relay.Frame{Type: t, Stream: stream, Payload: payload})
	if err != nil {
		return nil, err
	}
	cl.sendMu.Lock()
	defer cl.sendMu.Unlock()
	return cl.sess.Seal(plain)
}

// Open starts a new stream and returns its id and frame channel.
func (cl *Client) Open() (uint32, chan relay.Frame) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	cl.next++
	ch := make(chan relay.Frame, 1024)
	cl.subs[cl.next] = ch
	return cl.next, ch
}

// Response is a fully read tunnelled response.
type Response struct {
	Status int
	Header relay.Header
	Body   []byte
}

// Do performs one HTTP request over the tunnel and reads the whole response.
func (cl *Client) Do(ctx context.Context, method, url string, body []byte, header ...[2]string) (*Response, error) {
	id, ch := cl.Open()
	head, _ := json.Marshal(relay.RequestHead{Method: method, URL: url, Host: "phone.test", Header: header})
	if err := cl.Send(relay.FrameRequest, id, head); err != nil {
		return nil, err
	}
	for len(body) > 0 {
		n := min(len(body), relay.MaxChunk)
		if err := cl.Send(relay.FrameRequestBody, id, body[:n]); err != nil {
			return nil, err
		}
		body = body[n:]
	}
	if err := cl.Send(relay.FrameRequestEnd, id, nil); err != nil {
		return nil, err
	}
	resp := &Response{}
	for {
		select {
		case <-ctx.Done():
			_ = cl.Send(relay.FrameCancel, id, nil)
			return nil, ctx.Err()
		case f, ok := <-ch:
			if !ok {
				return nil, errors.New("tunnel closed")
			}
			switch f.Type {
			case relay.FrameResponse:
				var h relay.ResponseHead
				if err := json.Unmarshal(f.Payload, &h); err != nil {
					return nil, err
				}
				resp.Status, resp.Header = h.Status, h.Header
			case relay.FrameResponseBody:
				resp.Body = append(resp.Body, f.Payload...)
			case relay.FrameResponseEnd:
				return resp, nil
			case relay.FrameCancel:
				return nil, fmt.Errorf("stream cancelled: %s", f.Payload)
			}
		}
	}
}

// DoJSON is Do with a JSON body and a JSON content type.
func (cl *Client) DoJSON(ctx context.Context, method, url string, v any) (*Response, error) {
	var body []byte
	if v != nil {
		body, _ = json.Marshal(v)
	}
	return cl.Do(ctx, method, url, body, [2]string{"Content-Type", "application/json"})
}

// Wait is a small helper for tests polling over the tunnel.
func Wait(ctx context.Context, every time.Duration, cond func() bool) bool {
	for {
		if cond() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(every):
		}
	}
}
