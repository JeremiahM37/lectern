package nvim

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUnsavedContentReturnsUserEditsOnly(t *testing.T) {
	file := writeFile(t, "buf.txt", "disk")
	s := startNvim(t, file)
	if _, ok := UnsavedContent(file); ok {
		t.Fatal("a clean buffer reported unsaved content")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.Lua(ctx, `vim.api.nvim_buf_set_lines(0, 0, -1, false, {"typed", "more"})`); err != nil {
		t.Fatal(err)
	}
	text, ok := UnsavedContent(file)
	if !ok || text != "typed\nmore\n" {
		t.Fatalf("unsaved content %q, %v", text, ok)
	}
	if _, ok := UnsavedContent(filepath.Join(t.TempDir(), "other.txt")); ok {
		t.Fatal("a file that is not open reported content")
	}
}

func TestUnsavedContentSkipsNeovimWhenReadsOverlap(t *testing.T) {
	file := writeFile(t, "busy.txt", "x")
	startNvim(t, file)
	readsInFlight.Store(1) // one read already running
	defer readsInFlight.Store(0)
	if _, ok := UnsavedContent(file); ok {
		t.Fatal("a second concurrent read went to the editor")
	}
	if readsInFlight.Load() != 1 {
		t.Fatalf("the refused read leaked its count: %d", readsInFlight.Load())
	}
}

func TestUnsavedContentWithoutEditorIsQuiet(t *testing.T) {
	mu.Lock()
	prev := current
	current = nil
	mu.Unlock()
	defer func() { mu.Lock(); current = prev; mu.Unlock() }()
	if _, ok := UnsavedContent(filepath.Join(os.TempDir(), "x")); ok {
		t.Fatal("content reported with no editor open")
	}
}
