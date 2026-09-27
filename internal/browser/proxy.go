package browser

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// View serves one port of a target's localhost on an origin of its own, so the
// Browser pane can frame a dev server exactly as it runs: absolute asset paths,
// redirects and hot-reload websockets all work, which a path-prefix proxy
// breaks.
//
// A view is not an open door like a raw port forward. Every request needs a
// cookie the view sets only after it is opened with a single-use ticket, and
// tickets are minted by an authenticated Lectern API call. Lectern's own
// cookies are removed before anything reaches the dev server.
//
// Design Mode is the one place the proxy changes a response: while it is on,
// HTML documents from this view get one same-origin script tag for the picker.
// Nothing else is ever injected, and nothing from any other origin passes
// through here at all.
type View struct {
	ID           string
	Port         int // on the target's loopback
	ListenPort   int
	ParentOrigin string
	Created      float64

	secret  string
	design  atomic.Bool
	mu      sync.Mutex
	tickets map[string]time.Time
	ln      net.Listener
	srv     *http.Server
	proxy   *httputil.ReverseProxy
	TLS     bool // the listener also speaks TLS (it sniffs each connection)
}

// ViewCookiePrefix names the cookie each view sets; it is also stripped from
// what reaches the dev server, with Lectern's own cookies.
const ViewCookiePrefix = "lectern_view_"

// TicketParam carries a single-use ticket in the first URL a view is opened at.
const TicketParam = "__lectern_ticket"

// PickerPath is where a view serves the picker while Design Mode is on.
const PickerPath = "/__lectern/design.js"

func randomHex(n int) string {
	raw := make([]byte, n)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

var originRe = regexp.MustCompile(`^https?://[A-Za-z0-9.\-\[\]:]+$`)

// ValidOrigin reports whether s is a bare http(s) origin.
func ValidOrigin(s string) bool { return originRe.MatchString(s) }

// OpenView listens on the first free port in [lo, hi] on bindHost. tlsConf,
// when set, lets the same port answer https as well, so a view can be framed by
// a Lectern page served over TLS.
func OpenView(dial Dial, port int, bindHost string, lo, hi int, tlsConf *tls.Config, parentOrigin string) (*View, error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("port must be between 1 and 65535")
	}
	if !ValidOrigin(parentOrigin) {
		return nil, fmt.Errorf("parent_origin must be the http(s) origin of the Lectern page")
	}
	var ln net.Listener
	for p := lo; p <= hi; p++ {
		l, err := net.Listen("tcp", net.JoinHostPort(bindHost, strconv.Itoa(p)))
		if err == nil {
			ln = l
			break
		}
	}
	if ln == nil {
		return nil, errors.New("no free port for a browser view; close one that is no longer needed")
	}
	v := &View{ID: randomHex(8), Port: port, ListenPort: ln.Addr().(*net.TCPAddr).Port, ParentOrigin: parentOrigin,
		Created: now(), secret: randomHex(24), tickets: map[string]time.Time{}, ln: ln, TLS: tlsConf != nil}
	upstream := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	v.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) { v.rewrite(pr, port) },
		Transport: &http.Transport{
			DialContext:           func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx, upstream) },
			ResponseHeaderTimeout: 60 * time.Second,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       30 * time.Second,
		},
		ModifyResponse: func(resp *http.Response) error { return v.modify(resp, port) },
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `<!doctype html><title>Nothing on port %d</title><body style="font:15px system-ui;padding:2em;color:#555">`+
				`<h3>Nothing is answering on localhost:%d</h3><p>Start the dev server, then reload.</p>`, port, port)
		},
	}
	v.srv = &http.Server{Handler: v, ReadHeaderTimeout: 20 * time.Second}
	var serve net.Listener = ln
	if tlsConf != nil {
		serve = &sniffListener{Listener: ln, tls: tlsConf}
	}
	go func() { _ = v.srv.Serve(serve) }()
	return v, nil
}

// Close stops the view.
func (v *View) Close() { _ = v.srv.Close() }

// SetDesign turns picker injection on or off for this view's documents.
func (v *View) SetDesign(on bool) { v.design.Store(on) }

// Design reports whether Design Mode is on for this view.
func (v *View) Design() bool { return v.design.Load() }

// Ticket mints a single-use ticket, good for two minutes, that opens the view
// in one browser.
func (v *View) Ticket() string {
	t := randomHex(16)
	v.mu.Lock()
	defer v.mu.Unlock()
	cut := time.Now()
	for k, exp := range v.tickets {
		if cut.After(exp) {
			delete(v.tickets, k)
		}
	}
	v.tickets[t] = time.Now().Add(2 * time.Minute)
	return t
}

