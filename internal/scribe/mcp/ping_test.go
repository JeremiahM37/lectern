package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// A ready server answers ping, and the time it took is reported.
func TestClientPingAnswers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, _, err := connect(ctx, "test", stdioSpec(t, nil), "t", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	took, err := c.Ping(ctx)
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	if took <= 0 || took > 10*time.Second {
		t.Fatalf("ping took %v", took)
	}
}

// The manager pings a ready server by name, and refuses one that is not
// running or does not exist.
func TestManagerPingNamesAndStates(t *testing.T) {
	m := New(Options{Global: []config.MCPServer{selfServer(t, "ok", nil)}, StartTimeout: 10 * time.Second})
	defer m.Close()
	if _, err := m.Ping(context.Background(), "ok"); err == nil {
		t.Fatal("ping before start should say the server is not running")
	}
	m.Start(context.Background())
	waitFor(t, func() bool { return statusOf(m, "ok").State == StateReady })
	if _, err := m.Ping(context.Background(), "ok"); err != nil {
		t.Fatalf("ping ready server: %v", err)
	}
	if _, err := m.Ping(context.Background(), "nope"); err == nil || !strings.Contains(err.Error(), "no MCP server") {
		t.Fatalf("ping unknown = %v", err)
	}
}
