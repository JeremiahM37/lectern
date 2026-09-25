package isolation

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// startProxy serves p on a fresh loopback listener and returns its address,
// stopping the server when the test ends.
func startProxy(t *testing.T, p *AllowlistProxy) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go p.Serve(l)
	t.Cleanup(func() { l.Close() })
	return l.Addr().String()
}

func TestAllowlistProxyConnectAllowedPasses(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello from upstream")
	}))
	defer upstream.Close()
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")

	proxy := NewAllowlistProxy([]string{upstreamHost})
	addr := startProxy(t, proxy)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", upstreamHost, upstreamHost)
	r := bufio.NewReader(conn)
	status, err := r.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "200") {
		t.Fatalf("expected a 200 for an allowed CONNECT, got %q", status)
	}
	// consume the blank line ending the CONNECT response headers
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimRight(line, "\r\n") == "" {
			break
		}
	}
	// The tunnel is now a raw pipe to upstream: send a plain HTTP request
	// through it, exactly as a TLS ClientHello would ride through in reality.
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", upstreamHost)
	body, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "hello from upstream") {
		t.Fatalf("expected the upstream's response through the tunnel, got %q", body)
	}
}

func TestAllowlistProxyConnectDeniedRefused(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "should never be reached")
	}))
	defer upstream.Close()
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")

	// The allowlist names a different host entirely — upstream is denied.
	proxy := NewAllowlistProxy([]string{"example.com"})
	addr := startProxy(t, proxy)

	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", upstreamHost, upstreamHost)
	r := bufio.NewReader(conn)
	status, err := r.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "403") {
		t.Fatalf("expected a 403 for a denied CONNECT, got %q", status)
	}
}

func TestAllowlistProxyForwardAllowedAndDenied(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hook received: "+r.URL.Path)
	}))
	defer upstream.Close()
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")

	proxy := NewAllowlistProxy([]string{upstreamHost})
	addr := startProxy(t, proxy)

	proxyURL, err := url.Parse("http://" + addr)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	// Allowed: this is exactly how a hook callback (plain http://, not
	// https://) reaches Lectern through HTTP_PROXY — see docs/isolation.md.
	resp, err := client.Get(upstream.URL + "/api/hook/session/5")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "/api/hook/session/5") {
		t.Fatalf("expected the forwarded hook call to succeed, got %d %q", resp.StatusCode, body)
	}

	// Denied: some other plain-HTTP host the allowlist never named.
	resp2, err := client.Get("http://example.invalid/steal")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for a denied forward, got %d", resp2.StatusCode)
	}
}

func TestAllowlistProxyWildcard(t *testing.T) {
	p := NewAllowlistProxy([]string{"*.example.com"})
	if !p.allowed("api.example.com:443") {
		t.Error("expected a subdomain to match the wildcard")
	}
	if !p.allowed("example.com") {
		t.Error("expected the bare wildcard root to match too")
	}
	if p.allowed("evil-example.com") {
		t.Error("a wildcard must not match a host that merely ends with the suffix without the dot")
	}
	if p.allowed("example.org") {
		t.Error("a different TLD must not match")
	}
}
