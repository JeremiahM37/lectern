package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/relay"
)

const (
	// handshakeTimeout bounds how long a connected device may sit without
	// sending Noise message 1.
	handshakeTimeout = 20 * time.Second
	// maxStreams caps concurrent requests and WebSockets per device.
	maxStreams = 128
	// maxRequestBody caps one tunnelled request body (attachments included).
	maxRequestBody = 64 << 20
	// maxWSMessage caps one reassembled WebSocket message either way.
	maxWSMessage = 4 << 20
	// inboxDepth is how many undelivered frames a device may have queued at
	// the host before it is disconnected.
	inboxDepth = 1024
	// touchEvery limits last-seen writes to one per device per interval.
	touchEvery = time.Minute
)

// hopHeaders are connection-level headers that must not cross the tunnel in
// either direction.
var hopHeaders = map[string]bool{
	"connection": true, "keep-alive": true, "proxy-connection": true, "transfer-encoding": true,
	"te": true, "trailer": true, "upgrade": true, "host": true, "content-length": true,
	// The tunnel identity is the device; browser credentials mean nothing
	// here, and the transport must not negotiate compression on the page's
	// behalf (the page could not undo it).
	"cookie": true, "accept-encoding": true,
	"sec-websocket-key": true, "sec-websocket-version": true, "sec-websocket-extensions": true,
	"sec-websocket-protocol": true, "sec-websocket-accept": true,
}

// session is one phone connection arriving through the relay.
type session struct {
	h         *Host
	conn      uint32
	routeHash string

	ctx    context.Context
	cancel context.CancelFunc
	inbox  chan []byte
	once   sync.Once

	sendMu sync.Mutex
	noise  *relay.Session

	device    atomic.Pointer[Device]
	lastTouch atomic.Int64
	client    *http.Client

	mu      sync.Mutex
	streams map[uint32]*stream
}

func newSession(h *Host, conn uint32, routeHash string) *session {
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{h: h, conn: conn, routeHash: routeHash, ctx: ctx, cancel: cancel,
		inbox: make(chan []byte, inboxDepth), streams: map[uint32]*stream{}}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := s.device.Load()
			if d == nil {
				return nil, errors.New("relay: device not authenticated")
			}
			return h.ln.dial(ctx, d.ID, s.resolve)
		},
		// One in-memory connection per request: nothing is pooled, so no
		// connection (and so no identity) is ever shared between devices.
		DisableKeepAlives:      true,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 1 << 20,
	}
	s.client = &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return s
}

func (s *session) deviceID() int64 {
	if d := s.device.Load(); d != nil {
		return d.ID
	}
	return 0
}

// resolve is the per-request identity check the auth middleware calls: the
// device must still be paired right now.
func (s *session) resolve() (auth.Principal, bool) {
	d := s.device.Load()
	if d == nil {
		return auth.Principal{}, false
	}
	cur, err := s.h.cfg.Store.ActiveByID(d.ID)
	if err != nil || cur.publicKey != d.publicKey {
		go s.close(true)
		return auth.Principal{}, false
	}
	if now := time.Now().Unix(); now-s.lastTouch.Load() >= int64(touchEvery.Seconds()) {
		s.lastTouch.Store(now)
		s.h.cfg.Store.Touch(d.ID)
	}
	return cur.Principal(), true
}

func (s *session) deliver(b []byte) {
	select {
	case s.inbox <- b:
	case <-s.ctx.Done():
	default:
		s.h.cfg.Log.Warn("relay: device flooded the host; disconnecting", "conn", s.conn)
		go s.close(true)
	}
}

// close ends the session. notify tells the relay to drop the device's
// socket; it is false when the relay itself reported the close.
func (s *session) close(notify bool) {
	s.once.Do(func() {
		s.cancel()
		s.mu.Lock()
		streams := s.streams
		s.streams = map[uint32]*stream{}
		s.mu.Unlock()
		for _, st := range streams {
			st.abort()
		}
		if notify {
			_ = s.h.write(relay.EncodeWire(relay.WireClose, s.conn, nil))
		}
		s.h.removeSession(s)
	})
}