func (v *View) redeem(t string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	exp, ok := v.tickets[t]
	if ok {
		delete(v.tickets, t)
	}
	return ok && time.Now().Before(exp)
}

func (v *View) cookieName() string { return ViewCookiePrefix + strconv.Itoa(v.ListenPort) }

func (v *View) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if t := r.URL.Query().Get(TicketParam); t != "" {
		if !v.redeem(t) {
			v.refuse(w, "This link has already been used or has expired. Open the page again from Lectern.")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: v.cookieName(), Value: v.secret, Path: "/", HttpOnly: true,
			Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode})
		clean := *r.URL
		q := clean.Query()
		q.Del(TicketParam)
		clean.RawQuery = q.Encode()
		clean.Scheme, clean.Host = "", ""
		if clean.Path == "" {
			clean.Path = "/"
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, clean.String(), http.StatusFound)
		return
	}
	c, err := r.Cookie(v.cookieName())
	if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(v.secret)) != 1 {
		v.refuse(w, "Open this page from Lectern's Browser pane.")
		return
	}
	if r.URL.Path == PickerPath {
		if !v.Design() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		io.WriteString(w, PickerScript("frame", v.ParentOrigin))
		return
	}
	v.proxy.ServeHTTP(w, r)
}

func (v *View) refuse(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprintf(w, `<!doctype html><title>Lectern</title><body style="font:15px system-ui;padding:2em;color:#555">%s`, msg)
}

// StripLecternCookies removes Lectern's own credentials and every view cookie
// from a Cookie header, so a dev server never sees them.
func StripLecternCookies(h http.Header) {
	var keep []string
	for _, line := range h.Values("Cookie") {
		for _, part := range strings.Split(line, ";") {
			name, _, _ := strings.Cut(strings.TrimSpace(part), "=")
			if name == "" || strings.HasPrefix(name, "lectern_") || strings.HasPrefix(name, "agentdeck_") {
				continue
			}
			keep = append(keep, strings.TrimSpace(part))
		}
	}
	h.Del("Cookie")
	if len(keep) > 0 {
		h.Set("Cookie", strings.Join(keep, "; "))
	}
}

func (v *View) rewrite(pr *httputil.ProxyRequest, port int) {
	in, out := pr.In, pr.Out
	out.URL.Scheme, out.URL.Host = "http", net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	// Dev servers refuse hosts they do not know (Vite's allowedHosts, for
	// one), so the request looks as it would from a browser on the target.
	local := "localhost:" + strconv.Itoa(port)
	out.Host = local
	self := viewOrigin(in)
	for _, h := range []string{"Origin", "Referer"} {
		if val := out.Header.Get(h); val != "" && strings.HasPrefix(val, self) {
			out.Header.Set(h, "http://"+local+strings.TrimPrefix(val, self))
		}
	}
	StripLecternCookies(out.Header)
	out.Header.Del("X-Forwarded-For")
	pr.SetXForwarded()
	if v.Design() && documentRequest(in) {
		// Only an uncompressed, full body can take the script tag: a 304
		// would have the browser reuse the copy it cached without one.
		out.Header.Set("Accept-Encoding", "identity")
		for _, h := range []string{"If-None-Match", "If-Modified-Since", "If-Match", "If-Unmodified-Since", "If-Range", "Range"} {
			out.Header.Del(h)
		}
	}
}

func viewOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// documentRequest is a top-level or framed page load: the only request whose
// response may carry the picker.
func documentRequest(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	switch r.Header.Get("Sec-Fetch-Dest") {
	case "", "document", "iframe":
	default:
		return false
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "same-site", "none":
	default:
		// A page on another site navigating here gets the app, never the picker.
		return false
	}
	return true
}

// Injectable is the whole allowlist for Design Mode injection: Design Mode on,
// a same-site document request, and a successful, uncompressed, inline HTML
// response. Everything else passes through byte for byte.
func Injectable(design bool, req *http.Request, resp *http.Response) bool {
	if !design || req == nil || !documentRequest(req) {
		return false
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 || resp.StatusCode == http.StatusNoContent {
		return false
	}
	mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mt != "text/html" {
		return false
	}
	if enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))); enc != "" && enc != "identity" {
		return false
	}
	if d := strings.ToLower(resp.Header.Get("Content-Disposition")); strings.HasPrefix(d, "attachment") {
		return false
	}
	return true
}

var headOpen = regexp.MustCompile(`(?i)<head(\s[^>]*)?>`)
var htmlOpen = regexp.MustCompile(`(?i)<html(\s[^>]*)?>`)

