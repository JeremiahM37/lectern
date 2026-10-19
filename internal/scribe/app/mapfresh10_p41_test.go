package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
)

// readyMap runs a test backend until its first map build has finished.
func readyMap(t *testing.T) (*App, string, bool) {
	t.Helper()
	be, repo := testBackend(t)
	a := be.(*scribeBackend).current()
	if a.ext.cmap == nil {
		return nil, "", false
	}
	select {
	case <-a.ext.mapOK:
	case <-time.After(20 * time.Second):
		t.Fatal("the first build did not finish")
	}
	return a, repo, true
}

// A live provider (not caching the prompt) gets the new symbol on the next
// draw: the map is not frozen for it.
func TestLiveProviderRedrawsTheMapOnChange(t *testing.T) {
	a, repo, ok := readyMap(t)
	if !ok {
		t.Skip("the code map is off in this build")
	}
	local := a.Agent.CurrentModel()
	local.Ref = "ollama/qwen3.6"
	a.Agent.SetModel(local)
	if err := os.WriteFile(filepath.Join(repo, "live.go"), []byte("package main\n\nfunc LiveOnlyThing() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.ext.cmap.Update([]string{"live.go"}); err != nil {
		t.Fatal(err)
	}
	a.refreshCodeMap()
	if text := a.codeMapText(); !strings.Contains(text, "LiveOnlyThing") {
		t.Fatalf("a live provider's map does not show the new symbol:\n%s", text)
	}
}

// The map is sized for the conversation: a user's chosen budget wins, and the
// auto budget shrinks as the conversation grows.
func TestBudgetForUsesTheConversationWhenAuto(t *testing.T) {
	if got := budgetFor(1234, 50000); got != 1234 {
		t.Errorf("a chosen budget = %d, want 1234", got)
	}
	if got := budgetFor(0, 50000); got != repomap.Budget(50000) {
		t.Errorf("auto budget at 50000 tokens = %d, want %d", got, repomap.Budget(50000))
	}
	if budgetFor(0, 0) != 2500 || budgetFor(0, 0) == budgetFor(0, 90000) {
		t.Errorf("the auto budget must be 2500 at 0 tokens and smaller at 90000: %d vs %d", budgetFor(0, 0), budgetFor(0, 90000))
	}
}

// The agent latches the text the app holds once the map is drawn.
func TestCodeMapTextIsWhatTheConversationCarries(t *testing.T) {
	a, repo, ok := readyMap(t)
	if !ok {
		t.Skip("the code map is off in this build")
	}
	// The fixture repository starts empty. Add a file, then draw the map the
	// way the next prompt build does.
	if err := os.WriteFile(filepath.Join(repo, "held.go"), []byte("package main\n\nfunc HeldThing() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.ext.cmap.Update([]string{"held.go"}); err != nil {
		t.Fatal(err)
	}
	a.refreshCodeMap()
	if got := a.codeMapText(); !strings.Contains(got, "HeldThing") {
		t.Fatalf("the map the conversation carries lacks the new symbol:\n%s", got)
	}
}