func (s *session) run() {
	defer s.close(true)
	var first []byte
	select {
	case first = <-s.inbox:
	case <-s.ctx.Done():
		return
	case <-time.After(handshakeTimeout):
		return
	}
	if !s.handshake(first) {
		return
	}
	for {
		select {
		case <-s.ctx.Done():
			return
		case b := <-s.inbox:
			plain, err := s.noise.Open(b)
			if err != nil {
				s.h.cfg.Log.Warn("relay: dropping a device whose frame failed authentication", "device", s.deviceID())
				return
			}
			f, err := relay.DecodeFrame(plain)
			if err != nil || !relay.FromDevice(f.Type) {
				return
			}
			if !s.dispatch(f) {
				return
			}
		}
	}
}

// handshake runs the Noise responder. It returns true when the device is
// admitted and the transport session is live.
func (s *session) handshake(msg []byte) bool {
	id, err := s.h.cfg.Store.Identity()
	if err != nil {
		return false
	}
	resp, err := relay.Accept(id.Noise, id.Channel(), msg)
	if err != nil {
		// Not encrypted to our key (or tampered): there is nobody to
		// answer.
		return false
	}
	var hello relay.Hello
	welcome := relay.Welcome{}
	var dev Device
	switch {
	case json.Unmarshal(resp.Payload(), &hello) != nil || hello.V != relay.ProtocolVersion:
		welcome.Error = "version"
	case hello.Pair != nil:
		d, token, replaced, err := s.h.cfg.Store.Pair(hello.Pair.Code, resp.PeerStatic(), hello.Pair.Name)
		if err != nil {
			welcome.Error = "invalid_code"
			break
		}
		if replaced != "" {
			_ = s.h.control(relay.Control{T: "route_del", Hash: replaced})
		}
		if err := s.h.control(relay.Control{T: "route_add", Hash: relay.TokenHash(token)}); err != nil {
			welcome.Error = "relay"
			break
		}
		dev = d
		welcome = relay.Welcome{OK: true, DeviceID: d.ID, Name: d.Name, RouteToken: token}
		s.h.cfg.Log.Info("relay: paired a device", "device", d.ID, "name", d.Name, "fingerprint", d.Fingerprint)
	default:
		d, err := s.h.cfg.Store.Active(resp.PeerStatic())
		// A device must use its own route token: the relay already checked
		// that the token is registered, and this ties it to the key.
		if err != nil || d.routeHash != s.routeHash {
			welcome.Error = "unknown_device"
			break
		}
		dev = d
		welcome = relay.Welcome{OK: true, DeviceID: d.ID, Name: d.Name}
	}
	payload, _ := json.Marshal(welcome)
	reply, sess, err := resp.Reply(payload)
	if err != nil {
		return false
	}
	// On a refusal the caller then closes the connection; the relay delivers
	// this reply first, so the phone can say why.
	if err := s.h.write(relay.EncodeWire(relay.WireData, s.conn, reply)); err != nil || !welcome.OK {
		return false
	}
	s.noise = sess
	s.device.Store(&dev)
	s.lastTouch.Store(time.Now().Unix())
	s.h.cfg.Store.Touch(dev.ID)
	return true
}

// send encrypts and forwards one frame. Sealing and writing happen under one
// lock: Noise nonces are implicit, so frames must reach the wire in exactly
// the order they were sealed.
func (s *session) send(t byte, stream uint32, payload []byte) error {
	plain, err := relay.EncodeFrame(relay.Frame{Type: t, Stream: stream, Payload: payload})
	if err != nil {
		return err
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.ctx.Err() != nil {
		return relay.ErrClosed
	}
	ct, err := s.noise.Seal(plain)
	if err != nil {
		return err
	}
	if err := s.h.write(relay.EncodeWire(relay.WireData, s.conn, ct)); err != nil {
		go s.close(false)
		return err
	}
	return nil
}

func (s *session) sendJSON(t byte, stream uint32, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.send(t, stream, b)
}

type stream struct {
	id     uint32
	ws     bool
	ctx    context.Context
	cancel context.CancelFunc

	head    relay.RequestHead
	body    bytes.Buffer
	started bool

	// WebSocket streams: messages from the device, reassembled, queued for
	// the writer goroutine.
	out     chan wsMessage
	partial []byte
	partTyp byte
}

type wsMessage struct {
	binary bool
	data   []byte
	close  *relay.WSClose
}

func (st *stream) abort() { st.cancel() }

func (s *session) stream(id uint32) *stream {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streams[id]
}

func (s *session) addStream(st *stream) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.streams[st.id]; dup || len(s.streams) >= maxStreams {
		return false
	}
	s.streams[st.id] = st
	return true
}

