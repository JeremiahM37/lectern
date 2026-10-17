package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// The read tool of a subagent built with withReadCacheFor reports its cache
// outcomes under the subagent's id, so the dispatch row can show them.
func TestReadCacheForNamesTheSubagentInItsNotes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runs := 0
	reg := tools.NewRegistry()
	reg.Register(countRead{runs: &runs})
	bus := agent.NewAgentBus()
	var notes []agent.CacheNote
	bus.SetCacheObserver(func(n agent.CacheNote) { notes = append(notes, n) })

	cached := withReadCacheFor(reg, bus, "b3")
	tool, _ := cached.Get("read")
	env := &tools.Env{Workdir: dir, Files: &marks{}}
	args := json.RawMessage(`{"path":"a.go"}`)
	tool.Run(context.Background(), env, args)
	tool.Run(context.Background(), env, args)

	want := []agent.CacheNote{
		{Agent: "b3", Outcome: agent.CacheStore},
		{Agent: "b3", Outcome: agent.CacheHit, From: "b3"},
	}
	if len(notes) != len(want) {
		t.Fatalf("notes = %+v, want %+v", notes, want)
	}
	for i := range want {
		if notes[i] != want[i] {
			t.Fatalf("note %d = %+v, want %+v", i, notes[i], want[i])
		}
	}
}
