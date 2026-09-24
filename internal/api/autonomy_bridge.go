package api

import (
	"context"
	"errors"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Public-only proxy pins the validated DNS answer into DialContext, so a
// redirect or a rebinding cannot turn a public name into host/LAN access.
var autoDenied = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("224.0.0.0/3"), netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("2001::/32"),
}

func autoPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range autoDenied {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
func autoPublicDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, e := net.SplitHostPort(address)
	if e != nil {
		return nil, e
	}
	if port != "80" && port != "443" {
		return nil, errors.New("only public HTTP/HTTPS ports allowed")
	}
	ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if e != nil {
		return nil, e
	}
	if len(ips) == 0 {
		return nil, errors.New("no public address")
	}
	for _, ip := range ips {
		if !autoPublicIP(ip) {
			return nil, errors.New("private or special-use destination denied")
		}
	}
	d := net.Dialer{Timeout: 12 * time.Second}
	var last error
	for _, ip := range ips {
		c, e := d.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
		if e == nil {
			return c, nil
		}
		last = e
	}
	return nil, last
}
func autoProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		if !autoInferenceDestination(r.Host) {
			http.Error(w, "external publishing is disabled; use the read-only research bridge", 403)
			return
		}
		dst, e := autoPublicDial(r.Context(), "tcp", r.Host)
		if e != nil {
			http.Error(w, "destination denied or unavailable", 403)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			dst.Close()
			http.Error(w, "proxy unavailable", 500)
			return
		}
		client, rw, e := hj.Hijack()
		if e != nil {
			dst.Close()
			return
		}
		_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = rw.Flush()
		// Each tunnel expires within a job's maximum runtime, even if controller
		// shutdown races the connection's detached goroutine.
		_ = dst.SetDeadline(time.Now().Add(31 * time.Minute))
		_ = client.SetDeadline(time.Now().Add(31 * time.Minute))
		go func() {
			defer dst.Close()
			defer client.Close()
			go func() { _, _ = io.Copy(dst, rw); dst.Close() }()
			_, _ = io.Copy(client, dst)
		}()
		return
	}
	http.Error(w, "direct web requests are disabled; use the read-only research bridge", 403)
}

// Only the subscription inference services may use opaque TLS tunnels.
// Repository hosts, registries, social sites and arbitrary upload endpoints
// are deliberately absent. No wildcard or suffix matching.
func autoInferenceDestination(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" {
		return false
	}
	switch host {
	case "chatgpt.com", "api.openai.com", "api.anthropic.com":
		return true
	}
	return false
}

// Research is fetched by the controller, without worker-supplied credentials,
// cookies, bodies, methods or redirects. Only known reading surfaces are offered.
func autoResearchURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" || len(raw) > 2048 {
		return nil, errors.New("an approved HTTPS reading URL is required")
	}
	switch u.Host {
	case "raw.githubusercontent.com", "docs.python.org", "go.dev", "pkg.go.dev", "developer.mozilla.org", "arxiv.org", "export.arxiv.org", "en.wikipedia.org", "docs.anthropic.com", "code.claude.com", "platform.openai.com":
	default:
		return nil, errors.New("research host is not approved")
	}
	if u.RawQuery != "" {
		return nil, errors.New("research query strings are disabled")
	}
	return u, nil
}

func autoResearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "read only", 405)
		return
	}
	u, err := autoResearchURL(r.URL.Query().Get("url"))
	if err != nil {
		http.Error(w, err.Error(), 403)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		http.Error(w, "invalid URL", 400)
		return
	}
	transport := &http.Transport{DialContext: autoPublicDial, DisableKeepAlives: true, ResponseHeaderTimeout: 20 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		http.Error(w, "research unavailable", 502)
		return
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		http.Error(w, "research source did not return a document (redirects are refused)", 502)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, io.LimitReader(res.Body, 2<<20))
}

