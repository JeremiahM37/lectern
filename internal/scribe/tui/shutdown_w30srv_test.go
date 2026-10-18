package tui

import (
	"strings"
	"testing"
	"time"
)

// sdW30 adds the MCP and language-server stop steps to the W29 fake.
type sdW30 struct{ *sdW29 }

func (s *sdW30) StopMCP() error { s.log.add("mcp:" + s.name); return nil }
func (s *sdW30) StopLSP() error { s.log.add("lsp:" + s.name); return nil }

func TestEndSessionsStopsServersBeforeClosing(t *testing.T) {
	log := &sdW29Log{}
	a := &sdW30{newSdW29("a", log)}
	b := &sdW30{newSdW29("b", log)}
	if errs := EndSessions([]Backend{a, b}, time.Second); len(errs) != 0 {
		t.Fatalf("errors = %v", errs)
	}
	want := []string{
		"abort:a", "abort:b",
		"providers:a", "providers:b",
		"mcp:a", "mcp:b",
		"lsp:a", "lsp:b",
		"close:a", "close:b",
		"groups:a", "groups:b",
	}
	if got := log.snapshot(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("call order:\n got  %v\n want %v", got, want)
	}
}
