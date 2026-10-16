package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func fetchWith(t *testing.T, w *webTools, rawURL string, maxChars int) tools.Result {
	t.Helper()
	args := map[string]any{"url": rawURL}
	if maxChars > 0 {
		args["max_chars"] = maxChars
	}
	b, _ := json.Marshal(args)
	return (&fetchTool{w: w}).Run(context.Background(), autoEnv(), b)
}

func TestFetchHTMLCleanup(t *testing.T) {
	fixture, err := os.ReadFile("testdata/noisy_page.html")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(fixture)
	}))
	defer srv.Close()

	w := newWebTools(Options{AllowPrivate: true})
	res := fetchWith(t, w, srv.URL+"/docs/page", 0)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	out := res.Output

	// Title and URL come first.
	if !strings.HasPrefix(out, "Title: Widget Guide & Notes\nURL: "+srv.URL+"/docs/page\n\n") {
		t.Fatalf("bad header:\n%s", out)
	}
	mustContain := []string{
		"# Getting started",
		"Install the [Guide](" + srv.URL + "/guide) and run `widget --init` first.",
		"Second paragraph with \"entities\" & more.",
		"Third paragraph, implicitly closed.",
		"## Steps",
		"- Download the binary\n- Configure it\n  - Nested item\n- Run it",
		"```go\nfunc main() {\n    fmt.Println(\"hi\")\n}\n```",
		"| Name | Value |\n| --- | --- |\n| a\\|b | 2 |",
	}
	for _, s := range mustContain {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q\n---\n%s", s, out)
		}
	}
	mustNotContain := []string{
		"SCRIPT_NOISE", "doThing", "color: red", "NAV_NOISE", "HEADER_NOISE",
		"FOOTER_NOISE", "NOSCRIPT_NOISE", "SVG_NOISE", "ASIDE_NOISE", "Site Name",
	}
	for _, s := range mustNotContain {
		if strings.Contains(out, s) {
			t.Errorf("output still contains noise %q", s)
		}
	}
	if strings.Contains(out, "\n\n\n") {
		t.Error("output has runs of blank lines")
	}
}

func TestFetchTruncatesWithNote(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(strings.Repeat("abcdefghij", 100)))
	}))
	defer srv.Close()
	w := newWebTools(Options{AllowPrivate: true})
	res := fetchWith(t, w, srv.URL, 50)
	if !strings.Contains(res.Output, "[Truncated: showing the first 50 of 1000 characters") {
		t.Fatalf("missing truncation note:\n%s", res.Output)
	}
	body := res.Output[strings.Index(res.Output, "\n\n")+2:]
	if !strings.HasPrefix(body, strings.Repeat("abcdefghij", 5)) {
		t.Fatalf("body not cut to 50 chars: %q", body[:20])
	}
}

func TestJSONPassthroughIsPrettyPrinted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write([]byte(`{"a":1,"b":[true,null]}`))
	}))
	defer srv.Close()
	w := newWebTools(Options{AllowPrivate: true})
	res := fetchWith(t, w, srv.URL+"/api", 0)
	want := "{\n  \"a\": 1,\n  \"b\": [\n    true,\n    null\n  ]\n}"
	if !strings.HasSuffix(res.Output, want) {
		t.Fatalf("json not pretty-printed:\n%s", res.Output)
	}
}

func TestTextPassthroughUnchanged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		w.Write([]byte("# Raw\n\n*  kept   spacing\n"))
	}))
	defer srv.Close()
	w := newWebTools(Options{AllowPrivate: true})
	res := fetchWith(t, w, srv.URL, 0)
	if !strings.HasSuffix(res.Output, "# Raw\n\n*  kept   spacing\n") {
		t.Fatalf("text not passed through:\n%q", res.Output)
	}
}

func TestPDFIsReportedNotParsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write([]byte("%PDF-1.4 " + strings.Repeat("x", 2048)))
	}))
	defer srv.Close()
	w := newWebTools(Options{AllowPrivate: true})
	res := fetchWith(t, w, srv.URL+"/paper.pdf", 0)
	if res.IsError {
		t.Fatalf("a PDF is a valid result, got error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "This page is a PDF (2.0 KB)") {
		t.Fatalf("PDF note missing or wrong size:\n%s", res.Output)
	}
}

func TestSizeCapStopsReading(t *testing.T) {
	chunk := []byte(strings.Repeat("y", 1<<20))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		for i := 0; i < 7; i++ { // 7 MB, more than the 5 MB cap
			w.Write(chunk)
		}
	}))
	defer srv.Close()
	w := newWebTools(Options{AllowPrivate: true})
	res := fetchWith(t, w, srv.URL, maxFetchChars)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "Stopped reading at 5.0 MB") {
		t.Fatalf("size cap note missing:\n%s", res.Output[len(res.Output)-200:])
	}
	if len(res.Output) > maxFetchBytes+1024 {
		t.Fatalf("output is %d bytes, cap is %d", len(res.Output), maxFetchBytes)
	}
}