func (s *Server) ensureAutoBridges(j *autoJob) error {
	if s.autoBridges == nil {
		s.autoBridges = map[string][]*http.Server{}
	}
	if _, ok := s.autoBridges[j.ID]; ok {
		return nil
	}
	var servers []*http.Server
	success := false
	defer func() {
		if !success {
			for _, srv := range servers {
				_ = srv.Close()
			}
		}
	}()
	for name, handler := range map[string]http.HandlerFunc{"network.sock": autoProxy, "bridge.sock": s.autoReadBridge} {
		dir := filepath.Join(autoRoot, j.ID, "bridges")
		if e := os.MkdirAll(dir, 0755); e != nil {
			return e
		}
		path := filepath.Join(dir, name)
		if st, e := os.Lstat(path); e == nil {
			if st.Mode()&os.ModeSocket == 0 {
				return errors.New("bridge path is not a socket")
			}
			if e = os.Remove(path); e != nil {
				return e
			}
		}
		ln, e := net.Listen("unix", path)
		if e != nil {
			return e
		}
		if e = os.Chmod(path, 0666); e != nil {
			ln.Close()
			return e
		}
		srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
		servers = append(servers, srv)
		go func() {
			_ = srv.Serve(&autoLimitedListener{Listener: ln, slots: make(chan struct{}, 32), done: make(chan struct{})})
		}()
	}
	s.autoBridges[j.ID] = servers
	success = true
	return nil
}
func (s *Server) autoReadBridge(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "read-only bridge", 405)
		return
	}
	switch r.URL.Path {
	case "/research":
		autoResearch(w, r)
		return
	case "/history":
		a, e := s.loadAuto()
		if e != nil {
			http.Error(w, "history unavailable", 503)
			return
		}
		rows := a.Runs
		if len(rows) > 30 {
			rows = rows[len(rows)-30:]
		}
		writeJSON(w, 200, rows)
		return
	case "/projects":
		rows := []map[string]any{}
		for _, p := range s.autoProjects() {
			rows = append(rows, map[string]any{"id": p.ID, "name": p.Name})
		}
		writeJSON(w, 200, rows)
		return
	case "/tasks":
		rows, _ := s.DB.Tasks(store.TaskFilter{})
		out := []map[string]any{}
		for _, t := range rows {
			if t.Status == "done" || t.Status == "failed" {
				continue
			}
			out = append(out, map[string]any{"id": t.ID, "project_id": t.ProjectID, "title": t.Title, "status": t.Status})
			if len(out) >= 100 {
				break
			}
		}
		writeJSON(w, 200, out)
		return
	}
	var suffix string
	switch r.URL.Path {
	case "/grimoire/search":
		q := r.URL.Query().Get("q")
		if len(q) == 0 || len(q) > 300 {
			http.Error(w, "query required (max300)", 400)
			return
		}
		suffix = "/api/search?q=" + url.QueryEscape(q) + "&limit=8"
	case "/grimoire/read":
		p := r.URL.Query().Get("path")
		if len(p) > 500 || !strings.HasSuffix(p, ".md") || strings.Contains(p, "..") || strings.ContainsAny(p, "\\\x00\r\n?#") || strings.HasPrefix(p, "/") {
			http.Error(w, "note path required", 400)
			return
		}
		suffix = "/api/notes/" + url.PathEscape(p)
	default:
		http.NotFound(w, r)
		return
	}
	if s.Cfg.GrimoireURL == "" {
		http.Error(w, "Grimoire unavailable", 503)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(s.Cfg.GrimoireURL, "/")+suffix, nil)
	if e != nil {
		http.Error(w, "context unavailable", 503)
		return
	}
	// No admin token passes through this surface; only ordinary note reads.
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect denied") }}
	resp, e := client.Do(req)
	if e != nil {
		http.Error(w, "Grimoire unavailable", 503)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 128<<10))
}
func (s *Server) closeAutoBridge(id string) {
	for _, srv := range s.autoBridges[id] {
		_ = srv.Close()
	}
	delete(s.autoBridges, id)
}

// downloadAutonomy preserves a useful artifact without exposing the controller's
// credentials, prompts or runtime files. The privileged reader is UUID-scoped.
func (s *Server) downloadAutonomy(w http.ResponseWriter, r *http.Request) {
	if !s.autonomyHuman(w, r) {
		return
	}
	s.autoMu.Lock()
	a, e := s.loadAuto()
	var found *autoJob
	if e == nil {
		for _, j := range a.Jobs {
			if j.ID == r.PathValue("job") {
				copy := *j
				found = &copy
				break
			}
		}
	}
	s.autoMu.Unlock()
	if e != nil {
		respondErr(w, e)
		return
	}
	if found == nil || found.Status == "running" || found.Status == "starting" {
		httpError(w, 404, "completed job artifact not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sudo", "-n", autoRunner, "archive", "--job", found.ID)
	out, e := cmd.StdoutPipe()
	if e != nil {
		respondErr(w, e)
		return
	}
	if e = cmd.Start(); e != nil {
		respondErr(w, e)
		return
	}
	first := make([]byte, 512)
	n, readErr := out.Read(first)
	if n == 0 && readErr != nil {
		_ = cmd.Wait()
		httpError(w, 503, "artifact archive unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", "attachment; filename=workshop-"+found.ID+".tar.gz")
	_, _ = w.Write(first[:n])
	_, _ = io.Copy(w, io.LimitReader(out, 3<<30))
	_ = cmd.Wait()
}

// Bound sockets in the trusted control plane as well as worker processes.
type autoLimitedListener struct {
	net.Listener
	slots chan struct{}
	done  chan struct{}
	once  sync.Once
}

func (l *autoLimitedListener) Accept() (net.Conn, error) {
	select {
	case l.slots <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	c, e := l.Listener.Accept()
	if e != nil {
		<-l.slots
		return nil, e
	}
	return &autoLimitedConn{Conn: c, release: func() { <-l.slots }}, nil
}
func (l *autoLimitedListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}

type autoLimitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *autoLimitedConn) Close() error { e := c.Conn.Close(); c.once.Do(c.release); return e }
