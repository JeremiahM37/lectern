package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
)

func hookCheckServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s := &Server{Cfg: &config.Config{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/hook/ping", s.hookPingHandler)
	mux.HandleFunc("GET /api/diagnostics/hooks", s.hookDiagnostics)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return s, ts
}

func hookCheck(t *testing.T, url string) (bool, string) {
	t.Helper()
	res, err := http.Get(url + "/api/diagnostics/hooks")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var got struct {
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	return got.OK, got.Detail
}

func TestHookDiagnosticsNeedsThisServerToAnswer(t *testing.T) {
	self, selfURL := hookCheckServer(t)
	_, other := hookCheckServer(t)

	self.Cfg.HookBase = selfURL.URL
	if ok, detail := hookCheck(t, selfURL.URL); !ok {
		t.Fatalf("own hook base failed: %s", detail)
	}
	// The bug this guards: a runtime handing its sessions another server's
	// address (the default :9110) must not report OK.
	self.Cfg.HookBase = other.URL
	if ok, detail := hookCheck(t, selfURL.URL); ok || !strings.Contains(detail, "different Lectern") {
		t.Fatalf("another server's hook base passed: ok=%v %s", ok, detail)
	}
	self.Cfg.HookBase = "http://127.0.0.1:1"
	if ok, detail := hookCheck(t, selfURL.URL); ok || !strings.Contains(detail, "nothing answers") {
		t.Fatalf("unreachable hook base passed: ok=%v %s", ok, detail)
	}
}
