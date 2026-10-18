package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The login tests use a shell script as the binary rather than the test
// binary, so the fake in sidecar_test.go stays the only proxy fake. The script
// uses shell builtins only, because PATH is emptied for these tests.
func skipWithoutSh(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("login fakes are shell scripts")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
}

// writeFakeLogin writes a fake CLIProxyAPI. -help prints the given flags, one
// per line as Go's flag package does. Each login flag, when save is true,
// writes <flag without dashes>-user.json into authDir, which is what a real
// login leaves behind.
func writeFakeLogin(t *testing.T, flags []string, authDir string, save bool) string {
	t.Helper()
	var help strings.Builder
	for _, f := range flags {
		fmt.Fprintf(&help, "  %s\n    \tfake login\n", f)
	}
	var cases strings.Builder
	if save {
		for _, f := range flags {
			// -claude-login writes claude-user.json, and the bare -login
			// (Google) writes gemini-user.json, as CLIProxyAPI names them.
			name := strings.TrimSuffix(strings.TrimPrefix(f, "-"), "-login")
			if name == "login" || name == "" {
				name = "gemini"
			}
			fmt.Fprintf(&cases, "    %s) printf '{}' > '%s/%s-user.json' ;;\n", f, authDir, name)
		}
	}
	script := fmt.Sprintf(`#!/bin/sh
# Fake CLIProxyAPI for the login tests.
for a in "$@"; do
  if [ "$a" = "-help" ]; then
    printf '%%s' '%s'
    exit 0
  fi
done
for a in "$@"; do
  case "$a" in
%s    *) ;;
  esac
done
`, help.String(), cases.String())
	bin := filepath.Join(t.TempDir(), "cliproxyapi-fake")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// loginSidecar is a sidecar whose binary is the fake, with an empty PATH so
// no real cliproxyapi is found, and a base URL nothing listens on.
func loginSidecar(t *testing.T, bin, base string) (*Sidecar, string, string) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("PROXY_API_KEY", "")
	home := filepath.Join(t.TempDir(), "proxy")
	authDir := filepath.Join(t.TempDir(), "auth")
	if base == "" {
		base = "http://127.0.0.1:1/v1"
	}
	return NewSidecar(SidecarOptions{BaseURL: base, Home: home, AuthDir: authDir, Vendored: bin}), home, authDir
}

var allLoginFlags = []string{"-claude-login", "-login", "-codex-login", "-codex-device-login", "-antigravity-login", "-xai-login", "-kimi-login"}

func eqStrings(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func TestLoginProvidersListsSpecLogins(t *testing.T) {
	want := []string{"claude", "google", "codex", "codex-device", "antigravity", "xai", "kimi"}
	if got := LoginProviders(); !eqStrings(got, want) {
		t.Fatalf("LoginProviders() = %v, want %v", got, want)
	}
}

func TestPlanLoginNotInstalledWritesNothing(t *testing.T) {
	skipWithoutSh(t)
	s, home, authDir := loginSidecar(t, filepath.Join(t.TempDir(), "missing"), "")
	_, err := s.PlanLogin(context.Background(), "claude")
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("PlanLogin error = %v, want ErrNotInstalled", err)
	}
	if !strings.Contains(err.Error(), "github.com/router-for-me/CLIProxyAPI") {
		t.Errorf("error does not say where to install the proxy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "config.yaml")); !os.IsNotExist(err) {
		t.Errorf("config was written although nothing is installed: %v", err)
	}
	if _, err := os.Stat(authDir); !os.IsNotExist(err) {
		t.Errorf("auth dir was created although nothing is installed: %v", err)
	}
}

func TestPlanLoginUnknownProviderListsValidOnes(t *testing.T) {
	skipWithoutSh(t)
	s, _, _ := loginSidecar(t, writeFakeLogin(t, allLoginFlags, t.TempDir(), false), "")
	_, err := s.PlanLogin(context.Background(), "bogus")
	if err == nil {
		t.Fatal("expected an error for an unknown provider")
	}
	for _, id := range LoginProviders() {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("error %q does not list %q", err, id)
		}
	}
}

func TestPlanLoginRefusesBinaryWithoutTheFlag(t *testing.T) {
	skipWithoutSh(t)
	// This build only knows the Claude login, so xAI must not start the server.
	s, _, _ := loginSidecar(t, writeFakeLogin(t, []string{"-claude-login"}, t.TempDir(), false), "")
	_, err := s.PlanLogin(context.Background(), "xai")
	if err == nil || !strings.Contains(err.Error(), "-xai-login") || !strings.Contains(err.Error(), "Upgrade") {
		t.Fatalf("PlanLogin error = %v, want a message naming -xai-login and asking for an upgrade", err)
	}
}

