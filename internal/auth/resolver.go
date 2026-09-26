package auth

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
	"net/url"
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

// ClassifyRemote reports whether an http.Request.RemoteAddr is loopback or a
// tailnet address — the two "this is basically local" cases every mode
// already trusts to some degree. Exported for callers outside this package
// that want to warn about the third case (docs/remote-access.md's "Add a
// Settings hint when the request arrives over a non-tailnet, non-loopback
// origin"), without duplicating the address classification this package
// already owns.
func ClassifyRemote(remoteAddr string) (loopback, tailscale bool) {
	host := hostOf(remoteAddr)
	return isLoopbackIP(host), isTailscaleIP(host)
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

// cameFromOutside reports whether a request that reached us over loopback was
// actually relayed from somewhere else — a public tunnel (Cloudflare Tunnel,
// Tailscale Funnel) or any reverse proxy forwarding a non-local, non-tailnet
// client. Loopback is only trustworthy as "a process on this machine" when
// nothing says otherwise; a tunnel pointed at 127.0.0.1 must never inherit
// that trust, or every visitor on the internet would.
func cameFromOutside(r *http.Request) bool {
	for _, h := range []string{"Cf-Connecting-Ip", "Cf-Ray", "Tailscale-Funnel-Request"} {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	forwarded := []string{firstForwarded(r), strings.TrimSpace(r.Header.Get("X-Real-Ip"))}
	if f := r.Header.Get("Forwarded"); f != "" {
		for _, part := range strings.Split(strings.Split(f, ",")[0], ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok && strings.EqualFold(k, "for") {
				v = strings.Trim(strings.TrimSpace(v), `"[]`)
				if host, _, err := net.SplitHostPort(v); err == nil {
					v = host
				}
				forwarded = append(forwarded, v)
			}
		}
	}
	for _, ip := range forwarded {
		if ip != "" && !isLoopbackIP(ip) && !isTailscaleIP(ip) {
			return true
		}
	}
	return false
}

func extractToken(r *http.Request) string {
	if v := bearerToken(r); v != "" {
		return v
	}
	if c, err := r.Cookie("lectern_token"); err == nil && c.Value != "" {
		return c.Value
	}
	return r.URL.Query().Get("token")
}

// bearerToken reads only the Authorization header, with no cookie or query
// fallback — the one form a non-browser API client can send.
func bearerToken(r *http.Request) string {
	if v := r.Header.Get("Authorization"); strings.HasPrefix(v, "Bearer ") {
		return strings.TrimPrefix(v, "Bearer ")
	}
	return ""
}

// deviceCookieName is the paired-device credential's own cookie, deliberately
// distinct from "lectern_token" (the static LECTERN_AUTH_TOKEN cookie) so the
// two never collide and each can be reasoned about on its own.
const deviceCookieName = "lectern_device"

// DeviceLookup resolves a paired device's raw token to the principal it
// authenticates as. Implemented by internal/pairing and wired in via
// SetDeviceLookup — this package stays decoupled from storage, the same
// pattern LocalAPI uses for tailscaled.
type DeviceLookup interface {
	LookupDevice(ctx context.Context, rawToken string) (Principal, bool)
}

// csrfSafe reports whether a state-changing request authenticated by a
// cookie may proceed. SameSite=Strict already keeps the device cookie out of
// a genuine cross-site request; this is defense in depth against a same-site
// subdomain or a browser that gets SameSite wrong; a state-changing request
// with no Origin/Referer at all is refused rather than assumed safe.
func csrfSafe(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
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

	devMu   sync.RWMutex
	devices DeviceLookup
}

// SetDeviceLookup wires device-pairing authentication into the resolver.
// Called once at startup, after internal/pairing.Store exists, from
// internal/app — a setter rather than a Settings field because the resolver
// is otherwise built from plain config values, and the device store is a
// live object with its own database handle. Safe to call with nil (device
// pairing off, or not yet configured): Authenticate simply never finds a
// paired device.
func (a *Resolver) SetDeviceLookup(d DeviceLookup) {
	a.devMu.Lock()
	defer a.devMu.Unlock()
	a.devices = d
}

func (a *Resolver) deviceLookup() DeviceLookup {
	a.devMu.RLock()
	defer a.devMu.RUnlock()
	return a.devices
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
	// A paired device authenticates the same way in every mode — that is the
	// whole point: it is how a phone with no Tailscale and no static token
	// reaches a Lectern that would otherwise refuse it outright (mode token)
	// or never see it as human (mode tailscale, loopback). Checked before the
	// mode-specific rules below, but after the static token, which always
	// wins if both happen to be presented.
	if devices := a.deviceLookup(); devices != nil {
		if header := bearerToken(r); header != "" {
			// An API client presenting its device token as a bearer, not a
			// cookie: no browser is involved, so the cookie-only CSRF check
			// below does not apply.
			if p, ok := devices.LookupDevice(r.Context(), header); ok {
				return p, true
			}
		} else if c, err := r.Cookie(deviceCookieName); err == nil && c.Value != "" {
			if p, ok := devices.LookupDevice(r.Context(), c.Value); ok {
				if !csrfSafe(r) {
					// SameSite=Strict already stops a genuine cross-site
					// request from attaching this cookie; a state-changing
					// request that gets here anyway with no matching
					// Origin/Referer is refused rather than trusted.
					return Principal{}, false
				}
				return p, true
			}
		}
	}
	remote := hostOf(r.RemoteAddr)
	if isLoopbackIP(remote) && cameFromOutside(r) {
		// A tunnel or proxy delivering outside traffic over loopback: only a
		// credential (the static token or a paired device, both checked
		// above) gets in, whatever the mode.
		return Principal{}, false
	}
	if a.Mode == ModeNone {
		// Single-machine, no-login-ever mode: everyone who reaches the
		// process is the one operator it belongs to.
		return Principal{Kind: KindLocal, Human: true}, true
	}

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
