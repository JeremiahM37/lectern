package nvim

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func restoreSettings(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		SetStartConfig("user")
		SetSyncOnEdit(true)
		SetAccess(AccessOn)
	})
}

func TestStartConfigNoneStartsNeovimWithoutUserConfig(t *testing.T) {
	restoreSettings(t)
	SetStartConfig("user")
	if got := configArgs(); got != nil {
		t.Fatalf("user config adds args %v", got)
	}
	SetStartConfig("none")
	if got := configArgs(); !reflect.DeepEqual(got, []string{"-u", "NONE"}) {
		t.Fatalf("none config args = %v", got)
	}
	if StartConfig() != "none" {
		t.Fatalf("StartConfig = %q", StartConfig())
	}
	SetStartConfig("lazyvim") // not built: falls back to the user's config
	if StartConfig() != "user" {
		t.Fatalf("unknown mode should fall back to user, got %q", StartConfig())
	}
}

func TestSyncOnEditDefaultsOnAndCanBeTurnedOff(t *testing.T) {
	restoreSettings(t)
	if !SyncOnEdit() {
		t.Fatalf("sync should default on")
	}
	SetSyncOnEdit(false)
	if SyncOnEdit() {
		t.Fatalf("sync did not turn off")
	}
}

// With sync off, FollowThen does nothing: the done callback is not called.
func TestFollowThenIsSilentWhenSyncIsOff(t *testing.T) {
	restoreSettings(t)
	SetSyncOnEdit(false)
	called := make(chan string, 1)
	FollowThen("/nowhere.txt", 1, func(r string) { called <- r })
	select {
	case r := <-called:
		t.Fatalf("follow ran with sync off: %s", r)
	default:
	}
}

// highlight marks the lines in the buffer, and the next highlight replaces them.
func TestHighlightMarksLinesAndReplacesThem(t *testing.T) {
	requireNvim(t)
	restoreSettings(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "h.txt")
	if err := os.WriteFile(file, []byte("one\ntwo\nthree\nfour\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := startNvim(t, file)
	res := run(t, envFor(dir), map[string]any{"action": "highlight", "startLine": 2, "endLine": 3})
	if res.IsError {
		t.Fatalf("highlight failed: %s", res.Output)
	}
	if res.Output != "Highlighted lines 2-3 (the next highlight replaces it)" {
		t.Fatalf("output %q", res.Output)
	}
	count := func() int {
		v, err := s.Lua(context.Background(), `return #vim.api.nvim_buf_get_extmarks(0, vim.api.nvim_create_namespace("scribe_agent_highlight"), 0, -1, {})`)
		if err != nil {
			t.Fatalf("lua: %v", err)
		}
		return int(v.(int64))
	}
	if n := count(); n != 1 {
		t.Fatalf("extmarks after highlight = %d, want 1", n)
	}
	if res := run(t, envFor(dir), map[string]any{"action": "highlight", "startLine": 4}); res.IsError {
		t.Fatalf("second highlight failed: %s", res.Output)
	}
	if n := count(); n != 1 {
		t.Fatalf("extmarks after replacing = %d, want 1", n)
	}
}

func TestHighlightIsReadOnlyAndInTheSchema(t *testing.T) {
	if !readOnlyActions["highlight"] {
		t.Fatalf("highlight should be allowed in plan mode")
	}
	if !validAction("highlight") {
		t.Fatalf("highlight is not a valid action")
	}
}
