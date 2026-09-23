package auth

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// whoisCacheTTL matches the contract: a whois lookup is good for 60s, so a
// chatty client (the PWA's SSE stream, a polling terminal) doesn't hit
// tailscaled's LocalAPI on every request.
const whoisCacheTTL = 60 * time.Second

// tailscale's stable CGNAT ranges: 100.64.0.0/10 for IPv4, fd7a:115c:a1e0::/48
// for IPv6. An address in neither range is either loopback or LAN.
var (
	tailscaleV4 = mustCIDR("100.64.0.0/10")
	tailscaleV6 = mustCIDR("fd7a:115c:a1e0::/48")
)

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

func isTailscaleIP(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	return tailscaleV4.Contains(parsed) || tailscaleV6.Contains(parsed)
}

func isLoopbackIP(ip string) bool {
	parsed := net.ParseIP(ip)
	return parsed != nil && parsed.IsLoopback()
}

func hostOf(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

// firstForwarded returns the first hop of X-Forwarded-For, stripped of any
// port. `tailscale serve` sets this to the real tailnet client's address when
// proxying from loopback.
func firstForwarded(r *http.Request) string {
	v := r.Header.Get("X-Forwarded-For")
	if v == "" {
		return ""
	}
	first := strings.TrimSpace(strings.Split(v, ",")[0])
	if host, _, err := net.SplitHostPort(first); err == nil {
		return host
	}
	return first
}

func extractToken(r *http.Request) string {
	if v := r.Header.Get("Authorization"); strings.HasPrefix(v, "Bearer ") {
		return strings.TrimPrefix(v, "Bearer ")
	}
	if c, err := r.Cookie("lectern_token"); err == nil && c.Value != "" {
		return c.Value
	}
	return r.URL.Query().Get("token")
}

func tokensEqual(a, b string) bool {
	return a != "" && b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func splitCSV(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out[part] = struct{}{}
		}
	}
	return out
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Settings configures a Resolver. Individual fields rather than *config.Config
// so this package stays decoupled from internal/config, matching how the rest
// of the codebase's constructors take explicit values.
type Settings struct {
	Mode            string // LECTERN_AUTH: "", "auto", "none", "token", "tailscale"
	Host            string // LECTERN_HOST, to detect a loopback-only listener
	Token           string // LECTERN_AUTH_TOKEN
	Socket          string // LECTERN_TAILSCALE_SOCKET override
	AllowedUsersCSV string // LECTERN_TAILSCALE_USERS
	AllowedTagsCSV  string // LECTERN_TAILSCALE_TAGS

	// TrustServeHeaders is LECTERN_TRUST_SERVE_HEADERS=1. Unsafe wherever an
	// agent can run a process on this host: X-Forwarded-For and
	// Tailscale-User-Login are ordinary headers, and loopback cannot tell
	// `tailscale serve` proxying a real tailnet client from `curl -H
	// 'Tailscale-User-Login: owner@example.com' 127.0.0.1:PORT/...` run by
	// anything on the box, including a dispatched agent. Off by default, so a
	// loopback request is always KindLocal/non-human regardless of what
	// headers it carries. Opt in only when Lectern's HTTP listener is
	// unreachable except through `tailscale serve` on the same host and no
	// untrusted process shares that host.
	TrustServeHeaders bool
}

type cacheEntry struct {
	principal Principal
	ok        bool
	at        time.Time
}

// Resolver identifies the caller of one request and decides whether the
// request may proceed, given the mode it was constructed with.
type Resolver struct {
	Mode Mode

	token             string
	localAPI          LocalAPI
	allowedUsers      map[string]struct{}
	allowedTags       map[string]struct{}
	trustServeHeaders bool
	log               *slog.Logger

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// New resolves the mode once at startup (probing tailscaled if the mode is
// auto and the listener isn't loopback-only), logs it loudly, and returns a
// Resolver ready to gate requests.
func New(s Settings, log *slog.Logger) *Resolver {
	la := NewLocalAPIClient(s.Socket)
	tailscaleUp := func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := la.Status(ctx)
		return err == nil
	}
	mode := ResolveMode(s.Mode, s.Host, s.Token != "", tailscaleUp)
	return newWithClient(mode, la, s, log)
}

// newWithClient builds a Resolver for an already-resolved mode and a given
// LocalAPI client, doing the default-owner lookup and startup logging. Split
// out from New so tests can exercise this logic — the owner-allowlist
// default and the log line — against a fake client instead of a real
// tailscaled.
func newWithClient(mode Mode, la LocalAPI, s Settings, log *slog.Logger) *Resolver {
	if log == nil {
		log = slog.Default()
	}
	r := &Resolver{
		Mode:              mode,
		token:             s.Token,
		localAPI:          la,
		allowedUsers:      splitCSV(s.AllowedUsersCSV),
		allowedTags:       splitCSV(s.AllowedTagsCSV),
		trustServeHeaders: s.TrustServeHeaders,
		log:               log,
		cache:             map[string]cacheEntry{},
	}

	if mode == ModeTailscale && len(r.allowedUsers) == 0 {
		// No explicit allowlist: the default allowed identity is whoever owns
		// this tailscaled node, so a single-user tailnet needs zero config.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		st, err := la.Status(ctx)
		cancel()
		if err != nil {
			log.Warn("auth: tailscaled status probe failed; no tailnet identity will be allowed until LECTERN_TAILSCALE_USERS is set", "err", err)
		} else if login := st.OwnerLogin(); login != "" {
			r.allowedUsers[login] = struct{}{}
		} else {
			log.Warn("auth: could not determine this node's owner from tailscaled status; set LECTERN_TAILSCALE_USERS")
		}
	}

	log.Info("lectern auth mode resolved", "mode", mode,
		"tailscale_users", sortedKeys(r.allowedUsers), "tailscale_tags", sortedKeys(r.allowedTags),
		"token_configured", s.Token != "", "trust_serve_headers", s.TrustServeHeaders)
	if s.TrustServeHeaders {
		log.Warn("LECTERN_TRUST_SERVE_HEADERS=1: a loopback request carrying X-Forwarded-For or " +
			"Tailscale-User-Login is trusted as that identity. Safe only when this listener is reachable " +
			"solely through `tailscale serve` on this host and nothing untrusted (an agent included) can " +
			"run a process here")
	}
	if mode == ModeNone && !isLoopbackHost(s.Host) {
		log.Warn("LECTERN_AUTH=none on a non-loopback listener: every request is trusted with no identity check",
			"host", s.Host)
	}
	return r
}

// Authenticate resolves the caller's principal for one request and reports
// whether the request may proceed under the resolver's mode. The two travel
// together because the token and tailnet-allowlist checks answer both
// questions at once.
func (a *Resolver) Authenticate(r *http.Request) (Principal, bool) {
	if tok := extractToken(r); tok != "" && tokensEqual(tok, a.token) {
		return Principal{Kind: KindToken, Human: true}, true
	}
	if a.Mode == ModeNone {
		// Single-machine, no-login-ever mode: everyone who reaches the
		// process is the one operator it belongs to.
		return Principal{Kind: KindLocal, Human: true}, true
	}

	remote := hostOf(r.RemoteAddr)
	if isLoopbackIP(remote) {
		// Loopback cannot tell `tailscale serve` proxying a real tailnet
		// client from ANY other process on this box forging the same
		// headers — including a dispatched agent that wants to approve its
		// own permission request. Trust them only when the operator has
		// opted in, having confirmed nothing untrusted shares this host.
		if a.Mode == ModeTailscale && a.trustServeHeaders {
			if xff := firstForwarded(r); xff != "" && isTailscaleIP(xff) {
				return a.whois(r.Context(), xff)
			}
			if login := strings.TrimSpace(r.Header.Get("Tailscale-User-Login")); login != "" {
				ok, human := a.authorize(login, nil)
				return Principal{Kind: KindTailscale, Login: login, Human: human}, ok
			}
		}
		if a.Mode == ModeTailscale {
			// A process on this machine — the CLI, the MCP server, an
			// agent. Allowed for ordinary API use; not human, so it cannot
			// decide approvals (mode isn't none here — that returned above).
			return Principal{Kind: KindLocal}, true
		}
	}

	if a.Mode == ModeTailscale && isTailscaleIP(remote) {
		return a.whois(r.Context(), remote)
	}

	// Mode token: chosen specifically because there is no tailscale identity
	// to fall back on, so it does not extend loopback the same trust —
	// everyone, including a process on this box, needs the token. LAN, or a
	// tailnet address while running in a mode that doesn't trust tailscale
	// identity, falls here too: a token was required above and none matched.
	return Principal{}, false
}

// CanDecide reports whether a principal may decide a pending approval.
// Approval decisions need a human unless the whole control plane is running
// with no auth at all.
func (a *Resolver) CanDecide(p Principal) bool {
	return a.Mode == ModeNone || p.Human
}

// authorize checks a resolved identity against the allowlists. ok says the
// identity may use the API at all; human says it may also decide approvals —
// true only for an allowlisted user login, never for a tag match, because a
// tag identifies automation, not a person.
func (a *Resolver) authorize(login string, tags []string) (ok, human bool) {
	if login != "" {
		if _, allowed := a.allowedUsers[login]; allowed {
			return true, true
		}
	}
	for _, t := range tags {
		if _, allowed := a.allowedTags[t]; allowed {
			return true, false
		}
	}
	return false, false
}

func (a *Resolver) whois(ctx context.Context, ip string) (Principal, bool) {
	if p, ok, hit := a.cacheLookup(ip); hit {
		return p, ok
	}
	// The connecting port isn't observable when identity comes from
	// X-Forwarded-For (tailscale serve strips it), so a placeholder port is
	// used — tailscaled resolves an ordinary (non subnet-router) peer by IP
	// alone.
	addr := net.JoinHostPort(ip, "0")
	resp, err := a.localAPI.WhoIs(ctx, addr)
	if err != nil || resp == nil || resp.UserProfile == nil {
		a.cacheStore(ip, Principal{}, false)
		return Principal{}, false
	}
	login := resp.UserProfile.LoginName
	node, tags := "", []string(nil)
	if resp.Node != nil {
		node = resp.Node.Name
		tags = resp.Node.Tags
	}
	ok, human := a.authorize(login, tags)
	p := Principal{Kind: KindTailscale, Login: login, Node: node, Human: human}
	a.cacheStore(ip, p, ok)
	return p, ok
}

func (a *Resolver) cacheLookup(ip string) (Principal, bool, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, found := a.cache[ip]
	if !found || time.Since(e.at) > whoisCacheTTL {
		return Principal{}, false, false
	}
	return e.principal, e.ok, true
}

func (a *Resolver) cacheStore(ip string, p Principal, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cache[ip] = cacheEntry{principal: p, ok: ok, at: time.Now()}
}
