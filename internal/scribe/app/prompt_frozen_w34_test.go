package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
)

// The system prompt keeps the same bytes while the project changes under it
// (provider prompt caching depends on it), and reads the state again after a
// compaction.
func TestSystemPromptStaysTheSameUntilCompaction(t *testing.T) {
	be, repo := testBackend(t)
	a := be.(*scribeBackend).current()
	build := a.systemPrompt("")
	first := build("default")
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if again := build("default"); again != first {
		t.Fatal("the system prompt changed after an untracked file appeared")
	}
	a.emit(agent.Event{Kind: agent.EvCompactDone, Meta: map[string]any{"messages_before": 9, "messages_after": 3}})
	a.mapMu.Lock()
	cleared := a.frozen == nil
	a.mapMu.Unlock()
	if !cleared {
		t.Fatal("a compaction should let the prompt read the project state again")
	}
}
