package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/app"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/lecternbridge"
)

func TestScribeIsNotATrackedQuickVerb(t *testing.T) {
	verbs := builtinAgentQuickVerbs()
	if verbs["scribe"] {
		t.Fatal("`lectern scribe` must stay the terminal alias of `lectern code`, not a tracked session")
	}
	if !verbs["claude"] {
		t.Fatal("built-in claude should remain a quick verb")
	}
}

// TestCodeBridgeAttachesInsideLecternOnly checks the call site's contract:
// with the Lectern session environment, every Scribe session opened in this
// process is attached to the hook endpoint; without it, nothing changes.
func TestCodeBridgeAttachesInsideLecternOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unused", http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)

	t.Setenv(lecternbridge.EnvHookURL, srv.URL+"/api/hook/session/9")
	t.Setenv(lecternbridge.EnvHookToken, "tok")
	maybeLecternBridge()

	a, err := app.Open(context.Background(), app.Options{Workdir: t.TempDir(), Ephemeral: true, NoRepoMap: true})
	if err != nil {
		t.Fatalf("app.Open: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	if got := fmt.Sprintf("%T", a.Host()); !strings.Contains(got, "approvalHost") {
		t.Fatalf("host = %s, want the Lectern approval host", got)
	}
}
