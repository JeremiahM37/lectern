package app

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// TestSetRemoteDiscordNeedsWebhook refuses to turn Discord on without a
// webhook, and leaves the config untouched.
func TestSetRemoteDiscordNeedsWebhook(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	t.Setenv("SCRIBE_DISCORD_WEBHOOK", "")
	b := &scribeBackend{}
	if err := b.SetRemoteDiscord(true); err == nil {
		t.Fatal("expected a refusal without a webhook")
	}
	if r, _ := config.LoadRemote(); r.Discord != nil && r.Discord.Enabled {
		t.Fatal("config changed despite the refusal")
	}
	// Turning it off never needs a webhook.
	if err := b.SetRemoteDiscord(false); err != nil {
		t.Fatalf("off: %v", err)
	}
}
