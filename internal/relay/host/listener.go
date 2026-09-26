package host

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
)

// memListener feeds in-memory connections to an ordinary http.Server running
// Lectern's own handler. Tunnelled requests therefore go through exactly the
// code a browser's requests do (routing, auth middleware, SSE flushing,
// WebSocket hijacking) — there is no second API to keep in sync.
type memListener struct {
	conns  chan net.Conn
	done   chan struct{}
	once   sync.Once
	server *http.Server
}

func newMemListener(handler http.Handler) *memListener {
	l := &memListener{conns: make(chan net.Conn), done: make(chan struct{})}
	l.server = &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		// The only thing that identifies a tunnelled request is this
		// context value, set from the connection the listener made itself.
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if tc, ok := c.(*tunnelConn); ok {
				return auth.WithTunnel(ctx, tc.resolve)
			}
			return ctx
		},
	}
	go func() { _ = l.server.Serve(l) }()
	return l
}

func (l *memListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *memListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *memListener) Addr() net.Addr { return tunnelAddr("lectern-relay") }

// dial opens one connection into the server on behalf of a device.
func (l *memListener) dial(ctx context.Context, deviceID int64, resolve auth.TunnelResolver) (net.Conn, error) {
	client, server := net.Pipe()
	tc := &tunnelConn{Conn: server, resolve: resolve, remote: tunnelAddr("relay-device-" + strconv.FormatInt(deviceID, 10))}
	select {
	case l.conns <- tc:
		return client, nil
	case <-l.done:
		client.Close()
		server.Close()
		return nil, errors.New("relay: host is shutting down")
	case <-ctx.Done():
		client.Close()
		server.Close()
		return nil, ctx.Err()
	}
}

func (l *memListener) shutdown() {
	l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = l.server.Shutdown(ctx)
}

// tunnelConn is the server end of one tunnelled connection.
type tunnelConn struct {
	net.Conn
	resolve auth.TunnelResolver
	remote  net.Addr
}

// RemoteAddr is deliberately not an IP: nothing may mistake a relayed
// request for loopback, a tailnet address or a LAN client.
func (c *tunnelConn) RemoteAddr() net.Addr { return c.remote }

type tunnelAddr string

func (a tunnelAddr) Network() string { return "relay" }
func (a tunnelAddr) String() string  { return string(a) }