func (s *session) endStream(id uint32) {
	s.mu.Lock()
	st := s.streams[id]
	delete(s.streams, id)
	s.mu.Unlock()
	if st != nil {
		st.cancel()
	}
}

// validPath accepts only an origin-relative path: the tunnel serves this
// Lectern and nothing else, so it must never become a proxy to other hosts.
func validPath(u string) bool {
	return strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//") && !strings.ContainsAny(u, " \r\n\\") && len(u) < 8192
}

// validHost accepts a bare host[:port] for the Host header.
func validHost(h string) bool {
	return h != "" && len(h) < 256 && !strings.ContainsAny(h, "/@?# \r\n\\")
}

// dispatch handles one decrypted frame. It returns false on a protocol
// violation, which ends the session.
func (s *session) dispatch(f relay.Frame) bool {
	switch f.Type {
	case relay.FrameRequest:
		var head relay.RequestHead
		if json.Unmarshal(f.Payload, &head) != nil || !validPath(head.URL) || head.Method == "" || strings.ContainsAny(head.Method, " \r\n") {
			return false
		}
		ctx, cancel := context.WithCancel(s.ctx)
		st := &stream{id: f.Stream, ctx: ctx, cancel: cancel, head: head}
		if !s.addStream(st) {
			cancel()
			_ = s.send(relay.FrameCancel, f.Stream, []byte("too many streams"))
		}
	case relay.FrameRequestBody:
		st := s.stream(f.Stream)
		if st == nil || st.ws {
			return true // cancelled already
		}
		if st.started {
			return false
		}
		if st.body.Len()+len(f.Payload) > maxRequestBody {
			s.endStream(f.Stream)
			_ = s.sendJSON(relay.FrameResponse, f.Stream, relay.ResponseHead{Status: http.StatusRequestEntityTooLarge})
			_ = s.send(relay.FrameResponseEnd, f.Stream, nil)
			return true
		}
		st.body.Write(f.Payload)
	case relay.FrameRequestEnd:
		st := s.stream(f.Stream)
		if st == nil || st.ws {
			return true
		}
		if st.started {
			return false
		}
		st.started = true
		go s.serveHTTP(st)
	case relay.FrameCancel:
		s.endStream(f.Stream)
	case relay.FrameWSOpen:
		var open relay.WSOpen
		if json.Unmarshal(f.Payload, &open) != nil || !validPath(open.URL) {
			return false
		}
		ctx, cancel := context.WithCancel(s.ctx)
		st := &stream{id: f.Stream, ws: true, ctx: ctx, cancel: cancel, out: make(chan wsMessage, 256)}
		if !s.addStream(st) {
			cancel()
			_ = s.sendJSON(relay.FrameWSClose, f.Stream, relay.WSClose{Code: 1013, Reason: "too many streams"})
			return true
		}
		go s.serveWS(st, open)
	case relay.FrameWSText, relay.FrameWSBinary:
		st := s.stream(f.Stream)
		if st == nil || !st.ws || len(f.Payload) < 1 {
			return true
		}
		if len(st.partial)+len(f.Payload)-1 > maxWSMessage {
			s.endStream(f.Stream)
			return true
		}
		if st.partial == nil {
			st.partTyp = f.Type
		}
		st.partial = append(st.partial, f.Payload[1:]...)
		if f.Payload[0] == relay.WSFinal {
			msg := wsMessage{binary: st.partTyp == relay.FrameWSBinary, data: st.partial}
			st.partial = nil
			select {
			case st.out <- msg:
			default:
				s.endStream(f.Stream) // the device is sending faster than the terminal reads
			}
		}
	case relay.FrameWSClose:
		st := s.stream(f.Stream)
		if st == nil || !st.ws {
			return true
		}
		var c relay.WSClose
		_ = json.Unmarshal(f.Payload, &c)
		select {
		case st.out <- wsMessage{close: &c}:
		default:
			s.endStream(f.Stream)
		}
	}
	return true
}

func requestHost(h string) string {
	if validHost(h) {
		return h
	}
	return "lectern.relay"
}

