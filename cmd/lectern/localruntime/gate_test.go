package localruntime

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// gateFixture fronts a stand-in app that, like the real one in token mode,
// answers /api/* only with the runtime token.
func gateFixture(t *testing.T) (*gate, *httptest.Server) {
	t.Helper()
	g := newGate("runtime-token-0123456789abcdef0123", "browser-key-0123456789abcdef012345")
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/api/hook/") &&
			r.Header.Get("Authorization") != "Bearer "+g.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	ts := httptest.NewServer(localHandler(app, g, "instance", func() error { return nil }))
	t.Cleanup(ts.Close)
	return g, ts
}

func send(t *testing.T, method, target string, headers map[string]string, cookie *http.Cookie) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, target, strings.NewReader(`{"name":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
		} else {
			req.Header.Set(k, v)
		}
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func TestGateRefusesCrossSiteAndRebinding(t *testing.T) {
	g, ts := gateFixture(t)
	// The audit's reproduction: a page on another site posting a "simple"
	// request with no credential.
	if res := send(t, "POST", ts.URL+"/api/projects", map[string]string{"Content-Type": "text/plain", "Origin": "http://evil.example"}, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin simple request: %d", res.StatusCode)
	}
	// Even holding the token, another origin is refused.
	if res := send(t, "POST", ts.URL+"/api/projects", map[string]string{"Origin": "http://evil.example", "Authorization": "Bearer " + g.token}, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin request with token: %d", res.StatusCode)
	}
	// DNS rebinding: the attacker's name resolving to 127.0.0.1.
	if res := send(t, "GET", ts.URL+"/api/sessions", map[string]string{"Host": "evil.example:80", "Authorization": "Bearer " + g.token}, nil); res.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("rebound host: %d", res.StatusCode)
	}
	// No credential: the app refuses, and a page request gets the sign-in page.
	if res := send(t, "POST", ts.URL+"/api/projects", nil, nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no credential: %d", res.StatusCode)
	}
	res := send(t, "GET", ts.URL+"/", nil, nil)
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "lectern up") {
		t.Fatalf("unsigned page: %d %s", res.StatusCode, body)
	}
	// The CLI's bearer works; agent hooks carry their own credential.
	if res := send(t, "POST", ts.URL+"/api/projects", map[string]string{"Authorization": "Bearer " + g.token}, nil); res.StatusCode != http.StatusCreated {
		t.Fatalf("bearer: %d", res.StatusCode)
	}
	if res := send(t, "POST", ts.URL+"/api/hook/session/1/Stop", nil, nil); res.StatusCode != http.StatusCreated {
		t.Fatalf("hook route: %d", res.StatusCode)
	}
}

func TestGateBrowserSignIn(t *testing.T) {
	g, ts := gateFixture(t)
	u, _ := url.Parse(ts.URL)
	link, err := BrowserURL(t.Context(), Endpoint{URL: ts.URL, Token: g.token}, "/#sessions/new")
	if err != nil {
		t.Fatal(err)
	}
	res := send(t, "GET", link, nil, nil)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/#sessions/new" {
		t.Fatalf("login: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == "lectern_local_"+u.Port() {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("sign-in cookie: %+v", res.Cookies())
	}
	// A link works once.
	if res := send(t, "GET", link, nil, nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused link: %d", res.StatusCode)
	}
	if res := send(t, "GET", ts.URL+"/api/sessions", nil, cookie); res.StatusCode != http.StatusCreated {
		t.Fatalf("cookie read: %d", res.StatusCode)
	}
	// Changes need the runtime's own origin: no simple-request CSRF, even
	// from another program's page on 127.0.0.1 (the same "site").
	same := ts.URL
	if res := send(t, "POST", ts.URL+"/api/projects", map[string]string{"Origin": same, "Content-Type": "application/json"}, cookie); res.StatusCode != http.StatusCreated {
		t.Fatalf("same-origin change: %d", res.StatusCode)
	}
	if res := send(t, "POST", ts.URL+"/api/projects", map[string]string{"Content-Type": "text/plain"}, cookie); res.StatusCode != http.StatusForbidden {
		t.Fatalf("change with no origin: %d", res.StatusCode)
	}
	if res := send(t, "POST", ts.URL+"/api/projects", map[string]string{"Origin": "http://127.0.0.1:1"}, cookie); res.StatusCode != http.StatusForbidden {
		t.Fatalf("change from another local port: %d", res.StatusCode)
	}
	if res := send(t, "GET", ts.URL+"/api/sessions", map[string]string{"Sec-Fetch-Site": "same-site"}, cookie); res.StatusCode != http.StatusForbidden {
		t.Fatalf("read from another local port: %d", res.StatusCode)
	}
	// Only the runtime token can mint links; next cannot leave the runtime.
	if res := send(t, "POST", ts.URL+loginCodeRoute, map[string]string{"Origin": same}, cookie); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cookie minted a link: %d", res.StatusCode)
	}
	link, _ = BrowserURL(t.Context(), Endpoint{URL: ts.URL, Token: g.token}, "//evil.example/")
	if res := send(t, "GET", link, nil, nil); res.Header.Get("Location") != "/" {
		t.Fatalf("open redirect: %q", res.Header.Get("Location"))
	}
}
