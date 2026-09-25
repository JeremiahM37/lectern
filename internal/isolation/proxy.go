package isolation

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// AllowlistProxy is the "deny" network policy's one egress path. A sandboxed
// process reaches it over HTTP_PROXY/HTTPS_PROXY convention (both plain-HTTP
// forwarding and HTTPS CONNECT tunneling are handled), and only a host in
// Allow is let through. Everything else gets 403 before a single byte
// crosses the boundary.
//
// This is deliberately a plain host allowlist, not a full egress firewall:
// it does not inspect TLS SNI against the CONNECT target (they are the same
// value here, since the client names the host in the CONNECT line itself,
// but a process that lies about its own CONNECT target and then speaks TLS
// to a different SNI inside the tunnel is not caught). See docs/isolation.md.
type AllowlistProxy struct {
	// Allow is the set of hostnames (bare, e.g. "api.anthropic.com") or
	// "*.suffix" wildcards this proxy accepts. Checked case-insensitively.
	Allow []string
	// Dial defaults to a plain net.Dialer; tests override it to point at a
	// fixture without touching a real socket.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// NewAllowlistProxy builds a proxy that only accepts the given hosts.
func NewAllowlistProxy(allow []string) *AllowlistProxy {
	return &AllowlistProxy{Allow: append([]string(nil), allow...)}
}

func (p *AllowlistProxy) dial() func(ctx context.Context, network, addr string) (net.Conn, error) {
	if p.Dial != nil {
		return p.Dial
	}
	d := &net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext
}

// allowed reports whether host (optionally "host:port") may be reached. The
// allowlist is host-only — a CONNECT or forwarded request's port is never
// part of the match, so "api.anthropic.com" also allows it on any port and
// Allow entries never need to spell one out.
func (p *AllowlistProxy) allowed(host string) bool {
	host = bareHost(host)
	for _, raw := range p.Allow {
		a := bareHost(strings.TrimSpace(raw))
		if a == "" {
			continue
		}
		if strings.HasPrefix(a, "*.") {
			suffix := a[1:] // ".example.com"
			bare := a[2:]   // "example.com"
			if host == bare || strings.HasSuffix(host, suffix) {
				return true
			}
			continue
		}
		if host == a {
			return true
		}
	}
	return false
}

// bareHost strips an optional ":port" and lowercases what remains.
func bareHost(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(host)
}

// ServeHTTP implements http.Handler: CONNECT tunnels (HTTPS and anything
// else that CONNECTs first); every other request is treated as a standard
// forward-proxy request with an absolute-URI request line, which is how
// curl/npm/pip and Go's own http.Transport all speak to HTTP_PROXY for a
// plain-http:// target — the path the session's own hook callback uses.
func (p *AllowlistProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.serveConnect(w, r)
		return
	}
	host := r.Host
	if host == "" && r.URL != nil {
		host = r.URL.Host
	}
	if !p.allowed(host) {
		http.Error(w, "isolation: host not allowed: "+host, http.StatusForbidden)
		return
	}
	p.serveForward(w, r, host)
}

func (p *AllowlistProxy) serveConnect(w http.ResponseWriter, r *http.Request) {
	if !p.allowed(r.Host) {
		http.Error(w, "isolation: host not allowed: "+r.Host, http.StatusForbidden)
		return
	}
	upstream, err := p.dial()(r.Context(), "tcp", r.Host)
	if err != nil {
		http.Error(w, "isolation: upstream unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "isolation: proxy cannot hijack the connection", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		client.Close()
		upstream.Close()
		return
	}
	done := make(chan struct{}, 2)
	go func() { io.Copy(upstream, client); upstream.Close(); done <- struct{}{} }()
	go func() { io.Copy(client, upstream); client.Close(); done <- struct{}{} }()
	<-done
}

func (p *AllowlistProxy) serveForward(w http.ResponseWriter, r *http.Request, host string) {
	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	if outReq.URL.Host == "" {
		outReq.URL.Host = host
	}
	if outReq.URL.Scheme == "" {
		outReq.URL.Scheme = "http"
	}
	tr := &http.Transport{DialContext: p.dial()}
	resp, err := tr.RoundTrip(outReq)
	if err != nil {
		http.Error(w, "isolation: forward failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// Serve runs the proxy on l until it is closed. It never returns a nil
// error: use errors.Is(err, net.ErrClosed) (or a Close from the caller) to
// tell a deliberate shutdown from a real failure.
func (p *AllowlistProxy) Serve(l net.Listener) error {
	srv := &http.Server{Handler: p}
	return srv.Serve(l)
}
