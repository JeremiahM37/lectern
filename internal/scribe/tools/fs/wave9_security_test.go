package fs

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// A secret-named file is never written, even when the user would approve it.
func TestSecretNamedFileWriteIsHardDenied(t *testing.T) {
	env, _, _, host := newEnv(t)
	host.decision = tools.AllowOnce
	for _, name := range []string{".env", ".env.production", "server.pem", "id_rsa", "credentials.json"} {
		mustErr(t, call(t, NewWriteFile(nil), env, map[string]any{"path": name, "content": "x"}), "is a secret file")
	}
	if got := host.kinds(); len(got) != 0 {
		t.Fatalf("hard-denied writes must not prompt, got approvals %v", got)
	}
}

// CI and container definitions still ask rather than being refused.
func TestSensitiveWriteNameStillAsks(t *testing.T) {
	env, _, _, host := newEnv(t)
	host.decision = tools.AllowOnce
	mustOK(t, call(t, NewWriteFile(nil), env, map[string]any{"path": ".github/workflows/ci.yml", "content": "x"}))
	if got := host.kinds(); len(got) != 1 || got[0] != "sensitive" {
		t.Fatalf("approvals %v", got)
	}
}
