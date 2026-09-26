// Package host is the Lectern side of the end-to-end encrypted relay
// (docs/relay.md). It keeps one outbound WebSocket to a relay, runs a Noise
// responder for every phone connection that arrives through it, and serves
// each authenticated phone's requests with Lectern's own HTTP handler through
// an in-process listener, so every feature works over the relay unchanged.
package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/relay"
)

// Config wires a Host.
type Config struct {
	URL     string // LECTERN_RELAY_URL: ws:// or wss:// base of the relay
	Secret  string // LECTERN_RELAY_HOST_SECRET
	Store   *Store
	Handler http.Handler // Lectern's full handler, auth middleware included
	Log     *slog.Logger
}

// Host maintains the relay connection and the phones behind it.
type Host struct {
	cfg Config
	ln  *memListener

	mu        sync.Mutex
	conn      *websocket.Conn
	since     time.Time
	lastErr   string
	sessions  map[uint32]*session
	closed    bool
	cancelRun context.CancelFunc
}

// New builds a Host. Run starts it.
func New(cfg Config) *Host {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Host{cfg: cfg, ln: newMemListener(cfg.Handler), sessions: map[uint32]*session{}}
}

// Status is what Settings shows about the relay.
type Status struct {
	Configured       bool    `json:"configured"`
	Connected        bool    `json:"connected"`
	RelayURL         string  `json:"relay_url,omitempty"`
	Channel          string  `json:"channel,omitempty"`
	HostKey          string  `json:"host_key,omitempty"`
	HostFingerprint  string  `json:"host_fingerprint,omitempty"`
	ShellKey         string  `json:"shell_key,omitempty"`
	ShellFingerprint string  `json:"shell_fingerprint,omitempty"`
	ConnectedSince   float64 `json:"connected_since,omitempty"`
	Error            string  `json:"error,omitempty"`
}

// Status reports the relay connection and the host's public keys.
func (h *Host) Status() Status {
	st := Status{Configured: true, RelayURL: h.cfg.URL}
	if id, err := h.cfg.Store.Identity(); err == nil {
		st.Channel = id.Channel()
		st.HostKey = relay.B64(id.Noise.Public)
		st.HostFingerprint = relay.Fingerprint(id.Noise.Public)
		st.ShellKey = relay.B64(id.ShellPublic())
		st.ShellFingerprint = relay.Fingerprint(id.ShellPublic())
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st.Connected = h.conn != nil
	if st.Connected {
		st.ConnectedSince = float64(h.since.Unix())
	} else {
		st.Error = h.lastErr
	}
	return st
}

// Run keeps the relay connection up until ctx ends, reconnecting with
// backoff.
func (h *Host) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	h.cancelRun = cancel
	h.mu.Unlock()
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := h.connectOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		h.mu.Lock()
		if err != nil {
			h.lastErr = err.Error()
		}
		h.mu.Unlock()
		h.cfg.Log.Warn("relay: disconnected", "err", err)
		if time.Since(started) > 30*time.Second {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > time.Minute {
			backoff = time.Minute
		}
	}
}

// Close stops the host and every tunnelled connection.
func (h *Host) Close() {
	h.mu.Lock()
	h.closed = true
	cancel := h.cancelRun
	c := h.conn
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if c != nil {
		c.CloseNow()
	}
	h.ln.shutdown()
}

func (h *Host) relayEndpoint() (string, error) {
	u := strings.TrimRight(strings.TrimSpace(h.cfg.URL), "/")
	switch {
	case strings.HasPrefix(u, "wss://"), strings.HasPrefix(u, "ws://"):
	case strings.HasPrefix(u, "https://"):
		u = "wss://" + strings.TrimPrefix(u, "https://")
	case strings.HasPrefix(u, "http://"):
		u = "ws://" + strings.TrimPrefix(u, "http://")
	default:
		return "", fmt.Errorf("relay: LECTERN_RELAY_URL must start with wss:// (got %q)", h.cfg.URL)
	}
	return u + "/v1/host", nil
}

func readJSON(ctx context.Context, c *websocket.Conn) (relay.Control, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	typ, b, err := c.Read(ctx)
	if err != nil {
		return relay.Control{}, err
	}
	var msg relay.Control
	if typ != websocket.MessageText || json.Unmarshal(b, &msg) != nil {
		return relay.Control{}, errors.New("relay: unexpected message during handshake")
	}
	return msg, nil
}

