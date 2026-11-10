package api_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
)

// A backup carries every secret the server has, so only a person may set it
// up, run it or download it. An agent on this machine reaches the API as an
// unauthenticated local process (tailnet mode, as on a server) and is refused; the passphrase is shown once
// and never read back.
func TestBackupSettingsAreForAPersonOnly(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Auth, c.AuthToken = "tailscale", "owner-secret" })
	owner := map[string]string{"Authorization": "Bearer owner-secret"}
	body := map[string]any{"repo": "me/backups", "new_passphrase": true}

	for _, path := range []string{"/api/backup/run", "/api/backup/download", "/api/backup/list"} {
		method := "GET"
		if strings.HasSuffix(path, "/run") {
			method = "POST"
		}
		if code, _ := h.request(method, path, nil, nil); code != 403 {
			t.Errorf("agent %s %s = %d, want 403", method, path, code)
		}
	}
	if code, _ := h.request("PUT", "/api/backup", body, nil); code != 403 {
		t.Fatalf("agent PUT /api/backup = %d, want 403", code)
	}

	code, raw := h.request("PUT", "/api/backup", body, owner)
	if code != 200 {
		t.Fatalf("owner PUT /api/backup = %d %s", code, raw)
	}
	var out struct {
		Passphrase string `json:"passphrase"`
		Backup     struct {
			Repo          string `json:"repo"`
			HasPassphrase bool   `json:"has_passphrase"`
		} `json:"backup"`
	}
	json.Unmarshal(raw, &out)
	if len(out.Passphrase) < 20 || out.Backup.Repo != "me/backups" || !out.Backup.HasPassphrase {
		t.Fatalf("setup reply = %s", raw)
	}
	code, raw = h.request("GET", "/api/backup", nil, owner)
	if code != 200 || strings.Contains(string(raw), out.Passphrase) || !strings.Contains(string(raw), `"has_passphrase":true`) {
		t.Fatalf("status = %d %s (must never carry the passphrase)", code, raw)
	}
}
