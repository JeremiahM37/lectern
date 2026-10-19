package mcp

import (
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

func TestStartTimeoutFromConfigOverridesDefault(t *testing.T) {
	def := 20 * time.Second
	if got := startTimeout(config.MCPServer{}, def); got != def {
		t.Errorf("unset start_timeout = %v, want the default %v", got, def)
	}
	if got := startTimeout(config.MCPServer{StartTimeout: 7}, def); got != 7*time.Second {
		t.Errorf("start_timeout 7 = %v, want 7s (seconds, like timeout)", got)
	}
}

// A server that hangs on start is cut off at its own start_timeout, not the
// manager's 30 s default.
func TestHungServerFailsAtItsOwnStartTimeout(t *testing.T) {
	hang := selfServer(t, "hang", map[string]string{"SCRIBE_MCP_TEST_MODE": "hang"})
	hang.StartTimeout = 1
	m := New(Options{Global: []config.MCPServer{hang}, StartTimeout: 30 * time.Second})
	defer m.Close()

	began := time.Now()
	m.Start(t.Context())
	elapsed := time.Since(began)

	if s := statusOf(m, "hang"); s.State != StateFailed {
		t.Fatalf("hang: %+v, want failed", s)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("start took %v, want about the 1 s start_timeout", elapsed)
	}
}
