package localruntime

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The local runtime's front door. The runtime is a full control plane on a
// loopback port, and "only this machine can reach it" is not true of a web
// browser: any page the user opens can send requests to 127.0.0.1, and a
// DNS-rebound name can even read the answers. So every request here must:
//
//   - name the runtime by a loopback Host (no rebinding),
//   - come from the runtime's own origin if it says where it came from, and
//   - carry a credential: the runtime token (the CLI, as a bearer), or the
//     browser cookie that `lectern up` hands out through a one-time link.
//
// Cookie requests that change something must also prove they came from the
// runtime's own page (Origin or Referer). The app behind this runs in token
// mode, so anything that slips past here still meets a token check.

const (
	loginRoute     = "/__lectern_local/login"
	loginCodeRoute = "/__lectern_local/login-code"
	browserKeyFile = "browser.key"
	loginCodeTTL   = 10 * time.Minute
)

// cookieName is per port: cookies ignore ports, so two runtimes (or other
// local apps) on 127.0.0.1 would otherwise overwrite each other's.
func cookieName(r *http.Request) string {
	_, port, _ := net.SplitHostPort(r.Host)
	return "lectern_local_" + port
}

// loadBrowserKey reads the browser credential, creating it on first use. It
// outlives restarts (unlike the runtime token), so a signed-in browser stays
// signed in.
func loadBrowserKey(dir string) (string, error) {
	path := filepath.Join(dir, browserKeyFile)
	if data, err := os.ReadFile(path); err == nil {
		if key := strings.TrimSpace(string(data)); len(key) >= 32 {
			return key, nil
		}
	}
	key, err := newToken()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		return "", err
	}
	return key, nil
}

type gate struct {
	token, browserKey string
	mu                sync.Mutex
	codes             map[string]time.Time
}

func newGate(token, browserKey string) *gate {
	return &gate{token: token, browserKey: browserKey, codes: map[string]time.Time{}}
}

func (g *gate) mintCode() (string, error) {
	code, err := newToken()
	if err != nil {
		return "", err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	for c, exp := range g.codes {
		if now.After(exp) {
			delete(g.codes, c)
		}
	}
	g.codes[code] = now.Add(loginCodeTTL)
	return code, nil
}

func (g *gate) redeem(code string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	exp, ok := g.codes[code]
	delete(g.codes, code)
	return ok && time.Now().Before(exp)
}

func equal(a, b string) bool {
	return a != "" && b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func loopbackHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// sameOrigin reports whether an Origin or Referer names this runtime.
func sameOrigin(raw, host string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "http" && u.Host != "" && strings.EqualFold(u.Host, host)
}

func (g *gate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			http.Error(w, "this Lectern only answers at 127.0.0.1", http.StatusMisdirectedRequest)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r.Host) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		if r.URL.Path == loginRoute {
			g.login(w, r)
			return
		}
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		authorized := equal(bearer, g.token) || equal(r.URL.Query().Get("token"), g.token)
		// The runtime's own routes (identity, stop, sign-in links) take the
		// token itself, never the cookie.
		if !authorized && !strings.HasPrefix(r.URL.Path, "/__lectern_local/") {
			if c, err := r.Cookie(cookieName(r)); err == nil && equal(c.Value, g.browserKey) {
				if !cookieRequestSafe(r) {
					http.Error(w, "cross-site request refused", http.StatusForbidden)
					return
				}
				// The app behind this checks the runtime token; the cookie
				// stands in for it.
				r.Header.Set("Authorization", "Bearer "+g.token)
				authorized = true
			}
		}
		if !authorized && pageRequest(r) {
			signInPage(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// cookieRequestSafe is the CSRF rule for the browser cookie. SameSite=Strict
// keeps it off other sites' requests, but every port on 127.0.0.1 is the same
// "site", so a page served by some other local program would still send it.
// A change must name this origin; a read must not be another page's.
func cookieRequestSafe(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default:
		return false
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		return sameOrigin(origin, r.Host)
	}
	return sameOrigin(r.Header.Get("Referer"), r.Host)
}

// pageRequest reports a browser asking for the app's HTML, which without a
// sign-in gets a page saying how to open Lectern instead of an app that can
// only fail. Scripts, styles and API calls are left to answer for themselves.
func pageRequest(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	p := r.URL.Path
	for _, prefix := range []string{"/api/", "/term/", "/a2a", "/.well-known/", "/static/", "/__lectern_local/"} {
		if strings.HasPrefix(p, prefix) {
			return false
		}
	}
	return !strings.Contains(p[strings.LastIndex(p, "/")+1:], ".")
}

func (g *gate) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !g.redeem(r.URL.Query().Get("code")) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		writeGatePage(w, "This sign-in link has expired or was already used.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName(r), Value: g.browserKey, Path: "/",
		MaxAge: 400 * 24 * 60 * 60, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	next := r.URL.Query().Get("next")
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, `\`) {
		next = "/"
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (g *gate) mintHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !equal(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), g.token) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	code, err := g.mintCode()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code})
}

func signInPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	writeGatePage(w, "")
}

func writeGatePage(w http.ResponseWriter, lead string) {
	if lead != "" {
		lead = "<p>" + lead + "</p>"
	}
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Open Lectern</title><style>
:root{color-scheme:light dark;--bg:#fafafa;--fg:#1d1d1f;--muted:#5f6368;--code:#ececec}
@media (prefers-color-scheme:dark){:root{--bg:#161618;--fg:#ececf0;--muted:#a4a4ac;--code:#2a2a2e}}
body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.5 system-ui,sans-serif;display:grid;place-items:center;min-height:100vh;padding:0 16px}
main{max-width:30rem}code{background:var(--code);padding:.15em .4em;border-radius:4px}p.muted{color:var(--muted)}
</style></head><body><main><h1>Open Lectern from your terminal</h1>%s
<p>Run <code>lectern up</code>. It opens this page signed in.</p>
<p class="muted">Lectern asks for this so that other websites you visit cannot reach your agents.</p>
</main></body></html>`, lead)
}

// BrowserURL is a link that signs a browser in to the runtime and then opens
// next (a path such as "/#sessions/new"). The link works once, for ten
// minutes.
func BrowserURL(ctx context.Context, ep Endpoint, next string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL+loginCodeRoute, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	res, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return "", errors.New("the running local runtime is older than this CLI; run `lectern local stop`, then `lectern up`")
	}
	var out struct {
		Code string `json:"code"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&out) != nil || out.Code == "" {
		return "", fmt.Errorf("sign-in link: %s", res.Status)
	}
	q := url.Values{"code": {out.Code}}
	if next != "" {
		q.Set("next", next)
	}
	return ep.URL + loginRoute + "?" + q.Encode(), nil
}
