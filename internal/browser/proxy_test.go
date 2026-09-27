package browser

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestInjectionAllowlist(t *testing.T) {
	doc := func(mod func(*http.Request, *http.Response)) (*http.Request, *http.Response) {
		req := httptest.NewRequest("GET", "http://view/", nil)
		req.Header.Set("Sec-Fetch-Dest", "iframe")
		req.Header.Set("Sec-Fetch-Site", "same-site")
		resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/html; charset=utf-8"}}}
		if mod != nil {
			mod(req, resp)
		}
		return req, resp
	}
	req, resp := doc(nil)
	if !Injectable(true, req, resp) {
		t.Fatal("a framed HTML document in Design Mode was not injectable")
	}
	cases := map[string]func(*http.Request, *http.Response){
		"script fetch":    func(r *http.Request, _ *http.Response) { r.Header.Set("Sec-Fetch-Dest", "script") },
		"xhr":             func(r *http.Request, _ *http.Response) { r.Header.Set("Sec-Fetch-Dest", "empty") },
		"cross-site nav":  func(r *http.Request, _ *http.Response) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"POST":            func(r *http.Request, _ *http.Response) { r.Method = "POST" },
		"json":            func(_ *http.Request, w *http.Response) { w.Header.Set("Content-Type", "application/json") },
		"javascript":      func(_ *http.Request, w *http.Response) { w.Header.Set("Content-Type", "text/javascript") },
		"no content type": func(_ *http.Request, w *http.Response) { w.Header.Del("Content-Type") },
		"compressed":      func(_ *http.Request, w *http.Response) { w.Header.Set("Content-Encoding", "gzip") },
		"download": func(_ *http.Request, w *http.Response) {
			w.Header.Set("Content-Disposition", "attachment; filename=x.html")
		},
		"error page":          func(_ *http.Request, w *http.Response) { w.StatusCode = 500 },
		"redirect":            func(_ *http.Request, w *http.Response) { w.StatusCode = 302 },
		"no request recorded": nil,
	}
	for name, mod := range cases {
		req, resp := doc(mod)
		if name == "no request recorded" {
			req = nil
		}
		if Injectable(true, req, resp) {
			t.Errorf("%s: injectable", name)
		}
	}
	req, resp = doc(nil)
	if Injectable(false, req, resp) {
		t.Fatal("injectable with Design Mode off")
	}
	if got := string(InjectPicker([]byte(`<!doctype html><HTML lang=en><Head data-x="1"><title>t</title>`))); got !=
		`<!doctype html><HTML lang=en><Head data-x="1"><script src="/__lectern/design.js"></script><title>t</title>` {
		t.Fatalf("injected: %s", got)
	}
}

type viewFixture struct {
	view     *View
	base     string
	upstream int
	seen     chan *http.Request
}

func newViewFixture(t *testing.T, tlsConf *tls.Config) *viewFixture {
	t.Helper()
	f := &viewFixture{seen: make(chan *http.Request, 64)}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.seen <- r
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		w.Header().Set("Set-Cookie", "app=1; Domain=localhost; Path=/")
		io.WriteString(w, `<html><head><title>App</title></head><body>app page</body></html>`)
	})
	mux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		io.WriteString(w, `console.log("<head>")`)
	})
	mux.HandleFunc("/gz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Encoding", "gzip")
		z := gzip.NewWriter(w)
		io.WriteString(z, `<html><head></head>zipped</html>`)
		z.Close()
	})
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/elsewhere", http.StatusFound)
	})
	mux.HandleFunc("/home", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost:"+strconv.Itoa(f.upstream)+"/", http.StatusFound)
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.CloseNow()
		typ, msg, err := c.Read(r.Context())
		if err == nil {
			_ = c.Write(r.Context(), typ, append([]byte("echo:"), msg...))
		}
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	up := &http.Server{Handler: mux}
	go up.Serve(ln)
	t.Cleanup(func() { up.Close() })
	f.upstream = ln.Addr().(*net.TCPAddr).Port
	v, err := OpenView(localDial, f.upstream, "127.0.0.1", 0, 0, tlsConf, "http://lectern.test:9110")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(v.Close)
	f.view = v
	scheme := "http"
	if tlsConf != nil {
		scheme = "https"
	}
	f.base = scheme + "://127.0.0.1:" + strconv.Itoa(v.ListenPort)
	return f
}

