package executor

import (
	"bufio"
	"context"
	"errors"
	"net"
)

// Dialer is an executor that can open a TCP connection as the target sees it, so
// "127.0.0.1" means the target's loopback and not the control plane's. It is what
// lets the control plane carry a connection to a service that only listens on
// the target's own localhost.
type Dialer interface {
	DialTarget(ctx context.Context, addr string) (net.Conn, error)
}

// ErrNoDial is returned by targets whose loopback cannot be reached this way.
var ErrNoDial = errors.New("this kind of target cannot forward ports")

// DialTarget on the control-plane host is an ordinary dial.
func (l *Local) DialTarget(ctx context.Context, addr string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
}

// DialTarget reaches a Proxmox container's loopback through a relay started
// with `pct exec` (see bridge.go): the container has no route of its own
// from here, and needs none.
func (p *Pct) DialTarget(ctx context.Context, addr string) (net.Conn, error) {
	port, err := bridgeTarget(addr)
	if err != nil {
		return nil, err
	}
	return startBridge(ctx, Wrap(p.VMID, bridgeCommand(port), ""))
}

// bridge runs the relay through the wrapper, over one SSH session, for a
// target whose commands run somewhere the SSH host's loopback is not.
func (s *SSH) bridge(ctx context.Context, addr string) (net.Conn, error) {
	port, err := bridgeTarget(addr)
	if err != nil {
		return nil, err
	}
	client, err := s.client(ctx)
	if err != nil {
		return nil, err
	}
	sess, err := client.NewSession()
	if err != nil {
		return nil, Errf("ssh session failed: %v", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		return nil, err
	}
	if err := sess.Start(s.buildCommand(bridgeCommand(port), "")); err != nil {
		sess.Close()
		return nil, err
	}
	c := &pipeConn{r: bufio.NewReaderSize(stdout, 64<<10), w: stdin, closeFn: sess.Close}
	if err := handshake(ctx, c); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// DialTarget rides the SSH connection the executor already holds, which is the
// same thing `ssh -L` does, without a second process to supervise.
//
// A wrapped target runs its commands somewhere else again — inside a container
// on the SSH host, say — so the SSH host's loopback is not the loopback the
// session sees, and forwarding to it would reach the wrong machine's services.
func (s *SSH) DialTarget(ctx context.Context, addr string) (net.Conn, error) {
	if s.Wrapper != "" {
		return s.bridge(ctx, addr)
	}
	client, err := s.client(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := client.DialContext(ctx, "tcp", addr)
	if err != nil {
		// A cached connection may have died quietly; a fresh one is the honest retry.
		s.mu.Lock()
		if s.conn == client {
			s.conn = nil
		}
		s.mu.Unlock()
		if client, err = s.client(ctx); err != nil {
			return nil, err
		}
		return client.DialContext(ctx, "tcp", addr)
	}
	return conn, nil
}
