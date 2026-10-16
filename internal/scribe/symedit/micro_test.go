package symedit

import (
	"strings"
	"testing"
)

const tsMicroSrc = `import { a } from "./a";

export function load(id: string, retries: number) {
  return id;
}

let count = 0;
const limit: number = 3;

class Box {
  open() {
    return 1;
  }
}
`

// edit runs one symbol-addressed operation on path and returns the file text.
func editOK(t *testing.T, f *fixture, path string, args map[string]any) string {
	t.Helper()
	args["path"] = path
	mustOK(t, f.run(args))
	return f.read(path)
}

func TestMicroTSReturnTypeAndAsync(t *testing.T) {
	f := newFixture(t)
	p := f.write("m.ts", tsMicroSrc)
	got := editOK(t, f, p, map[string]any{"action": "set_return_type", "symbol": "load", "new_code": "Promise<string>"})
	if !strings.Contains(got, "export function load(id: string, retries: number): Promise<string> {") {
		t.Fatalf("return type not set:\n%s", got)
	}
	got = editOK(t, f, p, map[string]any{"action": "set_async", "symbol": "load"})
	if !strings.Contains(got, "export async function load(") {
		t.Fatalf("not made async:\n%s", got)
	}
	again := f.run(map[string]any{"path": p, "action": "set_async", "symbol": "load"})
	if again.IsError || !strings.Contains(again.Output, "already present") {
		t.Fatalf("second set_async should be a no-op: %s", again.Output)
	}
}

func TestMicroParametersAddAndRemove(t *testing.T) {
	f := newFixture(t)
	p := f.write("m.ts", tsMicroSrc)
	got := editOK(t, f, p, map[string]any{"action": "add_parameter", "symbol": "load", "new_code": "opts: Options"})
	if !strings.Contains(got, "load(id: string, retries: number, opts: Options) {") {
		t.Fatalf("parameter not appended:\n%s", got)
	}
	got = editOK(t, f, p, map[string]any{"action": "remove_parameter", "symbol": "load", "new_code": "retries"})
	if !strings.Contains(got, "export function load(id: string, opts: Options) {") {
		t.Fatalf("middle parameter not removed:\n%s", got)
	}
	got = editOK(t, f, p, map[string]any{"action": "remove_parameter", "symbol": "load", "old_name": "opts"})
	if !strings.Contains(got, "export function load(id: string) {") {
		t.Fatalf("last parameter not removed (old_name form):\n%s", got)
	}
	res := f.run(map[string]any{"path": p, "action": "remove_parameter", "symbol": "load", "new_code": "missing"})
	mustErr(t, res, "has no parameter named missing")
}

func TestMicroRenameDeclarationInTS(t *testing.T) {
	f := newFixture(t)
	p := f.write("m.ts", tsMicroSrc)
	got := editOK(t, f, p, map[string]any{"action": "rename", "symbol": "load", "new_name": "fetchItem"})
	if !strings.Contains(got, "export function fetchItem(") || strings.Contains(got, "load(") {
		t.Fatalf("declaration not renamed:\n%s", got)
	}
}

func TestMicroRenameKeepsReferencesAndRejectsBadNames(t *testing.T) {
	f := newFixture(t)
	p := f.write("m.ts", "function a() {}\nfunction b() { a(); }\n")
	got := editOK(t, f, p, map[string]any{"action": "rename", "symbol": "a", "new_code": "c"})
	if !strings.Contains(got, "function c() {}") || !strings.Contains(got, "a();") {
		t.Fatalf("rename should change only the declaration:\n%s", got)
	}
	res := f.run(map[string]any{"path": p, "action": "rename", "symbol": "c", "new_name": "not valid"})
	mustErr(t, res, "is not an identifier")
}

func TestMicroTargetTableRefusesWrongKinds(t *testing.T) {
	f := newFixture(t)
	p := f.write("m.ts", tsMicroSrc)
	res := f.run(map[string]any{"path": p, "action": "set_return_type", "symbol": "Box", "new_code": "number"})
	mustErr(t, res, "set_return_type cannot target Box, which is a class. Valid targets for set_return_type: function, method")
	res = f.run(map[string]any{"path": p, "action": "add_method", "symbol": "load", "new_code": "x() {}"})
	mustErr(t, res, "Valid targets for add_method: class, struct, interface, trait")
	res = f.run(map[string]any{"path": p, "action": "remove_member", "symbol": "load"})
	mustErr(t, res, "needs a member written Owner.member")
}

func TestMicroMembersAddAndRemove(t *testing.T) {
	f := newFixture(t)
	p := f.write("m.ts", tsMicroSrc)
	got := editOK(t, f, p, map[string]any{"action": "add_method", "symbol": "Box", "new_code": "close() {\n  return 0;\n}"})
	if !strings.Contains(got, "    return 1;\n  }\n  close() {\n    return 0;\n  }\n}") {
		t.Fatalf("method not added as the last member:\n%s", got)
	}
	got = editOK(t, f, p, map[string]any{"action": "remove_member", "symbol": "Box.open"})
	if strings.Contains(got, "open()") || !strings.Contains(got, "close()") {
		t.Fatalf("member not removed:\n%s", got)
	}
}

func TestMicroAddPropertyToInterface(t *testing.T) {
	f := newFixture(t)
	p := f.write("i.ts", "interface Shape {\n  area(): number;\n}\n")
	got := editOK(t, f, p, map[string]any{"action": "add_property", "symbol": "Shape", "new_code": "name: string;"})
	if !strings.Contains(got, "  area(): number;\n  name: string;\n}") {
		t.Fatalf("property not added:\n%s", got)
	}
}

