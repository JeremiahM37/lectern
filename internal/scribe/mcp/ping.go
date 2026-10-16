package mcp

import (
	"context"
	"fmt"
	"time"
)

// Ping sends the MCP ping request and reports how long the server took to
// answer. Any error reply counts as a failed ping.
func (c *Client) Ping(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	if _, err := c.request(ctx, "ping", struct{}{}); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

// Ping pings the named server. Only a ready server answers; any other state
// is reported as the reason there is no answer.
func (m *Manager) Ping(ctx context.Context, name string) (time.Duration, error) {
	m.mu.Lock()
	s, ok := m.servers[name]
	var c *Client
	var state State
	if ok {
		c, state = s.client, s.state
	}
	m.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("no MCP server named %q", name)
	}
	if state != StateReady || c == nil {
		return 0, fmt.Errorf("%s is %s, so it cannot be pinged", name, state)
	}
	return c.Ping(ctx)
}
