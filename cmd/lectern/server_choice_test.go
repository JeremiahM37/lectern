package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/cmd/lectern/localruntime"
	"github.com/JeremiahM37/lectern/v2/internal/config"
)

// fakeHosted answers like `lectern serve`: health is JSON, and every
// non-API path (the local identity route included) is the web app. With a
// token set it behaves like token auth mode.
func fakeHosted(t *testing.T, token string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/health":
			if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"detail":"unauthorized"}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"version":"9.9.9","build":{"version":"9.9.9"}}`))
		case "/.well-known/agent-card.json":
			_, _ = w.Write([]byte(`{"name":"Lectern","version":"9.9.9"}`))
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<!doctype html>"))
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// fakeLocalRuntime answers health but guards its identity route, like the
// private local runtime someone else (or a stale endpoint file) left behind.
func fakeLocalRuntime(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__lectern_local/identity" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"version":"2.4.0-dev","build":{"version":"2.4.0-dev"}}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func portOf(t *testing.T, s *httptest.Server) int {
	t.Helper()
	p, err := strconv.Atoi(s.URL[strings.LastIndex(s.URL, ":")+1:])
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// deadPort is a loopback port with nothing listening.
func deadPort(t *testing.T) int {
	t.Helper()
	s := httptest.NewServer(http.NotFoundHandler())
	p := portOf(t, s)
	s.Close()
	return p
}

// isolateChoice gives the test its own empty local-runtime state and no
// service env files, so only the servers it starts can be found.
func isolateChoice(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("TMUX_TMPDIR", "")
	t.Setenv("LECTERN_PORT", "")
	old := serviceEnvFiles
	serviceEnvFiles = nil
	t.Cleanup(func() { serviceEnvFiles = old })
}

func TestProbeServiceIdentifiesHostedLectern(t *testing.T) {
	hosted := fakeHosted(t, "")
	svc, ok := probeService(t.Context(), hosted.URL, "")
	if !ok || svc.Refused || svc.Version != "9.9.9" {
		t.Fatalf("hosted service not recognised: %+v ok=%v", svc, ok)
	}
	if _, ok := probeService(t.Context(), fakeLocalRuntime(t).URL, ""); ok {
		t.Fatal("a private local runtime was taken for a hosted service")
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"fine"}`))
	}))
	defer other.Close()
	if _, ok := probeService(t.Context(), other.URL, ""); ok {
		t.Fatal("an unrelated HTTP service was taken for Lectern")
	}
	if _, ok := probeService(t.Context(), "http://127.0.0.1:"+strconv.Itoa(deadPort(t)), ""); ok {
		t.Fatal("nothing listening was taken for Lectern")
	}
}

func TestProbeServiceTokenMode(t *testing.T) {
	hosted := fakeHosted(t, "secret")
	svc, ok := probeService(t.Context(), hosted.URL, "")
	if !ok || !svc.Refused {
		t.Fatalf("token-mode service without a token: %+v ok=%v", svc, ok)
	}
	svc, ok = probeService(t.Context(), hosted.URL, "secret")
	if !ok || svc.Refused {
		t.Fatalf("token-mode service with its token: %+v ok=%v", svc, ok)
	}
}

func TestChooseServerOrder(t *testing.T) {
	isolateChoice(t)
	hosted := fakeHosted(t, "")

	// (1) a hosted service on the configured port wins.
	choice := chooseServer(t.Context(), &config.Config{Port: portOf(t, hosted)})
	if choice.Hosted == nil || choice.Hosted.URL != hosted.URL {
		t.Fatalf("hosted service not chosen: %+v", choice)
	}

	// (2) nothing there: the private local runtime.
	choice = chooseServer(t.Context(), &config.Config{Port: deadPort(t)})
	if choice.Hosted != nil || choice.RefusedService != nil || choice.note() != "" {
		t.Fatalf("expected local runtime, got %+v", choice)
	}

	// A local runtime on the probed port is never a hosted service.
	choice = chooseServer(t.Context(), &config.Config{Port: portOf(t, fakeLocalRuntime(t))})
	if choice.Hosted != nil {
		t.Fatalf("local runtime chosen as the hosted service: %+v", choice)
	}

	// A service that refuses this CLI falls to local, and says why.
	refusing := fakeHosted(t, "secret")
	choice = chooseServer(t.Context(), &config.Config{Port: portOf(t, refusing)})
	if choice.Hosted != nil || choice.RefusedService == nil || !strings.Contains(choice.note(), "LECTERN_AUTH_TOKEN") {
		t.Fatalf("refusing service: %+v note=%q", choice, choice.note())
	}
	choice = chooseServer(t.Context(), &config.Config{Port: portOf(t, refusing), AuthToken: "secret"})
	if choice.Hosted == nil {
		t.Fatalf("token did not select the service: %+v", choice)
	}

	// Inside a local runtime session its commands stay on that runtime.
	dir, err := localruntime.TmuxDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_TMPDIR", dir)
	choice = chooseServer(t.Context(), &config.Config{Port: portOf(t, hosted)})
	if choice.Hosted != nil || !choice.InLocalSession {
		t.Fatalf("local session left its runtime: %+v", choice)
	}
}

