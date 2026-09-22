package creds

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// fakeExec records commands without touching anything.
type fakeExec struct{ cmds []string }

func (f *fakeExec) Run(_ context.Context, cmd string, _ executor.RunOpts) (executor.Result, error) {
	f.cmds = append(f.cmds, cmd)
	return executor.Result{}, nil
}
func (f *fakeExec) ReadFile(context.Context, string, int64) ([]byte, error) { return nil, nil }
func (f *fakeExec) WriteFile(context.Context, string, []byte) error         { return nil }
func (f *fakeExec) Close() error                                            { return nil }

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.NewFile(0, os.DevNull), nil))
}

func writeCreds(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "creds.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func hasCmd(cmds []string, want string) bool {
	for _, c := range cmds {
		if strings.Contains(c, want) {
			return true
		}
	}
	return false
}

func TestAPIKeyWinsAndSkipsPush(t *testing.T) {
	p := New(writeCreds(t, "{}"), "", "sk-ant-test", quietLog())
	if got := p.BaseAgentEnv()["ANTHROPIC_API_KEY"]; got != "sk-ant-test" {
		t.Fatalf("env: %v", p.BaseAgentEnv())
	}
	ex := &fakeExec{}
	p.Provision(context.Background(), ex, "ssh", "t", "claude")
	if len(ex.cmds) != 0 {
		t.Fatalf("nothing should be pushed — the key is injected via env: %v", ex.cmds)
	}
}

func TestNoKeyMeansNoEnv(t *testing.T) {
	p := New("", "", "", quietLog())
	if len(p.BaseAgentEnv()) != 0 {
		t.Fatalf("env: %v", p.BaseAgentEnv())
	}
}

// The recurring remote 401 was OAuth REFRESH-token rotation: a copy pushed once
// goes stale the moment the control plane's own CLI refreshes.
func TestOAuthProvisionPushesCurrentCreds(t *testing.T) {
	path := writeCreds(t, `{"claudeAiOauth": {"refreshToken": "rt-current"}}`)
	p := New(path, "", "", quietLog())
	ex := &fakeExec{}
	p.Provision(context.Background(), ex, "ssh", "lxc-101", "claude")
	if !hasCmd(ex.cmds, "base64 -d > ~/.claude/.credentials.json") {
		t.Errorf("credentials were not written: %v", ex.cmds)
	}
	if !hasCmd(ex.cmds, "chmod 600 ~/.claude/.credentials.json") {
		t.Errorf("credentials were left world-readable: %v", ex.cmds)
	}
}

func TestCodexCredentialsGoToTheirOwnPath(t *testing.T) {
	p := New(writeCreds(t, "{}"), writeCreds(t, `{"tokens":{}}`), "", quietLog())
	ex := &fakeExec{}
	p.Provision(context.Background(), ex, "ssh", "cx", "codex")
	if !hasCmd(ex.cmds, "~/.codex/auth.json") {
		t.Errorf("codex auth path: %v", ex.cmds)
	}
	// and claude's credentials are NOT pushed for a codex run
	if hasCmd(ex.cmds, ".claude/.credentials.json") {
		t.Errorf("a codex run must not push claude credentials: %v", ex.cmds)
	}
}

func TestGeminiHasNoCredentialsToPush(t *testing.T) {
	p := New(writeCreds(t, "{}"), writeCreds(t, "{}"), "", quietLog())
	ex := &fakeExec{}
	p.Provision(context.Background(), ex, "ssh", "gm", "gemini")
	if len(ex.cmds) != 0 {
		t.Fatalf("gemini has no known credential file: %v", ex.cmds)
	}
}

func TestLocalAndMockTargetsAreNoop(t *testing.T) {
	p := New(writeCreds(t, "{}"), "", "", quietLog())
	for _, kind := range []string{"local", "mock"} {
		ex := &fakeExec{}
		p.Provision(context.Background(), ex, kind, kind, "claude")
		if len(ex.cmds) != 0 {
			t.Errorf("%s uses the control plane's own creds: %v", kind, ex.cmds)
		}
	}
}

func TestMissingCredsIsGraceful(t *testing.T) {
	p := New("/nonexistent/creds.json", "", "", quietLog())
	ex := &fakeExec{}
	p.Provision(context.Background(), ex, "ssh", "t", "claude") // must not panic
	if len(ex.cmds) != 0 {
		t.Fatalf("nothing to push: %v", ex.cmds)
	}
}
