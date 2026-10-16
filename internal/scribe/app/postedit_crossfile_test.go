package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/lsp"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// scriptedDiags answers Diagnostics per file. Each call pops the next entry
// for that file; the last entry repeats. Active is fixed per file.
type scriptedDiags struct {
	active map[string]bool
	script map[string][][]lsp.FileDiagnostic
	calls  map[string]int
}

func newScriptedDiags() *scriptedDiags {
	return &scriptedDiags{active: map[string]bool{}, script: map[string][][]lsp.FileDiagnostic{}, calls: map[string]int{}}
}

func (f *scriptedDiags) Active(abs string) bool { return f.active[abs] }

func (f *scriptedDiags) Diagnostics(_ context.Context, abs string) ([]lsp.FileDiagnostic, error) {
	f.calls[abs]++
	s := f.script[abs]
	if len(s) == 0 {
		return nil, nil
	}
	out := s[0]
	if len(s) > 1 {
		f.script[abs] = s[1:]
	}
	return out, nil
}

// fakeDeps answers Impact from a fixed table of level-1 dependents keyed by
// the edited file's relative path. Files not in the table are not in the map.
type fakeDeps struct {
	dependents map[string][]repomap.Dependent
}

func (f fakeDeps) Impact(file string) (repomap.Impact, error) {
	d, ok := f.dependents[file]
	if !ok {
		return repomap.Impact{}, fmt.Errorf("%q is not in the code map", file)
	}
	return repomap.Impact{File: file, Dependents: d}, nil
}

func level1(paths ...string) []repomap.Dependent {
	var out []repomap.Dependent
	for _, p := range paths {
		out = append(out, repomap.Dependent{Path: p, Level: 1})
	}
	return out
}

func diagAt(abs string, line int, msg string) lsp.FileDiagnostic {
	d := errDiag(line, msg)
	d.Path = abs
	return d
}

// crossFixture is a workdir with lib.go (the edited file) and user.go (its
// importer), a scripted language server, and a post-editor wired to the map.
func crossFixture(t *testing.T) (dir string, sd *scriptedDiags, p *postEditor, libAbs, userAbs string) {
	t.Helper()
	dir = t.TempDir()
	libAbs = writeTemp(t, dir, "lib.go", "package lib\n\nfunc Foo() {}\n")
	userAbs = writeTemp(t, dir, "user.go", "package lib\n\nfunc Bar() {}\n")
	sd = newScriptedDiags()
	sd.active[userAbs] = true
	p = newPostEditor(dir, sd)
	p.SetDependents(fakeDeps{dependents: map[string][]repomap.Dependent{
		"lib.go": level1("user.go"),
	}})
	return dir, sd, p, libAbs, userAbs
}

