package browser

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// LoopbackProxy lets a browser on the control plane stand in for one on a
// target that has none: the browser's requests for localhost go to the
// target's loopback, and everything else goes out from here. It listens on
// the control plane's own loopback, for as long as that browser lives, and on
// Linux accepts only connections from processes of this same user.
type LoopbackProxy struct {
	Port int
	ln   net.Listener
	srv  *http.Server
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// StartLoopbackProxy starts the proxy. dial reaches the target's loopback.
func StartLoopbackProxy(dial Dial) (*LoopbackProxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	route := func(ctx context.Context, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if isLoopbackHost(host) {
			return dial(ctx, net.JoinHostPort("127.0.0.1", port))
		}
		return (&net.Dialer{Timeout: 20 * time.Second}).DialContext(ctx, "tcp", addr)
	}
	forward := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = pr.In.URL
			pr.Out.Host = pr.In.Host
			pr.Out.Header.Del("Proxy-Connection")
			pr.Out.Header.Del("Proxy-Authorization")
		},
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) { return route(ctx, addr) },
			ResponseHeaderTimeout: 60 * time.Second, IdleConnTimeout: 30 * time.Second},
	}
	p := &LoopbackProxy{Port: ln.Addr().(*net.TCPAddr).Port, ln: ln}
	p.srv = &http.Server{ReadHeaderTimeout: 20 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			tunnel(w, r, route)
			return
		}
		if !r.URL.IsAbs() {
			http.Error(w, "this is a proxy", http.StatusBadRequest)
			return
		}
		forward.ServeHTTP(w, r)
	})}
	go func() { _ = p.srv.Serve(&sameUserListener{ln}) }()
	return p, nil
}

// Close stops the proxy.
func (p *LoopbackProxy) Close() { _ = p.srv.Close() }

func tunnel(w http.ResponseWriter, r *http.Request, route func(context.Context, string) (net.Conn, error)) {
	addr := r.Host
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "443")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	upstream, err := route(ctx, addr)
	cancel()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "cannot tunnel", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	_, _ = io.WriteString(client, "HTTP/1.1 200 Connection established\r\n\r\n")
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, readerOf(buf, client)); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
	client.Close()
	upstream.Close()
}

func readerOf(buf *bufio.ReadWriter, conn net.Conn) io.Reader {
	if buf != nil && buf.Reader.Buffered() > 0 {
		return io.MultiReader(io.LimitReader(buf.Reader, int64(buf.Reader.Buffered())), conn)
	}
	return conn
}

// sameUserListener drops connections from other local users where the
// kernel says who they are.
type sameUserListener struct{ net.Listener }

func (l *sameUserListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if peerIsSameUser(c) {
			return c, nil
		}
		c.Close()
	}
}

// peerIsSameUser looks the client's socket up in /proc/net/tcp, whose rows
// name the owning uid. Where that table does not exist it allows the
// connection: the listener is loopback-only either way.
func peerIsSameUser(c net.Conn) bool {
	if runtime.GOOS != "linux" {
		return true
	}
	remote, ok := c.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return false
	}
	want := fmt.Sprintf(":%04X", remote.Port)
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		raw, err := os.ReadFile(table)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n")[1:] {
			f := strings.Fields(line)
			if len(f) < 8 || !strings.HasSuffix(f[1], want) {
				continue
			}
			uid, err := strconv.Atoi(f[7])
			return err == nil && uid == os.Getuid()
		}
	}
	return true
}
