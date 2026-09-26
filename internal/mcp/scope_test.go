package mcp

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/oauth"
)

// TestEveryToolHasAScope guards against a tool shipping unclassified: every
// entry in `tools` must be either in toolScope or in webExcluded, or it
// would be silently unreachable over the web connector (present in
// toolSchemas() but missing from every filtered tools/list) — a bug nobody
// could reproduce without knowing to look at scope.go.
func TestEveryToolHasAScope(t *testing.T) {
	for _, tl := range tools {
		if webExcluded[tl.Name] {
			continue
		}
		if _, ok := scopeFor(tl.Name); !ok {
			t.Errorf("tool %q has no entry in toolScope and is not webExcluded — it would be unreachable over the web connector", tl.Name)
		}
	}
}

// TestDecideApprovalIsWebExcludedNotScoped makes explicit the property
// http.go depends on: decide_approval must not simply carry a scope that
// happens to be ungranted by default — it must be structurally absent from
// toolScope, so no token, however broadly granted, could ever cover it.
func TestDecideApprovalIsWebExcludedNotScoped(t *testing.T) {
	if !webExcluded["decide_approval"] {
		t.Fatal("decide_approval must be in webExcluded")
	}
	if _, ok := toolScope["decide_approval"]; ok {
		t.Fatal("decide_approval must not appear in toolScope at all — it is excluded outright, not scope-gated")
	}
}

func TestFilterWebExcluded(t *testing.T) {
	all := toolSchemas()
	filtered := filterWebExcluded(all)
	if len(filtered) != len(all)-1 {
		t.Fatalf("expected exactly one tool (decide_approval) removed, got %d of %d", len(filtered), len(all))
	}
	for _, tl := range filtered {
		if tl["name"] == "decide_approval" {
			t.Fatal("decide_approval must never survive filterWebExcluded")
		}
	}
}

func TestFilterToolsByScope(t *testing.T) {
	all := filterWebExcluded(toolSchemas())

	readOnly := filterToolsByScope(all, []string{oauth.ScopeRead})
	names := map[string]bool{}
	for _, tl := range readOnly {
		names[tl["name"].(string)] = true
	}
	if !names["list_sessions"] {
		t.Fatal("a read-scoped token must still see list_sessions")
	}
	if names["start_session"] {
		t.Fatal("a read-scoped token must not see start_session")
	}

	full := filterToolsByScope(all, []string{oauth.ScopeRead, oauth.ScopeWrite})
	if len(full) != len(all) {
		t.Fatalf("a token with both scopes should see every non-excluded tool, got %d of %d", len(full), len(all))
	}
}

// Every tool must be classified (or deliberately web-excluded): an
// unclassified tool falls back to the write scope, which is safe but almost
// certainly not what its author meant.
func TestEveryToolHasAWebScope(t *testing.T) {
	for _, tl := range tools {
		if webExcluded[tl.Name] {
			continue
		}
		if _, ok := toolScope[tl.Name]; !ok {
			t.Errorf("tool %q has no entry in toolScope", tl.Name)
		}
	}
}

func TestUnclassifiedToolNeedsWriteScope(t *testing.T) {
	need, ok := scopeFor("some_future_tool")
	if !ok || need != oauth.ScopeWrite {
		t.Fatalf("scopeFor(unknown) = %q, %v; want write scope", need, ok)
	}
}
