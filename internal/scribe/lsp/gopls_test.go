package lsp

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// goplsFixture writes a small Go module and returns its root. Columns in the
// assertions below depend on these exact bytes.
//
//	a.go line 4:  func Greet  (G is column 6)
//	b.go line 4:  return "😀😀" + Greet("bob")   (G is column 16, UTF-16 char 17)
//	bad/bad.go:   a type error at 3:13
func goplsFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":      "module example.com/demo\n\ngo 1.22\n",
		"a.go":        "package demo\n\n// Greet returns a greeting.\nfunc Greet(name string) string {\n\treturn \"hi \" + name\n}\n",
		"b.go":        "package demo\n\nfunc Use() string {\n\treturn \"😀😀\" + Greet(\"bob\")\n}\n",
		"bad/bad.go":  "package bad\n\nvar X int = \"oops\"\n",
		"fmtpkg/f.go": "package fmtpkg\n\nfunc  F( )int{return 1}\n",
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		mustWrite(t, p, content)
	}
	return dir
}

// requireGopls skips the test unless gopls is on PATH or in ~/go/bin.
func requireGopls(t *testing.T, root string) {
	t.Helper()
	if _, ok := lookBinary("gopls", root); !ok {
		t.Skip("gopls not installed: GOBIN=$HOME/go/bin go install golang.org/x/tools/gopls@latest")
	}
}

func TestGoplsOperations(t *testing.T) {
	dir := goplsFixture(t)
	requireGopls(t, dir)
	m := NewManager(dir)
	defer m.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	a := filepath.Join(dir, "a.go")
	b := filepath.Join(dir, "b.go")

	t.Run("definition across files past an emoji", func(t *testing.T) {
		locs, err := m.Definition(ctx, b, Point{Line: 4, Column: 16})
		if err != nil {
			t.Fatal(err)
		}
		if len(locs) != 1 || locs[0].Path != a || locs[0].Line != 4 || locs[0].Column != 6 {
			t.Fatalf("definition = %+v, want a.go 4:6", locs)
		}
	})

	t.Run("references include both files", func(t *testing.T) {
		refs, err := m.References(ctx, a, Point{Line: 4, Column: 6})
		if err != nil {
			t.Fatal(err)
		}
		var sawB bool
		for _, r := range refs {
			if r.Path == b && r.Line == 4 && r.Column == 16 {
				sawB = true
			}
		}
		if !sawB {
			t.Fatalf("references = %+v, want one at b.go 4:16", refs)
		}
	})

	t.Run("hover shows the signature", func(t *testing.T) {
		text, err := m.Hover(ctx, a, Point{Line: 4, Column: 6})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(text, "func Greet(name string) string") {
			t.Fatalf("hover = %q", text)
		}
	})

	t.Run("document symbols", func(t *testing.T) {
		syms, err := m.DocumentSymbols(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, s := range syms {
			if s.Name == "Greet" && s.Kind == "function" && s.Line == 4 && s.Column == 6 {
				found = true
			}
		}
		if !found {
			t.Fatalf("symbols = %+v", syms)
		}
	})

	t.Run("workspace symbols", func(t *testing.T) {
		hits, err := m.WorkspaceSymbols(ctx, a, "Greet")
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) == 0 || hits[0].Name != "Greet" || hits[0].Path != a {
			t.Fatalf("workspace symbols = %+v", hits)
		}
		all, err := m.WorkspaceSymbols(ctx, "", "Greet")
		if err != nil || len(all) == 0 {
			t.Fatalf("running-server search = %+v, %v", all, err)
		}
	})

	t.Run("diagnostics report the type error", func(t *testing.T) {
		diags, err := m.Diagnostics(ctx, filepath.Join(dir, "bad", "bad.go"))
		if err != nil {
			t.Fatal(err)
		}
		if len(diags) != 1 || diags[0].Severity != "error" || diags[0].Line != 3 || diags[0].Column != 13 ||
			!strings.Contains(diags[0].Message, "cannot use") {
			t.Fatalf("diagnostics = %+v", diags)
		}
	})

	t.Run("format rewrites the file", func(t *testing.T) {
		f := filepath.Join(dir, "fmtpkg", "f.go")
		edits, err := m.Format(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		if len(edits) == 0 {
			t.Fatal("expected formatting edits")
		}
		if _, err := ApplyWorkspaceEdit(WorkspaceEdit{Changes: map[string][]TextEdit{pathToURI(f): edits}}); err != nil {
			t.Fatal(err)
		}
		if got := mustRead(t, f); !strings.Contains(got, "func F() int {") {
			t.Fatalf("formatted = %q", got)
		}
	})

	t.Run("rename across files keeps unicode intact", func(t *testing.T) {
		edit, err := m.Rename(ctx, a, Point{Line: 4, Column: 6}, "Hello")
		if err != nil {
			t.Fatal(err)
		}
		res, err := ApplyWorkspaceEdit(edit)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Changes) != 2 {
			t.Fatalf("changed %d files, want 2", len(res.Changes))
		}
		gotA, gotB := mustRead(t, a), mustRead(t, b)
		if !strings.Contains(gotA, "func Hello(name string) string {") || strings.Contains(gotA, "Greet") {
			t.Fatalf("a.go = %q", gotA)
		}
		if !strings.Contains(gotB, `return "😀😀" + Hello("bob")`) {
			t.Fatalf("b.go = %q", gotB)
		}
		// The server must see the new text: the renamed symbol resolves.
		m.FileChanged(a)
		m.FileChanged(b)
		locs, err := m.Definition(ctx, b, Point{Line: 4, Column: 16})
		if err != nil {
			t.Fatal(err)
		}
		if len(locs) != 1 || locs[0].Path != a || locs[0].Line != 4 {
			t.Fatalf("definition after rename = %+v", locs)
		}
	})
}

