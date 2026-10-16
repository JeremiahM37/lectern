package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/app"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/remote/telegram"
)

// --telegram without a token fails before the full-screen UI opens, and says
// where the token goes.
func TestTelegramFlagNeedsATokenBeforeTheUIOpens(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	t.Setenv(telegram.TokenEnv, "")
	var stderr bytes.Buffer
	if code := codeTUI([]string{"--telegram"}, &stderr); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), telegram.TokenEnv) {
		t.Fatalf("stderr does not name the token variable: %q", stderr.String())
	}
}

// A tab that is not bound to Telegram keeps its approvals local and does not
// turn the surface on.
func TestAttachRemoteLeavesAnUnboundTabLocal(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	t.Setenv(telegram.TokenEnv, "")
	a, err := app.Open(context.Background(), app.Options{Workdir: t.TempDir(), Ephemeral: true, NoRepoMap: true})
	if err != nil {
		t.Fatalf("app.Open: %v", err)
	}
	host := app.NewTUIHost()
	be := app.NewBackend(a, host)
	attachRemote(a, be, false)
	if telegram.Default.Enabled() || telegram.Default.IsBound(be) {
		t.Fatal("an unbound tab must not turn Telegram on or bind itself")
	}
}
