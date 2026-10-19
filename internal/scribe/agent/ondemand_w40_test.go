package agent

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// A subagent with on-demand tools offers its core tools and the request
// tools, and holds the rest back until request_tools loads them.
func TestDeferSubagentToolsOffersCoreOnly(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(readTool{})
	reg.Register(descTool{name: "web_search", desc: "Search the web."})
	reg.Register(descTool{name: "extra_tool", desc: "Something rare."})
	d := DeferSubagentTools(reg, []string{"read", "not_registered"})

	a := New(Options{Tools: reg, Deferred: d})
	got := a.offeredNames()
	if !got["read"] || !got["request_tools"] || !got["release_tools"] {
		t.Fatalf("core and request tools must be offered: %v", got)
	}
	if got["web_search"] || got["extra_tool"] {
		t.Fatalf("non-core tools must wait for request_tools: %v", got)
	}
	if cat := DeferredCatalog(reg, d.IsCore); cat == "" || !contains(cat, "- web_search") || !contains(cat, "- extra_tool") {
		t.Fatalf("the catalog must list the deferred tools, got %q", cat)
	}
}

// A tool registered after DeferSubagentTools is not core and is not active,
// so it stays hidden. That is why the caller must register everything first.
func TestDeferSubagentToolsHidesLateRegistrations(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(readTool{})
	d := DeferSubagentTools(reg, []string{"read"})
	reg.Register(descTool{name: "late_tool", desc: "Added after."})

	if New(Options{Tools: reg, Deferred: d}).offeredNames()["late_tool"] {
		t.Fatalf("a late tool should not be offered without request_tools")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
