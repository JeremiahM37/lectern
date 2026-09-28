package api_test

import (
	"context"
	"errors"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
)

func phoneOptions(t *testing.T, h *harness) (obj, map[string]obj) {
	t.Helper()
	out := h.get("/api/phone/addresses")
	byKind := map[string]obj{}
	for _, o := range out.list("options") {
		byKind[o.str("kind")] = o
	}
	return out, byKind
}

// "Connect your phone" never offers an address a phone cannot reach, and says
// why each unavailable one is unavailable.
func TestPhoneAddressesAreReachableOrExplained(t *testing.T) {
	h := newHarness(t)
	srv := h.App.Server
	srv.Cfg.Port = 41429
	srv.LANAddresses = func() []string { return []string{"192.168.0.75"} }
	srv.TailnetStatus = func(context.Context) (*auth.StatusResponse, error) {
		return &auth.StatusResponse{Self: &auth.StatusSelf{DNSName: "box.tail1234.ts.net.", TailscaleIPs: []string{"100.96.103.31"}}}, nil
	}

	// The local runtime listens on loopback only: nothing is available, and
	// the tailnet/LAN rows say that is why.
	srv.Cfg.Host = "127.0.0.1"
	out, opts := phoneOptions(t, h)
	if out["loopback_only"] != true {
		t.Fatalf("loopback: %v", out)
	}
	for _, kind := range []string{"tailnet", "lan"} {
		if opts[kind]["available"] != false || opts[kind].str("reason") != "loopback_only" {
			t.Fatalf("%s on loopback: %v", kind, opts[kind])
		}
	}
	if opts["relay"]["available"] != false || opts["relay"].str("reason") != "relay_not_set_up" {
		t.Fatalf("relay: %v", opts["relay"])
	}

	// Listening on the network: tailnet name and LAN address, never loopback.
	srv.Cfg.Host = "0.0.0.0"
	_, opts = phoneOptions(t, h)
	if opts["tailnet"].str("url") != "http://box.tail1234.ts.net:41429" || opts["tailnet"]["available"] != true {
		t.Fatalf("tailnet: %v", opts["tailnet"])
	}
	if opts["lan"].str("url") != "http://192.168.0.75:41429" || opts["lan"]["available"] != true {
		t.Fatalf("lan: %v", opts["lan"])
	}

	// The tailnet TLS listener wins: a real certificate.
	srv.PhoneURL = "https://box.tail1234.ts.net:8443"
	_, opts = phoneOptions(t, h)
	if opts["tailnet"].str("url") != srv.PhoneURL || opts["tailnet"]["secure"] != true {
		t.Fatalf("tailnet TLS: %v", opts["tailnet"])
	}
	srv.PhoneURL = ""

	// No Tailscale, no LAN.
	srv.TailnetStatus = func(context.Context) (*auth.StatusResponse, error) { return nil, errors.New("no tailscaled") }
	srv.LANAddresses = func() []string { return nil }
	_, opts = phoneOptions(t, h)
	if opts["tailnet"].str("reason") != "tailscale_off" || opts["lan"].str("reason") != "no_lan_address" {
		t.Fatalf("nothing available: %v", opts)
	}
	for _, o := range opts {
		if u := o.str("url"); u != "" && (contains(u, "127.0.0.1") || contains(u, "localhost")) {
			t.Fatalf("a loopback address was offered: %v", o)
		}
	}
}
