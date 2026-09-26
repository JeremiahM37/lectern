package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeDeviceLookup answers LookupDevice by exact raw-token match, so tests
// never touch a real internal/pairing.Store.
type fakeDeviceLookup struct {
	tokens map[string]Principal
	calls  int
}

func (f *fakeDeviceLookup) LookupDevice(_ context.Context, raw string) (Principal, bool) {
	f.calls++
	p, ok := f.tokens[raw]
	return p, ok
}

func cookieOf(name, value string) http.Cookie {
	return http.Cookie{Name: name, Value: value}
}

func TestAuthenticateDeviceCookieAuthorizesAcrossModes(t *testing.T) {
	for _, mode := range []Mode{ModeToken, ModeTailscale, ModeNone} {
		t.Run(string(mode), func(t *testing.T) {
			a := newTestResolver(mode, &fakeLocalAPI{}, "owner@example.com", "")
			devices := &fakeDeviceLookup{tokens: map[string]Principal{
				"dev-tok": {Kind: KindDevice, Login: "owner@example.com", Human: true},
			}}
			a.SetDeviceLookup(devices)
			r := httptest.NewRequest("GET", "/api/tasks", nil)
			r.RemoteAddr = "203.0.113.9:1" // a plain public address, no tailnet/loopback trust
			c := cookieOf("lectern_device", "dev-tok")
			r.AddCookie(&c)
			p, ok := a.Authenticate(r)
			if !ok || p.Kind != KindDevice || !p.Human || p.Login != "owner@example.com" {
				t.Fatalf("mode %s: got %+v ok=%v", mode, p, ok)
			}
		})
	}
}

func TestAuthenticateDeviceBearerAuthorizes(t *testing.T) {
	a := newTestResolver(ModeToken, &fakeLocalAPI{}, "", "")
	devices := &fakeDeviceLookup{tokens: map[string]Principal{
		"dev-tok": {Kind: KindDevice, Login: "owner@example.com", Human: true},
	}}
	a.SetDeviceLookup(devices)
	r := httptest.NewRequest("POST", "/api/tasks", nil)
	r.RemoteAddr = "203.0.113.9:1"
	r.Header.Set("Authorization", "Bearer dev-tok")
	// No Origin/Referer at all — must still succeed: the CSRF check only
	// applies to cookie-authenticated requests, never to an explicit bearer.
	p, ok := a.Authenticate(r)
	if !ok || p.Kind != KindDevice {
		t.Fatalf("got %+v ok=%v", p, ok)
	}
}

func TestAuthenticateDeviceCookiePostRequiresMatchingOrigin(t *testing.T) {
	a := newTestResolver(ModeToken, &fakeLocalAPI{}, "", "")
	devices := &fakeDeviceLookup{tokens: map[string]Principal{
		"dev-tok": {Kind: KindDevice, Login: "owner@example.com", Human: true},
	}}
	a.SetDeviceLookup(devices)

	// No Origin/Referer on a state-changing request: refused even though the
	// cookie itself is valid.
	r := httptest.NewRequest("POST", "/api/tasks", nil)
	r.RemoteAddr = "203.0.113.9:1"
	r.Host = "lectern.example.com"
	c := cookieOf("lectern_device", "dev-tok")
	r.AddCookie(&c)
	if _, ok := a.Authenticate(r); ok {
		t.Fatal("a state-changing device-cookie request with no Origin/Referer must be refused")
	}

	// A mismatched Origin: also refused.
	r2 := httptest.NewRequest("POST", "/api/tasks", nil)
	r2.RemoteAddr = "203.0.113.9:1"
	r2.Host = "lectern.example.com"
	r2.Header.Set("Origin", "https://evil.example.com")
	c2 := cookieOf("lectern_device", "dev-tok")
	r2.AddCookie(&c2)
	if _, ok := a.Authenticate(r2); ok {
		t.Fatal("a mismatched Origin must be refused")
	}

	// A matching Origin: allowed.
	r3 := httptest.NewRequest("POST", "/api/tasks", nil)
	r3.RemoteAddr = "203.0.113.9:1"
	r3.Host = "lectern.example.com"
	r3.Header.Set("Origin", "https://lectern.example.com")
	c3 := cookieOf("lectern_device", "dev-tok")
	r3.AddCookie(&c3)
	p, ok := a.Authenticate(r3)
	if !ok || p.Kind != KindDevice {
		t.Fatalf("a matching Origin must be allowed: got %+v ok=%v", p, ok)
	}
}

