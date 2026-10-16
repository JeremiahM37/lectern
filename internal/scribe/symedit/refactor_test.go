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

// refactorFixture is a project directory with the refactor tools registered.
type refactorFixture struct {
	t   *testing.T
	dir string
	trk *tracker
	reg *tools.Registry
	env *tools.Env
}

func newRefactorFixture(t *testing.T) *refactorFixture {
	dir := t.TempDir()
	trk := newTracker()
	reg := tools.NewRegistry()
	Register(reg)
	return &refactorFixture{t: t, dir: dir, trk: trk, reg: reg,
		env: &tools.Env{Workdir: dir, Files: trk, Mode: "auto"}}
}

func (f *refactorFixture) write(name, content string) string {
	f.t.Helper()
	p := filepath.Join(f.dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.trk.MarkRead(p)
	return p
}

func (f *refactorFixture) run(name string, args map[string]any) tools.Result {
	f.t.Helper()
	tool, ok := f.reg.Get(name)
	if !ok {
		f.t.Fatalf("%s not registered", name)
	}
	b, _ := json.Marshal(args)
	return tool.Run(context.Background(), f.env, b)
}

func (f *refactorFixture) read(name string) string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, filepath.FromSlash(name)))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

func (f *refactorFixture) exists(name string) bool {
	_, err := os.Stat(filepath.Join(f.dir, filepath.FromSlash(name)))
	return err == nil
}

const tsUtil = `import { other } from "./other";

// helper doubles its input.
export function helper(n: number): number {
  return other(n) * 2 + local(n);
}

export function local(n: number): number {
  return n + 1;
}
`

func TestTSMoveSymbolUpdatesImporter(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("other.ts", "export function other(n: number): number {\n  return n + 1;\n}\n")
	f.write("util.ts", tsUtil)
	f.write("consumer.ts", "import { helper, local } from \"./util\";\n\nexport const x = helper(local(1));\n")
	mustOK(t, f.run("move_symbol", map[string]any{"symbol": "helper", "from": "util.ts", "to": "lib/helpers.ts"}))

	consumer := f.read("consumer.ts")
	if !strings.Contains(consumer, `import { helper } from "./lib/helpers";`) {
		t.Fatalf("importer not rewritten to the new file:\n%s", consumer)
	}
	if !strings.Contains(consumer, `import { local } from "./util";`) {
		t.Fatalf("unrelated binding should stay on the old import:\n%s", consumer)
	}
	moved := f.read("lib/helpers.ts")
	if !strings.Contains(moved, "export function helper(n: number)") {
		t.Fatalf("declaration missing from destination:\n%s", moved)
	}
	if !strings.Contains(moved, `import { other } from "../other";`) || !strings.Contains(moved, `import { local } from "../util";`) {
		t.Fatalf("destination must import what the moved code uses, from where the source got it:\n%s", moved)
	}
	src := f.read("util.ts")
	if strings.Contains(src, "function helper") {
		t.Fatalf("declaration should have left the source:\n%s", src)
	}
	if !strings.Contains(src, "export function local") {
		t.Fatalf("other declarations must stay:\n%s", src)
	}
	// The source still uses nothing from helper, so no import comes back.
	if strings.Contains(src, "helper") || strings.Contains(src, "from \"./other\"") {
		t.Fatalf("source should not import helper back when nothing uses it:\n%s", src)
	}
}

func TestTSMoveSymbolImportsBackWhenStillUsed(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("util.ts", "export function helper(): number {\n  return 1;\n}\n\nexport const total = helper() + 1;\n")
	f.write("app.ts", "import { helper } from \"./util\";\nconsole.log(helper());\n")
	mustOK(t, f.run("move_symbol", map[string]any{"symbol": "helper", "from": "util.ts", "to": "helpers.ts"}))
	src := f.read("util.ts")
	if !strings.Contains(src, `import { helper } from "./helpers";`) {
		t.Fatalf("source still uses helper and must import it back:\n%s", src)
	}
	if got := f.read("app.ts"); !strings.Contains(got, `import { helper } from "./helpers";`) {
		t.Fatalf("app not rewritten:\n%s", got)
	}
}