// InjectPicker puts the picker's script tag at the top of a document.
func InjectPicker(doc []byte) []byte {
	tag := []byte(`<script src="` + PickerPath + `"></script>`)
	for _, re := range []*regexp.Regexp{headOpen, htmlOpen} {
		if loc := re.FindIndex(doc); loc != nil {
			out := make([]byte, 0, len(doc)+len(tag))
			out = append(out, doc[:loc[1]]...)
			out = append(out, tag...)
			return append(out, doc[loc[1]:]...)
		}
	}
	return append(tag, doc...)
}

const maxInjectBody = 8 << 20

func (v *View) modify(resp *http.Response, port int) error {
	h := resp.Header
	// The pane must be able to frame the app. Only the owner can open a view,
	// so this does not expose the app to anyone else's frames.
	h.Del("X-Frame-Options")
	if csp := h.Values("Content-Security-Policy"); len(csp) > 0 {
		h.Del("Content-Security-Policy")
		for _, policy := range csp {
			if p := dropDirective(policy, "frame-ancestors"); p != "" {
				h.Add("Content-Security-Policy", p)
			}
		}
	}
	// A redirect to the app's own localhost stays inside the view.
	if loc := h.Get("Location"); loc != "" {
		if u, err := url.Parse(loc); err == nil && u.IsAbs() {
			if (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1") && u.Port() == strconv.Itoa(port) {
				u.Scheme, u.Host = "", ""
				h.Set("Location", u.String())
			}
		}
	}
	if cookies := h.Values("Set-Cookie"); len(cookies) > 0 {
		h.Del("Set-Cookie")
		for _, c := range cookies {
			h.Add("Set-Cookie", dropCookieDomain(c))
		}
	}
	// A page the browser keeps in its cache comes back without a request, so
	// turning Design Mode on would reload the copy that has no picker. Many
	// dev and static servers let HTML be cached heuristically (Last-Modified,
	// no Cache-Control): python -m http.server does. So no document this view
	// serves is stored; its scripts, styles and images cache as the app says.
	if Injectable(true, resp.Request, resp) {
		h.Set("Cache-Control", "no-store")
		h.Del("Expires")
	}
	if !Injectable(v.Design(), resp.Request, resp) {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxInjectBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxInjectBody {
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), resp.Body), resp.Body}
		return nil
	}
	_ = resp.Body.Close()
	body = InjectPicker(body)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Del("ETag")
	h.Set("Cache-Control", "no-store")
	return nil
}

func dropDirective(policy, name string) string {
	var keep []string
	for _, d := range strings.Split(policy, ";") {
		if f := strings.Fields(d); len(f) > 0 && strings.EqualFold(f[0], name) {
			continue
		}
		if strings.TrimSpace(d) != "" {
			keep = append(keep, strings.TrimSpace(d))
		}
	}
	return strings.Join(keep, "; ")
}

func dropCookieDomain(c string) string {
	parts := strings.Split(c, ";")
	out := parts[:1]
	for _, p := range parts[1:] {
		if k, _, _ := strings.Cut(strings.TrimSpace(p), "="); strings.EqualFold(k, "domain") {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, ";")
}

// sniffListener serves TLS and plain HTTP on one port: a TLS handshake starts
// with byte 0x16, which no HTTP request does. Each connection is sniffed on its
// own goroutine, so a slow client cannot hold up the others.
type sniffListener struct {
	net.Listener
	tls    *tls.Config
	once   sync.Once
	ready  chan net.Conn
	errs   chan error
	closed chan struct{}
	shut   sync.Once
}

func (l *sniffListener) Close() error {
	l.init()
	l.shut.Do(func() { close(l.closed) })
	return l.Listener.Close()
}

func (l *sniffListener) init() {
	l.once.Do(func() {
		l.ready, l.errs, l.closed = make(chan net.Conn), make(chan error, 1), make(chan struct{})
		go l.loop()
	})
}

type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func (l *sniffListener) loop() {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			l.errs <- err
			return
		}
		go func() {
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			r := bufio.NewReader(conn)
			first, err := r.Peek(1)
			_ = conn.SetReadDeadline(time.Time{})
			if err != nil {
				conn.Close()
				return
			}
			var out net.Conn = &peekedConn{Conn: conn, r: r}
			if first[0] == 0x16 {
				out = tls.Server(out, l.tls)
			}
			select {
			case l.ready <- out:
			case <-l.closed:
				out.Close()
			}
		}()
	}
}

func (l *sniffListener) Accept() (net.Conn, error) {
	l.init()
	select {
	case c := <-l.ready:
		return c, nil
	case err := <-l.errs:
		l.errs <- err
		return nil, err
	}
}
