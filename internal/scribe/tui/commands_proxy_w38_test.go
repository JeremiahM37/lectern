package tui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/proxy"
)

// proxyTestBackend is the fake backend with a proxy sidecar attached.
type proxyTestBackend struct {
	*FakeBackend
	sc *proxy.Sidecar
}

func (p *proxyTestBackend) ProxySidecar() *proxy.Sidecar { return p.sc }

// newProxyTestModel is newTestModel with a backend that manages a proxy.
func newProxyTestModel(t *testing.T, sc *proxy.Sidecar) *Model {
	t.Helper()
	fb := NewFakeBackend()
	fb.Delay = 0
	pb := &proxyTestBackend{FakeBackend: fb, sc: sc}
	m := NewModel(pb, func() (Backend, error) {
		n := NewFakeBackend()
		n.Delay = 0
		return n, nil
	}, Options{Width: 100, Height: 30, Theme: "scribe"})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m
}

// proxyTestSidecar is a sidecar with no binary on PATH, and an auth dir and
// home under t.TempDir(). vendored is the binary the sidecar falls back to.
func proxyTestSidecar(t *testing.T, vendored string) (*proxy.Sidecar, string) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("PROXY_API_KEY", "")
	authDir := filepath.Join(t.TempDir(), "auth")
	sc := proxy.NewSidecar(proxy.SidecarOptions{
		BaseURL:  "http://127.0.0.1:1/v1",
		Home:     filepath.Join(t.TempDir(), "proxy"),
		AuthDir:  authDir,
		Vendored: vendored,
	})
	return sc, authDir
}

// fakeClaudeLogin is a binary whose -help lists the Claude login flag. Login
// is never run in these tests, only planned.
func fakeClaudeLogin(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake proxy is a shell script")
	}
	bin := filepath.Join(t.TempDir(), "cliproxyapi-fake")
	script := "#!/bin/sh\nprintf '  -claude-login\\n'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestProxyCommandIsRegistered(t *testing.T) {
	for _, c := range commandTable {
		if c.Name == "proxy" {
			return
		}
	}
	t.Fatal("/proxy is not in the command table")
}

func TestProxyNeedsAProxyBackend(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	fm := flashOf(t, proxyCommand(m, "status"))
	if fm.kind != "err" || !strings.Contains(fm.text, "no proxy support") {
		t.Fatalf("flash = %+v, want an error about proxy support", fm)
	}
}

func TestProxyStatusOpensPopupWithInstallHint(t *testing.T) {
	sc, _ := proxyTestSidecar(t, filepath.Join(t.TempDir(), "missing"))
	m := newProxyTestModel(t, sc)
	if cmd := proxyCommand(m, ""); cmd != nil {
		t.Fatalf("status should open a popup, not return a command")
	}
	iv, ok := m.topModal().(*InfoView)
	if !ok {
		t.Fatalf("top modal is %T, want the status popup", m.topModal())
	}
	if iv.title != "Proxy status" {
		t.Errorf("popup title = %q", iv.title)
	}
	joined := strings.Join(iv.lines, "\n")
	if !strings.Contains(joined, "Binary: not installed") || !strings.Contains(joined, "github.com/router-for-me/CLIProxyAPI") {
		t.Errorf("popup does not say how to install:\n%s", joined)
	}
}

func TestProxyStatusLinesCoverEachState(t *testing.T) {
	running := strings.Join(proxyStatusLines(proxy.SidecarStatus{Base: "b", State: proxy.Running, Owned: true, Binary: "/x/cliproxyapi",
		Accounts: []string{"claude-a.json"}}), "\n")
	for _, want := range []string{"State: running", "Started by: this Scribe", "Binary: /x/cliproxyapi", "Accounts: 1 (claude-a.json)"} {
		if !strings.Contains(running, want) {
			t.Errorf("running status lacks %q:\n%s", want, running)
		}
	}
	rejected := strings.Join(proxyStatusLines(proxy.SidecarStatus{Base: "b", State: proxy.NeedsAuth}), "\n")
	if !strings.Contains(rejected, "needs login") || !strings.Contains(rejected, "Accounts: none") {
		t.Errorf("rejected status:\n%s", rejected)
	}
	// Something answered, so it is up and has an owner worth naming.
	if !strings.Contains(rejected, "Started by") {
		t.Errorf("a proxy that answered should say who started it:\n%s", rejected)
	}
	down := strings.Join(proxyStatusLines(proxy.SidecarStatus{Base: "b", State: proxy.Unreachable}), "\n")
	if strings.Contains(down, "Started by") {
		t.Errorf("a proxy that is not answering should not say who started it:\n%s", down)
	}
}

