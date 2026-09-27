// Package server is `lectern relay`: a single-process, stateless router of
// opaque frames between a Lectern host and that host's paired devices. It
// holds no keys and never sees plaintext (docs/relay.md). What it does check:
//
//   - a host proves it owns its channel (Ed25519 over a fresh challenge) and
//     knows this relay's host secret (HMAC), so strangers cannot use the relay
//     as a free message bus;
//   - a device presents a route token whose hash that host registered, before
//     anything it sends is forwarded;
//   - sizes, rates and connection counts stay inside fixed limits.
//
// It never dials out and serves no HTML or JavaScript: only two WebSocket
// endpoints and a plain-text health check.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/JeremiahM37/lectern/v2/internal/relay"
)

// Close codes the relay sends. 4xxx is the application range; phones show
// a message per code.
const (
	CodeBadRequest   websocket.StatusCode = 4400
	CodeUnauthorized websocket.StatusCode = 4401
	CodeHostOffline  websocket.StatusCode = 4404
	CodeAuthTimeout  websocket.StatusCode = 4408
	CodeTooBig       websocket.StatusCode = 4413
	CodeTooMany      websocket.StatusCode = 4429
	CodeReplaced     websocket.StatusCode = 4409
	CodeSlow         websocket.StatusCode = 4503
)

// Config sets the relay's secret and limits. Zero values take the defaults
// documented on each field.
type Config struct {
	// HostSecret must be known (as an HMAC key) by every host allowed to
	// register a channel. Required.
	HostSecret string
	// TrustForwarded takes the client IP from X-Forwarded-For's first hop.
	// Only for a relay reachable solely through a proxy you run; otherwise a
	// client could pick its own address and dodge the per-IP limits.
	TrustForwarded bool

	MaxConns             int           // total open connections (1024)
	MaxConnsPerIP        int           // open connections per client IP (32)
	ConnectsPerMinute    int           // new connections per client IP per minute (120)
	MaxChannels          int           // connected hosts (64)
	MaxDevicesPerChannel int           // open device connections per host (32)
	MaxRoutesPerChannel  int           // registered route tokens per host (512)
	DeviceRate           int           // device → host bytes per second (256 KiB)
	DeviceBurst          int           // device → host burst bytes (2 MiB)
	DeviceQueue          int           // bytes queued toward one slow device before dropping it (4 MiB)
	AuthTimeout          time.Duration // time a new connection has to authenticate (10s)
	PingInterval         time.Duration // keepalive ping (30s)

	// OnFrame, when set, sees every payload the relay forwards — the
	// ciphertext exactly as it crossed the relay. Tests use it to prove the
	// relay never holds plaintext.
	OnFrame func(direction, channel string, conn uint32, payload []byte)

	Log *slog.Logger
}

