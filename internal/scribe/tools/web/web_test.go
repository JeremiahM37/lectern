package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// fakeHost answers approvals with a fixed decision and counts asks.
type fakeHost struct {
	mu       sync.Mutex
	decision tools.Decision
	asks     int
}

func (h *fakeHost) Approve(ctx context.Context, a tools.Approval) tools.Decision {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.asks++
	return h.decision
}
func (h *fakeHost) Ask(ctx context.Context, q tools.Question) (string, error) { return "", nil }
func (h *fakeHost) Progress(callID, text string)                              {}

func autoEnv() *tools.Env { return &tools.Env{Mode: "auto"} }

func runTool(t *testing.T, tool tools.Tool, env *tools.Env, args map[string]any) tools.Result {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return tool.Run(context.Background(), env, b)
}

func TestSearXNGJSON(t *testing.T) {
	var gotQ, gotFormat string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("path = %q, want /search", r.URL.Path)
		}
		gotQ = r.URL.Query().Get("q")
		gotFormat = r.URL.Query().Get("format")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[
			{"title":"First  hit","url":"https://one.example/a","content":"Snippet   one."},
			{"title":"","url":"https://two.example/b","content":""},
			{"title":"Third","url":"https://three.example/c","content":"Snippet three."},
			{"title":"No URL","url":"  ","content":"skipped"}
		]}`))
	}))
	defer srv.Close()

	w := newWebTools(Options{SearchURL: srv.URL, AllowPrivate: true})
	tool := &searchTool{w: w}
	res := runTool(t, tool, autoEnv(), map[string]any{"query": "go generics", "max_results": 2})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if gotQ != "go generics" || gotFormat != "json" {
		t.Fatalf("query params q=%q format=%q", gotQ, gotFormat)
	}
	want := "1. First hit\n   https://one.example/a\n   Snippet one.\n\n" +
		"2. https://two.example/b\n   https://two.example/b\n"
	if res.Output != want {
		t.Fatalf("output:\n%s\nwant:\n%s", res.Output, want)
	}
}

func TestSearXNGJSONDisabledHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	w := newWebTools(Options{SearchURL: srv.URL, AllowPrivate: true})
	res := runTool(t, &searchTool{w: w}, autoEnv(), map[string]any{"query": "x"})
	if !res.IsError || !strings.Contains(res.Output, "search.formats") {
		t.Fatalf("want an error that names search.formats, got %+v", res)
	}
}

func TestDuckDuckGoFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/ddg_results.html")
	if err != nil {
		t.Fatal(err)
	}
	var gotQ string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if ua := r.Header.Get("User-Agent"); ua == "" {
			t.Error("missing User-Agent")
		}
		r.ParseForm()
		gotQ = r.PostForm.Get("q")
		w.Write(fixture)
	}))
	defer srv.Close()

	w := newWebTools(Options{AllowPrivate: true})
	w.ddgURL = srv.URL
	res := runTool(t, &searchTool{w: w}, autoEnv(), map[string]any{"query": "golang", "max_results": 10})
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if gotQ != "golang" {
		t.Fatalf("form q = %q", gotQ)
	}
	want := "1. Go Documentation\n" +
		"   https://go.dev/doc/\n" +
		"   The Go programming language documentation & guides.\n\n" +
		"2. Example \"Page\"\n" +
		"   https://example.com/page?x=1&y=2\n" +
		"   An example page with a multi-line snippet.\n\n" +
		"3. Go (programming language) - Wikipedia\n" +
		"   https://en.wikipedia.org/wiki/Go_(programming_language)\n" +
		"   Go is a statically typed language.\n"
	if res.Output != want {
		t.Fatalf("output:\n%s\nwant:\n%s", res.Output, want)
	}
	if strings.Contains(res.Output, "Sponsored") || strings.Contains(res.Output, "must not appear") {
		t.Fatal("sponsored result leaked into output")
	}
}

func TestDuckDuckGoCaptchaIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body><div class="anomaly-modal">Please complete the CAPTCHA</div></body></html>`))
	}))
	defer srv.Close()
	w := newWebTools(Options{AllowPrivate: true})
	w.ddgURL = srv.URL
	res := runTool(t, &searchTool{w: w}, autoEnv(), map[string]any{"query": "x"})
	if !res.IsError || !strings.Contains(res.Output, "CAPTCHA") {
		t.Fatalf("want CAPTCHA error, got %+v", res)
	}
}

func TestSearchApprovalIsCachedForSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[{"title":"A","url":"https://a.example/","content":""}]}`))
	}))
	defer srv.Close()
	w := newWebTools(Options{SearchURL: srv.URL, AllowPrivate: true})
	host := &fakeHost{decision: tools.AllowSession}
	env := &tools.Env{Mode: "default", Host: host}
	tool := &searchTool{w: w}

	for i := 0; i < 3; i++ {
		if res := runTool(t, tool, env, map[string]any{"query": "q"}); res.IsError {
			t.Fatalf("run %d: %s", i, res.Output)
		}
	}
	if host.asks != 1 {
		t.Fatalf("asked %d times, want 1 (AllowSession should be cached)", host.asks)
	}
}

func TestSearchDeniedMakesNoRequest(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	defer srv.Close()
	w := newWebTools(Options{SearchURL: srv.URL, AllowPrivate: true})
	host := &fakeHost{decision: tools.Deny}
	res := runTool(t, &searchTool{w: w}, &tools.Env{Mode: "default", Host: host}, map[string]any{"query": "q"})
	if !res.IsError || !strings.Contains(res.Output, "Denied: the user chose Deny") {
		t.Fatalf("want denial, got %+v", res)
	}
	if hits != 0 {
		t.Fatal("a denied search still made a request")
	}
}

func TestSearchAutoModeSkipsApproval(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()
	w := newWebTools(Options{SearchURL: srv.URL, AllowPrivate: true})
	host := &fakeHost{decision: tools.Deny}
	res := runTool(t, &searchTool{w: w}, &tools.Env{Mode: "auto", Host: host}, map[string]any{"query": "q"})
	if res.IsError || host.asks != 0 {
		t.Fatalf("auto mode should not ask; asks=%d res=%+v", host.asks, res)
	}
}

func TestSearchValidation(t *testing.T) {
	w := newWebTools(Options{AllowPrivate: true})
	res := runTool(t, &searchTool{w: w}, autoEnv(), map[string]any{"query": "   "})
	if !res.IsError {
		t.Fatal("empty query should be an error")
	}
}

func TestRegisterAddsBothTools(t *testing.T) {
	r := tools.NewRegistry()
	Register(r, Options{})
	names := r.SortedNames()
	if strings.Join(names, ",") != "fetch_page,web_search" {
		t.Fatalf("registered %v", names)
	}
	for _, n := range names {
		tool, _ := r.Get(n)
		if !tool.ReadOnly() {
			t.Errorf("%s should be read-only", n)
		}
	}
}
