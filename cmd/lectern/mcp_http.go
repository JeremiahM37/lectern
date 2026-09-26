// `lectern mcp --http` serves Lectern's MCP tool surface over the
// Streamable-HTTP web-connector transport (internal/mcp/http.go), so
// claude.ai's own cloud — not the owner's browser — can add Lectern as a
// custom connector and drive it from an ordinary chat: "start a Lectern
// session that builds this," "give my open session doing X this file."
//
// Ported from grimoire-mcp's identical feature
// (github.com/JeremiahM37/grimoire/go/cmd/grimoire-mcp/main.go), the same
// owner's MIT-licensed sibling project, already live in production for the
// same problem: a hosted MCP client can only authenticate with whatever an
// OAuth 2.1 authorization-code-with-PKCE flow hands it, so this is what
// makes Lectern usable as a connector without a static header.
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/mcp"
	"github.com/JeremiahM37/lectern/v2/internal/oauth"
)

// mcpHTTPCommand runs `lectern mcp --http`: a standalone, long-running
// process (meant for a systemd unit, not a per-session stdio child) that
// serves the same tool surface as `lectern mcp` over HTTP. It always talks
// to LECTERN_API like the ordinary `lectern mcp --api` remote path — see
// main.go's explicitRemote handling — rather than adopting or starting a
// local runtime: this is a front door onto one already-running control
// plane, not a second way to launch one.
func mcpHTTPCommand(cfg *config.Config) error {
	api := env("LECTERN_API", "http://127.0.0.1:"+strconv.Itoa(cfg.Port))
	srv := mcp.New(api, cfg.AuthToken)
	srv.Remote = true
	srv.InboundToken = strings.TrimSpace(os.Getenv("LECTERN_MCP_TOKEN"))

	oa, err := oauthFromEnv(cfg)
	if err != nil {
		return fmt.Errorf("OAuth configuration: %w", err)
	}
	srv.OAuth = oa

	if oa != nil {
		go serveAuthorize(oa)
		go reportWebEndpoint(srv, strings.TrimRight(os.Getenv("LECTERN_PUBLIC_BASE"), "/")+"/mcp")
	}

	addr := env("LECTERN_MCP_ADDR", "127.0.0.1:"+env("LECTERN_MCP_PORT", "9119"))
	fmt.Fprintf(os.Stderr, "lectern mcp --http: serving MCP over http at http://%s/mcp\n", addr)
	return srv.ListenAndServe(addr)
}

// oauthFromEnv builds the OAuth authorization server the HTTP transport's
// bearer tokens come from, or nil — the default — when neither
// LECTERN_PUBLIC_BASE nor LECTERN_OAUTH_AUTHORIZE_BASE is set. OAuth is off
// unless BOTH are set: a deployment that never heard of claude.ai connectors
// gets no new attack surface just because a binary was upgraded.
func oauthFromEnv(cfg *config.Config) (*oauth.Handler, error) {
	publicBase := strings.TrimSpace(os.Getenv("LECTERN_PUBLIC_BASE"))
	authorizeBase := strings.TrimSpace(os.Getenv("LECTERN_OAUTH_AUTHORIZE_BASE"))
	if publicBase == "" && authorizeBase == "" {
		return nil, nil
	}
	if publicBase == "" || authorizeBase == "" {
		return nil, fmt.Errorf(
			"LECTERN_PUBLIC_BASE and LECTERN_OAUTH_AUTHORIZE_BASE must both be set to enable OAuth " +
				"(one was set without the other) — see docs/web-connector.md")
	}

	dbPath := strings.TrimSpace(os.Getenv("LECTERN_OAUTH_DB"))
	if dbPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("no LECTERN_OAUTH_DB and could not find a home directory: %w", err)
		}
		dbPath = filepath.Join(home, ".lectern-mcp", "oauth.db")
	}
	store, err := oauth.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening OAuth store at %s: %w", dbPath, err)
	}

	var redirects *oauth.RedirectAllowlist
	if raw := strings.TrimSpace(os.Getenv("LECTERN_OAUTH_ALLOWED_REDIRECTS")); raw != "" {
		redirects = oauth.NewRedirectAllowlist(strings.Split(raw, ","))
	}

	var allowedLogins []string
	if raw := strings.TrimSpace(os.Getenv("LECTERN_OAUTH_ALLOWED_LOGINS")); raw != "" {
		allowedLogins = strings.Split(raw, ",")
	}

	// LECTERN_OAUTH_ADMIN_TOKEN, falling back to the ordinary API's
	// LECTERN_AUTH_TOKEN so a deployment that already has one secret doesn't
	// need to mint a second just for this admin surface.
	adminToken := strings.TrimSpace(os.Getenv("LECTERN_OAUTH_ADMIN_TOKEN"))
	if adminToken == "" {
		adminToken = cfg.AuthToken
	}

	return oauth.New(oauth.Config{
		Store:         store,
		PublicBase:    publicBase,
		AuthorizeBase: authorizeBase,
		Redirects:     redirects,
		Identity:      identityFromEnv(cfg),
		AllowedLogins: allowedLogins,
		AdminToken:    adminToken,
	})
}

