package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/JeremiahM37/lectern/v2/internal/relay"
)

// maxRouteTTL caps a temporary route (a pairing token): long enough to scan
// a QR code and type a name, short enough that a leaked one is soon useless.
const maxRouteTTL = time.Hour

type hostConn struct {
	c  *websocket.Conn
	ch *channel
}

func (h *hostConn) send(b []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := h.c.Write(ctx, websocket.MessageBinary, b); err != nil {
		h.c.CloseNow()
		return err
	}
	return nil
}

func (s *Server) host(ctx context.Context, c *websocket.Conn, _ *http.Request) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return
	}
	if writeControl(ctx, c, relay.Control{T: "challenge", V: relay.ProtocolVersion, Nonce: relay.B64(nonce)}) != nil {
		return
	}
	hello, err := readControl(ctx, c, s.cfg.AuthTimeout, func() { closeWith(c, CodeAuthTimeout, "no hello") })
	if err != nil {
		return
	}
	if hello.T != "hello" || hello.V != relay.ProtocolVersion || !relay.VerifyHostProof(hello, s.cfg.HostSecret, nonce) {
		s.cfg.Log.Warn("relay: host refused", "channel", hello.Channel)
		closeWith(c, CodeUnauthorized, "host not admitted")
		return
	}
	hc := &hostConn{c: c}
	s.mu.Lock()
	ch := s.channels[hello.Channel]
	if ch == nil {
		if len(s.channels) >= s.cfg.MaxChannels {
			s.mu.Unlock()
			closeWith(c, CodeTooMany, "relay is full")
			return
		}
		ch = &channel{id: hello.Channel}
		s.channels[ch.id] = ch
	}
	// A reconnecting host replaces its old connection. Only the key holder
	// can do this, so it is not a takeover; it is what makes a network blip
	// recover without waiting for the old socket to time out.
	old, oldDevices := ch.host, ch.devices
	ch.host, ch.devices, ch.routes = hc, map[uint32]*deviceConn{}, map[string]route{}
	hc.ch = ch
	s.mu.Unlock()
	if old != nil {
		go closeWith(old.c, CodeReplaced, "replaced by a new host connection")
	}
	for _, d := range oldDevices {
		d.close(CodeHostOffline, "host reconnected")
	}
	s.cfg.Log.Info("relay: host connected", "channel", ch.id)
	if writeControl(ctx, c, relay.Control{T: "ready"}) != nil {
		s.dropHost(hc)
		return
	}
	kctx, stop := context.WithCancel(ctx)
	defer stop()
	go s.keepalive(kctx, c)
	for {
		typ, b, err := c.Read(ctx)
		if err != nil {
			break
		}
		if typ == websocket.MessageText {
			if !s.hostControl(hc, b) {
				closeWith(c, CodeBadRequest, "bad control message")
				break
			}
			continue
		}
		kind, id, payload, err := relay.DecodeWire(b)
		if err != nil || kind == relay.WireOpen {
			closeWith(c, CodeBadRequest, "bad frame")
			break
		}
		s.mu.Lock()
		d := ch.devices[id]
		s.mu.Unlock()
		if d == nil {
			continue // the device already left; its close notice is on its way
		}
		switch kind {
		case relay.WireData:
			if s.cfg.OnFrame != nil {
				s.cfg.OnFrame("to_device", ch.id, id, payload)
			}
			d.enqueue(payload)
		case relay.WireClose:
			d.close(websocket.StatusNormalClosure, "closed by host")
		}
	}
	s.dropHost(hc)
}

// dropHost forgets a host connection (unless a newer one already replaced
// it) and disconnects its devices.
func (s *Server) dropHost(hc *hostConn) {
	s.mu.Lock()
	ch := hc.ch
	var devices map[uint32]*deviceConn
	if ch.host == hc {
		devices = ch.devices
		delete(s.channels, ch.id)
		ch.host, ch.devices = nil, map[uint32]*deviceConn{}
	}
	s.mu.Unlock()
	for _, d := range devices {
		d.close(CodeHostOffline, "host offline")
	}
	if devices != nil {
		s.cfg.Log.Info("relay: host disconnected", "channel", ch.id)
	}
}

func (s *Server) hostControl(hc *hostConn, b []byte) bool {
	var msg relay.Control
	if err := json.Unmarshal(b, &msg); err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := hc.ch
	if ch.host != hc {
		return true
	}
	switch msg.T {
	case "routes":
		if len(msg.Set) > s.cfg.MaxRoutesPerChannel {
			return false
		}
		routes := map[string]route{}
		for _, h := range msg.Set {
			if !relay.ValidTokenHash(h) {
				return false
			}
			routes[h] = route{}
		}
		// Keep pending temporary routes (pairing tokens) across a resend.
		for h, r := range ch.routes {
			if !r.expires.IsZero() && time.Now().Before(r.expires) {
				routes[h] = r
			}
		}
		ch.routes = routes
	case "route_add":
		if !relay.ValidTokenHash(msg.Hash) {
			return false
		}
		if _, exists := ch.routes[msg.Hash]; !exists && len(ch.routes) >= s.cfg.MaxRoutesPerChannel {
			s.pruneRoutes(ch)
			if len(ch.routes) >= s.cfg.MaxRoutesPerChannel {
				return false
			}
		}
		r := route{once: msg.Once}
		if msg.TTL > 0 || msg.Once {
			ttl := time.Duration(msg.TTL) * time.Second
			if ttl <= 0 || ttl > maxRouteTTL {
				ttl = maxRouteTTL
			}
			r.expires = time.Now().Add(ttl)
		}
		ch.routes[msg.Hash] = r
	case "route_del":
		delete(ch.routes, msg.Hash)
	default:
		return false
	}
	return true
}

