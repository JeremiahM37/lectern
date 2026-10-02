package api

import (
	"context"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
)

// "Connect your phone" (docs/design/simple-ui.md): the addresses a phone could
// use to reach this Lectern, each with whether it can work and, when not, why.
// The QR code a person scans is built from one of these, so a loopback
// address is never offered: a phone cannot reach 127.0.0.1 of this computer.

type phoneOption struct {
	// Kind is tailnet, lan or relay.
	Kind      string `json:"kind"`
	URL       string `json:"url,omitempty"`
	Available bool   `json:"available"`
	// Reason says why an option cannot work: loopback_only, tailscale_off,
	// no_lan_address, relay_not_set_up or relay_disconnected.
	Reason string `json:"reason,omitempty"`
	// Secure is true for an https address with a real certificate.
	Secure bool `json:"secure,omitempty"`
}

type phoneAddresses struct {
	// Listening is where this server accepts connections; LoopbackOnly means
	// nothing but this computer can reach it.
	Listening    string        `json:"listening"`
	LoopbackOnly bool          `json:"loopback_only"`
	Options      []phoneOption `json:"options"`
	// CanEnableWiFi says this is the private runtime, which can also listen
	// on the Wi-Fi address on request; WiFiOpen says it is doing so now.
	CanEnableWiFi bool `json:"can_enable_wifi"`
	WiFiOpen      bool `json:"wifi_open"`
}

// tailnetStatus asks tailscaled who this node is; tests replace it.
func (s *Server) tailnetStatus(ctx context.Context) (*auth.StatusResponse, error) {
	if s.TailnetStatus != nil {
		return s.TailnetStatus(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return auth.NewLocalAPIClient(s.Cfg.TailscaleSocket).Status(ctx)
}

// lanAddresses are this computer's private IPv4 addresses on its local
// networks: not loopback, not link-local, not the tailnet's CGNAT range.
func (s *Server) lanAddresses() []string {
	if s.LANAddresses != nil {
		return s.LANAddresses()
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	out := []string{}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP.To4()
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || !ip.IsPrivate() || cgnat.Contains(ip) {
			continue
		}
		out = append(out, ip.String())
	}
	sort.Strings(out)
	return out
}

func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// phoneAddressesHandler is GET /api/phone/addresses.
func (s *Server) phoneAddressesHandler(w http.ResponseWriter, r *http.Request) {
	port := strconv.Itoa(s.Cfg.Port)
	out := phoneAddresses{CanEnableWiFi: s.EnableWiFi != nil, Listening: net.JoinHostPort(s.Cfg.Host, port), LoopbackOnly: loopbackHost(s.Cfg.Host)}

	tailnet := phoneOption{Kind: "tailnet"}
	switch {
	case s.PhoneURL != "":
		// The tailnet HTTPS listener: a real certificate, bound to the
		// tailnet addresses whatever LECTERN_HOST says.
		tailnet.URL, tailnet.Available, tailnet.Secure = s.PhoneURL, true, true
	default:
		st, err := s.tailnetStatus(r.Context())
		name := ""
		if err == nil && st != nil && st.Self != nil {
			name = strings.TrimSuffix(st.Self.DNSName, ".")
			if name == "" && len(st.Self.TailscaleIPs) > 0 {
				name = st.Self.TailscaleIPs[0]
			}
		}
		switch {
		case name == "":
			tailnet.Reason = "tailscale_off"
		case out.LoopbackOnly:
			tailnet.URL, tailnet.Reason = "http://"+net.JoinHostPort(name, port), "loopback_only"
		default:
			tailnet.URL, tailnet.Available = "http://"+net.JoinHostPort(name, port), true
		}
	}
	out.Options = append(out.Options, tailnet)

	lan := phoneOption{Kind: "lan"}
	if ips := s.lanAddresses(); len(ips) == 0 {
		lan.Reason = "no_lan_address"
	} else {
		lan.URL = "http://" + net.JoinHostPort(ips[0], port)
		if out.LoopbackOnly {
			lan.Reason = "loopback_only"
		} else {
			lan.Available = true
		}
	}
	if s.WiFiURL != nil {
		if address := s.WiFiURL(); address != "" {
			lan.URL, lan.Available, lan.Reason = address, true, ""
			out.WiFiOpen = true
		}
	}
	out.Options = append(out.Options, lan)

	relay := phoneOption{Kind: "relay"}
	switch {
	case s.Relay == nil:
		relay.Reason = "relay_not_set_up"
	case !s.Relay.Status().Connected:
		relay.URL, relay.Reason = s.Relay.Status().RelayURL, "relay_disconnected"
	default:
		relay.URL, relay.Available, relay.Secure = s.Relay.Status().RelayURL, true, true
	}
	out.Options = append(out.Options, relay)

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

// enablePhoneWiFi is POST /api/phone/wifi: the private runtime also listens
// on this computer's Wi-Fi address, where only a paired device gets in, and
// pairing is turned on. Owner-only.
func (s *Server) enablePhoneWiFi(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireOwner(w, r); !ok {
		return
	}
	if s.EnableWiFi == nil {
		httpError(w, 409, "Wi-Fi setup is available for the private local runtime; this server uses its configured network listener")
		return
	}
	address, err := s.EnableWiFi()
	if err != nil {
		httpError(w, 409, "%s", err)
		return
	}
	if err := s.DB.SetSetting("pairing_enabled", "1"); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"url": address})
}

// disablePhoneWiFi is DELETE /api/phone/wifi: phones on the Wi-Fi can no
// longer reach Lectern. Paired devices stay paired for the next time.
func (s *Server) disablePhoneWiFi(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireOwner(w, r); !ok {
		return
	}
	if s.DisableWiFi != nil {
		s.DisableWiFi()
	}
	writeJSON(w, 200, map[string]any{"open": false})
}
