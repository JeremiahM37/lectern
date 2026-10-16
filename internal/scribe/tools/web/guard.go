package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// cgnat is 100.64.0.0/10 (shared address space). Tailnet addresses live
// here, so it must not be reachable through fetch_page.
var (
	cgnat    = mustCIDR("100.64.0.0/10")
	thisNet  = mustCIDR("0.0.0.0/8")
	dialTime = 15 * time.Second
)

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// blockedIP reports whether fetch_page must refuse to connect to ip.
func blockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || cgnat.Contains(ip) || thisNet.Contains(ip)
}

// resolve returns the addresses host may be dialled at. Every address is
// checked: one private answer refuses the whole host, so a mixed DNS answer
// cannot smuggle a local address through.
func (w *webTools) resolve(ctx context.Context, host string) ([]net.IP, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if ip := net.ParseIP(host); ip != nil {
		if !w.opts.AllowPrivate && blockedIP(ip) {
			return nil, fmt.Errorf("refusing to fetch %s: it is a private or local address", host)
		}
		return []net.IP{ip}, nil
	}
	if !w.opts.AllowPrivate && (host == "localhost" || strings.HasSuffix(host, ".localhost")) {
		return nil, fmt.Errorf("refusing to fetch %s: localhost is a private address", host)
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve %s: %v", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("cannot resolve %s: no addresses", host)
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		if !w.opts.AllowPrivate && blockedIP(a.IP) {
			return nil, fmt.Errorf("refusing to fetch %s: it resolves to %s, a private or local address", host, a.IP)
		}
		ips = append(ips, a.IP)
	}
	return ips, nil
}

// checkURL is the early check run before approval and on every redirect:
// scheme, host and resolved addresses.
func (w *webTools) checkURL(ctx context.Context, u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("only http and https URLs can be fetched, not %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("the URL %s has no host name", u.Redacted())
	}
	_, err := w.resolve(ctx, u.Hostname())
	return err
}

// checkRedirect enforces the redirect cap and re-checks each hop.
func (w *webTools) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	return w.checkURL(req.Context(), req.URL)
}

// dial connects only to addresses that passed resolve. Checking here, not
// only before the request, closes the DNS-rebinding gap: the name is looked
// up again and the connection goes to a validated IP.
func (w *webTools) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := w.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	d := &net.Dialer{Timeout: dialTime}
	var last error
	for _, ip := range ips {
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

// guardedTransport never uses environment proxies: a proxy would hide the
// real destination from the address check.
func (w *webTools) guardedTransport() *http.Transport {
	return &http.Transport{
		Proxy:               nil,
		DialContext:         w.dial,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     30 * time.Second,
		MaxIdleConns:        8,
	}
}
