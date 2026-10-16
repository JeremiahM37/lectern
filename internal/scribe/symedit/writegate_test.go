package symedit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func TestCheckWritableRequiresReadAndRefusesGitAndOutside(t *testing.T) {
	dir := t.TempDir()
	trk := newTracker()
	env := &tools.Env{Workdir: dir, Files: trk, Mode: "auto"}
	file := filepath.Join(dir, "a.go")
	writeFile(t, file, "package a\n")

	err := CheckWritable(context.Background(), env, file)
	if err == nil || !strings.Contains(err.Error(), "has not been read this session") {
		t.Fatalf("unread file should be refused: %v", err)
	}
	trk.MarkRead(file)
	if err := CheckWritable(context.Background(), env, file); err != nil {
		t.Fatalf("read file should be writable: %v", err)
	}
	trk.stale[file] = true
	if err := CheckWritable(context.Background(), env, file); err == nil || !strings.Contains(err.Error(), "changed on disk") {
		t.Fatalf("stale file should be refused: %v", err)
	}
	if err := CheckWritable(context.Background(), env, filepath.Join(dir, ".git", "config")); err == nil || !strings.Contains(err.Error(), ".git") {
		t.Fatalf("git internals should be refused: %v", err)
	}
	fresh := filepath.Join(dir, "new.go")
	if err := CheckWritable(context.Background(), env, fresh); err != nil {
		t.Fatalf("a file that does not exist yet has nothing to be stale against: %v", err)
	}
	NoteWritten(env, file)
	if !trk.WasRead(file) || trk.StaleSinceRead(file) {
		t.Fatal("NoteWritten should record the file as read at its new content")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
