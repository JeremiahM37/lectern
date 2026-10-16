package symedit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// tracker is a FileTracker that remembers reads and lets a test mark a file stale.
type tracker struct {
	read, stale map[string]bool
	written     []string
}

func newTracker() *tracker { return &tracker{read: map[string]bool{}, stale: map[string]bool{}} }

func (t *tracker) BeforeWrite(abs string)         { t.written = append(t.written, abs) }
func (t *tracker) MarkRead(abs string)            { t.read[abs] = true; delete(t.stale, abs) }
func (t *tracker) StaleSinceRead(abs string) bool { return t.stale[abs] }
func (t *tracker) WasRead(abs string) bool        { return t.read[abs] }

type fixture struct {
	t    *testing.T
	dir  string
	env  *tools.Env
	trk  *tracker
	tool tools.Tool
}

func newFixture(t *testing.T) *fixture {
	dir := t.TempDir()
	trk := newTracker()
	r := tools.NewRegistry()
	Register(r)
	tool, ok := r.Get(toolName)
	if !ok {
		t.Fatal("ast_edit not registered")
	}
	return &fixture{t: t, dir: dir, trk: trk, tool: tool,
		env: &tools.Env{Workdir: dir, Files: trk, Mode: "auto"}}
}

// write creates a file under the fixture and marks it read, as a prior read would.
func (f *fixture) write(name, content string) string {
	f.t.Helper()
	p := filepath.Join(f.dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.trk.MarkRead(p)
	return p
}

func (f *fixture) run(args map[string]any) tools.Result {
	f.t.Helper()
	b, _ := json.Marshal(args)
	return f.tool.Run(context.Background(), f.env, b)
}

func (f *fixture) read(p string) string {
	f.t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

func mustOK(t *testing.T, res tools.Result) tools.Result {
	t.Helper()
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	return res
}

func mustErr(t *testing.T, res tools.Result, want string) {
	t.Helper()
	if !res.IsError {
		t.Fatalf("expected an error containing %q, got success: %s", want, res.Output)
	}
	if !strings.Contains(res.Output, want) {
		t.Fatalf("error %q does not contain %q", res.Output, want)
	}
}

const goSrc = `package demo

import "strings"

// Server handles requests.
type Server struct {
	name string
}

// Start begins serving.
// It is a method.
func (s *Server) Start() error {
	return nil
}

// Stop is the other method.
func (s *Server) Stop() {
	s.name = strings.ToUpper(s.name)
}

func (c *Client) Start() {}

func ParseList(in string) []string {
	parts := strings.Split(in, ",")
	return parts
}
`

func TestGoMethodReplaceKeepsDocAndFormats(t *testing.T) {
	f := newFixture(t)
	p := f.write("demo.go", goSrc)
	res := mustOK(t, f.run(map[string]any{
		"path": "demo.go", "symbol": "Server.Start", "action": "replace",
		"new_code": "// Start begins serving, now with a check.\nfunc (s *Server) Start() error {\n  if s.name == \"\" {\n    return errEmpty\n  }\n  return nil\n}",
	}))
	got := f.read(p)
	if !strings.Contains(got, "\tif s.name == \"\" {\n\t\treturn errEmpty") {
		t.Fatalf("new method not re-indented and gofmt'd:\n%s", got)
	}
	if strings.Contains(got, "// Start begins serving.\n") {
		t.Fatalf("old doc comment should have been replaced:\n%s", got)
	}
	if !strings.Contains(got, "// Stop is the other method.") {
		t.Fatalf("neighbouring method was disturbed:\n%s", got)
	}
	if res.Summary == "" || !strings.Contains(res.Summary, "in Server.Start") || !strings.HasPrefix(res.Summary, "+") {
		t.Fatalf("summary = %q", res.Summary)
	}
	if !strings.Contains(res.Diff, "+\tif s.name == \"\" {") {
		t.Fatalf("diff missing change:\n%s", res.Diff)
	}
	if len(res.Files) != 1 || res.Files[0] != "demo.go" {
		t.Fatalf("files = %v", res.Files)
	}
}

func TestGoReplaceBodyKeepsHeader(t *testing.T) {
	f := newFixture(t)
	p := f.write("demo.go", goSrc)
	mustOK(t, f.run(map[string]any{
		"path": "demo.go", "symbol": "ParseList", "action": "replace_body",
		"new_code": "parts := strings.Fields(in)\nreturn parts",
	}))
	got := f.read(p)
	if !strings.Contains(got, "func ParseList(in string) []string {\n\tparts := strings.Fields(in)\n\treturn parts\n}") {
		t.Fatalf("body not replaced as expected:\n%s", got)
	}
}

func TestGoDeleteRemovesDocComment(t *testing.T) {
	f := newFixture(t)
	p := f.write("demo.go", goSrc)
	mustOK(t, f.run(map[string]any{"path": "demo.go", "symbol": "Server.Start", "action": "delete"}))
	got := f.read(p)
	if strings.Contains(got, "Start begins serving") || strings.Contains(got, "func (s *Server) Start") {
		t.Fatalf("method or its doc comment survived:\n%s", got)
	}
	if strings.Contains(got, "\n\n\n") {
		t.Fatalf("delete left a double blank line:\n%q", got)
	}
	if !strings.Contains(got, "// Stop is the other method.") {
		t.Fatalf("wrong symbol deleted:\n%s", got)
	}
}

func TestGoInsertAfter(t *testing.T) {
	f := newFixture(t)
	p := f.write("demo.go", goSrc)
	mustOK(t, f.run(map[string]any{
		"path": "demo.go", "symbol": "ParseList", "action": "insert_after",
		"new_code": "func Empty() []string { return nil }",
	}))
	got := f.read(p)
	i := strings.Index(got, "func ParseList")
	j := strings.Index(got, "func Empty() []string")
	if i < 0 || j < i || !strings.Contains(got[i:j], "return parts\n}\n\n") {
		t.Fatalf("insert not placed after ParseList:\n%s", got)
	}
}

func TestGoReplaceInBodyUniqueness(t *testing.T) {
	f := newFixture(t)
	src := "package demo\n\nfunc A() int {\n\tx := 1\n\treturn x\n}\n\nfunc B() int {\n\tx := 1\n\treturn x\n}\n"
	p := f.write("demo.go", src)

	// x := 1 appears once in A and once in B: scoped to A it must still be unique.
	mustOK(t, f.run(map[string]any{
		"path": "demo.go", "symbol": "A", "action": "replace_in_body",
		"old_string": "x := 1", "new_string": "x := 2",
	}))
	got := f.read(p)
	if !strings.Contains(got, "func A() int {\n\tx := 2") || !strings.Contains(got, "func B() int {\n\tx := 1") {
		t.Fatalf("anchored edit hit the wrong symbol:\n%s", got)
	}

	// Two matches inside one symbol are refused and nothing changes.
	f.write("twice.go", "package demo\n\nfunc C() {\n\ty()\n\ty()\n}\n")
	before := f.read(filepath.Join(f.dir, "twice.go"))
	res := f.run(map[string]any{
		"path": "twice.go", "symbol": "C", "action": "replace_in_body",
		"old_string": "y()", "new_string": "z()",
	})
	mustErr(t, res, "matches 2 places")
	if f.read(filepath.Join(f.dir, "twice.go")) != before {
		t.Fatal("ambiguous replace_in_body wrote to disk")
	}
}

func TestGoBatchIsAtomicOnParseError(t *testing.T) {
	f := newFixture(t)
	p := f.write("demo.go", goSrc)
	before := f.read(p)
	res := f.run(map[string]any{
		"path": "demo.go",
		"edits": []map[string]any{
			{"action": "insert_after", "symbol": "ParseList", "new_code": "func Ok() {}"},
			{"action": "replace", "symbol": "Server.Stop", "new_code": "func (s *Server) Stop() {\n\tif {\n}"},
		},
	})
	mustErr(t, res, "NO edits were applied")
	mustErr(t, res, "would not parse as Go")
	mustErr(t, res, "line ")
	if f.read(p) != before {
		t.Fatal("failed batch changed the file")
	}
}

func TestGoBatchSucceedsAndReportsAllSymbols(t *testing.T) {
	f := newFixture(t)
	p := f.write("demo.go", goSrc)
	res := mustOK(t, f.run(map[string]any{
		"path": "demo.go",
		"edits": []map[string]any{
			{"action": "rename_local", "symbol": "ParseList", "old_name": "parts", "new_name": "fields"},
			{"action": "delete", "symbol": "Client.Start"},
		},
	}))
	got := f.read(p)
	if !strings.Contains(got, "fields := strings.Split") || strings.Contains(got, "Client") {
		t.Fatalf("batch not applied:\n%s", got)
	}
	if !strings.Contains(res.Summary, "ParseList") || !strings.Contains(res.Summary, "Client.Start") {
		t.Fatalf("summary should name both symbols: %q", res.Summary)
	}
}

func TestGoAddImport(t *testing.T) {
	f := newFixture(t)
	p := f.write("demo.go", goSrc)
	mustOK(t, f.run(map[string]any{"path": "demo.go", "action": "add_import", "new_code": `"os"`}))
	got := f.read(p)
	if !strings.Contains(got, "\"os\"") || !strings.Contains(got, "\"strings\"") {
		t.Fatalf("import not added:\n%s", got)
	}
	// Adding it again is a no-op.
	res := mustOK(t, f.run(map[string]any{"path": "demo.go", "action": "add_import", "new_code": `"os"`}))
	if res.Summary != "no changes" {
		t.Fatalf("second add_import summary = %q", res.Summary)
	}
	f.write("single.go", "package demo\n\nimport \"fmt\"\n\nfunc F() { fmt.Println() }\n")
	mustOK(t, f.run(map[string]any{"path": "single.go", "action": "add_import", "new_code": `"errors"`}))
	if got := f.read(filepath.Join(f.dir, "single.go")); !strings.Contains(got, "import \"errors\"") {
		t.Fatalf("single import form not extended:\n%s", got)
	}
}

func TestAmbiguousAndNotFound(t *testing.T) {
	f := newFixture(t)
	f.write("demo.go", goSrc)
	mustErr(t, f.run(map[string]any{"path": "demo.go", "symbol": "Start", "action": "delete"}), "matches 2 definitions")
	res := f.run(map[string]any{"path": "demo.go", "symbol": "ParseLst", "action": "delete"})
	mustErr(t, res, "Closest: ParseList")
}

func TestReadBeforeEditAndStale(t *testing.T) {
	f := newFixture(t)
	p := f.write("demo.go", goSrc)
	delete(f.trk.read, p)
	mustErr(t, f.run(map[string]any{"path": "demo.go", "symbol": "ParseList", "action": "delete"}), "has not been read")
	f.trk.MarkRead(p)
	f.trk.stale[p] = true
	mustErr(t, f.run(map[string]any{"path": "demo.go", "symbol": "ParseList", "action": "delete"}), "changed on disk")
}

func TestUnsupportedAndGitPaths(t *testing.T) {
	f := newFixture(t)
	f.write("notes.md", "# hi\n")
	mustErr(t, f.run(map[string]any{"path": "notes.md", "symbol": "x", "action": "delete"}), "edit_file")
	f.write(".git/config", "x")
	mustErr(t, f.run(map[string]any{"path": ".git/config", "symbol": "x", "action": "delete"}), "Refusing to write inside .git")
}

const tsSrc = `import { join } from "path";

export class Greeter {
  private name: string;

  /** Says hello. */
  greet(who: string): string {
    const msg = "hello " + who;
    return msg;
  }

  bye(): void {
    console.log("bye {");
  }
}

export function top(): number {
  return 1;
}
`

func TestTypeScriptClassMethodReplace(t *testing.T) {
	f := newFixture(t)
	p := f.write("greet.ts", tsSrc)
	mustOK(t, f.run(map[string]any{
		"path": "greet.ts", "symbol": "Greeter.greet", "action": "replace",
		"new_code": "greet(who: string): string {\n  return `hi ${who}`;\n}",
	}))
	got := f.read(p)
	if !strings.Contains(got, "  greet(who: string): string {\n    return `hi ${who}`;\n  }\n") {
		t.Fatalf("method not replaced with class indentation:\n%s", got)
	}
	if strings.Contains(got, "/** Says hello. */") {
		t.Fatalf("doc comment should be part of the replaced range:\n%s", got)
	}
	if !strings.Contains(got, "bye(): void {") || !strings.Contains(got, "export function top()") {
		t.Fatalf("neighbours disturbed:\n%s", got)
	}
}

func TestTypeScriptBracketGuardRefuses(t *testing.T) {
	f := newFixture(t)
	p := f.write("greet.ts", tsSrc)
	before := f.read(p)
	mustErr(t, f.run(map[string]any{
		"path": "greet.ts", "symbol": "top", "action": "replace_body",
		"new_code": "return 2;\n}}",
	}), "unbalanced")
	if f.read(p) != before {
		t.Fatal("refused edit was written")
	}
}

func TestTypeScriptAddImport(t *testing.T) {
	f := newFixture(t)
	p := f.write("greet.ts", tsSrc)
	mustOK(t, f.run(map[string]any{"path": "greet.ts", "action": "add_import", "new_code": `import { resolve } from "path";`}))
	got := f.read(p)
	if !strings.HasPrefix(got, "import { join } from \"path\";\nimport { resolve } from \"path\";\n\nexport class") {
		t.Fatalf("import not placed after existing imports:\n%s", got)
	}
}

const pySrc = `import os


class Report:
    def __init__(self, name):
        self.name = name

    @staticmethod
    @cache
    def build(items):
        """Build it."""
        total = 0
        for i in items:
            total += i
        return total

    def done(self):
        return True
`

func TestPythonDecoratedReplaceBody(t *testing.T) {
	f := newFixture(t)
	p := f.write("report.py", pySrc)
	mustOK(t, f.run(map[string]any{
		"path": "report.py", "symbol": "Report.build", "action": "replace_body",
		"new_code": "\"\"\"Build it.\"\"\"\ntotal = sum(items)\nreturn total",
	}))
	got := f.read(p)
	want := "    @staticmethod\n    @cache\n    def build(items):\n        \"\"\"Build it.\"\"\"\n        total = sum(items)\n        return total\n\n    def done(self):"
	if !strings.Contains(got, want) {
		t.Fatalf("decorated body not replaced with the right indentation:\n%s", got)
	}
}

func TestPythonBodyMustStayUnderHeader(t *testing.T) {
	f := newFixture(t)
	p := f.write("report.py", pySrc)
	before := f.read(p)
	// A body that dedents to the class level would escape the method.
	res := f.run(map[string]any{
		"path": "report.py", "symbol": "Report.done", "action": "replace_body",
		"new_code": "x = 1\nreturn x",
	})
	if res.IsError {
		t.Fatalf("a normal body should succeed: %s", res.Output)
	}
	if f.read(p) == before {
		t.Fatal("expected the body to change")
	}
}

const rustSrc = `pub struct Server {
    port: u16,
}

impl Server {
    /// Creates a server.
    pub fn new(port: u16) -> Self {
        Server { port }
    }

    pub fn start(&self) -> bool {
        self.port > 0
    }
}

fn free() -> u8 {
    1
}
`

func TestRustImplMethodReplace(t *testing.T) {
	f := newFixture(t)
	p := f.write("server.rs", rustSrc)
	mustOK(t, f.run(map[string]any{
		"path": "server.rs", "symbol": "Server.start", "action": "replace",
		"new_code": "pub fn start(&self) -> bool {\n    self.port != 0\n}",
	}))
	got := f.read(p)
	if !strings.Contains(got, "    pub fn start(&self) -> bool {\n        self.port != 0\n    }\n}") {
		t.Fatalf("impl method not replaced inside impl indentation:\n%s", got)
	}
	if !strings.Contains(got, "/// Creates a server.") || !strings.Contains(got, "fn free() -> u8") {
		t.Fatalf("neighbours disturbed:\n%s", got)
	}
}

func TestRustReplaceBodyOnImplMethod(t *testing.T) {
	f := newFixture(t)
	p := f.write("server.rs", rustSrc)
	mustOK(t, f.run(map[string]any{
		"path": "server.rs", "symbol": "new", "action": "replace_body",
		"new_code": "Server { port: port + 1 }",
	}))
	if got := f.read(p); !strings.Contains(got, "    pub fn new(port: u16) -> Self {\n        Server { port: port + 1 }\n    }") {
		t.Fatalf("body not replaced:\n%s", got)
	}
}
