package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/remote/discord"
)

// resetDiscord lets a test build the process-wide notifier again.
func resetDiscord() {
	discordOnce = sync.Once{}
	discordNote = nil
}

// Discord is built only when the config enables it and a webhook is set.
func TestDiscordNotifierNeedsConfigAndWebhook(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	t.Setenv("SCRIBE_HOME", t.TempDir())
	t.Setenv(discord.WebhookEnv, "")

	resetDiscord()
	if discordNotifier() != nil {
		t.Fatal("no config and no webhook must give no notifier")
	}

	cfg := `{"remote":{"discord":{"enabled":true}}}`
	if err := os.WriteFile(filepath.Join(os.Getenv("SCRIBE_HOME"), "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	resetDiscord()
	if discordNotifier() != nil {
		t.Fatal("enabled without a webhook must give no notifier")
	}

	t.Setenv(discord.WebhookEnv, srv.URL+"/webhook")
	resetDiscord()
	if discordNotifier() == nil {
		t.Fatal("enabled with a webhook must build the notifier")
	}
	resetDiscord()
}
