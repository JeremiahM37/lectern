package localruntime

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/version"
)

func TestLocalHandlerRequiresIdentityAndReportsStopConflict(t *testing.T) {
	h := localHandler(http.NotFoundHandler(), newGate("secret", "browser-key"), "instance", func() error {
		return errors.New("active task must finish first")
	})
	server := httptest.NewServer(h)
	defer server.Close()

	res, err := http.Get(server.URL + identityRoute)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated identity status = %d", res.StatusCode)
	}
	_ = res.Body.Close()
	req, _ := http.NewRequest(http.MethodPost, server.URL+engineRoute, nil)
	req.Header.Set("Authorization", "Bearer secret")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("stop status = %d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "active task") {
		t.Fatalf("stop response omitted actionable conflict: %q", body)
	}
}

func TestHealthyEndpointRejectsWrongAuthenticatedInstance(t *testing.T) {
	dir := t.TempDir()
	token := "0123456789abcdef0123456789abcdef"
	ep := Endpoint{URL: "http://127.0.0.1:1", Token: token, Instance: "expected", PID: 42, Build: version.Current()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatalf("identity request did not carry bearer")
		}
		_, _ = w.Write([]byte(`{"instance":"different","pid":42,"version":"2.2.0","build":{"version":"2.2.0"}}`))
	}))
	defer server.Close()
	ep.URL = "http://" + strings.TrimPrefix(server.URL, "http://")
	if err := writeEndpoint(dir, ep); err != nil {
		t.Fatal(err)
	}
	if _, ok := healthyEndpoint(t.Context(), dir); ok {
		t.Fatal("accepted identity from wrong instance")
	}
}

func TestLocalEnvUsesPrivateTmuxNamespace(t *testing.T) {
	t.Setenv("TMUX", "/tmp/outer,1,0")
	t.Setenv("TMUX_TMPDIR", "/tmp/shared")
	got := localEnv("/private/lectern/tmux")
	values := map[string]string{}
	for _, item := range got {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = value
		}
	}
	if values["TMUX_TMPDIR"] != "/private/lectern/tmux" || values["TMUX"] != "" {
		t.Fatalf("local tmux environment not isolated: %v", values)
	}
}

func TestEngineConfigPointsHooksAtItsOwnPort(t *testing.T) {
	// What config.Load produces in the engine: LECTERN_PORT is stripped by
	// localEnv, so both bases name the default port.
	base := &config.Config{BaseURL: "http://127.0.0.1:9110", HookBase: "http://127.0.0.1:9110"}
	cfg := engineConfig(base, t.TempDir(), 41429, "token")
	if cfg.BaseURL != "http://127.0.0.1:41429" || cfg.HookBase != cfg.BaseURL {
		t.Fatalf("hooks would call back to %q while the runtime listens on %q", cfg.HookBase, cfg.BaseURL)
	}
	// An operator's explicit LECTERN_HOOK_BASE is kept.
	base.HookBase = "http://100.64.0.9:7000"
	if cfg := engineConfig(base, t.TempDir(), 41429, "token"); cfg.HookBase != "http://100.64.0.9:7000" {
		t.Fatalf("explicit hook base replaced: %q", cfg.HookBase)
	}
}
