package agents

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// A target with a lectern binary runs the Go helpers; any other keeps the
// Python command, byte for byte.
func TestTargetHelpersFollowTheTargetsLecternBinary(t *testing.T) {
	ex := executor.NewMock(0)
	rel, payload := "lectern/mcp/s1/mcp.json", []byte(`{"mcpServers":{}}`)
	servers := map[string]any{"lectern": map[string]any{"httpUrl": "http://x"}}
	if got := MCPInstallCommandFor(ex, rel, payload); got != MCPInstallCommand(rel, payload) {
		t.Fatalf("no lectern binary: %s", got)
	}
	if got := GeminiWorkspaceMCPCommandFor(ex, "/w", 3, true, servers); got != GeminiWorkspaceMCPCommand("/w", 3, true, servers) {
		t.Fatalf("no lectern binary: %s", got)
	}
	executor.SetTargetEnv(ex, executor.TargetEnv{Lectern: "/opt/lectern/bin/lectern"})
	if got := MCPInstallCommandFor(ex, rel, payload); got != "/opt/lectern/bin/lectern helper mcp-install -- lectern/mcp/s1/mcp.json eyJtY3BTZXJ2ZXJzIjp7fX0=" {
		t.Fatalf("with lectern: %s", got)
	}
	got := GeminiWorkspaceMCPCommandFor(ex, "/w", 3, false, nil)
	if got != `/opt/lectern/bin/lectern helper gemini-workspace '{"op":"remove","session":3,"workdir":"/w"}'` {
		t.Fatalf("with lectern: %s", got)
	}
	if strings.Contains(GeminiWorkspaceMCPCommandFor(ex, "/w", 3, true, servers), "python3") {
		t.Fatal("install still runs Python")
	}
}
