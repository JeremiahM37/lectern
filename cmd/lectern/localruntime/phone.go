package localruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// "Let my phone connect on this Wi-Fi": the private runtime normally answers
// only 127.0.0.1, which no phone can reach. Turning this on makes the SAME
// runtime (same sessions, same database) also listen on this computer's
// local network address, and hands out one-time pairing codes for it. A
// phone that redeems one gets a device credential; nothing else on the
// network gets in, because the app runs in token mode and only a paired
// device or the runtime token is accepted there. It is plain HTTP on the
// LAN, which the person is told before it starts.

const phoneRoute = "/api/local/phone"

// PhoneWarning is said wherever this is turned on.
const PhoneWarning = "Lectern is now reachable from this Wi-Fi network at this address. " +
	"Only paired devices can use it, but the connection is not encrypted: use it on a network you trust, " +
	"and prefer Tailscale or a relay elsewhere. Turn it off with: lectern phone --off"

// PhoneState is GET/POST /api/local/phone's answer.
type PhoneState struct {
	// Open says the LAN listener is running, at URL.
	Open bool   `json:"open"`
	URL  string `json:"url,omitempty"`
	// LANAddress is the address it would use; empty when this computer has
	// no local network address.
	LANAddress string `json:"lan_address,omitempty"`
	// PairURL is a one-time pairing link (POST only), valid until ExpiresAt.
	PairURL   string  `json:"pair_url,omitempty"`
	ExpiresAt float64 `json:"expires_at,omitempty"`
	Warning   string  `json:"warning,omitempty"`
}

// phoneShare owns the LAN listener. mint returns a fresh pairing code
// (enabling device pairing first); serve is the app's own handler.
type phoneShare struct {
	token     string
	loopback  int // the runtime's loopback port, tried first on the LAN
	serve     http.Handler
	mint      func() (code string, expires time.Time, err error)
	addresses func() []string

	mu     sync.Mutex
	server *http.Server
	url    string
}

// lanAddresses are this computer's private IPv4 addresses: not loopback,
// link-local, or the tailnet's CGNAT range.
func lanAddresses() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	var out []string
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

func (p *phoneShare) state() PhoneState {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := PhoneState{Open: p.server != nil, URL: p.url}
	if ips := p.addresses(); len(ips) > 0 {
		st.LANAddress = ips[0]
	}
	if st.Open {
		st.Warning = PhoneWarning
	}
	return st
}

// open starts the LAN listener if it is not running yet.
func (p *phoneShare) open() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.server != nil {
		return nil
	}
	ips := p.addresses()
	if len(ips) == 0 {
		return errors.New("this computer has no local network address; connect it to the Wi-Fi first, or use Tailscale or a relay")
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(ips[0], strconv.Itoa(p.loopback)))
	if err != nil {
		if ln, err = net.Listen("tcp", net.JoinHostPort(ips[0], "0")); err != nil {
			return fmt.Errorf("listen on %s: %w", ips[0], err)
		}
	}
	host := ln.Addr().String()
	p.server = &http.Server{Handler: lanGate(host, p.serve), ReadHeaderTimeout: 15 * time.Second}
	p.url = "http://" + host
	go func(s *http.Server) { _ = s.Serve(ln) }(p.server)
	return nil
}

func (p *phoneShare) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = p.server.Shutdown(ctx)
		p.server, p.url = nil, ""
	}
}

// handler is /api/local/phone: GET reports, POST turns it on and mints a
// pairing link, DELETE turns it off. The runtime token (or the browser
// cookie the gate turns into it) is required for all three.
func (p *phoneShare) handler(w http.ResponseWriter, r *http.Request) {
	if !equal(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), p.token) {
		writePhoneJSON(w, http.StatusUnauthorized, map[string]string{"detail": "unauthorized"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writePhoneJSON(w, http.StatusOK, p.state())
	case http.MethodPost:
		if err := p.open(); err != nil {
			writePhoneJSON(w, http.StatusConflict, map[string]string{"detail": err.Error()})
			return
		}
		code, expires, err := p.mint()
		if err != nil {
			writePhoneJSON(w, http.StatusInternalServerError, map[string]string{"detail": err.Error()})
			return
		}
		st := p.state()
		st.PairURL = st.URL + "/pair#code=" + url.QueryEscape(code)
		st.ExpiresAt = float64(expires.Unix())
		writePhoneJSON(w, http.StatusOK, st)
	case http.MethodDelete:
		p.close()
		writePhoneJSON(w, http.StatusOK, p.state())
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func writePhoneJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// lanGate fronts the app on the LAN address: the request must name that
// address (no DNS rebinding) and come from its own origin, and the
// runtime's own controls are not reachable from the network at all. Who may
// use the API is then the app's token-mode check: a paired device, nothing
// else.
func lanGate(host string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Host, host) {
			http.Error(w, "wrong address", http.StatusMisdirectedRequest)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r.Host) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
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

// Phone turns phone access on (on) and returns a fresh pairing link, or
// turns it off.
func Phone(ctx context.Context, ep Endpoint, on bool) (PhoneState, error) {
	method := http.MethodPost
	if !on {
		method = http.MethodDelete
	}
	req, err := http.NewRequestWithContext(ctx, method, ep.URL+phoneRoute, nil)
	if err != nil {
		return PhoneState{}, err
	}
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return PhoneState{}, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return PhoneState{}, errors.New("the running local runtime is older than this CLI; run `lectern local stop`, then try again")
	}
	if res.StatusCode != http.StatusOK {
		var e struct {
			Detail string `json:"detail"`
		}
		_ = json.NewDecoder(res.Body).Decode(&e)
		if e.Detail == "" {
			e.Detail = res.Status
		}
		return PhoneState{}, errors.New(e.Detail)
	}
	var st PhoneState
	return st, json.NewDecoder(res.Body).Decode(&st)
}
