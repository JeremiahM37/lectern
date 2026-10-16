package fs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// CheckWrite applies the edit tools' write rules to a path a caller writes
// without them: secret names and .git are refused, CI files need approval.
func TestCheckWriteRefusesSecretsAndGitAndAsksForCI(t *testing.T) {
	dir := t.TempDir()
	env := &tools.Env{Workdir: dir, Mode: "default", Host: &denyHost{}}
	mk := func(rel string) string {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ctx := context.Background()

	if err := CheckWrite(ctx, env, "rename", mk("pkg/a.go")); err != nil {
		t.Fatalf("an ordinary source file should be writable: %v", err)
	}
	if err := CheckWrite(ctx, env, "rename", mk(".env")); err == nil || !strings.Contains(err.Error(), "secret file") {
		t.Fatalf(".env must be refused as a secret file, got %v", err)
	}
	if err := CheckWrite(ctx, env, "rename", mk("keys/server.pem")); err == nil || !strings.Contains(err.Error(), "secret file") {
		t.Fatalf("a key file must be refused, got %v", err)
	}
	if err := CheckWrite(ctx, env, "rename", filepath.Join(dir, ".git", "config")); err == nil || !strings.Contains(err.Error(), ".git") {
		t.Fatalf(".git internals must be refused, got %v", err)
	}
	// CI definitions need approval; a host that refuses gets a refusal.
	if err := CheckWrite(ctx, env, "rename", mk(".github/workflows/ci.yml")); err == nil || !strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("a CI file should be denied without approval, got %v", err)
	}
	ok := &tools.Env{Workdir: dir, Mode: "default", Host: &allowHost{}}
	if err := CheckWrite(ctx, ok, "rename", mk(".github/workflows/ci.yml")); err != nil {
		t.Fatalf("an approved CI file should be writable: %v", err)
	}
	if err := CheckWrite(ctx, nil, "rename", mk("a.go")); err == nil {
		t.Fatal("no environment means no check, so it must refuse")
	}
}

// denyHost refuses every approval; allowHost approves every one.
type denyHost struct{}

func (denyHost) Approve(context.Context, tools.Approval) tools.Decision { return tools.Deny }
func (denyHost) Ask(context.Context, tools.Question) (string, error)    { return "", nil }
func (denyHost) Progress(string, string)                                {}

type allowHost struct{ denyHost }

func (allowHost) Approve(context.Context, tools.Approval) tools.Decision { return tools.AllowOnce }
