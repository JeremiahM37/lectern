package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
)

// TestTrackerWritesNeedAHuman: every route that changes a tracker or its
// credentials refuses an authenticated caller that is not a person — a
// process on the host, an agent, a tagged service node — before touching
// anything, while reads stay open to it.
func TestTrackerWritesNeedAHuman(t *testing.T) {
	s := mcpTestServer(t, auth.ModeTailscale)
	mux := http.NewServeMux()
	s.registerTrackerRoutes(mux)
	writes := []struct{ method, path, body string }{
		{"POST", "/api/projects/1/trackers", `{"kind":"linear"}`},
		{"PATCH", "/api/trackers/1", `{"name":"x"}`},
		{"DELETE", "/api/trackers/1", ``},
		{"POST", "/api/trackers/1/issues/ENG-1/status", `{"id":"s"}`},
		{"POST", "/api/trackers/1/issues/ENG-1/comments", `{"body":"x"}`},
		{"POST", "/api/projects/1/forge/prs/1/merge", `{"method":"merge","confirm":true,"head_sha":"a"}`},
		{"DELETE", "/api/projects/1/forge/prs/1/auto-merge", ``},
		{"POST", "/api/projects/1/forge/prs/1/reviewers", `{"add":["a"]}`},
		{"POST", "/api/projects/1/forge/prs/1/labels", `{"add":["a"]}`},
		{"POST", "/api/projects/1/forge/issues/1/comments", `{"body":"x"}`},
		{"POST", "/api/projects/1/forge/issues/1/state", `{"open":false}`},
		{"POST", "/api/projects/1/forge/prs/1/resolve", `{}`},
		{"POST", "/api/projects/1/forge/prs/1/fix-checks", `{}`},
		{"POST", "/api/projects/1/forge/prs/1/reactions", `{"emoji":"+1"}`},
		{"POST", "/api/trackers/1/issues/ENG-1/reactions", `{"emoji":"+1"}`},
		{"POST", "/api/trackers/1/issues/ENG-1/description", `{"body":"x"}`},
		{"POST", "/api/projects/1/forge/queue/remove", `{"base":"main","id":"x"}`},
	}
	for _, p := range []auth.Principal{
		{Kind: auth.KindLocal},
		{Kind: auth.KindTailscale, Node: "tagged-worker", Human: false},
	} {
		for _, wr := range writes {
			r := httptest.NewRequest(wr.method, wr.path, strings.NewReader(wr.body))
			r = r.WithContext(auth.WithPrincipal(r.Context(), p))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Errorf("%s %s as %+v: %d %s", wr.method, wr.path, p, w.Code, w.Body.String())
			}
		}
	}
	// a read by the same caller is not refused for being non-human (it gets
	// as far as the missing project)
	r := httptest.NewRequest("GET", "/api/projects/1/work", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Kind: auth.KindLocal}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("read as a local process: %d %s", w.Code, w.Body.String())
	}
	// and a person gets past the gate
	r = httptest.NewRequest("POST", "/api/projects/1/trackers", strings.NewReader(`{"kind":"linear"}`))
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Kind: auth.KindTailscale, Login: "me@example.com", Human: true}))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("human create on a missing project: %d %s", w.Code, w.Body.String())
	}
}
