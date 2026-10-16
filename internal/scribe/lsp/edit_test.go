package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func rng(l1, c1, l2, c2 int) Range {
	return Range{Start: Position{l1, c1}, End: Position{l2, c2}}
}

func TestApplyEditsMultipleInOneFileUTF16(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.go")
	mustWrite(t, path, "x := \"😀\"\ny := x\n")
	// Given out of order, and the second edit sits after an emoji.
	edit := WorkspaceEdit{Changes: map[string][]TextEdit{
		pathToURI(path): {
			{Range: rng(1, 5, 1, 6), NewText: "beta"},
			{Range: rng(0, 6, 0, 8), NewText: "ok"},
			{Range: rng(0, 0, 0, 1), NewText: "alpha"},
		},
	}}
	res, err := ApplyWorkspaceEdit(edit)
	if err != nil {
		t.Fatal(err)
	}
	want := "alpha := \"ok\"\ny := beta\n"
	if got := mustRead(t, path); got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if len(res.Changes) != 1 || res.Changes[0].Before != "x := \"😀\"\ny := x\n" {
		t.Fatalf("changes = %+v", res.Changes)
	}
}

func TestOverlappingEditsLeaveDiskAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "o.txt")
	mustWrite(t, path, "abcdef\n")
	calls := 0
	edit := WorkspaceEdit{Changes: map[string][]TextEdit{
		pathToURI(path): {
			{Range: rng(0, 0, 0, 3), NewText: "X"},
			{Range: rng(0, 2, 0, 4), NewText: "Y"},
		},
	}}
	_, err := ApplyWorkspaceEditWith(edit, func(string) { calls++ })
	if err == nil {
		t.Fatal("expected an overlap error")
	}
	if got := mustRead(t, path); got != "abcdef\n" {
		t.Fatalf("file changed despite error: %q", got)
	}
	if calls != 0 {
		t.Fatalf("before hook called %d times on a failed edit", calls)
	}
}

func TestMultiFileChangesCallBeforeHookOnlyForChangedFiles(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.go")
	b := filepath.Join(dir, "sub", "b.go")
	untouched := filepath.Join(dir, "c.go")
	mustWrite(t, a, "Greet()\n")
	mustWrite(t, b, "Greet() + Greet()\n")
	mustWrite(t, untouched, "Other()\n")

	edit := WorkspaceEdit{Changes: map[string][]TextEdit{
		pathToURI(a): {{Range: rng(0, 0, 0, 5), NewText: "Hello"}},
		pathToURI(b): {
			{Range: rng(0, 0, 0, 5), NewText: "Hello"},
			{Range: rng(0, 10, 0, 15), NewText: "Hello"},
		},
		pathToURI(untouched): {},
	}}
	var hooked []string
	res, err := ApplyWorkspaceEditWith(edit, func(p string) { hooked = append(hooked, p) })
	if err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, b); got != "Hello() + Hello()\n" {
		t.Fatalf("b.go = %q", got)
	}
	if got := mustRead(t, a); got != "Hello()\n" {
		t.Fatalf("a.go = %q", got)
	}
	want := []string{a, b}
	if !reflect.DeepEqual(hooked, want) {
		t.Fatalf("before hook = %v, want %v", hooked, want)
	}
	if len(res.Changes) != 2 {
		t.Fatalf("changes = %+v", res.Changes)
	}
}

func TestDocumentChangesCreateRenameDelete(t *testing.T) {
	dir := t.TempDir()
	created := filepath.Join(dir, "new.txt")
	oldPath := filepath.Join(dir, "a.txt")
	newPath := filepath.Join(dir, "b.txt")
	gone := filepath.Join(dir, "gone.txt")
	mustWrite(t, oldPath, "keep me\n")
	mustWrite(t, gone, "bye\n")

	raw := func(v any) json.RawMessage {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	edit := WorkspaceEdit{DocumentChanges: []json.RawMessage{
		raw(map[string]any{"kind": "create", "uri": pathToURI(created)}),
		raw(map[string]any{"textDocument": map[string]any{"uri": pathToURI(created)},
			"edits": []TextEdit{{Range: rng(0, 0, 0, 0), NewText: "hi"}}}),
		raw(map[string]any{"kind": "rename", "oldUri": pathToURI(oldPath), "newUri": pathToURI(newPath)}),
		raw(map[string]any{"kind": "delete", "uri": pathToURI(gone)}),
	}}
	res, err := ApplyWorkspaceEdit(edit)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, created); got != "hi" {
		t.Fatalf("created = %q", got)
	}
	if got := mustRead(t, newPath); got != "keep me\n" {
		t.Fatalf("renamed = %q", got)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old path still exists: %v", err)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Fatalf("deleted file still exists: %v", err)
	}
	if len(res.Changes) != 4 {
		t.Fatalf("changes = %d, want 4 (%+v)", len(res.Changes), res.Changes)
	}
}

func TestCreateOntoExistingFileIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	mustWrite(t, path, "original")
	raw, _ := json.Marshal(map[string]any{"kind": "create", "uri": pathToURI(path)})
	if _, err := ApplyWorkspaceEdit(WorkspaceEdit{DocumentChanges: []json.RawMessage{raw}}); err == nil {
		t.Fatal("expected refusal")
	}
	if got := mustRead(t, path); got != "original" {
		t.Fatalf("file changed: %q", got)
	}
}

func TestNonFileURIsAreRefused(t *testing.T) {
	edit := WorkspaceEdit{Changes: map[string][]TextEdit{"jar:///x.jar!/Y.class": {}}}
	if _, err := ApplyWorkspaceEdit(edit); err == nil {
		t.Fatal("expected refusal of a non-file URI")
	}
}

func TestURIRoundTripWithSpaces(t *testing.T) {
	p := filepath.Join("/tmp", "my dir", "file (1).go")
	back, ok := uriToPath(pathToURI(p))
	if !ok || back != p {
		t.Fatalf("round trip %q -> %q (%v)", p, back, ok)
	}
}