// editLib rewrites lib.go the way the tool would, then runs AfterTool for it.
func editLib(t *testing.T, p *postEditor, libAbs string, pre func()) tools.Result {
	t.Helper()
	ctx := context.Background()
	p.BeforeTool(ctx, "write_file", json.RawMessage(`{"path":"lib.go"}`))
	if pre != nil {
		pre()
	}
	if err := os.WriteFile(libAbs, []byte("package lib\n\nfunc Baz() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := tools.Result{Output: "Wrote lib.go.", Files: []string{"lib.go"}}
	p.AfterTool(ctx, "write_file", nil, &res)
	return res
}

// A new error in an importer, present only after the edit, is reported as a
// cross-file error with the importer's own location.
func TestCrossFileReportsErrorIntroducedInImporter(t *testing.T) {
	_, sd, p, libAbs, userAbs := crossFixture(t)
	// user.go: clean before the edit, broken after it.
	sd.script[userAbs] = [][]lsp.FileDiagnostic{{}, {diagAt(userAbs, 3, "undefined: Foo")}}

	res := editLib(t, p, libAbs, nil)

	if !strings.Contains(res.Output, "Cross-file: 1 new error(s) in files that import the edit (1 checked).") {
		t.Fatalf("output lacks cross-file header: %q", res.Output)
	}
	if !strings.Contains(res.Output, "user.go:3:1 error: undefined: Foo (gopls T)") {
		t.Fatalf("output lacks importer error line: %q", res.Output)
	}
}

// With no baseline from before the edit, the first check only records the
// importer's errors. An error that appears on the second edit is reported.
func TestCrossFileWithoutBaselineSeedsThenReports(t *testing.T) {
	_, sd, p, libAbs, userAbs := crossFixture(t)
	sd.script[userAbs] = [][]lsp.FileDiagnostic{
		{},                                  // first edit, after: nothing yet
		{},                                  // second edit, after: still clean
		{diagAt(userAbs, 3, "broken here")}, // third edit, after: broken
	}
	ctx := context.Background()
	run := func() tools.Result {
		res := tools.Result{Output: "ok", Files: []string{"lib.go"}}
		if err := os.WriteFile(libAbs, []byte("package lib\n\nfunc X() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		p.AfterTool(ctx, "edit_file", nil, &res)
		return res
	}

	if out := run().Output; strings.Contains(out, "Cross-file") {
		t.Fatalf("first check with no baseline reported: %q", out)
	}
	if out := run().Output; strings.Contains(out, "Cross-file") {
		t.Fatalf("clean second check reported: %q", out)
	}
	if out := run().Output; !strings.Contains(out, "Cross-file: 1 new error(s)") {
		t.Fatalf("third check did not report the new importer error: %q", out)
	}
}

// An importer error that was there before the edit is not new, and one that
// the edit removed is counted as fixed.
func TestCrossFileBaselineSeparatesNewAndFixed(t *testing.T) {
	_, sd, p, libAbs, userAbs := crossFixture(t)
	// Before: user.go has "old" and "gone". After: "old" and "fresh".
	sd.script[userAbs] = [][]lsp.FileDiagnostic{
		{diagAt(userAbs, 3, "old"), diagAt(userAbs, 4, "gone")},
		{diagAt(userAbs, 3, "old"), diagAt(userAbs, 9, "fresh")},
	}

	res := editLib(t, p, libAbs, nil)

	if !strings.Contains(res.Output, "Cross-file: 1 new error(s)") || !strings.Contains(res.Output, "1 fixed.") {
		t.Fatalf("header wrong: %q", res.Output)
	}
	if !strings.Contains(res.Output, "user.go:9:1 error: fresh") {
		t.Fatalf("fresh error not listed: %q", res.Output)
	}
	if strings.Contains(res.Output, "error: old") {
		t.Fatalf("pre-existing error reported as new: %q", res.Output)
	}
}

// Only the first maxDependentsChecked importers are asked, and the block says
// how many were left out.
func TestCrossFileCapsImporters(t *testing.T) {
	dir := t.TempDir()
	libAbs := writeTemp(t, dir, "lib.go", "package lib\n\nfunc Foo() {}\n")
	sd := newScriptedDiags()
	var deps []repomap.Dependent
	const n = maxDependentsChecked + 4
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("u%02d.go", i)
		abs := writeTemp(t, dir, name, "package lib\n")
		sd.active[abs] = true
		deps = append(deps, repomap.Dependent{Path: name, Level: 1})
		if i == 0 {
			// The first importer (highest rank) gets a new error after the edit.
			sd.script[abs] = [][]lsp.FileDiagnostic{{}, {diagAt(abs, 1, "new")}}
		}
	}
	p := newPostEditor(dir, sd)
	p.SetDependents(fakeDeps{dependents: map[string][]repomap.Dependent{"lib.go": deps}})

	res := editLib(t, p, libAbs, nil)

	if !strings.Contains(res.Output, fmt.Sprintf("(%d checked)", maxDependentsChecked)) {
		t.Fatalf("expected %d checked: %q", maxDependentsChecked, res.Output)
	}
	if !strings.Contains(res.Output, "Not checked: 4 importer(s) beyond the cap") {
		t.Fatalf("cap not reported: %q", res.Output)
	}
	checked := 0
	for abs, c := range sd.calls {
		if filepath.Base(abs) != "lib.go" {
			checked += c
		}
	}
	// Each checked importer is asked once before the edit (the baseline) and
	// once after it.
	if checked != 2*maxDependentsChecked {
		t.Fatalf("importer diagnostics calls = %d, want %d", checked, 2*maxDependentsChecked)
	}
}

// Importers without a running language server are not asked, and the edited
// file itself and second-level dependents are never treated as importers.
func TestCrossFileSkipsInactiveAndNonDirectImporters(t *testing.T) {
	dir := t.TempDir()
	libAbs := writeTemp(t, dir, "lib.go", "package lib\n\nfunc Foo() {}\n")
	inactiveAbs := writeTemp(t, dir, "off.go", "package lib\n")
	sd := newScriptedDiags() // nothing active
	p := newPostEditor(dir, sd)
	p.SetDependents(fakeDeps{dependents: map[string][]repomap.Dependent{
		"lib.go": {
			{Path: "off.go", Level: 1},
			{Path: "lib.go", Level: 1},        // the edited file itself
			{Path: "deep.go", Level: 2},       // importer of an importer
			{Path: "../outside.go", Level: 1}, // outside the workdir
		},
	}})

	res := editLib(t, p, libAbs, nil)

	if strings.Contains(res.Output, "Cross-file") {
		t.Fatalf("no server, yet cross-file reported: %q", res.Output)
	}
	if sd.calls[inactiveAbs] != 0 {
		t.Fatalf("inactive importer was asked %d times", sd.calls[inactiveAbs])
	}
	imp := p.importers([]string{libAbs})
	if len(imp.paths) != 1 || imp.paths[0] != inactiveAbs || imp.total != 1 {
		t.Fatalf("importers = %+v, want only off.go", imp)
	}
}

// Without a code map there are no importers, and nothing is checked or
// reported.
func TestCrossFileNoCodeMapDoesNothing(t *testing.T) {
	dir, sd, _, libAbs, userAbs := crossFixture(t)
	sd.script[userAbs] = [][]lsp.FileDiagnostic{{}, {diagAt(userAbs, 3, "x")}}
	p := newPostEditor(dir, sd) // no SetDependents

	res := editLib(t, p, libAbs, nil)

	if strings.Contains(res.Output, "Cross-file") || sd.calls[userAbs] != 0 {
		t.Fatalf("checked without a code map: output %q, calls %d", res.Output, sd.calls[userAbs])
	}
}

// A failed edit consumes its baseline, so a later successful edit of the same
// file does not compare against a stale snapshot.
func TestCrossFileFailedEditDropsBaseline(t *testing.T) {
	_, sd, p, _, userAbs := crossFixture(t)
	sd.script[userAbs] = [][]lsp.FileDiagnostic{{diagAt(userAbs, 3, "old")}}
	ctx := context.Background()

	p.BeforeTool(ctx, "edit_file", json.RawMessage(`{"path":"lib.go"}`))
	if len(p.pending) != 1 {
		t.Fatalf("baseline not recorded: %d pending", len(p.pending))
	}
	failed := tools.Result{IsError: true, Output: "lib.go: no match", Files: []string{"lib.go"}}
	p.AfterTool(ctx, "edit_file", nil, &failed)
	if len(p.pending) != 0 {
		t.Fatalf("failed edit left %d baselines", len(p.pending))
	}
}

// A failed call whose result names no files leaves its baseline in place,
// but the next write to that file replaces it, so the stale one is never used.
func TestCrossFileNextWriteReplacesStaleBaseline(t *testing.T) {
	_, sd, p, libAbs, userAbs := crossFixture(t)
	ctx := context.Background()
	// Call 1 (failed edit's snapshot): user.go has "old". Call 2 (the real
	// edit's snapshot): clean. Call 3 (after the real edit): clean.
	sd.script[userAbs] = [][]lsp.FileDiagnostic{
		{diagAt(userAbs, 3, "old")},
		{},
		{},
	}
	p.BeforeTool(ctx, "edit_file", json.RawMessage(`{"path":"lib.go"}`))
	failed := tools.Result{IsError: true, Output: "no match"}
	p.AfterTool(ctx, "edit_file", nil, &failed)

	res := editLib(t, p, libAbs, nil)

	if strings.Contains(res.Output, "Cross-file") {
		t.Fatalf("stale baseline used: %q", res.Output)
	}
}

// Clearing the baseline when there is nothing to record: a write with no
// active importers drops an earlier entry for the same file.
func TestBeforeToolNoImportersClearsEarlierBaseline(t *testing.T) {
	_, sd, p, _, userAbs := crossFixture(t)
	ctx := context.Background()
	p.BeforeTool(ctx, "edit_file", json.RawMessage(`{"path":"lib.go"}`))
	if len(p.pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(p.pending))
	}
	sd.active[userAbs] = false
	p.BeforeTool(ctx, "edit_file", json.RawMessage(`{"path":"lib.go"}`))
	if len(p.pending) != 0 {
		t.Fatalf("pending = %d after a write with no active importers, want 0", len(p.pending))
	}
}

// Tools that do not write one named file take no baseline.
func TestBeforeToolRecordsOnlyWriteTools(t *testing.T) {
	_, _, p, _, _ := crossFixture(t)
	ctx := context.Background()

	p.BeforeTool(ctx, "shell", json.RawMessage(`{"path":"lib.go"}`))
	p.BeforeTool(ctx, "rename_symbol", json.RawMessage(`{"path":"lib.go"}`))
	p.BeforeTool(ctx, "edit_file", json.RawMessage(`{"path":"../escape.go"}`))
	p.BeforeTool(ctx, "edit_file", json.RawMessage(`not json`))
	if len(p.pending) != 0 {
		t.Fatalf("pending = %d, want 0", len(p.pending))
	}
}

// An importer whose errors are all unchanged reports nothing, and the cache
// is updated so the next edit compares against the latest state.
func TestCrossFileUnchangedImporterIsSilent(t *testing.T) {
	_, sd, p, libAbs, userAbs := crossFixture(t)
	sd.script[userAbs] = [][]lsp.FileDiagnostic{{diagAt(userAbs, 3, "same")}}

	res := editLib(t, p, libAbs, nil)

	if strings.Contains(res.Output, "Cross-file") {
		t.Fatalf("unchanged importer reported: %q", res.Output)
	}
}

// A cancelled context stops the importer checks before they start.
func TestCrossFileRespectsCancelledContext(t *testing.T) {
	_, sd, p, _, userAbs := crossFixture(t)
	sd.script[userAbs] = [][]lsp.FileDiagnostic{{}, {diagAt(userAbs, 3, "x")}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := tools.Result{Output: "ok", Files: []string{"lib.go"}}

	p.AfterTool(ctx, "write_file", nil, &res)

	if sd.calls[userAbs] != 0 || strings.Contains(res.Output, "Cross-file") {
		t.Fatalf("checked under a cancelled context: calls %d, output %q", sd.calls[userAbs], res.Output)
	}
}

// The block never exceeds maxCrossLines, and says how many were left out.
func TestFormatCrossFileCapsLines(t *testing.T) {
	p := newPostEditor(t.TempDir(), nil)
	var added []lsp.FileDiagnostic
	for i := 0; i < 9; i++ {
		added = append(added, diagAt(filepath.Join(p.workdir, fmt.Sprintf("f%d.go", i)), i+1, "boom"))
	}

	out := p.formatCrossFile(added, 0, 3, 3)

	lines := strings.Split(out, "\n")
	if len(lines) > maxCrossLines {
		t.Fatalf("block has %d lines, cap %d:\n%s", len(lines), maxCrossLines, out)
	}
	if !strings.Contains(out, "... and") {
		t.Fatalf("no overflow line:\n%s", out)
	}
}

// fixed-only results still produce a block, with the count and no error lines.
func TestFormatCrossFileFixedOnly(t *testing.T) {
	p := newPostEditor(t.TempDir(), nil)
	out := p.formatCrossFile(nil, 2, 1, 1)
	if out != "Cross-file: 0 new error(s) in files that import the edit (1 checked). 2 fixed." {
		t.Fatalf("out = %q", out)
	}
}