func TestTSMoveSymbolRefusesUnexportedDependency(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("util.ts", "const secret = 3;\n\nexport function helper(): number {\n  return secret;\n}\n")
	res := f.run("move_symbol", map[string]any{"symbol": "helper", "from": "util.ts", "to": "helpers.ts"})
	mustErr(t, res, "does not export")
	if f.exists("helpers.ts") {
		t.Fatal("a refused move must not create the destination")
	}
}

func TestTSMoveSymbolNeedsRead(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("util.ts", "export function helper(): number {\n  return 1;\n}\n")
	f.trk.read = map[string]bool{}
	res := f.run("move_symbol", map[string]any{"symbol": "helper", "from": "util.ts", "to": "helpers.ts"})
	mustErr(t, res, "has not been read")
}

func TestTSRenameFileRewritesImports(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("other.ts", "export const o = 1;\n")
	f.write("util.ts", "import { o } from \"./other\";\nexport function u() { return o; }\n")
	f.write("consumer.ts", "import { u } from \"./util\";\nexport const c = u();\n")
	f.write("js/esm.js", "import { u } from \"../util.js\";\nexport default u;\n")
	f.write("cjs.js", "const u = require(\"./util\");\nmodule.exports = u;\n")
	mustOK(t, f.run("rename_file", map[string]any{"from": "util.ts", "to": "lib/util.ts"}))

	if f.exists("util.ts") || !f.exists("lib/util.ts") {
		t.Fatal("file was not moved")
	}
	if got := f.read("lib/util.ts"); !strings.Contains(got, `import { o } from "../other";`) {
		t.Fatalf("moved file's own relative import not rebased:\n%s", got)
	}
	if got := f.read("consumer.ts"); !strings.Contains(got, `import { u } from "./lib/util";`) {
		t.Fatalf("consumer not rewritten:\n%s", got)
	}
	if got := f.read("js/esm.js"); !strings.Contains(got, `import { u } from "../lib/util.js";`) {
		t.Fatalf("esm import not rewritten:\n%s", got)
	}
	if got := f.read("cjs.js"); !strings.Contains(got, `require("./lib/util")`) {
		t.Fatalf("require not rewritten:\n%s", got)
	}
}

func TestTSRenameFileKeepsJSExtensionStyle(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("util.ts", "export const u = 1;\n")
	f.write("app.ts", "import { u } from \"./util.js\";\nexport const a = u;\n")
	mustOK(t, f.run("rename_file", map[string]any{"from": "util.ts", "to": "lib/util.ts"}))
	if got := f.read("app.ts"); !strings.Contains(got, `from "./lib/util.js";`) {
		t.Fatalf("a .js specifier must stay in .js style:\n%s", got)
	}
}

func TestRenameFileRefusesExistingDestination(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("a.ts", "export const a = 1;\n")
	f.write("b.ts", "export const b = 2;\n")
	mustErr(t, f.run("rename_file", map[string]any{"from": "a.ts", "to": "b.ts"}), "already exists")
	if !f.exists("a.ts") {
		t.Fatal("refused move removed the source")
	}
}

func TestGoMoveSymbolSamePackage(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("a.go", "package demo\n\nimport \"strings\"\n\n// Shout upper-cases s.\nfunc Shout(s string) string {\n\treturn strings.ToUpper(s)\n}\n\nfunc Keep() int { return 1 }\n")
	f.write("b.go", "package demo\n\nfunc Use() string { return Shout(\"x\") }\n")
	mustOK(t, f.run("move_symbol", map[string]any{"symbol": "Shout", "from": "a.go", "to": "shout.go"}))

	moved := f.read("shout.go")
	if !strings.Contains(moved, "package demo") || !strings.Contains(moved, "func Shout") || !strings.Contains(moved, "// Shout upper-cases s.") {
		t.Fatalf("destination wrong:\n%s", moved)
	}
	if !strings.Contains(moved, "\"strings\"") {
		t.Fatalf("destination needs the strings import the moved code uses:\n%s", moved)
	}
	src := f.read("a.go")
	if strings.Contains(src, "Shout") || strings.Contains(src, "\"strings\"") {
		t.Fatalf("source should lose the declaration and its now-unused import:\n%s", src)
	}
	if !strings.Contains(src, "func Keep()") {
		t.Fatalf("other declarations must stay:\n%s", src)
	}
}

