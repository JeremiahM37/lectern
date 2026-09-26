package auth

import (
	"testing"
)

// A request that came through the end-to-end encrypted relay is exactly the
// paired device that sent it, or nothing — whatever the mode, and whatever
// else the request carries.
func TestTunnelPrincipalIsAuthoritative(t *testing.T) {
	device := Principal{Kind: KindRelayDevice, Login: "owner@example.com", Human: true}
	for _, mode := range []Mode{ModeNone, ModeToken, ModeTailscale} {
		a := newTestResolver(mode, &fakeLocalAPI{}, "owner@example.com", "")
		a.token = "static-token"
		r := req("relay-device-1", map[string]string{"Authorization": "Bearer static-token"})
		r = r.WithContext(WithTunnel(r.Context(), func() (Principal, bool) { return device, true }))
		p, ok := a.Authenticate(r)
		if !ok || p != device || !a.CanDecide(p) {
			t.Fatalf("%s: tunnel request = %+v, %v", mode, p, ok)
		}
	}
}

// A revoked device is refused even in mode none, where any other in-process
// or loopback caller would be the owner.
func TestRevokedTunnelDeviceIsRefusedInEveryMode(t *testing.T) {
	for _, mode := range []Mode{ModeNone, ModeToken, ModeTailscale} {
		a := newTestResolver(mode, &fakeLocalAPI{}, "owner@example.com", "")
		a.token = "static-token"
		r := req("127.0.0.1:50000", map[string]string{"Authorization": "Bearer static-token"})
		r = r.WithContext(WithTunnel(r.Context(), func() (Principal, bool) { return Principal{}, false }))
		if p, ok := a.Authenticate(r); ok {
			t.Fatalf("%s: revoked tunnel device admitted as %+v", mode, p)
		}
	}
}

// Nothing in a request can claim to be tunnelled: only the context value set
// by the relay host's own listener counts. A local process forging the
// principal's fields in headers stays a non-human local caller.
func TestTunnelIdentityCannotBeForged(t *testing.T) {
	a := newTestResolver(ModeTailscale, &fakeLocalAPI{}, "owner@example.com", "")
	p, ok := a.Authenticate(req("127.0.0.1:50000", map[string]string{
		"X-Lectern-Relay-Device": "1", "Tailscale-User-Login": "owner@example.com",
	}))
	if !ok || p.Kind != KindLocal || a.CanDecide(p) {
		t.Fatalf("forged relay headers = %+v, %v", p, ok)
	}
}
