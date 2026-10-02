package localruntime

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPhoneShareOpensTheLANAddressBehindItsGate(t *testing.T) {
	minted := 0
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Stands in for the app in token mode: only a paired device gets in.
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Header.Get("Cookie") != "lectern_device=ok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "token="+r.URL.Query().Get("token"))
	})
	p := &phoneShare{token: "runtime-token", serve: app, addresses: func() []string { return []string{"127.0.0.1"} },
		mint: func() (string, time.Time, error) { minted++; return "CODE-1", time.Now().Add(5 * time.Minute), nil }}
	t.Cleanup(p.close)
	h := httptest.NewServer(http.HandlerFunc(p.handler))
	t.Cleanup(h.Close)
	call := func(method, token string) *http.Response {
		req, _ := http.NewRequest(method, h.URL, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = res.Body.Close() })
		return res
	}
	if res := call("POST", ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("opened without the runtime token: %d", res.StatusCode)
	}
	if p.state().Open {
		t.Fatal("open without the token")
	}
	res := call("POST", "runtime-token")
	body, _ := io.ReadAll(res.Body)
	st := p.state()
	if res.StatusCode != 200 || !st.Open || !strings.Contains(string(body), `"pair_url":"`+st.URL+`/pair#code=CODE-1"`) || !strings.Contains(string(body), "not encrypted") || minted != 1 {
		t.Fatalf("open: %d %s", res.StatusCode, body)
	}
	lan, _ := url.Parse(st.URL)
	get := func(path string, mod func(*http.Request)) (int, string) {
		req, _ := http.NewRequest("GET", st.URL+path, nil)
		if mod != nil {
			mod(req)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, _ := get("/api/sessions", nil); code != http.StatusUnauthorized {
		t.Fatalf("unpaired API call: %d", code)
	}
	if code, _ := get("/api/sessions", func(r *http.Request) { r.Header.Set("Cookie", "lectern_device=ok") }); code != 200 {
		t.Fatalf("paired API call: %d", code)
	}
	if code, _ := get("/api/sessions", func(r *http.Request) { r.Host = "evil.example:" + lan.Port() }); code != http.StatusMisdirectedRequest {
		t.Fatalf("rebound host: %d", code)
	}
	if code, _ := get("/api/local/phone", func(r *http.Request) { r.Header.Set("Authorization", "Bearer runtime-token") }); code != http.StatusNotFound {
		t.Fatalf("runtime controls reachable from the network: %d", code)
	}
	if _, body := get("/?token=runtime-token", nil); strings.Contains(body, "runtime-token") {
		t.Fatalf("runtime token accepted from the network: %s", body)
	}
	if res := call("DELETE", "runtime-token"); res.StatusCode != 200 || p.state().Open {
		t.Fatalf("close: %d", res.StatusCode)
	}
	if _, err := http.Get(st.URL + "/"); err == nil {
		t.Fatal("still listening after close")
	}
}
