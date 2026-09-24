package auth

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeLocalAPI answers whois by source IP and a fixed status, so tests never
// touch a real tailscaled.
type fakeLocalAPI struct {
	whois       map[string]*WhoIsResponse // keyed by the IP passed to WhoIs (port stripped)
	statusErr   error
	ownerLogin  string
	self        *StatusSelf // overrides the default Self in Status(), when set
	statusCalls int

	// Cert() fixtures: either a single fixed pair, or per-dnsName pairs.
	certPEM, keyPEM []byte
	certFor         map[string]certPair
	certErr         error
	certCalls       int
}

type certPair struct{ cert, key []byte }

func (f *fakeLocalAPI) WhoIs(_ context.Context, addr string) (*WhoIsResponse, error) {
	host, _, err := splitAddr(addr)
	if err != nil {
		host = addr
	}
	if resp, ok := f.whois[host]; ok {
		return resp, nil
	}
	return nil, errNotFound
}

func (f *fakeLocalAPI) Status(context.Context) (*StatusResponse, error) {
	f.statusCalls++
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	self := f.self
	if self == nil {
		self = &StatusSelf{UserID: 1}
	}
	return &StatusResponse{
		Self: self,
		User: map[string]StatusUser{"1": {LoginName: f.ownerLogin}},
	}, nil
}

func (f *fakeLocalAPI) Cert(_ context.Context, dnsName string) ([]byte, []byte, error) {
	f.certCalls++
	if f.certErr != nil {
		return nil, nil, f.certErr
	}
	if f.certFor != nil {
		if pair, ok := f.certFor[dnsName]; ok {
			return pair.cert, pair.key, nil
		}
		return nil, nil, errNotFound
	}
	return f.certPEM, f.keyPEM, nil
}

func splitAddr(addr string) (string, string, error) {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i], addr[i+1:], nil
		}
	}
	return addr, "", errNotFound
}

var errNotFound = &notFoundErr{}

type notFoundErr struct{}

func (*notFoundErr) Error() string { return "not found" }

func newTestResolver(mode Mode, la LocalAPI, users, tags string) *Resolver {
	return newTestResolverOpt(mode, la, users, tags, false)
}

// newTestResolverTrusting builds a resolver with LECTERN_TRUST_SERVE_HEADERS=1
// — only for tests that specifically exercise that opt-in path.
func newTestResolverTrusting(mode Mode, la LocalAPI, users, tags string) *Resolver {
	return newTestResolverOpt(mode, la, users, tags, true)
}

func newTestResolverOpt(mode Mode, la LocalAPI, users, tags string, trustServeHeaders bool) *Resolver {
	return &Resolver{
		Mode:              mode,
		token:             "secret",
		localAPI:          la,
		allowedUsers:      splitCSV(users),
		allowedTags:       splitCSV(tags),
		trustServeHeaders: trustServeHeaders,
		log:               slog.Default(),
		cache:             map[string]cacheEntry{},
	}
}

