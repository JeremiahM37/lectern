package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTTLCacheExpiresAndEvicts(t *testing.T) {
	c := newTTLCache[int](time.Minute, 2)
	now := time.Unix(0, 0)
	c.now = func() time.Time { return now }

	c.Put("a", 1)
	c.Put("b", 2)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatalf("fresh entry: %v %v", v, ok)
	}
	// A third key evicts the one stored longest ago.
	c.Put("c", 3)
	if _, ok := c.Get("a"); ok {
		t.Fatal("oldest entry should have been evicted")
	}
	if _, ok := c.Get("b"); !ok {
		t.Fatal("newer entry should survive")
	}

	now = now.Add(time.Minute)
	if _, ok := c.Get("b"); ok {
		t.Fatal("entry should expire after the TTL")
	}
}

func TestSearchCacheReusesResults(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, `{"results":[{"title":"Go","url":"https://go.dev","content":"The language"}]}`)
	}))
	defer srv.Close()

	w := newWebTools(Options{SearchURL: srv.URL, AllowPrivate: true})
	tool := &searchTool{w: w}
	first := runTool(t, tool, autoEnv(), map[string]any{"query": "go language"})
	second := runTool(t, tool, autoEnv(), map[string]any{"query": "Go Language"})
	if first.IsError || second.IsError {
		t.Fatalf("errors: %q / %q", first.Output, second.Output)
	}
	if hits.Load() != 1 {
		t.Fatalf("backend hit %d times, want 1 (second call should be cached)", hits.Load())
	}
	if !strings.Contains(second.Summary, "cached") {
		t.Fatalf("summary %q should say cached", second.Summary)
	}
	if !strings.Contains(second.Output, "https://go.dev") {
		t.Fatalf("cached output lost results: %q", second.Output)
	}
}

func TestFetchCacheReusesPage(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "hello from the page")
	}))
	defer srv.Close()

	w := newWebTools(Options{AllowPrivate: true})
	tool := &fetchTool{w: w}
	runTool(t, tool, autoEnv(), map[string]any{"url": srv.URL + "/p"})
	r := runTool(t, tool, autoEnv(), map[string]any{"url": srv.URL + "/p"})
	if r.IsError || !strings.Contains(r.Output, "hello from the page") {
		t.Fatalf("second fetch: %+v", r)
	}
	if hits.Load() != 1 {
		t.Fatalf("server hit %d times, want 1", hits.Load())
	}
}

func TestBraveBackendParsesAndFallsBack(t *testing.T) {
	var gotKey, gotQ, gotCount string
	brave := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Subscription-Token")
		gotQ = r.URL.Query().Get("q")
		gotCount = r.URL.Query().Get("count")
		fmt.Fprint(w, `{"web":{"results":[
			{"title":"<strong>Scribe</strong> &amp; more","url":"https://example.com/a","description":"A <strong>coding</strong> agent"},
			{"title":"no url","url":"  ","description":"skipped"}
		]}}`)
	}))
	defer brave.Close()

	w := newWebTools(Options{BraveAPIKey: "k-123", AllowPrivate: true})
	w.braveURL = brave.URL
	rs, err := w.search(t.Context(), "scribe agent", 3)
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "k-123" || gotQ != "scribe agent" || gotCount != "3" {
		t.Fatalf("request: key=%q q=%q count=%q", gotKey, gotQ, gotCount)
	}
	if len(rs) != 1 || rs[0].Title != "Scribe & more" || rs[0].Snippet != "A coding agent" {
		t.Fatalf("parsed %+v", rs)
	}

	// A failing Brave key falls back to SearXNG without failing the search.
	searx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			fmt.Fprint(w, `{"results":[{"title":"Fallback","url":"https://fallback.example","content":"from searx"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer searx.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusUnauthorized)
	}))
	defer bad.Close()

	w2 := newWebTools(Options{BraveAPIKey: "wrong", SearchURL: searx.URL, AllowPrivate: true})
	w2.braveURL = bad.URL
	rs, err = w2.search(t.Context(), "x", 5)
	if err != nil || len(rs) != 1 || rs[0].URL != "https://fallback.example" {
		t.Fatalf("fallback: %+v %v", rs, err)
	}
}

func TestBraveNotUsedWithoutKey(t *testing.T) {
	var braveHits atomic.Int32
	brave := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		braveHits.Add(1)
	}))
	defer brave.Close()
	searx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[]}`)
	}))
	defer searx.Close()
	w := newWebTools(Options{SearchURL: searx.URL, AllowPrivate: true})
	w.braveURL = brave.URL
	_, _ = w.search(t.Context(), "q", 5)
	if braveHits.Load() != 0 {
		t.Fatal("Brave must not be called without a key")
	}
}
