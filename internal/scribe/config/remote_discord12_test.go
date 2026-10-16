package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetRemoteDiscordEnabledKeepsOtherKeys checks the toggle writes only
// remote.discord.enabled and leaves the rest of the global file alone,
// including the Telegram section and top-level keys it does not know.
func TestSetRemoteDiscordEnabledKeepsOtherKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SCRIBE_HOME", home)
	path := GlobalPath()
	seed := `{"theme":"dark","remote":{"question_timeout_sec":90,"telegram":{"enabled":true,"chat_ids":[-1001234567890]}}}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetRemoteDiscordEnabled(true); err != nil {
		t.Fatal(err)
	}
	r, err := LoadRemote()
	if err != nil {
		t.Fatal(err)
	}
	if r.Discord == nil || !r.Discord.Enabled {
		t.Fatalf("discord not enabled: %+v", r.Discord)
	}
	if r.Telegram == nil || !r.Telegram.Enabled || len(r.Telegram.ChatIDs) != 1 || r.Telegram.ChatIDs[0] != -1001234567890 {
		t.Fatalf("telegram section changed: %+v", r.Telegram)
	}
	if r.QuestionTimeoutSec != 90 {
		t.Fatalf("question timeout changed: %d", r.QuestionTimeoutSec)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"theme": "dark"`) {
		t.Fatalf("top-level key lost:\n%s", b)
	}
	if err := SetRemoteDiscordEnabled(false); err != nil {
		t.Fatal(err)
	}
	r, _ = LoadRemote()
	if r.Discord.Enabled {
		t.Fatal("discord still enabled after off")
	}
}

// TestSetRemoteDiscordEnabledNoFile creates the file when none exists.
func TestSetRemoteDiscordEnabledNoFile(t *testing.T) {
	home := filepath.Join(t.TempDir(), "fresh")
	t.Setenv("SCRIBE_HOME", home)
	if err := SetRemoteDiscordEnabled(true); err != nil {
		t.Fatal(err)
	}
	r, err := LoadRemote()
	if err != nil || r.Discord == nil || !r.Discord.Enabled {
		t.Fatalf("got %+v, %v", r, err)
	}
}
