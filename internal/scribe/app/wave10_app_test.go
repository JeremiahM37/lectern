package app

import (
	"context"
	"encoding/json"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

func TestCheckUpdateFillsNotesAndURLFromOneRequest(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"tag_name":"v99.0.0","html_url":"https://example.test/rel/v99","body":"## Changes\n- Faster reindex\n- Fixed the **Changes** panel\n"}`))
	}))
	defer srv.Close()
	t.Setenv("LECTERN_RELEASE_BASE", srv.URL)
	st := (&scribeBackend{opts: Options{Version: "2.10.1"}}).CheckUpdate()
	if st.Err != "" || st.Latest != "v99.0.0" || !st.Newer {
		t.Fatalf("status = %+v", st)
	}
	if want := []string{"Faster reindex", "Fixed the Changes panel"}; len(st.Notes) != 2 || st.Notes[0] != want[0] || st.Notes[1] != want[1] {
		t.Fatalf("notes = %q", st.Notes)
	}
	if st.URL != "https://example.test/rel/v99" {
		t.Fatalf("url = %q", st.URL)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("the check made %d requests, want 1", n)
	}
}

func TestReleaseNotesBulletsOrPlainLines(t *testing.T) {
	got := releaseNotes("# v2\n\n1. First\n2. Second\n\nthanks all\n")
	if len(got) != 2 || got[0] != "First" || got[1] != "Second" {
		t.Fatalf("numbered items = %q", got)
	}
	got = releaseNotes("Plain paragraph one.\n\nPlain paragraph two.\n")
	if len(got) != 2 || got[0] != "Plain paragraph one." {
		t.Fatalf("plain lines = %q", got)
	}
	if got := releaseNotes(""); len(got) != 0 {
		t.Fatalf("empty body = %q", got)
	}
}

func TestMentionsFileNeedsADistinctiveName(t *testing.T) {
	if !mentionsFile("why does internal/parser/parse.go loop?", "internal/parser/parse.go") {
		t.Fatal("full path should match")
	}
	if !mentionsFile("the parser file is broken: parser.go", "internal/parser.go") {
		t.Fatal("base name of six characters should match")
	}
	if mentionsFile("a quick a.go question", "x/a.go") {
		t.Fatal("a three-letter base name must not match")
	}
}

func TestSaveCheckpointWritesARefAndLeavesTheIndexAlone(t *testing.T) {
	be, repo := testBackend(t)
	cs := be.(*scribeBackend)
	if err := os.WriteFile(filepath.Join(repo, "draft.txt"), []byte("half done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := cs.SaveCheckpoint("before-refactor")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !strings.HasPrefix(ref, "before-refactor (") {
		t.Fatalf("ref = %q", ref)
	}
	if out := gitOut(t, repo, "show-ref", "--verify", "refs/scribe/checkpoints/before-refactor"); out == "" {
		t.Fatal("the checkpoint ref was not written")
	}
	if out := gitOut(t, repo, "show", "refs/scribe/checkpoints/before-refactor:draft.txt"); out != "half done" {
		t.Fatalf("checkpoint content = %q", out)
	}
	// The untracked file is still untracked: the real index was not touched.
	if st := gitOut(t, repo, "status", "--porcelain"); st != "?? draft.txt" {
		t.Fatalf("status after save = %q", st)
	}
	if _, err := cs.SaveCheckpoint("bad name!"); err == nil {
		t.Fatal("a name with a space should be refused")
	}
}

func TestContextWindowOverrideRoundTrip(t *testing.T) {
	be, _ := testBackend(t)
	cw := be.(*scribeBackend)
	ref, window, override := cw.ContextWindow()
	if ref != "fake/test-model" || override != 0 || window <= 0 {
		t.Fatalf("before: ref=%q window=%d override=%d", ref, window, override)
	}
	if err := cw.SetContextWindow(64000); err != nil {
		t.Fatalf("set: %v", err)
	}
	_, window, override = cw.ContextWindow()
	if override != 64000 || window != 64000 {
		t.Fatalf("after set: window=%d override=%d", window, override)
	}
	if err := cw.SetContextWindow(0); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, _, override = cw.ContextWindow(); override != 0 {
		t.Fatalf("override after remove = %d", override)
	}
	if err := cw.SetContextWindow(-5); err == nil {
		t.Fatal("a negative window should be refused")
	}
}

func TestClearTasksEmptiesThePlannerList(t *testing.T) {
	be, _ := testBackend(t)
	a := be.(*scribeBackend).current()
	tool, ok := a.Tools.Get("task_list")
	if !ok {
		t.Fatal("task_list is not registered")
	}
	tool.Run(context.Background(), nil, json.RawMessage(`{"action":"set","tasks":["one","two"]}`))
	if n := len(a.Planner.Tasks()); n != 2 {
		t.Fatalf("tasks before clear = %d", n)
	}
	if err := be.(*scribeBackend).ClearTasks(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if n := len(a.Planner.Tasks()); n != 0 {
		t.Fatalf("tasks after clear = %d", n)
	}
}

// The code map used in the prompt follows an edit without a restart: the
// generation moves, the next prompt build re-renders, and the new symbol is
// in the text. This runs through the real backend's App.
func TestCodeMapSnapshotIsFrozenUntilCompaction(t *testing.T) {
	be, repo := testBackend(t)
	a := be.(*scribeBackend).current()
	if a.ext.cmap == nil {
		t.Skip("the code map is off in this build")
	}
	select {
	case <-a.ext.mapOK:
	case <-time.After(20 * time.Second):
		t.Fatal("the first build did not finish")
	}
	if err := os.WriteFile(filepath.Join(repo, "extra.go"), []byte("package main\n\nfunc FreshlyAddedThing() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.ext.cmap.Update([]string{"extra.go"}); err != nil {
		t.Fatal(err)
	}
	a.refreshCodeMap()
	a.mu.Lock()
	text := a.codeMap
	a.mu.Unlock()
	if !strings.Contains(text, "FreshlyAddedThing") {
		t.Fatalf("the prompt's map does not show the new symbol:\n%s", text)
	}
	// A step with no change re-renders nothing: the text is the same object.
	gen := a.mapGen
	a.refreshCodeMap()
	if a.mapGen != gen {
		t.Fatal("an unchanged map moved its generation")
	}

	// Once drawn, the snapshot stays frozen, so the system prompt keeps the
	// same bytes and provider prompt caching holds.
	if err := os.WriteFile(filepath.Join(repo, "second.go"), []byte("package main\n\nfunc SecondFreshThing() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.ext.cmap.Update([]string{"second.go"}); err != nil {
		t.Fatal(err)
	}
	a.refreshCodeMap()
	a.mu.Lock()
	text = a.codeMap
	a.mu.Unlock()
	if strings.Contains(text, "SecondFreshThing") {
		t.Fatal("the frozen snapshot changed after an edit")
	}
	// A compaction is a prefix boundary: the next prompt draws a fresh map.
	a.emit(agent.Event{Kind: agent.EvCompactDone, Meta: map[string]any{"messages_before": 9, "messages_after": 3}})
	a.refreshCodeMap()
	a.mu.Lock()
	text = a.codeMap
	a.mu.Unlock()
	if !strings.Contains(text, "SecondFreshThing") {
		t.Fatalf("after compaction the map was not redrawn:\n%s", text)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

var _ tui.CheckpointSaver = (*scribeBackend)(nil)