func TestChooseServerReadsServiceEnvFile(t *testing.T) {
	isolateChoice(t)
	hosted := fakeHosted(t, "")
	envFile := filepath.Join(t.TempDir(), "lectern.env")
	if err := os.WriteFile(envFile, []byte("# service\nLECTERN_TLS_PORT=8443\nLECTERN_PORT=\""+strconv.Itoa(portOf(t, hosted))+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	serviceEnvFiles = []string{filepath.Join(t.TempDir(), "missing.env"), envFile}
	choice := chooseServer(t.Context(), &config.Config{Port: deadPort(t)})
	if choice.Hosted == nil || choice.Hosted.URL != hosted.URL {
		t.Fatalf("service port from env file not probed: %+v", choice)
	}
	// An explicit LECTERN_PORT names the service; the files are not read.
	t.Setenv("LECTERN_PORT", "1")
	if ports := servicePorts(&config.Config{Port: 1}); len(ports) != 1 {
		t.Fatalf("explicit LECTERN_PORT still read env files: %v", ports)
	}
}

func TestServerChoiceNote(t *testing.T) {
	both := serverChoice{Hosted: &hostedService{URL: "http://127.0.0.1:9110"}, LocalRunning: true}
	if got := both.note(); got != "lectern: using the Lectern service on :9110; `lectern local …` uses your private runtime" {
		t.Fatalf("note = %q", got)
	}
	if got := (serverChoice{Hosted: &hostedService{URL: "http://127.0.0.1:9110"}}).note(); got != "" {
		t.Fatalf("hosted-only choice printed a note: %q", got)
	}
}

func TestRoutesToServer(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{nil, true}, {[]string{"api", "GET", "/health"}, true}, {[]string{"plugin", "list"}, true},
		{[]string{"restore"}, true}, {[]string{"claude"}, true}, {[]string{"attach", "session", "1"}, true},
		{[]string{"mcp"}, true}, {[]string{"my-custom-agent"}, true},
		{[]string{"mcp", "--http"}, false}, {[]string{"plugin", "new", "x"}, false}, {[]string{"plugin", "validate"}, false},
		{[]string{"local", "api"}, false}, {[]string{"up"}, false}, {[]string{"doctor"}, false},
		{[]string{"serve"}, false}, {[]string{"version"}, false}, {[]string{"help"}, false},
		{[]string{"--hosted-attach", "attach"}, false}, {[]string{"relay"}, false},
	}
	for _, c := range cases {
		if got := routesToServer(c.args, true); got != c.want {
			t.Errorf("routesToServer(%q) = %v, want %v", c.args, got, c.want)
		}
	}
	if routesToServer(nil, false) {
		t.Error("bare non-interactive lectern should serve, not probe")
	}
}

func TestDoctorServerChecks(t *testing.T) {
	hosted := serverChoice{Hosted: &hostedService{URL: "http://127.0.0.1:9110", Version: "2.4.1"}, LocalRunning: true}
	stale := localruntime.Status{State: "running", Version: "2.4.0-dev", CLIVersion: "2.4.1", Outdated: true, Endpoint: &localruntime.Endpoint{URL: "http://127.0.0.1:37619"}}

	report, ok := renderDoctorReport(serverChecks("", hosted, stale))
	if !ok || !strings.Contains(report, "Lectern service at http://127.0.0.1:9110 (2.4.1)") || !strings.Contains(report, "older than this CLI (2.4.1)") || !strings.Contains(report, "lectern local stop") {
		t.Fatalf("hosted + stale local report (ok=%v):\n%s", ok, report)
	}
	report, ok = renderDoctorReport(serverChecks("", serverChoice{}, stale))
	if ok || !strings.Contains(report, "private local runtime") || !strings.Contains(report, "fix: run `lectern local stop`") {
		t.Fatalf("stale local runtime in use must fail with a fix (ok=%v):\n%s", ok, report)
	}
	report, ok = renderDoctorReport(serverChecks("", serverChoice{RefusedService: &hostedService{URL: "http://127.0.0.1:9110"}}, localruntime.Status{State: "stopped"}))
	if ok || !strings.Contains(report, "refused this CLI") || !strings.Contains(report, "LECTERN_AUTH_TOKEN") {
		t.Fatalf("refusing service report (ok=%v):\n%s", ok, report)
	}
	report, _ = renderDoctorReport(serverChecks("https://lectern.example", serverChoice{}, localruntime.Status{State: "stopped"}))
	if !strings.Contains(report, "LECTERN_API=https://lectern.example") {
		t.Fatalf("explicit API report:\n%s", report)
	}
}
