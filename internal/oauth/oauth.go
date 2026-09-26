// Package oauth is the authorization server for Lectern's public MCP web
// connector (Streamable HTTP), ported from Grimoire's grimoire-mcp OAuth
// server — the same owner's MIT-licensed sibling project, already live in
// production for exactly this problem. See
// github.com/JeremiahM37/grimoire/go/internal/oauth for the original.
//
// claude.ai adds an MCP server from its own cloud, not from the owner's
// browser: the thing that must authenticate a caller is a public HTTPS
// endpoint, and the only credential the connector UI can send is whatever a
// normal OAuth 2.1 authorization-code-with-PKCE flow hands it. It cannot be
// configured with a static bearer header, so LECTERN_AUTH_TOKEN does not
// reach this case at all — this package is what makes a connector-shaped
// client usable without one.
//
// The design leans on a fact claude.ai's flow doesn't require of a typical
// public API: the approval step does not have to happen in its UI. Nothing
// in the OAuth authorization-code flow requires the authorization endpoint to
// be reachable from the same network as the client — only the browser doing
// the redirect has to reach it. So the /oauth/authorize page that actually
// grants access is never exposed on the public listener at all; it lives on
// a second, tailnet-only listener, and the owner's own browser is the only
// browser that ever loads it. See server.go for the split.
//
// Tokens are opaque random values, stored hashed, matching the idiom the rest
// of the codebase already uses for secrets.
package oauth

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"time"
)

// Now is overridden in tests that need to move expiry around without
// sleeping.
var Now = time.Now

// Scopes gate which MCP tools a token may call. There are exactly two:
// lectern.read covers every tool that only observes the board (sessions,
// tasks, projects, approvals, claims); lectern.write covers everything that
// starts a session, sends into one, files or dispatches work, or decides a
// build. decide_approval is not reachable through either scope — it is
// excluded from the web connector's tool list unconditionally, in
// internal/mcp/http.go, because an approval decision must come from the
// human, never from a remote LLM.
const (
	ScopeRead  = "lectern.read"
	ScopeWrite = "lectern.write"
)

// AllScopes is the full set, in the order presented on the consent page.
var AllScopes = []string{ScopeRead, ScopeWrite}

// DefaultScopes are what a web connector is granted when the owner approves
// without touching the checkboxes. Both are on by default: unlike Grimoire's
// credential broker, neither Lectern scope can reach outside the board itself
// (no third-party spend, no arbitrary host file read — that path is refused
// separately for any remote caller, scoped or not, see internal/mcp/http.go),
// so there is no reason to make write an opt-in extra step.
var DefaultScopes = []string{ScopeRead, ScopeWrite}

// ValidScope reports whether s is one of the two scopes this server knows.
func ValidScope(s string) bool {
	for _, v := range AllScopes {
		if v == s {
			return true
		}
	}
	return false
}

// ValidateScopes filters requested down to the known scopes, deduplicated,
// order preserved. Unknown scope strings are dropped rather than rejected —
// a client asking for a scope this server never advertised is not an attack,
// and refusing outright would break a client that sends one extra scope this
// server doesn't have a tool for yet.
func ValidateScopes(requested []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(requested))
	for _, s := range requested {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] || !ValidScope(s) {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// splitScope parses the space-separated scope string OAuth wire formats use.
func splitScope(s string) []string {
	return ValidateScopes(strings.Fields(s))
}

// joinScope is splitScope's inverse, for storage and the token response.
func joinScope(scopes []string) string {
	return strings.Join(scopes, " ")
}

// randomToken returns a URL-safe opaque token. 32 bytes matches the entropy
// the rest of this package uses for every other secret it mints.
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// scopeGranted reports whether need is present in granted.
func scopeGranted(granted []string, need string) bool {
	for _, g := range granted {
		if g == need {
			return true
		}
	}
	return false
}