// identityFromEnv builds the oauth.Identity used to pre-recognise the owner
// at /oauth/authorize, or nil — the default — which leaves every consent to
// the admin-token form. Off unless LECTERN_IDENTITY=tailscale is set
// explicitly, the same "named explicitly or inert" rule Grimoire's identity
// backend selection uses.
//
// This reuses Lectern's OWN tailnet-identity code — internal/auth's
// LocalAPI/WhoIs, the exact mechanism auth.Resolver already uses to gate the
// ordinary API in LECTERN_AUTH=tailscale mode — rather than porting a second
// identity abstraction from Grimoire for the same fact (who does this
// tailnet IP belong to).
func identityFromEnv(cfg *config.Config) oauth.Identity {
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("LECTERN_IDENTITY")), "tailscale") {
		return nil
	}
	return tailscaleIdentity{la: auth.NewLocalAPIClient(cfg.TailscaleSocket)}
}

// tailscaleIdentity adapts auth.LocalAPI's whois call to oauth.Identity for
// the private consent listener. Unlike auth.Resolver.whois it has no cache:
// /oauth/authorize is a one-request-at-a-time human action, not the ordinary
// API's per-request hot path, so the extra round trip to tailscaled costs
// nothing worth avoiding.
type tailscaleIdentity struct{ la auth.LocalAPI }

func (t tailscaleIdentity) Identify(r *http.Request) (string, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	resp, err := t.la.WhoIs(ctx, net.JoinHostPort(host, "0"))
	if err != nil || resp == nil || resp.UserProfile == nil || resp.UserProfile.LoginName == "" {
		return "", false
	}
	return resp.UserProfile.LoginName, true
}

// serveAuthorize runs the private, owner-only consent listener. It is a
// second HTTP server on a second address on purpose — see internal/oauth's
// package doc and docs/web-connector.md for why /oauth/authorize must never
// share the public listener's address, and why binding it is this process's
// job rather than something the public HTTPHandler could gate with a check.
//
// This listener speaks plain HTTP; TLS is deliberately not this process's
// job — LECTERN_OAUTH_AUTHORIZE_BASE names whatever already terminates HTTPS
// in front of it (a `tailscale serve` entry, or Lectern's own tailnet TLS
// listener on a different port). Binding this to anything other than
// loopback or a tailnet-only address defeats the whole reason the surface is
// separate from the public one.
func serveAuthorize(oa *oauth.Handler) {
	addr := env("LECTERN_OAUTH_AUTHORIZE_ADDR", "127.0.0.1:9120")
	fmt.Fprintf(os.Stderr, "lectern mcp --http: serving the OAuth consent page at http://%s/oauth/authorize "+
		"(reachable to the outside only via LECTERN_OAUTH_AUTHORIZE_BASE, e.g. through `tailscale serve`)\n", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           oa.PrivateMux(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       time.Minute,
		WriteTimeout:      time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "lectern mcp --http: OAuth consent listener:", err)
		os.Exit(1)
	}
}

// hasFlag reports whether name is present among args, e.g. "--http" among
// os.Args[2:] for `lectern mcp --http`.
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

// reportWebEndpoint tells the control plane this connector's public MCP URL,
// so Settings can show the exact address to paste into claude.ai or ChatGPT.
// Best effort, retried while Lectern itself may still be starting; a failure
// only means the card keeps its generic hint.
func reportWebEndpoint(srv *mcp.Server, url string) {
	for attempt := 0; attempt < 10; attempt++ {
		if err := srv.ReportWebEndpoint(url); err == nil {
			return
		}
		time.Sleep(time.Duration(attempt+1) * 3 * time.Second)
	}
}
