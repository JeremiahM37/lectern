package localruntime

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// "Let phones on this Wi-Fi connect": the private runtime normally answers
// only 127.0.0.1, which no phone can reach. Turning this on (POST
// /api/phone/wifi, `lectern phone`, Settings → Connect your phone) makes the
// SAME runtime — same sessions, same database, same handler — also listen on
// one private address of this computer. Nothing else on the network gets in:
// the app runs in token mode, so only a paired device is accepted there, and
// the runtime's own controls and token are never reachable from the network.
// It is plain HTTP on the LAN, which the person is told before it starts.
// It ends with the runtime, or with DELETE /api/phone/wifi (`lectern phone
// --off`).

// wifiListener serves the existing runtime on one private interface. It never
// creates another database or changes the loopback endpoint used by agents.
type wifiListener struct {
	handler http.Handler
	// port is the runtime's loopback port, tried first so the phone's address
	// matches the one on this computer.
	port int

	mu     sync.Mutex
	server *http.Server
	url    string
}

func (p *wifiListener) address() string { p.mu.Lock(); defer p.mu.Unlock(); return p.url }

// lanCandidates are this computer's private IPv4 addresses that a phone on
// the same network could reach — not loopback, link-local, or the tailnet's
// CGNAT range — with the default route's address first, ahead of Docker and
// VM bridges.
func lanCandidates() []string {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	// A UDP connect selects a route without sending a packet. The destination
	// is reserved TEST-NET, not a service dependency.
	var preferred net.IP
	if route, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 9}); err == nil {
		preferred = route.LocalAddr().(*net.UDPAddr).IP
		route.Close()
	}
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	var out []string
	for _, a := range addresses {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP.To4()
		if ip == nil || !ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || cgnat.Contains(ip) {
			continue
		}
		out = append(out, ip.String())
	}
	sort.SliceStable(out, func(i, j int) bool {
		return preferred != nil && out[i] == preferred.String() && out[j] != preferred.String()
	})
	return out
}

func (p *wifiListener) enable() (string, error) {
	if address := p.address(); address != "" {
		return address, nil
	}
	for _, ip := range lanCandidates() {
		if address, err := p.enableAt(ip); err == nil {
			return address, nil
		}
	}
	return "", fmt.Errorf("this computer has no private Wi-Fi or Ethernet address; connect it to your network first, or use Tailscale or a relay")
}

// enableAt binds one selected interface and reuses the same handler and port
// on repeated requests. Tests use a loopback interface in their namespace.
func (p *wifiListener) enableAt(ip string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.server != nil {
		return p.url, nil
	}
	var listener net.Listener
	err := fmt.Errorf("no port")
	if p.port > 0 {
		listener, err = net.Listen("tcp", net.JoinHostPort(ip, strconv.Itoa(p.port)))
	}
	if err != nil {
		if listener, err = net.Listen("tcp", net.JoinHostPort(ip, "0")); err != nil {
			return "", err
		}
	}
	address := listener.Addr().String()
	p.server = &http.Server{ReadHeaderTimeout: 15 * time.Second, Handler: wifiHandler(address, p.handler)}
	p.url = "http://" + address
	server := p.server
	go func() {
		_ = server.Serve(listener)
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.server == server {
			p.server = nil
			p.url = ""
		}
	}()
	return p.url, nil
}

func (p *wifiListener) close() {
	p.mu.Lock()
	server := p.server
	p.server = nil
	p.url = ""
	p.mu.Unlock()
	if server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
		}
	}
}

// wifiHandler fronts the app on the Wi-Fi address: the request must name that
// address (no DNS rebinding) and come from its own origin, and the runtime's
// own controls are not reachable from the network at all. Who may use the API
// is then the app's token-mode check: a paired device, nothing else.
func wifiHandler(address string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Host, address) {
			http.Error(w, "unexpected host", http.StatusMisdirectedRequest)
			return
		}
		for _, header := range []string{"Origin", "Referer"} {
			if value := r.Header.Get(header); value != "" && !sameOrigin(value, address) {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/__lectern_local/") || strings.HasPrefix(r.URL.Path, "/api/local/") {
			http.NotFound(w, r)
			return
		}
		// The runtime token is for this computer only: never accepted from
		// the network, even if someone learned it.
		if q := r.URL.Query(); q.Has("token") {
			q.Del("token")
			r.URL.RawQuery = q.Encode()
		}
		next.ServeHTTP(w, r)
	})
}
