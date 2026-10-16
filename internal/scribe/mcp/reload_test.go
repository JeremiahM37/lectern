package mcp

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// specsNames lists the tool names the agent would be offered.
func specsNames(reg *tools.Registry) []string {
	var out []string
	for _, s := range reg.Specs(nil) {
		out = append(out, s.Name)
	}
	return out
}

// TestReloadDisableRemovesToolsFromNextRequest is the contract the UI relies
// on: once a server is disabled or removed, its tools are gone from the
// registry, so the next model request does not offer them.
func TestReloadDisableRemovesToolsFromNextRequest(t *testing.T) {
	ok := selfServer(t, "ok", nil)
	m := New(Options{Global: []config.MCPServer{ok}, StartTimeout: 10 * time.Second})
	reg := tools.NewRegistry()
	m.RegisterTools(reg)
	m.Start(context.Background())
	defer m.Close()

	if !slices.Contains(specsNames(reg), "mcp__ok__echo") {
		t.Fatalf("echo not offered before disabling: %v", specsNames(reg))
	}

	off := ok
	off.Disabled = true
	m.Reload(Options{Global: []config.MCPServer{off}})
	if s := statusOf(m, "ok"); s.State != StateDisabled {
		t.Fatalf("after disabling: %+v", s)
	}
	for _, n := range specsNames(reg) {
		if strings.HasPrefix(n, "mcp__ok__") {
			t.Fatalf("disabled server's tools still offered: %v", specsNames(reg))
		}
	}
	if _, ok := reg.Get("mcp__ok__echo"); ok {
		t.Fatal("disabled server's tool still registered")
	}

	// Re-enabling starts it again in the background and the tools come back.
	m.Reload(Options{Global: []config.MCPServer{ok}})
	waitFor(t, func() bool { _, found := reg.Get("mcp__ok__echo"); return found })
	env := &tools.Env{Workdir: t.TempDir(), Mode: "auto"}
	if res := runTool(t, reg, "mcp__ok__echo", env, `{"text":"back"}`); res.Output != "back" {
		t.Fatalf("after re-enabling: %+v", res)
	}
}

func TestReloadRemovedServerUnregistersTools(t *testing.T) {
	m := New(Options{Global: []config.MCPServer{selfServer(t, "gone", nil)}, StartTimeout: 10 * time.Second})
	reg := tools.NewRegistry()
	m.RegisterTools(reg)
	m.Start(context.Background())
	defer m.Close()
	if _, ok := reg.Get("mcp__gone__echo"); !ok {
		t.Fatal("setup: tool not registered")
	}

	m.Reload(Options{})
	if _, ok := reg.Get("mcp__gone__echo"); ok {
		t.Fatal("removed server's tool is still registered")
	}
	for _, s := range m.Status() {
		if s.Name == "gone" {
			t.Fatalf("removed server still in status: %+v", s)
		}
	}
}

func TestReloadKeepsUnchangedConnection(t *testing.T) {
	ok := selfServer(t, "keep", nil)
	m := New(Options{Global: []config.MCPServer{ok}, StartTimeout: 10 * time.Second})
	reg := tools.NewRegistry()
	m.RegisterTools(reg)
	m.Start(context.Background())
	defer m.Close()

	m.mu.Lock()
	before := m.servers["keep"].client
	m.mu.Unlock()

	m.Reload(Options{Global: []config.MCPServer{ok}})

	m.mu.Lock()
	after := m.servers["keep"].client
	m.mu.Unlock()
	if before == nil || before != after {
		t.Fatalf("an unchanged server was reconnected: before=%p after=%p", before, after)
	}
	if _, found := reg.Get("mcp__keep__echo"); !found {
		t.Fatal("tool lost after an unchanged reload")
	}
}

func TestReloadAddsNewServerAndChangedDefinitionReconnects(t *testing.T) {
	first := selfServer(t, "svc", nil)
	m := New(Options{Global: []config.MCPServer{first}, StartTimeout: 10 * time.Second})
	reg := tools.NewRegistry()
	m.RegisterTools(reg)
	m.Start(context.Background())
	defer m.Close()

	// A new server appears next to the existing one.
	extra := selfServer(t, "extra", nil)
	m.Reload(Options{Global: []config.MCPServer{first, extra}})
	waitFor(t, func() bool { _, found := reg.Get("mcp__extra__echo"); return found })

	// Changing a server's definition reconnects it with the new spec.
	changed := selfServer(t, "svc", map[string]string{"SCRIBE_MCP_TEST_MODE": "exit"})
	m.Reload(Options{Global: []config.MCPServer{changed, extra}})
	waitFor(t, func() bool { return statusOf(m, "svc").State == StateFailed })
	if _, found := reg.Get("mcp__svc__echo"); found {
		t.Fatal("tools of a server that now fails to start are still registered")
	}
	if _, found := reg.Get("mcp__extra__echo"); !found {
		t.Fatal("unrelated server lost during reload")
	}
}

func TestReloadUntrustedProjectServerDoesNotRun(t *testing.T) {
	m := New(Options{StartTimeout: 10 * time.Second})
	reg := tools.NewRegistry()
	m.RegisterTools(reg)
	defer m.Close()

	m.Reload(Options{Project: []config.MCPServer{selfServer(t, "proj", nil)}, ProjectTrusted: false})
	if s := statusOf(m, "proj"); s.State != StateUntrusted || s.Source != "project" {
		t.Fatalf("untrusted project server: %+v", s)
	}
	if _, found := reg.Get("mcp__proj__echo"); found {
		t.Fatal("untrusted project server's tools were registered")
	}
}