func TestRedirectChainLimit(t *testing.T) {
	// /hop/N redirects to /hop/N-1 ... /hop/0 is the final 200.
	mux := http.NewServeMux()
	mux.HandleFunc("/hop/", func(w http.ResponseWriter, r *http.Request) {
		var n int
		fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/hop/"), "%d", &n)
		if n == 0 {
			w.Write([]byte("<html><title>Arrived</title><body><p>final page</p></body></html>"))
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/hop/%d", n-1), http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	w := newWebTools(Options{AllowPrivate: true})

	// Five redirects is the limit and must succeed.
	res := fetchWith(t, w, srv.URL+"/hop/5", 0)
	if res.IsError || !strings.Contains(res.Output, "final page") {
		t.Fatalf("5 redirects should work, got %+v", res)
	}
	// Six must be refused.
	res = fetchWith(t, w, srv.URL+"/hop/6", 0)
	if !res.IsError || !strings.Contains(res.Output, "stopped after 5 redirects") {
		t.Fatalf("6 redirects should be refused, got %+v", res)
	}
}

func TestRelativeRedirectTargetIsResolved(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/landing/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/landing/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<title>Landed</title><p>see <a href="../other">other</a></p>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	w := newWebTools(Options{AllowPrivate: true})
	res := fetchWith(t, w, srv.URL+"/start", 0)
	if !strings.Contains(res.Output, "URL: "+srv.URL+"/landing/\n") {
		t.Fatalf("final URL not reported:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "[other]("+srv.URL+"/other)") {
		t.Fatalf("relative link not resolved against final URL:\n%s", res.Output)
	}
}

func TestNon2xxIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	w := newWebTools(Options{AllowPrivate: true})
	res := fetchWith(t, w, srv.URL+"/missing", 0)
	if !res.IsError || !strings.Contains(res.Output, "HTTP 404") {
		t.Fatalf("want 404 error, got %+v", res)
	}
}

func TestNonHTTPSchemesRefused(t *testing.T) {
	w := newWebTools(Options{})
	for _, u := range []string{"file:///etc/passwd", "ftp://example.com/x", "gopher://example.com/"} {
		res := fetchWith(t, w, u, 0)
		if !res.IsError || !strings.Contains(res.Output, "http") {
			t.Errorf("%s: want refusal, got %+v", u, res)
		}
	}
}

func TestSSRFRefusesLoopbackWithoutAllowPrivate(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()

	w := newWebTools(Options{}) // AllowPrivate off
	host := &fakeHost{decision: tools.AllowOnce}
	env := &tools.Env{Mode: "default", Host: host}
	b, _ := json.Marshal(map[string]any{"url": srv.URL})
	res := (&fetchTool{w: w}).Run(context.Background(), env, b)
	if !res.IsError || !strings.Contains(res.Output, "private or local") {
		t.Fatalf("loopback should be refused, got %+v", res)
	}
	if host.asks != 0 {
		t.Error("a refused target should not reach the approval prompt")
	}
	if hits != 0 {
		t.Error("request reached the loopback server")
	}
	for _, u := range []string{"http://localhost:1/", "http://169.254.169.254/latest/meta-data", "http://[::1]:1/", "http://100.100.100.100/"} {
		if res := fetchWith(t, w, u, 0); !res.IsError {
			t.Errorf("%s should be refused", u)
		}
	}
}

func TestGuardBlockedAddresses(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.8.8.8", "10.1.2.3", "172.16.0.1", "192.168.0.75", "169.254.169.254",
		"0.0.0.0", "100.64.0.1", "100.100.100.100", "224.0.0.1", "::1", "fe80::1", "fd00::1", "::ffff:127.0.0.1",
	}
	for _, s := range blocked {
		if !blockedIP(net.ParseIP(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "140.82.112.3", "2606:4700:4700::1111"}
	for _, s := range allowed {
		if blockedIP(net.ParseIP(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

func TestGuardRedirectAndDialChecks(t *testing.T) {
	w := newWebTools(Options{})
	// A redirect hop to a private address must be refused.
	req, _ := http.NewRequest(http.MethodGet, "http://10.0.0.5/admin", nil)
	if err := w.checkRedirect(req, nil); err == nil {
		t.Error("redirect to 10.0.0.5 should be refused")
	}
	// The dialer itself refuses, whatever the request said.
	if _, err := w.dial(context.Background(), "tcp", "127.0.0.1:80"); err == nil {
		t.Error("dial to 127.0.0.1 should be refused")
	}
	// The same checks pass when AllowPrivate is set.
	open := newWebTools(Options{AllowPrivate: true})
	u, _ := url.Parse("http://127.0.0.1:1/")
	if err := open.checkURL(context.Background(), u); err != nil {
		t.Errorf("AllowPrivate should allow loopback, got %v", err)
	}
}

func TestFetchUsesCallerHTTPClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "TestAgent/1" {
			t.Errorf("user agent = %q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	w := newWebTools(Options{AllowPrivate: true, UserAgent: "TestAgent/1", HTTPClient: srv.Client()})
	res := fetchWith(t, w, srv.URL, 0)
	if res.IsError || !strings.HasSuffix(res.Output, "ok") {
		t.Fatalf("got %+v", res)
	}
}

func TestFetchApprovalDenied(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	w := newWebTools(Options{AllowPrivate: true})
	host := &fakeHost{decision: tools.Deny}
	b, _ := json.Marshal(map[string]any{"url": srv.URL})
	res := (&fetchTool{w: w}).Run(context.Background(), &tools.Env{Mode: "default", Host: host}, b)
	if !res.IsError || hits != 0 {
		t.Fatalf("denied fetch should not connect: hits=%d res=%+v", hits, res)
	}
}
