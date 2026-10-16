package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools/fs"
)

// runMultiEdit calls multi_edit directly, with no read tracker, so the test is
// about the result data and not the read gate.
func runMultiEdit(t *testing.T, dir, path string, edits string) tools.Result {
	t.Helper()
	args := json.RawMessage(`{"path":"` + path + `","edits":` + edits + `}`)
	env := &tools.Env{Workdir: dir}
	return fs.NewMultiEdit(nil).Run(context.Background(), env, args)
}

// A successful batch carries the whole file before and after, byte for byte
// as on disk.
func TestMultiEditResultCarriesBeforeAndAfter(t *testing.T) {
	dir := t.TempDir()
	before := "a\nb\nc\n"
	writeTemp(t, dir, "f.txt", before)

	res := runMultiEdit(t, dir, "f.txt",
		`[{"old_string":"b","new_string":"B"},{"old_string":"c","new_string":"C\nD"}]`)

	if res.IsError {
		t.Fatalf("batch failed: %s", res.Output)
	}
	after, err := os.ReadFile(filepath.Join(dir, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != "a\nB\nC\nD\n" {
		t.Fatalf("file after batch = %q", after)
	}
	if got, _ := res.Meta["before"].(string); got != before {
		t.Fatalf("Meta[before] = %q, want %q", got, before)
	}
	if got, _ := res.Meta["after"].(string); got != string(after) {
		t.Fatalf("Meta[after] = %q, want %q", got, after)
	}
	if res.Diff == "" {
		t.Fatal("diff is empty; the result should still carry the unified diff")
	}
}

// CRLF files keep their line endings in both snapshots, so the UI compares
// exactly what was on disk.
func TestMultiEditResultKeepsCRLF(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "w.txt", "a\r\nb\r\n")

	res := runMultiEdit(t, dir, "w.txt", `[{"old_string":"b","new_string":"B"}]`)

	if res.IsError {
		t.Fatalf("batch failed: %s", res.Output)
	}
	if got, _ := res.Meta["before"].(string); got != "a\r\nb\r\n" {
		t.Fatalf("Meta[before] = %q", got)
	}
	if got, _ := res.Meta["after"].(string); got != "a\r\nB\r\n" {
		t.Fatalf("Meta[after] = %q", got)
	}
}

// An atomic rollback writes nothing and carries no before or after data, so
// the UI cannot show a change that did not happen.
func TestMultiEditFailedBatchCarriesNoData(t *testing.T) {
	dir := t.TempDir()
	const original = "a\nb\nc\n"
	p := writeTemp(t, dir, "g.txt", original)

	res := runMultiEdit(t, dir, "g.txt",
		`[{"old_string":"b","new_string":"B"},{"old_string":"missing","new_string":"x"}]`)

	if !res.IsError {
		t.Fatal("batch with a missing match succeeded")
	}
	if !strings.Contains(res.Output, "atomic rollback") {
		t.Fatalf("output lacks rollback wording: %q", res.Output)
	}
	if res.Meta != nil {
		t.Fatalf("failed batch carries Meta: %v", res.Meta)
	}
	if res.Diff != "" {
		t.Fatalf("failed batch carries a diff: %q", res.Diff)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("file changed after rollback: %q", got)
	}
}

// The single-edit tool keeps its existing result: no before/after data is
// added to edit_file.
func TestEditFileResultHasNoBeforeAfterData(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "h.txt", "a\nb\n")
	args := json.RawMessage(`{"path":"h.txt","old_string":"b","new_string":"B"}`)

	res := fs.NewEditFile(nil).Run(context.Background(), &tools.Env{Workdir: dir}, args)

	if res.IsError {
		t.Fatalf("edit failed: %s", res.Output)
	}
	if res.Meta != nil {
		t.Fatalf("edit_file gained Meta: %v", res.Meta)
	}
}
