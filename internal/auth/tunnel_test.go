package auth

import (
	"testing"
)

// A public tunnel pointed at 127.0.0.1 (Cloudflare Tunnel's default,
// Tailscale Funnel) delivers internet traffic over loopback. None of it may
// inherit "a process on this machine" trust, in any mode.
func TestLoopbackRelayedFromOutsideIsRefused(t *testing.T) {
	outside := map[string]map[string]string{
		"cloudflare tunnel":   {"Cf-Connecting-Ip": "198.51.100.7", "Cf-Ray": "8a1b2c3d4e5f-SEA"},
		"cloudflare ray only": {"Cf-Ray": "8a1b2c3d4e5f-SEA"},
		"tailscale funnel":    {"Tailscale-Funnel-Request": "?1", "X-Forwarded-For": "198.51.100.7"},
		"generic proxy xff":   {"X-Forwarded-For": "198.51.100.7, 127.0.0.1"},
		"x-real-ip":           {"X-Real-Ip": "198.51.100.7"},
		"rfc7239 forwarded":   {"Forwarded": `for="198.51.100.7:4711";proto=https`},
	}
	for _, mode := range []Mode{ModeTailscale, ModeNone} {
		for name, headers := range outside {
			t.Run(string(mode)+"/"+name, func(t *testing.T) {
				a := newTestResolver(mode, &fakeLocalAPI{}, "owner@example.com", "")
				if p, ok := a.Authenticate(req("127.0.0.1:50000", headers)); ok {
					t.Fatalf("relayed outside request was admitted as %+v", p)
				}
			})
		}
	}
}

// Ordinary local callers (the CLI, the MCP server, an agent) keep working.
func TestPlainLoopbackStillLocal(t *testing.T) {
	a := newTestResolver(ModeTailscale, &fakeLocalAPI{}, "owner@example.com", "")
	p, ok := a.Authenticate(req("127.0.0.1:50000", nil))
	if !ok || p.Kind != KindLocal || p.Human {
		t.Fatalf("plain loopback = %+v, %v; want local, non-human", p, ok)
	}
	// A forwarded address that is itself loopback or tailnet is not "outside".
	for _, xff := range []string{"127.0.0.1", "100.64.1.2"} {
		if _, ok := a.Authenticate(req("127.0.0.1:50000", map[string]string{"X-Forwarded-For": xff})); !ok {
			t.Fatalf("X-Forwarded-For %s should not count as outside", xff)
		}
	}
}

// The static token and a paired device are exactly how outside callers are
// supposed to get in, tunnel or not.
func TestCredentialsStillWorkThroughATunnel(t *testing.T) {
	a := newTestResolver(ModeTailscale, &fakeLocalAPI{}, "owner@example.com", "")
	a.SetDeviceLookup(&fakeDeviceLookup{tokens: map[string]Principal{
		"dev-tok": {Kind: KindDevice, Login: "owner@example.com", Human: true},
	}})
	tunnel := map[string]string{"Cf-Connecting-Ip": "198.51.100.7", "Authorization": "Bearer dev-tok"}
	if p, ok := a.Authenticate(req("127.0.0.1:50000", tunnel)); !ok || p.Kind != KindDevice {
		t.Fatalf("paired device through a tunnel = %+v, %v", p, ok)
	}
	tunnel["Authorization"] = "Bearer secret"
	if p, ok := a.Authenticate(req("127.0.0.1:50000", tunnel)); !ok || p.Kind != KindToken {
		t.Fatalf("static token through a tunnel = %+v, %v", p, ok)
	}
}
