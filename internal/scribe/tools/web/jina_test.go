package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

const testJinaKey = "jina_test_key_must_not_leak_8f3a"

// pageServer serves one HTML page as the target that the reader would read.
func pageServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<html><head><title>Local</title></head><body><h1>Local page</h1><p>from the origin</p></body></html>"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// readerServer is a stand-in for r.jina.ai. respond decides the reply from the
// request's Authorization header. It records every request it sees.
type readerServer struct {
	*httptest.Server
	mu   sync.Mutex
	seen []http.Header
}

func newReader(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) *readerServer {
	t.Helper()
	rs := &readerServer{}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.seen = append(rs.seen, r.Header.Clone())
		rs.mu.Unlock()
		respond(w, r)
	}))
	t.Cleanup(rs.Close)
	return rs
}

func (rs *readerServer) hits() []http.Header {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([]http.Header(nil), rs.seen...)
}

func readerBody(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/markdown")
	w.Write([]byte("Title: Reader Title\n\nURL Source: http://x\n\nMarkdown Content:\n# Reader heading\n\nText from the reader.\n"))
}

func jinaWebTools(reader *readerServer, key string, on bool) *webTools {
	w := newWebTools(Options{AllowPrivate: true, JinaAPIKey: key, JinaReader: on})
	w.jinaURL = reader.URL + "/"
	return w
}

func runFetch(t *testing.T, w *webTools, rawURL string) tools.Result {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"url": rawURL})
	return (&fetchTool{w: w}).Run(t.Context(), autoEnv(), b)
}

func TestJinaReaderUsedWithKey(t *testing.T) {
	page := pageServer(t)
	reader := newReader(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/markdown" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testJinaKey {
			t.Errorf("Authorization = %q", got)
		}
		readerBody(w)
	})
	w := jinaWebTools(reader, testJinaKey, true)
	res := runFetch(t, w, page.URL+"/doc")
	if res.IsError {
		t.Fatalf("fetch failed: %s", res.Output)
	}
	if !strings.Contains(res.Output, "Reader heading") || !strings.Contains(res.Output, "Title: Reader Title") {
		t.Fatalf("reader text not used:\n%s", res.Output)
	}
	if strings.Contains(res.Output, "from the origin") {
		t.Fatal("the local fetch ran although the reader answered")
	}
	if res.Meta["backend"] != "jina-api" {
		t.Fatalf("backend = %v, want jina-api", res.Meta["backend"])
	}
}

func TestJinaReaderWithoutKeyUsesFreeTier(t *testing.T) {
	page := pageServer(t)
	reader := newReader(t, func(w http.ResponseWriter, r *http.Request) { readerBody(w) })
	res := runFetch(t, jinaWebTools(reader, "", true), page.URL+"/doc")
	if res.Meta["backend"] != "jina" {
		t.Fatalf("backend = %v, want jina", res.Meta["backend"])
	}
	for _, h := range reader.hits() {
		if h.Get("Authorization") != "" {
			t.Fatal("no key is configured, yet an Authorization header was sent")
		}
	}
}

// A refused key is retried on the free tier, and the key never reaches the
// model.
func TestJinaRefusedKeyRetriesFreeTier(t *testing.T) {
	page := pageServer(t)
	reader := newReader(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		readerBody(w)
	})
	res := runFetch(t, jinaWebTools(reader, testJinaKey, true), page.URL+"/doc")
	if res.IsError || res.Meta["backend"] != "jina" {
		t.Fatalf("expected the free tier to answer, got backend %v: %s", res.Meta["backend"], res.Output)
	}
	if n := len(reader.hits()); n != 2 {
		t.Fatalf("reader saw %d requests, want 2 (keyed, then free)", n)
	}
	assertNoKey(t, res)
}

// A reader outage falls back to the local fetch, silently.
func TestJinaOutageFallsBackToLocal(t *testing.T) {
	page := pageServer(t)
	reader := newReader(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream "+testJinaKey, http.StatusInternalServerError)
	})
	res := runFetch(t, jinaWebTools(reader, testJinaKey, true), page.URL+"/doc")
	if res.IsError {
		t.Fatalf("fetch failed instead of falling back: %s", res.Output)
	}
	if !strings.Contains(res.Output, "from the origin") {
		t.Fatalf("local fetch did not answer:\n%s", res.Output)
	}
	if res.Meta["backend"] != "local" {
		t.Fatalf("backend = %v, want local", res.Meta["backend"])
	}
	assertNoKey(t, res)
}

// An empty reader answer is treated as a failure, not as an empty page.
func TestJinaEmptyAnswerFallsBack(t *testing.T) {
	page := pageServer(t)
	reader := newReader(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Title: Nothing\n\nMarkdown Content:\n   \n"))
	})
	res := runFetch(t, jinaWebTools(reader, testJinaKey, true), page.URL+"/doc")
	if res.Meta["backend"] != "local" {
		t.Fatalf("backend = %v, want local", res.Meta["backend"])
	}
}

// With the reader toggle off, no request reaches the reader, even with a key.
func TestJinaOffNeverContactsReader(t *testing.T) {
	page := pageServer(t)
	reader := newReader(t, func(w http.ResponseWriter, r *http.Request) { readerBody(w) })
	res := runFetch(t, jinaWebTools(reader, testJinaKey, false), page.URL+"/doc")
	if n := len(reader.hits()); n != 0 {
		t.Fatalf("reader was contacted %d times with the toggle off", n)
	}
	if res.Meta["backend"] != "local" || !strings.Contains(res.Output, "from the origin") {
		t.Fatalf("expected the local path: %v %s", res.Meta["backend"], res.Output)
	}
	assertNoKey(t, res)
}

func TestParseJinaBody(t *testing.T) {
	title, text := parseJinaBody("Title: A\nURL Source: u\n\nMarkdown Content:\n\nbody line\n")
	if title != "A" || text != "body line\n" {
		t.Fatalf("parseJinaBody = %q, %q", title, text)
	}
	title, text = parseJinaBody("plain body")
	if title != "" || text != "plain body" {
		t.Fatalf("no marker should return the body whole: %q, %q", title, text)
	}
}

func assertNoKey(t *testing.T, res tools.Result) {
	t.Helper()
	if strings.Contains(res.Output, testJinaKey) || strings.Contains(res.Summary, testJinaKey) {
		t.Fatal("the Jina key reached the tool output")
	}
	if strings.Contains(string(mustJSON(t, res.Meta)), testJinaKey) {
		t.Fatal("the Jina key reached the tool metadata")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
