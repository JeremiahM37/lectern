package localruntime

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// wifiListener serves the existing runtime on one private interface. It never
// creates another database or changes the loopback endpoint used by agents.
type wifiListener struct {
	mu      sync.Mutex
	server  *http.Server
	url     string
	handler http.Handler
}

func (p *wifiListener) address() string { p.mu.Lock(); defer p.mu.Unlock(); return p.url }
func (p *wifiListener) enable() (string, error) {
	if address := p.address(); address != "" {
		return address, nil
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	// A UDP connect selects a route without sending a packet. Prefer the
	// default interface over Docker/VM bridges when several private addresses
	// exist. The destination is reserved TEST-NET, not a service dependency.
	var preferred net.IP
	if route, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 9}); err == nil {
		preferred = route.LocalAddr().(*net.UDPAddr).IP
		route.Close()
	}
	sort.SliceStable(addresses, func(i, j int) bool {
		a, aok := addresses[i].(*net.IPNet)
		b, bok := addresses[j].(*net.IPNet)
		return aok && a.IP.Equal(preferred) && !(bok && b.IP.Equal(preferred))
	})
	for _, a := range addresses {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP.To4()
		if ip == nil || !ip.IsPrivate() || ip.IsLoopback() {
			continue
		}
		if address, err := p.enableAt(ip.String()); err == nil {
			return address, nil
		}
	}
	return "", fmt.Errorf("no private Wi-Fi or Ethernet address is available; connect this computer to your network first")
}

// enableAt binds one selected interface and reuses the same handler and port
// on repeated requests. Tests use a loopback interface in their namespace.
func (p *wifiListener) enableAt(ip string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.server != nil {
		return p.url, nil
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		return "", err
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

// Keep the main API's token/device authentication, plus exact Host and Origin
// checks. This listener exposes no local-runtime sign-in or shutdown routes.
func wifiHandler(address string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != address {
			http.Error(w, "unexpected host", http.StatusMisdirectedRequest)
			return
		}
		for _, header := range []string{"Origin", "Referer"} {
			if value := r.Header.Get(header); value != "" && !sameOrigin(value, address) {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/__lectern_local/") {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
