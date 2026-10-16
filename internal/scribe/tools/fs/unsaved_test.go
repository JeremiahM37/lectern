package fs

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// A read sees the editor's unsaved text when there is any, and the disk
// otherwise.
func TestOpenForReadPrefersUnsavedEditorText(t *testing.T) {
	t.Cleanup(func() { SetUnsavedReader(nil) })
	file := filepath.Join(t.TempDir(), "u.txt")
	if err := os.WriteFile(file, []byte("disk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		f, err := openForRead(file)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		b, _ := io.ReadAll(f)
		return string(b)
	}
	if got := read(); got != "disk\n" {
		t.Fatalf("no hook: got %q", got)
	}
	SetUnsavedReader(func(abs string) (string, bool) {
		if abs == file {
			return "typed but not saved\n", true
		}
		return "", false
	})
	if got := read(); got != "typed but not saved\n" {
		t.Fatalf("hook ignored: got %q", got)
	}
	if b, _ := readFileForRead(file); string(b) != "typed but not saved\n" {
		t.Fatalf("readFileForRead ignored the hook: %q", b)
	}
	other := filepath.Join(t.TempDir(), "other.txt")
	os.WriteFile(other, []byte("other\n"), 0o644)
	if b, _ := readFileForRead(other); string(b) != "other\n" {
		t.Fatalf("file without unsaved text should come from disk: %q", b)
	}
}

func TestMultiEditWithOneEditReadsLikeAnEdit(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.go")
	if err := os.WriteFile(p, []byte("package a\n\nfunc Mul() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := &tools.Env{Workdir: dir}
	r := tools.NewRegistry()
	Register(r)
	read, _ := r.Get("read")
	read.Run(context.Background(), env, json.RawMessage(`{"path":"a.go"}`))
	me, _ := r.Get("multi_edit")
	res := me.Run(context.Background(), env, json.RawMessage(`{"path":"a.go","edits":[{"old_string":"Mul","new_string":"Multiply"}]}`))
	if res.IsError || !strings.HasPrefix(res.Output, "Edited a.go: 1 replacement") {
		t.Fatalf("output = %q", res.Output)
	}
}