func (s *Server) pruneRoutes(ch *channel) {
	now := time.Now()
	for h, r := range ch.routes {
		if !r.expires.IsZero() && now.After(r.expires) {
			delete(ch.routes, h)
		}
	}
}

type deviceConn struct {
	id    uint32
	c     *websocket.Conn
	limit int

	mu     sync.Mutex
	out    chan []byte
	queued int
	done   chan struct{}
	once   sync.Once
}

// enqueue hands a frame to the device's writer. A device that cannot keep up
// is disconnected rather than allowed to hold memory or stall the host's one
// shared connection.
func (d *deviceConn) enqueue(b []byte) {
	d.mu.Lock()
	if d.queued+len(b) > d.limit {
		d.mu.Unlock()
		d.close(CodeSlow, "device too slow")
		return
	}
	select {
	case d.out <- b:
		d.queued += len(b)
		d.mu.Unlock()
	default:
		d.mu.Unlock()
		d.close(CodeSlow, "device too slow")
	}
}

func (d *deviceConn) close(code websocket.StatusCode, reason string) {
	d.once.Do(func() {
		close(d.done)
		go closeWith(d.c, code, reason)
	})
}

func (d *deviceConn) writer() {
	for {
		select {
		case <-d.done:
			return
		case b := <-d.out:
			d.mu.Lock()
			d.queued -= len(b)
			d.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := d.c.Write(ctx, websocket.MessageBinary, b)
			cancel()
			if err != nil {
				d.close(CodeSlow, "write failed")
				return
			}
		}
	}
}

func (s *Server) device(ctx context.Context, c *websocket.Conn, r *http.Request) {
	chID := r.URL.Query().Get("ch")
	if !relay.ValidChannelID(chID) {
		closeWith(c, CodeBadRequest, "bad channel")
		return
	}
	auth, err := readControl(ctx, c, s.cfg.AuthTimeout, func() { closeWith(c, CodeAuthTimeout, "no auth") })
	if err != nil {
		return
	}
	if auth.T != "auth" || auth.Token == "" || len(auth.Token) > 256 {
		closeWith(c, CodeUnauthorized, "not authorized")
		return
	}
	hash := relay.TokenHash(auth.Token)
	s.mu.Lock()
	ch := s.channels[chID]
	if ch == nil || ch.host == nil {
		s.mu.Unlock()
		closeWith(c, CodeHostOffline, "host offline")
		return
	}
	rt, ok := ch.routes[hash]
	if !ok || (!rt.expires.IsZero() && time.Now().After(rt.expires)) {
		s.mu.Unlock()
		closeWith(c, CodeUnauthorized, "not authorized")
		return
	}
	if len(ch.devices) >= s.cfg.MaxDevicesPerChannel {
		s.mu.Unlock()
		closeWith(c, CodeTooMany, "too many devices")
		return
	}
	if rt.once {
		delete(ch.routes, hash)
	}
	ch.next++
	d := &deviceConn{id: ch.next, c: c, limit: s.cfg.DeviceQueue, out: make(chan []byte, 4096), done: make(chan struct{})}
	ch.devices[d.id] = d
	host := ch.host
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		current := ch.devices[d.id] == d
		if current {
			delete(ch.devices, d.id)
		}
		stillHost := ch.host == host
		s.mu.Unlock()
		d.close(websocket.StatusNormalClosure, "")
		if current && stillHost {
			_ = host.send(relay.EncodeWire(relay.WireClose, d.id, nil))
		}
	}()
	if host.send(relay.EncodeWire(relay.WireOpen, d.id, []byte(hash))) != nil {
		return
	}
	if writeControl(ctx, c, relay.Control{T: "ok"}) != nil {
		return
	}
	go d.writer()
	kctx, stop := context.WithCancel(ctx)
	defer stop()
	go s.keepalive(kctx, c)
	limiter := newBucket(float64(s.cfg.DeviceRate), float64(s.cfg.DeviceBurst))
	for {
		typ, b, err := c.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary || len(b) > relay.MaxNoiseMessage {
			closeWith(c, CodeBadRequest, "bad frame")
			return
		}
		if wait := limiter.reserve(float64(len(b))); wait > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return
			case <-d.done:
				return
			}
		}
		select {
		case <-d.done:
			return
		default:
		}
		if s.cfg.OnFrame != nil {
			s.cfg.OnFrame("to_host", ch.id, d.id, b)
		}
		if host.send(relay.EncodeWire(relay.WireData, d.id, b)) != nil {
			return
		}
	}
}