func (s *session) serveHTTP(st *stream) {
	defer s.endStream(st.id)
	head := st.head
	req, err := http.NewRequestWithContext(st.ctx, head.Method, "http://"+requestHost(head.Host)+head.URL, bytes.NewReader(st.body.Bytes()))
	if err != nil {
		_ = s.sendJSON(relay.FrameResponse, st.id, relay.ResponseHead{Status: http.StatusBadRequest})
		_ = s.send(relay.FrameResponseEnd, st.id, nil)
		return
	}
	for _, kv := range head.Header {
		if !hopHeaders[strings.ToLower(kv[0])] {
			req.Header.Add(kv[0], kv[1])
		}
	}
	resp, err := s.client.Do(req)
	if err != nil {
		if st.ctx.Err() == nil {
			_ = s.sendJSON(relay.FrameResponse, st.id, relay.ResponseHead{Status: http.StatusBadGateway})
			_ = s.send(relay.FrameResponseEnd, st.id, nil)
		}
		return
	}
	defer resp.Body.Close()
	out := relay.ResponseHead{Status: resp.StatusCode}
	for k, vs := range resp.Header {
		if hopHeaders[strings.ToLower(k)] || strings.EqualFold(k, "Set-Cookie") {
			continue
		}
		for _, v := range vs {
			out.Header = append(out.Header, [2]string{k, v})
		}
	}
	if s.sendJSON(relay.FrameResponse, st.id, out) != nil {
		return
	}
	buf := make([]byte, relay.MaxChunk)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if s.send(relay.FrameResponseBody, st.id, buf[:n]) != nil {
				return
			}
		}
		if errors.Is(err, io.EOF) {
			_ = s.send(relay.FrameResponseEnd, st.id, nil)
			return
		}
		if err != nil {
			if st.ctx.Err() == nil {
				_ = s.send(relay.FrameCancel, st.id, []byte("upstream error"))
			}
			return
		}
	}
}

func (s *session) serveWS(st *stream, open relay.WSOpen) {
	defer s.endStream(st.id)
	header := http.Header{}
	for _, kv := range open.Header {
		if !hopHeaders[strings.ToLower(kv[0])] {
			header.Add(kv[0], kv[1])
		}
	}
	dctx, cancel := context.WithTimeout(st.ctx, 20*time.Second)
	c, _, err := websocket.Dial(dctx, "ws://"+requestHost(open.Host)+open.URL, &websocket.DialOptions{
		HTTPClient: s.client, HTTPHeader: header, Subprotocols: open.Protocols,
		CompressionMode: websocket.CompressionDisabled,
	})
	cancel()
	if err != nil {
		if st.ctx.Err() == nil {
			_ = s.sendJSON(relay.FrameWSClose, st.id, relay.WSClose{Code: 1006, Reason: "could not connect"})
		}
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(maxWSMessage)
	if s.sendJSON(relay.FrameWSOpened, st.id, relay.WSOpened{Protocol: c.Subprotocol()}) != nil {
		return
	}
	// Device → Lectern.
	go func() {
		for {
			select {
			case <-st.ctx.Done():
				return
			case m := <-st.out:
				if m.close != nil {
					code := websocket.StatusCode(m.close.Code)
					if code < 1000 || code >= 5000 || code == 1005 || code == 1006 {
						code = websocket.StatusNormalClosure
					}
					_ = c.Close(code, m.close.Reason)
					st.cancel()
					return
				}
				typ := websocket.MessageText
				if m.binary {
					typ = websocket.MessageBinary
				}
				wctx, cancel := context.WithTimeout(st.ctx, 30*time.Second)
				err := c.Write(wctx, typ, m.data)
				cancel()
				if err != nil {
					st.cancel()
					return
				}
			}
		}
	}()
	// Lectern → device.
	for {
		typ, data, err := c.Read(st.ctx)
		if err != nil {
			if st.ctx.Err() == nil || s.ctx.Err() == nil {
				code := websocket.CloseStatus(err)
				if code == -1 {
					code = websocket.StatusNormalClosure
				}
				_ = s.sendJSON(relay.FrameWSClose, st.id, relay.WSClose{Code: int(code)})
			}
			return
		}
		ft := relay.FrameWSText
		if typ == websocket.MessageBinary {
			ft = relay.FrameWSBinary
		}
		for {
			n := len(data)
			more := relay.WSFinal
			if n > relay.MaxChunk {
				n, more = relay.MaxChunk, relay.WSMore
			}
			if s.send(ft, st.id, append([]byte{more}, data[:n]...)) != nil {
				return
			}
			data = data[n:]
			if more == relay.WSFinal {
				break
			}
		}
	}
}