func TestAuthenticateDeviceCookieGetNeedsNoOrigin(t *testing.T) {
	a := newTestResolver(ModeToken, &fakeLocalAPI{}, "", "")
	devices := &fakeDeviceLookup{tokens: map[string]Principal{
		"dev-tok": {Kind: KindDevice, Login: "owner@example.com", Human: true},
	}}
	a.SetDeviceLookup(devices)
	r := httptest.NewRequest("GET", "/api/tasks", nil)
	r.RemoteAddr = "203.0.113.9:1"
	r.Host = "lectern.example.com"
	c := cookieOf("lectern_device", "dev-tok")
	r.AddCookie(&c)
	if _, ok := a.Authenticate(r); !ok {
		t.Fatal("a GET request needs no Origin/Referer check")
	}
}

func TestAuthenticateDeviceUnknownTokenFallsThroughToOrdinaryRules(t *testing.T) {
	a := newTestResolver(ModeToken, &fakeLocalAPI{}, "", "")
	a.SetDeviceLookup(&fakeDeviceLookup{tokens: map[string]Principal{}})
	r := httptest.NewRequest("GET", "/api/tasks", nil)
	r.RemoteAddr = "203.0.113.9:1"
	c := cookieOf("lectern_device", "nonsense")
	r.AddCookie(&c)
	if _, ok := a.Authenticate(r); ok {
		t.Fatal("an unrecognized device token must not authenticate, and mode token has no other path here")
	}
}

func TestAuthenticateStaticTokenWinsOverDeviceLookup(t *testing.T) {
	a := newTestResolver(ModeToken, &fakeLocalAPI{}, "", "")
	devices := &fakeDeviceLookup{tokens: map[string]Principal{}}
	a.SetDeviceLookup(devices)
	r := httptest.NewRequest("GET", "/api/tasks", nil)
	r.RemoteAddr = "203.0.113.9:1"
	r.Header.Set("Authorization", "Bearer secret") // the static LECTERN_AUTH_TOKEN
	p, ok := a.Authenticate(r)
	if !ok || p.Kind != KindToken {
		t.Fatalf("the static token must be checked first: got %+v ok=%v", p, ok)
	}
	if devices.calls != 0 {
		t.Fatalf("the device lookup must not even run once the static token matches, got %d calls", devices.calls)
	}
}

// A nil device lookup (the zero value every pre-pairing test already builds
// its resolver with) must leave every existing mode's behavior exactly as it
// was — this is the regression guard the other tests in resolver_test.go
// already exercise implicitly; this one names it explicitly.
func TestAuthenticateNilDeviceLookupDoesNotAffectExistingModes(t *testing.T) {
	for _, mode := range []Mode{ModeNone, ModeToken, ModeTailscale} {
		a := newTestResolver(mode, &fakeLocalAPI{}, "owner@example.com", "")
		r := httptest.NewRequest("GET", "/api/tasks", nil)
		r.RemoteAddr = "192.168.0.9:1"
		c := cookieOf("lectern_device", "anything")
		r.AddCookie(&c)
		_, ok := a.Authenticate(r)
		wantOK := mode == ModeNone
		if ok != wantOK {
			t.Fatalf("mode %s: got ok=%v, want %v", mode, ok, wantOK)
		}
	}
}
