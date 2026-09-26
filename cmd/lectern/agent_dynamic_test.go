package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/console"
)

// agentsRegistryServer stubs GET /api/agents with a fixed registry, for
// dynamicAgentQuick's resolution tests.
func agentsRegistryServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/agents" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDynamicAgentQuickResolvesARegisteredCustomAgent(t *testing.T) {
	srv := agentsRegistryServer(t, `[{"name":"claude","builtin":true},{"name":"aider","command":"aider"}]`)
	c := console.New(srv.URL, "")
	ok, err := dynamicAgentQuick(c, "aider")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("aider is in the registry — dynamicAgentQuick should have resolved it")
	}
}

func TestDynamicAgentQuickRejectsAnUnregisteredName(t *testing.T) {
	srv := agentsRegistryServer(t, `[{"name":"claude","builtin":true}]`)
	c := console.New(srv.URL, "")
	ok, err := dynamicAgentQuick(c, "totally-not-a-thing")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("an unregistered name must not resolve")
	}
}

func TestDynamicAgentQuickPropagatesTransportErrors(t *testing.T) {
	// No server listening on this URL at all.
	c := console.New("http://127.0.0.1:1", "")
	if _, err := dynamicAgentQuick(c, "aider"); err == nil {
		t.Fatal("an unreachable API should surface as an error, not a silent false")
	}
}

// TestUnknownVerbCollisionSafety proves the estate of names main.go and its
// switch already claim (clientVerbs, reservedVerbs, and the built-in
// agentQuickVerbs) can never even reach dynamicAgentQuick: an operator who
// registers a custom agent under one of those names cannot shadow the
// existing subcommand from the CLI dispatch tables alone — "existing
// subcommands always win" is a property of switch/map lookup order, not of
// anything the registry enforces. See TestAgentQuickVerbsNeverShadow for the
// static half of this; this asserts the sets stay large enough to matter
// (i.e. the tables were not accidentally emptied) and that a builtin agent's
// own name is one of them.
func TestUnknownVerbCollisionSafety(t *testing.T) {
	for _, must := range []string{"console", "attach", "mcp", "claude", "codex", "gemini"} {
		if !(clientVerbs[must] || reservedVerbs[must] || agentQuickVerbs[must]) {
			t.Errorf("%q is expected to be claimed by an existing subcommand/verb table, but is not — "+
				"a custom agent by this name would incorrectly reach dynamicAgentQuick", must)
		}
	}
	// A name nobody claims must fall through to dynamic resolution rather
	// than being silently swallowed by any of the three tables.
	for _, free := range []string{"aider", "opencode", "totally-custom-name"} {
		if clientVerbs[free] || reservedVerbs[free] || agentQuickVerbs[free] {
			t.Errorf("%q unexpectedly collides with a reserved verb table", free)
		}
	}
}