func TestProxyLoginWithoutBinaryFlashesInstallHint(t *testing.T) {
	sc, _ := proxyTestSidecar(t, filepath.Join(t.TempDir(), "missing"))
	m := newProxyTestModel(t, sc)
	fm := flashOf(t, proxyCommand(m, "login claude"))
	if fm.kind != "err" || !strings.Contains(fm.text, "Proxy addon not installed") || !strings.Contains(fm.text, "github.com/router-for-me/CLIProxyAPI") {
		t.Fatalf("flash = %+v, want the not-installed message with the install page", fm)
	}
}

func TestProxyLoginUnknownProviderListsValidOnes(t *testing.T) {
	sc, _ := proxyTestSidecar(t, fakeClaudeLogin(t))
	m := newProxyTestModel(t, sc)
	fm := flashOf(t, proxyCommand(m, "login bogus"))
	if fm.kind != "err" || !strings.Contains(fm.text, "xai") || !strings.Contains(fm.text, "codex-device") {
		t.Fatalf("flash = %+v, want the valid providers listed", fm)
	}
}

func TestProxyLoginNeedsAProvider(t *testing.T) {
	sc, _ := proxyTestSidecar(t, fakeClaudeLogin(t))
	m := newProxyTestModel(t, sc)
	fm := flashOf(t, proxyCommand(m, "login"))
	if fm.kind != "err" || !strings.Contains(fm.text, "Usage: /proxy login <provider>") {
		t.Fatalf("flash = %+v, want usage", fm)
	}
}

func TestProxyLoginStartsTheTerminalHandover(t *testing.T) {
	sc, _ := proxyTestSidecar(t, fakeClaudeLogin(t))
	m := newProxyTestModel(t, sc)
	cmd := proxyCommand(m, "login claude")
	if cmd == nil {
		t.Fatal("login should return a command that hands over the terminal")
	}
}

func TestProxyLoginFlashReportsSavedAndMissing(t *testing.T) {
	saved := proxyLoginFlash(proxy.LoginResult{Provider: "claude", Name: "Claude",
		Added: []string{"claude-user.json"}, State: proxy.Running})
	if saved.kind != "ok" || !strings.Contains(saved.text, "claude-user.json") || !strings.Contains(saved.text, "running") {
		t.Errorf("saved flash = %+v", saved)
	}
	missing := proxyLoginFlash(proxy.LoginResult{Provider: "claude", Name: "Claude", State: proxy.NeedsAuth})
	if missing.kind != "info" || !strings.Contains(missing.text, "/proxy login claude") || !strings.Contains(missing.text, "needs login") {
		t.Errorf("missing flash = %+v", missing)
	}
}

func TestProxyLogoutRemovesAndReports(t *testing.T) {
	sc, authDir := proxyTestSidecar(t, fakeClaudeLogin(t))
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authDir, "claude-a.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newProxyTestModel(t, sc)
	fm := flashOf(t, proxyCommand(m, "logout claude"))
	if fm.kind != "ok" || !strings.Contains(fm.text, "claude-a.json") {
		t.Fatalf("first logout flash = %+v", fm)
	}
	if fm := flashOf(t, proxyCommand(m, "logout claude")); fm.kind != "info" || !strings.Contains(fm.text, "No saved account") {
		t.Fatalf("second logout flash = %+v, want nothing to remove", fm)
	}
}

func TestProxyUnknownSubcommandShowsUsage(t *testing.T) {
	sc, _ := proxyTestSidecar(t, fakeClaudeLogin(t))
	m := newProxyTestModel(t, sc)
	for _, args := range []string{"frobnicate", "status now", "login claude extra"} {
		fm := flashOf(t, proxyCommand(m, args))
		if fm.kind != "err" || !strings.Contains(fm.text, "/proxy login <provider>") {
			t.Errorf("%q: flash = %+v, want usage", args, fm)
		}
	}
}
