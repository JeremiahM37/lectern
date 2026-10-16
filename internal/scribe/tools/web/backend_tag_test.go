package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// web_search names the backend that answered, and a cached answer keeps it.
func TestWebSearchReportsBackend(t *testing.T) {
	searx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[{"title":"Go","url":"https://go.dev","content":"lang"}]}`)
	}))
	defer searx.Close()
	w := newWebTools(Options{SearchURL: searx.URL, AllowPrivate: true})
	tool := &searchTool{w: w}
	first := runTool(t, tool, autoEnv(), map[string]any{"query": "backend tag"})
	if first.IsError || first.Meta["backend"] != "searxng" {
		t.Fatalf("first answer should be tagged searxng: %+v", first.Meta)
	}
	second := runTool(t, tool, autoEnv(), map[string]any{"query": "Backend Tag"})
	if second.Meta["backend"] != "searxng" {
		t.Fatalf("cached answer lost its backend tag: %+v", second.Meta)
	}
}