// fakeTracker records which files were about to be written.
type fakeTracker struct{ written []string }

func (f *fakeTracker) BeforeWrite(abs string)     { f.written = append(f.written, abs) }
func (f *fakeTracker) MarkRead(string)            {}
func (f *fakeTracker) StaleSinceRead(string) bool { return false }
func (f *fakeTracker) WasRead(string) bool        { return false }

func TestGoplsTools(t *testing.T) {
	dir := goplsFixture(t)
	requireGopls(t, dir)
	m := NewManager(dir)
	defer m.Shutdown()
	reg := newRegistry(m)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	tracker := &fakeTracker{}
	env := &envT{dir: dir, files: tracker}

	run := func(name, args string) (string, bool, []string) {
		t.Helper()
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("tool %s not registered", name)
		}
		res := tool.Run(ctx, env.toEnv(), []byte(args))
		return res.Output, res.IsError, res.Files
	}

	out, isErr, _ := run("navigate", `{"action":"definition","symbol":"Greet"}`)
	if isErr || !strings.Contains(out, "a.go:4:6") {
		t.Fatalf("navigate by symbol: %q (err=%v)", out, isErr)
	}
	out, isErr, _ = run("navigate", `{"action":"definition","path":"b.go","line":4,"column":16}`)
	if isErr || !strings.Contains(out, "a.go:4:6") || !strings.Contains(out, "func Greet") {
		t.Fatalf("navigate by position: %q (err=%v)", out, isErr)
	}
	out, isErr, _ = run("navigate", `{"action":"symbols","path":"a.go"}`)
	if isErr || !strings.Contains(out, "function Greet  line 4") {
		t.Fatalf("navigate symbols: %q", out)
	}
	out, isErr, _ = run("diagnostics", `{"path":"bad/bad.go"}`)
	if isErr || !strings.HasPrefix(out, "bad/bad.go:3:13 error cannot use") {
		t.Fatalf("diagnostics: %q (err=%v)", out, isErr)
	}
	out, isErr, _ = run("navigate", `{"action":"nope","path":"a.go"}`)
	if !isErr {
		t.Fatalf("unknown action should be an error: %q", out)
	}
	out, isErr, _ = run("navigate", `{"action":"hover","symbol":"NoSuchThing"}`)
	if !isErr || !strings.Contains(out, "could not find the symbol") {
		t.Fatalf("missing symbol: %q (err=%v)", out, isErr)
	}

	out, isErr, files := run("rename_symbol", `{"symbol":"Greet","new_name":"Salute"}`)
	if isErr {
		t.Fatalf("rename_symbol: %s", out)
	}
	if len(files) != 2 || !strings.Contains(out, "Renamed 'Greet' to 'Salute' in 2 file(s)") {
		t.Fatalf("rename output = %q files=%v", out, files)
	}
	if got := mustRead(t, filepath.Join(dir, "a.go")); !strings.Contains(got, "func Salute(") {
		t.Fatalf("a.go after rename = %q", got)
	}
	if got := mustRead(t, filepath.Join(dir, "b.go")); !strings.Contains(got, `Salute("bob")`) {
		t.Fatalf("b.go after rename = %q", got)
	}
	if len(tracker.written) != 2 {
		t.Fatalf("BeforeWrite calls = %v, want the two changed files", tracker.written)
	}

	out, isErr, files = run("format_file", `{"path":"fmtpkg/f.go"}`)
	if isErr || len(files) != 1 || !strings.Contains(mustRead(t, filepath.Join(dir, "fmtpkg", "f.go")), "func F() int {") {
		t.Fatalf("format_file: %q err=%v", out, isErr)
	}
	out, _, _ = run("format_file", `{"path":"fmtpkg/f.go"}`)
	if !strings.Contains(out, "Already formatted") {
		t.Fatalf("second format should be a no-op: %q", out)
	}
}

