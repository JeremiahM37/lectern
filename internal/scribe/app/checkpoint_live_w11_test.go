package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLiveCheckpointBranch drives the real model through three turns (edit,
// chat, edit), then cuts the conversation after turn 1 with the same call the
// backend's BranchFrom makes. It needs a live Ollama, so it runs only when
// SCRIBE_LIVE_CHECKPOINT names the Ollama host, for example
// SCRIBE_LIVE_CHECKPOINT=http://100.127.85.58:11434.
func TestLiveCheckpointBranch(t *testing.T) {
	host := os.Getenv("SCRIBE_LIVE_CHECKPOINT")
	if host == "" {
		t.Skip("set SCRIBE_LIVE_CHECKPOINT to an Ollama host to run")
	}
	t.Setenv("OLLAMA_HOST", host)
	t.Setenv("SCRIBE_HOME", t.TempDir())
	work := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	a, err := Open(ctx, Options{Workdir: work, Model: "ollama/qwen3.6:35b-a3b", Mode: "auto", Version: "test", MaxSteps: 8})
	if err != nil {
		t.Fatal(err)
	}
	run := func(p string) {
		t.Helper()
		st, err := a.Run(ctx, p, nil)
		if err != nil || st.Error != "" {
			t.Fatalf("turn %q: err %v stats error %q", p, err, st.Error)
		}
	}
	run("Create a file named a.txt containing exactly the word hello. Use the write tool, then say done.")
	afterOne := len(a.Agent.Messages())
	run("What is 2+2? Answer with just the number.")
	run("Create a file named b.txt containing exactly the word world. Use the write tool, then say done.")
	if _, err := os.Stat(filepath.Join(work, "b.txt")); err != nil {
		t.Fatalf("turn 3 did not create b.txt: %v", err)
	}

	dropped, err := a.Checks.TruncateAfter(1, false)
	if err != nil {
		t.Fatalf("branch after turn 1: %v", err)
	}
	if dropped != 1 {
		t.Fatalf("dropped %d file-changing turns, want 1 (turn 3)", dropped)
	}
	if n := len(a.Agent.Messages()); n != afterOne {
		t.Fatalf("messages after branch = %d, want %d (the end of turn 1)", n, afterOne)
	}
	if _, err := os.Stat(filepath.Join(work, "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("b.txt should be gone after the branch, stat err %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, "a.txt")); err != nil {
		t.Fatalf("a.txt from turn 1 must stay: %v", err)
	}
}