func TestInsertTextAnchors(t *testing.T) {
	f := newFixture(t)
	p := f.write("i.ts", "import { a } from \"./a\";\nimport {\n  b,\n} from \"./b\";\n\nexport const x = 1;\n")
	got := editOK(t, f, p, map[string]any{"action": "insert_text", "value": "after-imports", "new_code": "import { c } from \"./c\";"})
	want := "import {\n  b,\n} from \"./b\";\nimport { c } from \"./c\";\n\nexport const x = 1;\n"
	if !strings.Contains(got, want) {
		t.Fatalf("after-imports wrong:\n%s", got)
	}
	got = editOK(t, f, p, map[string]any{"action": "insert_text", "value": "before-exports", "new_code": "const y = 2;"})
	if !strings.Contains(got, "\nconst y = 2;\nexport const x = 1;") {
		t.Fatalf("before-exports wrong:\n%s", got)
	}
	got = editOK(t, f, p, map[string]any{"action": "insert_text", "index": 0, "new_code": "// top"})
	if !strings.HasPrefix(got, "// top\n") {
		t.Fatalf("index 0 is not the top:\n%s", got)
	}
	got = editOK(t, f, p, map[string]any{"action": "insert_text", "index": -1, "new_code": "// end"})
	if !strings.HasSuffix(got, "// end\n") {
		t.Fatalf("index -1 is not the bottom:\n%s", got)
	}
	got = editOK(t, f, p, map[string]any{"action": "insert_text", "index": "2", "new_code": "// second"})
	if !strings.Contains(got, "// top\n// second\nimport { a }") {
		t.Fatalf("numeric string index should insert before that line:\n%s", got)
	}
}

func TestInsertTextErrors(t *testing.T) {
	f := newFixture(t)
	p := f.write("i.ts", "const a = 1;\n")
	mustErr(t, f.run(map[string]any{"path": p, "action": "insert_text", "new_code": "x"}), "needs an anchor")
	mustErr(t, f.run(map[string]any{"path": p, "action": "insert_text", "index": 9, "new_code": "x"}), "past the end")
	mustErr(t, f.run(map[string]any{"path": p, "action": "insert_text", "value": "before-exports", "new_code": "x"}), "has no export")
	mustErr(t, f.run(map[string]any{"path": p, "action": "insert_text", "index": 1, "new_code": "  "}), "new_code is required")
}

func TestMicroVariablesGoSideTypeAndInitializer(t *testing.T) {
	f := newFixture(t)
	p := f.write("v.go", "package demo\n\nvar Limit = 1\nvar Name string = \"x\"\n")
	got := editOK(t, f, p, map[string]any{"action": "set_initializer", "symbol": "Limit", "new_code": "9"})
	if !strings.Contains(got, "var Limit = 9\n") {
		t.Fatalf("initializer:\n%s", got)
	}
	got = editOK(t, f, p, map[string]any{"action": "set_type", "symbol": "Name", "new_code": "[]byte"})
	if !strings.Contains(got, "var Name []byte = \"x\"") {
		t.Fatalf("type on typed var:\n%s", got)
	}
	got = editOK(t, f, p, map[string]any{"action": "remove_initializer", "symbol": "Name"})
	if !strings.Contains(got, "var Name []byte\n") {
		t.Fatalf("remove initializer:\n%s", got)
	}
	// Dropping the only value of an untyped Go var would leave invalid Go,
	// and the Go check refuses it.
	mustErr(t, f.run(map[string]any{"path": p, "action": "remove_initializer", "symbol": "Limit"}), "would not parse as Go")
}

func TestMicroGoSignatureAndInitializer(t *testing.T) {
	f := newFixture(t)
	p := f.write("g.go", "package demo\n\nvar Limit = 1\n\nfunc Greet(name string) string {\n\treturn name\n}\n\ntype Box struct {\n\tn int\n}\n")
	got := editOK(t, f, p, map[string]any{"action": "set_return_type", "symbol": "Greet", "new_code": "error"})
	if !strings.Contains(got, "func Greet(name string) error {") {
		t.Fatalf("go return type:\n%s", got)
	}
	got = editOK(t, f, p, map[string]any{"action": "set_initializer", "symbol": "Limit", "new_code": "2"})
	if !strings.Contains(got, "var Limit = 2\n") {
		t.Fatalf("go initializer:\n%s", got)
	}
	got = editOK(t, f, p, map[string]any{"action": "add_parameter", "symbol": "Greet", "new_code": "ctx context.Context"})
	if !strings.Contains(got, "func Greet(name string, ctx context.Context) error {") {
		t.Fatalf("go parameter:\n%s", got)
	}
}

func TestMicroPythonReturnTypeAsyncAndMethod(t *testing.T) {
	f := newFixture(t)
	p := f.write("p.py", "class Box:\n    def open(self, n):\n        return n\n\n\ndef load(x):\n    return x\n")
	got := editOK(t, f, p, map[string]any{"action": "set_return_type", "symbol": "load", "new_code": "int"})
	if !strings.Contains(got, "def load(x) -> int:\n") {
		t.Fatalf("python return type:\n%s", got)
	}
	got = editOK(t, f, p, map[string]any{"action": "set_async", "symbol": "load"})
	if !strings.Contains(got, "async def load(x) -> int:") {
		t.Fatalf("python async:\n%s", got)
	}
	got = editOK(t, f, p, map[string]any{"action": "add_method", "symbol": "Box", "new_code": "def close(self):\n    return 0"})
	if !strings.Contains(got, "        return n\n    def close(self):\n        return 0\n") {
		t.Fatalf("python member:\n%s", got)
	}
}
