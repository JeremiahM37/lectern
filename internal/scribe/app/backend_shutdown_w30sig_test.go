package app

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/lsp"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/mcp"
)

// The MCP and LSP steps are separate methods so the quit path can run them in
// spec order. Each must be safe to call before, after and again: the App close
// calls them too.
func TestStopMCPAndLSPAreRepeatableW30sig(t *testing.T) {
	b, a := newTestBackend(t, t.TempDir())
	a.ext.mcp = mcp.New(mcp.Options{})
	a.ext.lsp = lsp.NewManager(t.TempDir())

	for i := 0; i < 2; i++ {
		if err := b.StopMCP(); err != nil {
			t.Fatalf("StopMCP call %d: %v", i+1, err)
		}
		if err := b.StopLSP(); err != nil {
			t.Fatalf("StopLSP call %d: %v", i+1, err)
		}
	}
	if n := len(a.ext.lsp.Sessions()); n != 0 {
		t.Fatalf("language servers after StopLSP: %d, want 0", n)
	}
}

// A tab with no App, or an App with neither subsystem, has nothing to stop.
func TestStopMCPAndLSPWithNothingToStopW30sig(t *testing.T) {
	bare := &scribeBackend{}
	if err := bare.StopMCP(); err != nil {
		t.Fatalf("StopMCP without an App: %v", err)
	}
	if err := bare.StopLSP(); err != nil {
		t.Fatalf("StopLSP without an App: %v", err)
	}

	b, _ := newTestBackend(t, t.TempDir())
	if err := b.StopMCP(); err != nil {
		t.Fatalf("StopMCP with no MCP manager: %v", err)
	}
	if err := b.StopLSP(); err != nil {
		t.Fatalf("StopLSP with no LSP manager: %v", err)
	}
}

// App close must still stop both subsystems, now that they are separate steps.
func TestExtCloseStillStopsLSPW30sig(t *testing.T) {
	_, a := newTestBackend(t, t.TempDir())
	a.ext.lsp = lsp.NewManager(t.TempDir())
	a.ext.mcp = mcp.New(mcp.Options{})

	a.ext.close(a)

	if n := len(a.ext.lsp.Sessions()); n != 0 {
		t.Fatalf("language servers after close: %d, want 0", n)
	}
}