func req(remoteAddr string, headers map[string]string) *http.Request {
	r := httptest.NewRequest("GET", "/api/whoami", nil)
	r.RemoteAddr = remoteAddr
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func TestAuthenticateLoopbackIsLocal(t *testing.T) {
	a := newTestResolver(ModeTailscale, &fakeLocalAPI{}, "owner@example.com", "")
	p, ok := a.Authenticate(req("127.0.0.1:5555", nil))
	if !ok || p.Kind != KindLocal || p.Human {
		t.Fatalf("got %+v ok=%v", p, ok)
	}
}

func TestAuthenticateOwnerAllowed(t *testing.T) {
	la := &fakeLocalAPI{whois: map[string]*WhoIsResponse{
		"100.64.1.2": {UserProfile: &WhoIsUser{LoginName: "owner@example.com"}, Node: &WhoIsNode{Name: "phone"}},
	}}
	a := newTestResolver(ModeTailscale, la, "owner@example.com", "")
	p, ok := a.Authenticate(req("100.64.1.2:9999", nil))
	if !ok || p.Kind != KindTailscale || !p.Human || p.Login != "owner@example.com" || p.Node != "phone" {
		t.Fatalf("got %+v ok=%v", p, ok)
	}
}

func TestAuthenticateOtherUserDenied(t *testing.T) {
	la := &fakeLocalAPI{whois: map[string]*WhoIsResponse{
		"100.64.1.3": {UserProfile: &WhoIsUser{LoginName: "stranger@example.com"}},
	}}
	a := newTestResolver(ModeTailscale, la, "owner@example.com", "")
	p, ok := a.Authenticate(req("100.64.1.3:9999", nil))
	if ok {
		t.Fatalf("a non-allowlisted tailnet user must be denied, got %+v ok=%v", p, ok)
	}
}

func TestAuthenticateTaggedNodeDeniedByDefault(t *testing.T) {
	la := &fakeLocalAPI{whois: map[string]*WhoIsResponse{
		"100.64.1.4": {UserProfile: &WhoIsUser{}, Node: &WhoIsNode{Name: "ci-runner", Tags: []string{"tag:ci"}}},
	}}
	a := newTestResolver(ModeTailscale, la, "owner@example.com", "")
	p, ok := a.Authenticate(req("100.64.1.4:9999", nil))
	if ok {
		t.Fatalf("an un-allowlisted tag must be denied, got %+v ok=%v", p, ok)
	}
}

func TestAuthenticateTaggedNodeAllowedByTag(t *testing.T) {
	la := &fakeLocalAPI{whois: map[string]*WhoIsResponse{
		"100.64.1.4": {UserProfile: &WhoIsUser{}, Node: &WhoIsNode{Name: "ci-runner", Tags: []string{"tag:ci"}}},
	}}
	a := newTestResolver(ModeTailscale, la, "owner@example.com", "tag:ci")
	p, ok := a.Authenticate(req("100.64.1.4:9999", nil))
	if !ok || p.Human {
		// allowed for ordinary API use, but a tag is not a person: it must
		// never be able to decide an approval.
		t.Fatalf("got %+v ok=%v — expected allowed and not human", p, ok)
	}
	if a.CanDecide(p) {
		t.Fatalf("a tagged principal must not be able to decide approvals")
	}
}

func TestAuthenticateLoopbackForwardedForTailnetRequiresOptIn(t *testing.T) {
	la := &fakeLocalAPI{whois: map[string]*WhoIsResponse{
		"100.64.2.5": {UserProfile: &WhoIsUser{LoginName: "owner@example.com"}},
	}}
	// tailscale serve proxies from loopback and sets X-Forwarded-For — but so
	// can anything else on this box, so by default it must NOT be trusted.
	a := newTestResolver(ModeTailscale, la, "owner@example.com", "")
	p, ok := a.Authenticate(req("127.0.0.1:6789", map[string]string{"X-Forwarded-For": "100.64.2.5"}))
	if !ok || p.Kind != KindLocal || p.Human {
		t.Fatalf("X-Forwarded-For must be ignored on loopback by default: got %+v ok=%v", p, ok)
	}

	trusting := newTestResolverTrusting(ModeTailscale, la, "owner@example.com", "")
	p, ok = trusting.Authenticate(req("127.0.0.1:6789", map[string]string{"X-Forwarded-For": "100.64.2.5"}))
	if !ok || p.Kind != KindTailscale || !p.Human || p.Login != "owner@example.com" {
		t.Fatalf("with LECTERN_TRUST_SERVE_HEADERS=1: got %+v ok=%v", p, ok)
	}
}

// The exact attack the trust flag exists to prevent by default: any process
// on this machine — including a dispatched agent — can forge these headers
// against the plain loopback listener and must not become a human principal.
func TestAuthenticateLoopbackForgedTailscaleHeadersAreNotHuman(t *testing.T) {
	la := &fakeLocalAPI{whois: map[string]*WhoIsResponse{
		"100.64.2.5": {UserProfile: &WhoIsUser{LoginName: "owner@example.com"}},
	}}
	a := newTestResolver(ModeTailscale, la, "owner@example.com", "")

	forgedXFF := req("127.0.0.1:1", map[string]string{"X-Forwarded-For": "100.64.2.5"})
	p, ok := a.Authenticate(forgedXFF)
	if !ok || p.Human {
		t.Fatalf("forged X-Forwarded-For must not grant a human principal: %+v ok=%v", p, ok)
	}

	forgedLogin := req("127.0.0.1:1", map[string]string{"Tailscale-User-Login": "owner@example.com"})
	p, ok = a.Authenticate(forgedLogin)
	if !ok || p.Human {
		t.Fatalf("forged Tailscale-User-Login must not grant a human principal: %+v ok=%v", p, ok)
	}
	if a.CanDecide(p) {
		t.Fatal("a principal from forged loopback headers must not be able to decide approvals")
	}
}

func TestAuthenticateLANNeedsToken(t *testing.T) {
	a := newTestResolver(ModeTailscale, &fakeLocalAPI{}, "owner@example.com", "")
	// A plain LAN address, no tailnet range, no token.
	if _, ok := a.Authenticate(req("192.168.0.9:4321", nil)); ok {
		t.Fatal("a LAN client with no token must be denied")
	}
	// The same client with a valid token succeeds.
	p, ok := a.Authenticate(req("192.168.0.9:4321", map[string]string{"Authorization": "Bearer secret"}))
	if !ok || p.Kind != KindToken || !p.Human {
		t.Fatalf("got %+v ok=%v", p, ok)
	}
}

// Token mode is chosen specifically because there is no tailscale identity to
// fall back on — unlike none/tailscale mode, it does not extend loopback
// special trust: a token is required even from a process on this box.
func TestAuthenticateTokenModeRequiresTokenEvenFromLoopback(t *testing.T) {
	a := newTestResolver(ModeToken, &fakeLocalAPI{}, "", "")
	if _, ok := a.Authenticate(req("127.0.0.1:1", nil)); ok {
		t.Fatal("token mode must require the token even from loopback")
	}
	p, ok := a.Authenticate(req("127.0.0.1:1", map[string]string{"Authorization": "Bearer secret"}))
	if !ok || p.Kind != KindToken {
		t.Fatalf("got %+v ok=%v", p, ok)
	}
}

func TestAuthenticateTokenModeIgnoresTailscaleIdentity(t *testing.T) {
	// Token mode does not consult tailscaled at all — a tailnet source
	// address still needs the token, even though the same address would be
	// accepted in tailscale mode.
	la := &fakeLocalAPI{whois: map[string]*WhoIsResponse{
		"100.64.1.2": {UserProfile: &WhoIsUser{LoginName: "owner@example.com"}},
	}}
	a := newTestResolver(ModeToken, la, "owner@example.com", "")
	if _, ok := a.Authenticate(req("100.64.1.2:1", nil)); ok {
		t.Fatal("token mode must not trust tailscale identity")
	}
}

func TestAuthenticateNoneModeAlwaysAllowsAndIsHuman(t *testing.T) {
	a := newTestResolver(ModeNone, &fakeLocalAPI{}, "", "")
	p, ok := a.Authenticate(req("203.0.113.5:1", nil))
	if !ok || !p.Human {
		t.Fatalf("got %+v ok=%v", p, ok)
	}
	if !a.CanDecide(p) {
		t.Fatal("mode none must always be able to decide approvals")
	}
}

func TestWhoisIsCached(t *testing.T) {
	la := &fakeLocalAPI{whois: map[string]*WhoIsResponse{
		"100.64.9.9": {UserProfile: &WhoIsUser{LoginName: "owner@example.com"}},
	}}
	a := newTestResolver(ModeTailscale, la, "owner@example.com", "")
	r1 := req("100.64.9.9:1", nil)
	r2 := req("100.64.9.9:2", nil) // different source port, same client IP
	if _, ok := a.Authenticate(r1); !ok {
		t.Fatal("first lookup should succeed")
	}
	// Remove the fixture: a cached second lookup must not need it again.
	delete(la.whois, "100.64.9.9")
	if _, ok := a.Authenticate(r2); !ok {
		t.Fatal("cached lookup should still succeed without hitting the fake again")
	}
}

func TestNewResolvesDefaultOwnerAllowlist(t *testing.T) {
	la := &fakeLocalAPI{ownerLogin: "owner@example.com"}
	r := newWithClient(ModeTailscale, la, Settings{}, slog.Default())
	if _, ok := r.allowedUsers["owner@example.com"]; !ok {
		t.Fatalf("expected the node owner to be the default allowed identity: %v", r.allowedUsers)
	}
	// An explicit allowlist is never overridden by the owner lookup.
	la2 := &fakeLocalAPI{ownerLogin: "owner@example.com"}
	r2 := newWithClient(ModeTailscale, la2, Settings{AllowedUsersCSV: "someone-else@example.com"}, slog.Default())
	if _, ok := r2.allowedUsers["owner@example.com"]; ok {
		t.Fatalf("an explicit allowlist must not gain the owner too: %v", r2.allowedUsers)
	}
	if la2.statusCalls != 0 {
		t.Fatalf("status must not be probed when an allowlist is already configured, got %d calls", la2.statusCalls)
	}
}

func TestNewWarnsOnNoneModeNonLoopback(t *testing.T) {
	// Exercised for the side effect (no panic, no probe) rather than the log
	// text — mode resolution and the warning condition are already covered
	// by TestResolveMode* and the isLoopbackHost cases above.
	r := newWithClient(ModeNone, &fakeLocalAPI{}, Settings{Host: "0.0.0.0"}, slog.Default())
	if r.Mode != ModeNone {
		t.Fatalf("got %v", r.Mode)
	}
}