func TestMissingServerExplainsInstall(t *testing.T) {
	spec := &serverSpec{Language: "fake", Candidates: []candidate{{"lectern-no-such-server", nil}}, Install: "install it now"}
	if _, _, ok := findServer(spec, t.TempDir()); ok {
		t.Fatal("a server that does not exist was found")
	}
	msg := notInstalled(spec).Error()
	if !strings.Contains(msg, "lectern-no-such-server") || !strings.Contains(msg, "install it now") {
		t.Fatalf("message = %q", msg)
	}
	var ni *NotInstalledError
	if !errors.As(notInstalled(spec), &ni) {
		t.Fatal("not a NotInstalledError")
	}
}

func TestRootDetectionAndSyncOnUnopenedFile(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "sub", "go.mod"), "module example.com/sub\n\ngo 1.22\n")
	mustWrite(t, filepath.Join(dir, "sub", "pkg", "x.go"), "package pkg\n")
	mustWrite(t, filepath.Join(dir, "top.go"), "package top\n")
	m := NewManager(dir)
	spec := serverSpecs["go"]
	if got := m.rootFor(spec, filepath.Join(dir, "sub", "pkg", "x.go")); got != filepath.Join(dir, "sub") {
		t.Fatalf("root for sub file = %s", got)
	}
	if got := m.rootFor(spec, filepath.Join(dir, "top.go")); got != dir {
		t.Fatalf("root for top file = %s", got)
	}
	// Nothing is running: FileChanged must be a harmless no-op.
	m.FileChanged(filepath.Join(dir, "top.go"))
	m.Shutdown()
	if _, err := m.Definition(context.Background(), filepath.Join(dir, "top.go"), Point{1, 1}); err == nil {
		t.Fatal("expected an error after Shutdown")
	}
}

func newRegistry(m *Manager) *tools.Registry {
	r := tools.NewRegistry()
	Register(r, m)
	return r
}

type envT struct {
	dir   string
	files tools.FileTracker
}

func (e *envT) toEnv() *tools.Env {
	return &tools.Env{Workdir: e.dir, Files: e.files}
}