func TestLoginSavesCredentialAndReportsIt(t *testing.T) {
	skipWithoutSh(t)
	authDir := filepath.Join(t.TempDir(), "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	s, _, _ := loginSidecar(t, writeFakeLogin(t, allLoginFlags, authDir, true), "")
	s.authDir = authDir

	plan, err := s.PlanLogin(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Command, "'-claude-login'") || !strings.Contains(plan.Command, "'-config'") {
		t.Fatalf("command %q lacks the login flag or -config", plan.Command)
	}
	// The terminal runs the command; the test runs it the same way.
	if out, err := exec.Command("/bin/sh", "-c", plan.Command).CombinedOutput(); err != nil {
		t.Fatalf("login command failed: %v: %s", err, out)
	}
	res := s.FinishLogin(context.Background(), plan)
	if !eqStrings(res.Added, []string{"claude-user.json"}) || len(res.Updated) != 0 {
		t.Fatalf("FinishLogin added=%v updated=%v, want added [claude-user.json]", res.Added, res.Updated)
	}
	if res.State != Unreachable {
		t.Errorf("state = %s, want unreachable (nothing listens on the test port)", res.State)
	}
}

func TestLoginThatSavesNothingIsReported(t *testing.T) {
	skipWithoutSh(t)
	authDir := filepath.Join(t.TempDir(), "auth")
	s, _, _ := loginSidecar(t, writeFakeLogin(t, allLoginFlags, authDir, false), "")
	s.authDir = authDir
	plan, err := s.PlanLogin(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/bin/sh", "-c", plan.Command).CombinedOutput(); err != nil {
		t.Fatalf("login command failed: %v: %s", err, out)
	}
	res := s.FinishLogin(context.Background(), plan)
	if len(res.Added) != 0 || len(res.Updated) != 0 {
		t.Fatalf("a login that saved nothing reported added=%v updated=%v", res.Added, res.Updated)
	}
}

func TestLoginRewrittenCredentialCountsAsUpdated(t *testing.T) {
	skipWithoutSh(t)
	authDir := filepath.Join(t.TempDir(), "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(authDir, "claude-user.json")
	if err := os.WriteFile(old, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	s, _, _ := loginSidecar(t, writeFakeLogin(t, allLoginFlags, authDir, true), "")
	s.authDir = authDir
	plan, err := s.PlanLogin(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/bin/sh", "-c", plan.Command).CombinedOutput(); err != nil {
		t.Fatalf("login command failed: %v: %s", err, out)
	}
	res := s.FinishLogin(context.Background(), plan)
	if len(res.Added) != 0 || !eqStrings(res.Updated, []string{"claude-user.json"}) {
		t.Fatalf("FinishLogin added=%v updated=%v, want updated [claude-user.json]", res.Added, res.Updated)
	}
}

func TestFinishLoginReportsRunningProxy(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer ts.Close()
	s, _, _ := loginSidecar(t, writeFakeLogin(t, allLoginFlags, t.TempDir(), false), ts.URL+"/v1")
	res := s.FinishLogin(context.Background(), LoginPlan{Provider: "claude", Name: "Claude"})
	if res.State != Running {
		t.Fatalf("state = %s, want running", res.State)
	}
}

func TestLogoutRemovesOnlyThatProvidersAccounts(t *testing.T) {
	authDir := t.TempDir()
	for _, n := range []string{"claude-a.json", "claude-b.json", "gemini-x.json", "claude-notes.txt"} {
		if err := os.WriteFile(filepath.Join(authDir, n), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := &Sidecar{authDir: authDir}
	removed, err := s.Logout("claude")
	if err != nil {
		t.Fatal(err)
	}
	if !eqStrings(removed, []string{"claude-a.json", "claude-b.json"}) {
		t.Fatalf("removed %v, want the two claude accounts", removed)
	}
	for _, keep := range []string{"gemini-x.json", "claude-notes.txt"} {
		if _, err := os.Stat(filepath.Join(authDir, keep)); err != nil {
			t.Errorf("%s was removed: %v", keep, err)
		}
	}
}

func TestLogoutRefusesCodexDeviceAndUnknownProviders(t *testing.T) {
	s := &Sidecar{authDir: t.TempDir()}
	if _, err := s.Logout("codex-device"); err == nil || !strings.Contains(err.Error(), "/proxy logout codex") {
		t.Errorf("codex-device logout error = %v, want a pointer to /proxy logout codex", err)
	}
	if _, err := s.Logout("bogus"); err == nil || !strings.Contains(err.Error(), "claude, google") {
		t.Errorf("unknown logout error = %v, want the valid providers", err)
	}
}

func TestLogoutWithNoAuthDirRemovesNothing(t *testing.T) {
	s := &Sidecar{authDir: filepath.Join(t.TempDir(), "absent")}
	removed, err := s.Logout("claude")
	if err != nil || len(removed) != 0 {
		t.Fatalf("Logout on a missing dir = %v, %v; want nothing and no error", removed, err)
	}
}

func TestStatusReportsBinaryAndAccounts(t *testing.T) {
	skipWithoutSh(t)
	bin := writeFakeLogin(t, allLoginFlags, t.TempDir(), false)
	s, _, authDir := loginSidecar(t, bin, "")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"gemini-z.json", "claude-a.json"} {
		if err := os.WriteFile(filepath.Join(authDir, n), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st := s.Status(context.Background())
	if st.Binary != bin {
		t.Errorf("binary = %q, want %q", st.Binary, bin)
	}
	if !eqStrings(st.Accounts, []string{"claude-a.json", "gemini-z.json"}) {
		t.Errorf("accounts = %v", st.Accounts)
	}
	if st.Owned || st.State != Unreachable {
		t.Errorf("owned=%v state=%s, want not owned and unreachable", st.Owned, st.State)
	}
}

func TestStatusWithoutBinaryShowsNone(t *testing.T) {
	s, _, _ := loginSidecar(t, filepath.Join(t.TempDir(), "missing"), "")
	if st := s.Status(context.Background()); st.Binary != "" {
		t.Fatalf("binary = %q, want empty when nothing is installed", st.Binary)
	}
}

func TestShellCommandQuotesEveryArgument(t *testing.T) {
	skipWithoutSh(t)
	cmd := ShellCommand("printf", "%s|%s|%s", "a b", "it's", "$HOME")
	out, err := exec.Command("/bin/sh", "-c", cmd).Output()
	if err != nil {
		t.Fatalf("%s: %v", cmd, err)
	}
	if got := string(out); got != "a b|it's|$HOME" {
		t.Fatalf("output = %q, want the arguments unchanged", got)
	}
}