func (c *Config) defaults() {
	set := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	set(&c.MaxConns, 1024)
	set(&c.MaxConnsPerIP, 32)
	set(&c.ConnectsPerMinute, 120)
	set(&c.MaxChannels, 64)
	set(&c.MaxDevicesPerChannel, 32)
	set(&c.MaxRoutesPerChannel, 512)
	set(&c.DeviceRate, 256<<10)
	set(&c.DeviceBurst, 2<<20)
	set(&c.DeviceQueue, 4<<20)
	if c.AuthTimeout <= 0 {
		c.AuthTimeout = 10 * time.Second
	}
	if c.PingInterval <= 0 {
		c.PingInterval = 30 * time.Second
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
}

// MinHostSecret is the shortest host secret New accepts.
const MinHostSecret = 32

// Server is one relay.
type Server struct {
	cfg Config

	mu       sync.Mutex
	channels map[string]*channel
	conns    int
	perIP    map[string]int
	connects map[string]*bucket
}

// New validates cfg and builds a relay.
func New(cfg Config) (*Server, error) {
	if len(cfg.HostSecret) < MinHostSecret {
		return nil, errors.New("relay: the host secret must be at least 32 characters (LECTERN_RELAY_HOST_SECRET)")
	}
	cfg.defaults()
	return &Server{cfg: cfg, channels: map[string]*channel{}, perIP: map[string]int{}, connects: map[string]*bucket{}}, nil
}

type route struct {
	expires time.Time // zero: until the host removes it
	once    bool
}

type channel struct {
	id      string
	host    *hostConn
	routes  map[string]route
	devices map[uint32]*deviceConn
	next    uint32
}

// Handler serves the relay endpoints. Anything else is a bodyless 404: the
// relay must never be somewhere a browser loads a page or a script from.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Cache-Control", "no-store")
		switch {
		case r.URL.Path == "/healthz" && r.Method == http.MethodGet:
			h.Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("ok\n"))
		case r.URL.Path == "/v1/host" && r.Method == http.MethodGet:
			s.serveConn(w, r, s.host)
		case r.URL.Path == "/v1/device" && r.Method == http.MethodGet:
			s.serveConn(w, r, s.device)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustForwarded {
		if v := r.Header.Get("X-Forwarded-For"); v != "" {
			return strings.TrimSpace(strings.Split(v, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// admit applies the connection-count and connection-rate limits before the
// WebSocket upgrade, so a flood costs the relay one HTTP response each.
func (s *Server) admit(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conns >= s.cfg.MaxConns || s.perIP[ip] >= s.cfg.MaxConnsPerIP {
		return false
	}
	b := s.connects[ip]
	if b == nil {
		if len(s.connects) > 4*s.cfg.MaxConns {
			s.pruneConnects()
		}
		per := float64(s.cfg.ConnectsPerMinute)
		b = newBucket(per/60, per)
		s.connects[ip] = b
	}
	if !b.take(1) {
		return false
	}
	s.conns++
	s.perIP[ip]++
	return true
}

func (s *Server) release(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns--
	if s.perIP[ip]--; s.perIP[ip] <= 0 {
		delete(s.perIP, ip)
	}
}

func (s *Server) pruneConnects() {
	for ip, b := range s.connects {
		if b.full() {
			delete(s.connects, ip)
		}
	}
}

func (s *Server) serveConn(w http.ResponseWriter, r *http.Request, run func(context.Context, *websocket.Conn, *http.Request)) {
	ip := s.clientIP(r)
	if !s.admit(ip) {
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	defer s.release(ip)
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Any page may try to connect; nothing is forwarded until the
		// connection authenticates, and the payloads are ciphertext anyway.
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	c.SetReadLimit(relay.MaxWireMessage)
	defer c.CloseNow()
	run(r.Context(), c, r)
}

func closeWith(c *websocket.Conn, code websocket.StatusCode, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	b, _ := json.Marshal(relay.Control{T: "error", Error: reason})
	_ = c.Write(ctx, websocket.MessageText, b)
	_ = c.Close(code, reason)
}

// readControl reads one JSON text message, giving up after timeout. The
// deadline is a timer rather than a read context: coder/websocket tears the
// connection down when a read context ends, which would lose the close code
// that tells the peer why.
func readControl(ctx context.Context, c *websocket.Conn, timeout time.Duration, onTimeout func()) (relay.Control, error) {
	timer := time.AfterFunc(timeout, onTimeout)
	typ, b, err := c.Read(ctx)
	if !timer.Stop() {
		return relay.Control{}, errTimeout
	}
	if err != nil {
		return relay.Control{}, err
	}
	var msg relay.Control
	if typ != websocket.MessageText || len(b) > 64<<10 || json.Unmarshal(b, &msg) != nil {
		return relay.Control{}, errors.New("bad control message")
	}
	return msg, nil
}

var errTimeout = errors.New("timed out")

func writeControl(ctx context.Context, c *websocket.Conn, msg relay.Control) error {
	b, _ := json.Marshal(msg)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.Write(ctx, websocket.MessageText, b)
}

// keepalive pings until ctx ends, closing the connection when a ping goes
// unanswered, so a vanished peer does not hold a slot forever.
func (s *Server) keepalive(ctx context.Context, c *websocket.Conn) {
	t := time.NewTicker(s.cfg.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, s.cfg.PingInterval)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				c.CloseNow()
				return
			}
		}
	}
}