func TestGoMoveSymbolRefusesOtherPackage(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("a.go", "package demo\n\nfunc Shout() {}\n")
	mustErr(t, f.run("move_symbol", map[string]any{"symbol": "Shout", "from": "a.go", "to": "sub/shout.go"}), "another package")
}

func TestGoRenameFileSamePackageOnly(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("a.go", "package demo\n\nfunc A() {}\n")
	mustOK(t, f.run("rename_file", map[string]any{"from": "a.go", "to": "renamed.go"}))
	if f.exists("a.go") || !f.exists("renamed.go") {
		t.Fatal("same-package rename should just move the file")
	}
	f.write("sub/x.go", "package sub\n")
	mustErr(t, f.run("rename_file", map[string]any{"from": "renamed.go", "to": "sub/renamed.go"}), "another package")
	if !f.exists("renamed.go") {
		t.Fatal("refused cross-package move must leave the file in place")
	}
}

func TestPythonRenameFileRewritesModules(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("pkg/__init__.py", "")
	f.write("pkg/util.py", "from .helpers import h\n\ndef g():\n    return h()\n")
	f.write("pkg/helpers.py", "def h():\n    return 1\n")
	f.write("pkg/app.py", "from pkg.util import g\nfrom .util import g as g2\nimport pkg.util as u\n")
	mustOK(t, f.run("rename_file", map[string]any{"from": "pkg/util.py", "to": "pkg/tools/util.py"}))

	app := f.read("pkg/app.py")
	for _, want := range []string{"from pkg.tools.util import g", "from .tools.util import g as g2", "import pkg.tools.util as u"} {
		if !strings.Contains(app, want) {
			t.Fatalf("app.py missing %q:\n%s", want, app)
		}
	}
	moved := f.read("pkg/tools/util.py")
	if !strings.Contains(moved, "from ..helpers import h") {
		t.Fatalf("moved module's relative import not rebased:\n%s", moved)
	}
}

func TestTextRenameGoCallSites(t *testing.T) {
	dir := t.TempDir()
	env := &tools.Env{Workdir: dir, Files: newTracker()}
	must := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("demo.go", "package demo\n\n// Greet says hi. Greet is documented here.\nfunc Greet(name string) string {\n\treturn \"Greet \" + name\n}\n\nfunc Use() string { return Greet(\"bob\") + GreetAll() }\n")
	must("other.go", "package demo\n\nvar _ = Greet\n")
	must("notes.md", "Greet is mentioned in prose.\n")

	res := TextRename(env, dir, "Greet", "Hello")
	if res.IsError {
		t.Fatalf("rename failed: %s", res.Output)
	}
	got := mustRead(t, filepath.Join(dir, "demo.go"))
	if !strings.Contains(got, "func Hello(name string)") || !strings.Contains(got, "return Hello(\"bob\")") {
		t.Fatalf("definition and call site should be renamed:\n%s", got)
	}
	if !strings.Contains(got, "GreetAll") {
		t.Fatalf("whole-word match only; GreetAll must stay:\n%s", got)
	}
	if !strings.Contains(got, "// Greet says hi. Greet is documented here.") || !strings.Contains(got, "\"Greet \" + name") {
		t.Fatalf("comments and strings must not change:\n%s", got)
	}
	if other := mustRead(t, filepath.Join(dir, "other.go")); !strings.Contains(other, "var _ = Hello") {
		t.Fatalf("other file's reference not renamed:\n%s", other)
	}
	if md := mustRead(t, filepath.Join(dir, "notes.md")); !strings.Contains(md, "Greet is mentioned") {
		t.Fatalf("files without comment rules must not be rewritten:\n%s", md)
	}
	if !strings.Contains(res.Output, "text-only") || !strings.Contains(res.Output, "demo.go:8: ") || !strings.Contains(res.Output, "notes.md") {
		t.Fatalf("result should say the rename was text-only, list sites, and name skipped files:\n%s", res.Output)
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