func (h *Host) connectOnce(ctx context.Context) error {
	id, err := h.cfg.Store.Identity()
	if err != nil {
		return err
	}
	endpoint, err := h.relayEndpoint()
	if err != nil {
		return err
	}
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	c, _, err := websocket.Dial(dctx, endpoint, &websocket.DialOptions{CompressionMode: websocket.CompressionDisabled})
	cancel()
	if err != nil {
		return err
	}
	defer c.CloseNow()
	c.SetReadLimit(relay.MaxWireMessage)
	challenge, err := readJSON(ctx, c)
	if err != nil {
		return err
	}
	nonce, err := relay.UnB64(challenge.Nonce)
	if challenge.T != "challenge" || err != nil || len(nonce) < 16 {
		return errors.New("relay: bad challenge")
	}
	if err := writeJSON(ctx, c, relay.HostProof(id.Route, h.cfg.Secret, nonce)); err != nil {
		return err
	}
	ready, err := readJSON(ctx, c)
	if err != nil {
		return err
	}
	if ready.T != "ready" {
		return fmt.Errorf("relay refused this host: %s", ready.Error)
	}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return errors.New("relay: host closed")
	}
	h.conn, h.since, h.lastErr = c, time.Now(), ""
	h.mu.Unlock()
	h.cfg.Log.Info("relay: connected", "relay", h.cfg.URL, "channel", id.Channel())
	defer h.dropConnection(c)

	if err := h.syncRoutes(ctx, c); err != nil {
		return err
	}
	kctx, stop := context.WithCancel(ctx)
	defer stop()
	go keepalive(kctx, c)
	for {
		typ, b, err := c.Read(ctx)
		if err != nil {
			return err
		}
		if typ == websocket.MessageText {
			var msg relay.Control
			if json.Unmarshal(b, &msg) == nil && msg.T == "error" {
				h.cfg.Log.Warn("relay: error from relay", "error", msg.Error)
			}
			continue
		}
		kind, conn, payload, err := relay.DecodeWire(b)
		if err != nil {
			return err
		}
		switch kind {
		case relay.WireOpen:
			s := newSession(h, conn, string(payload))
			h.mu.Lock()
			if old := h.sessions[conn]; old != nil {
				h.mu.Unlock()
				old.close(false)
				h.mu.Lock()
			}
			h.sessions[conn] = s
			h.mu.Unlock()
			go s.run()
		case relay.WireData:
			h.mu.Lock()
			s := h.sessions[conn]
			h.mu.Unlock()
			if s != nil {
				s.deliver(payload)
			}
		case relay.WireClose:
			h.mu.Lock()
			s := h.sessions[conn]
			h.mu.Unlock()
			if s != nil {
				s.close(false)
			}
		}
	}
}

func keepalive(ctx context.Context, c *websocket.Conn) {
	t := time.NewTicker(25 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				c.CloseNow()
				return
			}
		}
	}
}

func (h *Host) dropConnection(c *websocket.Conn) {
	h.mu.Lock()
	if h.conn == c {
		h.conn = nil
	}
	sessions := h.sessions
	h.sessions = map[uint32]*session{}
	h.mu.Unlock()
	for _, s := range sessions {
		s.close(false)
	}
}

func writeJSON(ctx context.Context, c *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return c.Write(ctx, websocket.MessageText, b)
}

// syncRoutes tells a freshly connected relay every route this host accepts:
// paired devices, and pairings still waiting to be scanned.
func (h *Host) syncRoutes(ctx context.Context, c *websocket.Conn) error {
	set, err := h.cfg.Store.RouteHashes()
	if err != nil {
		return err
	}
	if err := writeJSON(ctx, c, relay.Control{T: "routes", Set: set}); err != nil {
		return err
	}
	pending, err := h.cfg.Store.PendingRoutes()
	if err != nil {
		return err
	}
	for hash, ttl := range pending {
		if err := writeJSON(ctx, c, relay.Control{T: "route_add", Hash: hash, TTL: ttl, Once: true}); err != nil {
			return err
		}
	}
	return nil
}

// ErrOffline is returned when an action needs the relay and it is not
// connected.
var ErrOffline = errors.New("relay: not connected to the relay")

func (h *Host) current() *websocket.Conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conn
}

func (h *Host) control(msg relay.Control) error {
	c := h.current()
	if c == nil {
		return ErrOffline
	}
	return writeJSON(context.Background(), c, msg)
}

// write sends one routing frame to the relay.
func (h *Host) write(b []byte) error {
	c := h.current()
	if c == nil {
		return ErrOffline
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageBinary, b); err != nil {
		c.CloseNow()
		return err
	}
	return nil
}

// MintPairing creates a pairing and registers its one-time route with the
// relay. It fails when the relay is unreachable: a QR code the phone cannot
// use would only confuse.
func (h *Host) MintPairing(owner auth.Principal) (Pairing, error) {
	if h.current() == nil {
		return Pairing{}, ErrOffline
	}
	p, err := h.cfg.Store.MintPairing(owner)
	if err != nil {
		return Pairing{}, err
	}
	if err := h.control(relay.Control{T: "route_add", Hash: p.RouteHash, TTL: int(PairingTTL.Seconds()) + 60, Once: true}); err != nil {
		return Pairing{}, err
	}
	return p, nil
}

// Revoke removes a device: its row, its relay route and its live
// connections.
func (h *Host) Revoke(id int64) (Device, error) {
	d, err := h.cfg.Store.Revoke(id)
	if err != nil {
		return Device{}, err
	}
	_ = h.control(relay.Control{T: "route_del", Hash: d.routeHash})
	h.mu.Lock()
	var drop []*session
	for _, s := range h.sessions {
		if s.deviceID() == id {
			drop = append(drop, s)
		}
	}
	h.mu.Unlock()
	for _, s := range drop {
		s.close(true)
	}
	return d, nil
}

// ConnectedDevices is the set of device ids with a live tunnel.
func (h *Host) ConnectedDevices() map[int64]bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[int64]bool{}
	for _, s := range h.sessions {
		if id := s.deviceID(); id != 0 {
			out[id] = true
		}
	}
	return out
}

func (h *Host) removeSession(s *session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessions[s.conn] == s {
		delete(h.sessions, s.conn)
	}
}