func client(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Timeout: 10 * time.Second,
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func get(t *testing.T, c *http.Client, u string, headers ...string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", u, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func openWithTicket(t *testing.T, f *viewFixture, c *http.Client) {
	t.Helper()
	ticket := f.view.Ticket()
	resp, _ := get(t, c, f.base+"/?a=1&"+TicketParam+"="+ticket)
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/?a=1" {
		t.Fatalf("ticket exchange: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	u, _ := url.Parse(f.base)
	jarred := false
	for _, ck := range c.Jar.Cookies(u) {
		jarred = jarred || strings.HasPrefix(ck.Name, ViewCookiePrefix)
	}
	if !jarred {
		t.Fatal("no view cookie after the ticket")
	}
	// A ticket works once.
	if resp, _ := get(t, client(t), f.base+"/?"+TicketParam+"="+ticket); resp.StatusCode != 403 {
		t.Fatalf("a used ticket was accepted: %d", resp.StatusCode)
	}
}

func TestViewNeedsATicketAndHidesLecternCookies(t *testing.T) {
	f := newViewFixture(t, nil)
	c := client(t)
	if resp, body := get(t, c, f.base+"/"); resp.StatusCode != 403 || strings.Contains(body, "app page") {
		t.Fatalf("an unticketed request reached the app: %d", resp.StatusCode)
	}
	if resp, _ := get(t, c, f.base+"/?"+TicketParam+"=forged"); resp.StatusCode != 403 {
		t.Fatalf("a forged ticket: %d", resp.StatusCode)
	}
	openWithTicket(t, f, c)
	for len(f.seen) > 0 {
		<-f.seen
	}
	u, _ := url.Parse(f.base)
	c.Jar.SetCookies(u, []*http.Cookie{{Name: "lectern_token", Value: "secret"}, {Name: "lectern_device", Value: "d"}, {Name: "theme", Value: "dark"}})
	resp, body := get(t, c, f.base+"/", "Origin", f.base)
	if resp.StatusCode != 200 || !strings.Contains(body, "app page") {
		t.Fatalf("app through the view: %d %s", resp.StatusCode, body)
	}
	up := <-f.seen
	if ck := up.Header.Get("Cookie"); ck != "theme=dark" {
		t.Fatalf("the dev server saw cookies %q", ck)
	}
	if up.Host != "localhost:"+strconv.Itoa(f.upstream) || up.Header.Get("Origin") != "http://localhost:"+strconv.Itoa(f.upstream) {
		t.Fatalf("host %q origin %q", up.Host, up.Header.Get("Origin"))
	}
	if resp.Header.Get("X-Frame-Options") != "" || resp.Header.Get("Content-Security-Policy") != "default-src 'self'" {
		t.Fatalf("frame headers: %v", resp.Header)
	}
	if sc := resp.Header.Get("Set-Cookie"); strings.Contains(strings.ToLower(sc), "domain") {
		t.Fatalf("set-cookie kept its domain: %s", sc)
	}
}

func TestViewInjectsOnlyInDesignModeDocuments(t *testing.T) {
	f := newViewFixture(t, nil)
	c := client(t)
	openWithTicket(t, f, c)
	nav := []string{"Sec-Fetch-Dest", "iframe", "Sec-Fetch-Site", "same-site"}
	const page = `<html><head><title>App</title></head><body>app page</body></html>`
	if _, body := get(t, c, f.base+"/", nav...); body != page {
		t.Fatalf("Design Mode off changed the page: %s", body)
	}
	if resp, _ := get(t, c, f.base+PickerPath); resp.StatusCode != 404 {
		t.Fatalf("picker served with Design Mode off: %d", resp.StatusCode)
	}
	f.view.SetDesign(true)
	// A revalidation must not get a 304: the browser would reuse its copy
	// without the picker.
	for len(f.seen) > 0 {
		<-f.seen
	}
	resp, body := get(t, c, f.base+"/", append(nav, "If-None-Match", `"x"`, "If-Modified-Since", "Mon, 01 Jan 2024 00:00:00 GMT")...)
	if up := <-f.seen; up.Header.Get("If-None-Match") != "" || up.Header.Get("If-Modified-Since") != "" {
		t.Fatalf("a conditional document request reached the dev server in Design Mode: %v", up.Header)
	}
	if !strings.Contains(body, `<head><script src="/__lectern/design.js"></script><title>`) {
		t.Fatalf("no picker in a Design Mode document: %s", body)
	}
	if resp.Header.Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Fatalf("content length %s for %d bytes", resp.Header.Get("Content-Length"), len(body))
	}
	// Everything that is not a same-site HTML document passes untouched.
	if _, body := get(t, c, f.base+"/app.js", "Sec-Fetch-Dest", "script"); body != `console.log("<head>")` {
		t.Fatalf("script changed: %s", body)
	}
	if _, body := get(t, c, f.base+"/", "Sec-Fetch-Dest", "empty"); body != page {
		t.Fatalf("fetch() response changed: %s", body)
	}
	if _, body := get(t, c, f.base+"/", "Sec-Fetch-Dest", "document", "Sec-Fetch-Site", "cross-site"); body != page {
		t.Fatalf("cross-site navigation got the picker: %s", body)
	}
	// Go's client asked for gzip and decoded it; the proxy asked upstream for
	// identity on a document, and the upstream ignored that, so no injection.
	if _, body := get(t, c, f.base+"/gz", nav...); strings.Contains(body, "design.js") {
		t.Fatalf("a compressed body was injected: %s", body)
	}
	// Another origin is never proxied: a redirect away leaves the view as is.
	resp, body = get(t, c, f.base+"/away", nav...)
	if resp.Header.Get("Location") != "https://example.com/elsewhere" || strings.Contains(body, "design.js") {
		t.Fatalf("external redirect: %s %s", resp.Header.Get("Location"), body)
	}
	resp, _ = get(t, c, f.base+"/home", nav...)
	if resp.Header.Get("Location") != "/" {
		t.Fatalf("a redirect to the app's own localhost left the view: %s", resp.Header.Get("Location"))
	}
	resp, js := get(t, c, f.base+PickerPath)
	if resp.StatusCode != 200 || !strings.Contains(js, `"parentOrigin":"http://lectern.test:9110"`) || !strings.Contains(js, `"mode":"frame"`) {
		t.Fatalf("picker script: %d %.200s", resp.StatusCode, js)
	}
	f.view.SetDesign(false)
	if _, body := get(t, c, f.base+"/", nav...); body != page {
		t.Fatalf("injection outlived Design Mode: %s", body)
	}
}

func TestViewCarriesWebsocketsAndTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	f := newViewFixture(t, srv.TLS)
	c := client(t)
	openWithTicket(t, f, c)
	resp, body := get(t, c, f.base+"/")
	if resp.TLS == nil || !strings.Contains(body, "app page") {
		t.Fatalf("https through the view: %v %s", resp.TLS, body)
	}
	// Plain http answers on the same port.
	plain := strings.Replace(f.base, "https://", "http://", 1)
	if resp, _ := get(t, client(t), plain+"/"); resp.StatusCode != 403 {
		t.Fatalf("plain http on the TLS port: %d", resp.StatusCode)
	}
	u, _ := url.Parse(f.base)
	var cookie string
	for _, ck := range c.Jar.Cookies(u) {
		cookie = ck.Name + "=" + ck.Value
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, strings.Replace(f.base, "https", "wss", 1)+"/ws", &websocket.DialOptions{
		HTTPClient: c, HTTPHeader: http.Header{"Cookie": {cookie}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	_ = ws.Write(ctx, websocket.MessageText, []byte("hmr"))
	_, msg, err := ws.Read(ctx)
	if err != nil || string(msg) != "echo:hmr" {
		t.Fatalf("websocket through the view: %q %v", msg, err)
	}
}

func TestLoopbackProxyReachesTheTargetsLocalhost(t *testing.T) {
	// The "target" loopback is a separate listener the dialer maps to.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "target") }))
	defer target.Close()
	tport := target.Listener.Addr().(*net.TCPAddr).Port
	var dialed []string
	p, err := StartLoopbackProxy(func(ctx context.Context, addr string) (net.Conn, error) {
		dialed = append(dialed, addr)
		return localDial(ctx, target.Listener.Addr().String())
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	proxyURL, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(p.Port))
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 10 * time.Second}
	resp, err := c.Get("http://localhost:3000/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "target" || len(dialed) != 1 || dialed[0] != "127.0.0.1:3000" {
		t.Fatalf("got %q via %v (target on %d)", body, dialed, tport)
	}
}
