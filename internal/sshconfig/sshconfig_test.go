package sshconfig

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	extra := filepath.Join(dir, "extra.conf")
	if err := os.WriteFile(extra, []byte("Host included\n  HostName 10.9.9.9\n  User inc\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config")
	body := `# comment
Include ` + extra + `
Host build-box builder
  HostName 10.0.0.5
  User dev
  Port 2222
  ProxyJump bastion
  ForwardAgent yes
  GSSAPIAuthentication yes

Host bastion
  HostName bastion.example
  User jump

Host *.corp !skip.corp
  User corp

Host *
  User fallback
  IdentityAgent none
`
	if err := os.WriteFile(cfg, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestAliasesSkipPatternsAndFollowInclude(t *testing.T) {
	got, err := Aliases(writeConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"included", "build-box", "builder", "bastion"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("aliases = %v, want %v", got, want)
	}
}

func TestFileResolverIsFirstMatchWins(t *testing.T) {
	h := fromFile(writeConfig(t), "build-box")
	if h.HostName != "10.0.0.5" || h.User != "dev" || h.Port != 2222 || h.ProxyJump != "bastion" ||
		!h.ForwardAgent || !h.GSSAPI {
		t.Fatalf("resolved %+v", h)
	}
	if b := fromFile(writeConfig(t), "bastion"); b.User != "jump" || b.Port != 22 {
		t.Fatalf("bastion %+v", b)
	}
	if o := fromFile(writeConfig(t), "x.corp"); o.User != "corp" || o.HostName != "x.corp" {
		t.Fatalf("wildcard %+v", o)
	}
	if n := fromFile(writeConfig(t), "skip.corp"); n.User != "fallback" {
		t.Fatalf("negated pattern still matched: %+v", n)
	}
}

// With OpenSSH installed the values come from ssh -G, which must agree with
// the file for plain entries.
func TestResolveAgreesWithOpenSSH(t *testing.T) {
	cfg := writeConfig(t)
	h := Resolve(context.Background(), cfg, "build-box")
	if h.HostName != "10.0.0.5" || h.User != "dev" || h.Port != 2222 || h.ProxyJump != "bastion" || !h.ForwardAgent {
		t.Fatalf("resolved (%s) %+v", h.Resolver, h)
	}
}
