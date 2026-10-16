package symedit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetExtendsPython(t *testing.T) {
	f := newFixture(t)
	p := f.write("dog.py", "class Dog(Animal):\n    def bark(self):\n        return 1\n")
	got := editOK(t, f, p, map[string]any{"action": "set_extends", "symbol": "Dog", "new_code": "Base, Mixin"})
	if !strings.Contains(got, "class Dog(Base, Mixin):") {
		t.Fatalf("bases not replaced:\n%s", got)
	}
	got = editOK(t, f, p, map[string]any{"action": "set_extends", "symbol": "Dog", "new_code": ""})
	if !strings.Contains(got, "class Dog:\n") {
		t.Fatalf("bases not removed:\n%s", got)
	}
	got = editOK(t, f, p, map[string]any{"action": "set_extends", "symbol": "Dog", "new_code": "Animal"})
	if !strings.Contains(got, "class Dog(Animal):\n") {
		t.Fatalf("base not added to a class without one:\n%s", got)
	}
}

func TestSetExtendsTypeScriptAndJava(t *testing.T) {
	f := newFixture(t)
	ts := f.write("box.ts", "class Box<T extends Base> {\n  open() {\n    return 1;\n  }\n}\n")
	got := editOK(t, f, ts, map[string]any{"action": "set_extends", "symbol": "Box", "new_code": "Other"})
	if !strings.Contains(got, "class Box<T extends Base> extends Other {") {
		t.Fatalf("extends not added after type parameters:\n%s", got)
	}
	got = editOK(t, f, ts, map[string]any{"action": "set_extends", "symbol": "Box", "new_code": "Third"})
	if !strings.Contains(got, "class Box<T extends Base> extends Third {") {
		t.Fatalf("extends not replaced:\n%s", got)
	}
	got = editOK(t, f, ts, map[string]any{"action": "set_extends", "symbol": "Box", "new_code": ""})
	if !strings.Contains(got, "class Box<T extends Base> {\n") {
		t.Fatalf("extends not removed:\n%s", got)
	}

	jv := f.write("Dog.java", "public class Dog extends Animal implements Pet {\n    void run() {}\n}\n")
	got = editOK(t, f, jv, map[string]any{"action": "set_extends", "symbol": "Dog", "new_code": "Cat"})
	if !strings.Contains(got, "public class Dog extends Cat implements Pet {") {
		t.Fatalf("java base not replaced before implements:\n%s", got)
	}
}

func TestSetExtendsRefusesFunctionsAndUnsupportedLanguages(t *testing.T) {
	f := newFixture(t)
	p := f.write("f.ts", "export function load() {\n  return 1;\n}\n")
	res := f.run(map[string]any{"path": p, "action": "set_extends", "symbol": "load", "new_code": "Base"})
	if !res.IsError || !strings.Contains(res.Output, "set_extends cannot target load") {
		t.Fatalf("function accepted: %s", res.Output)
	}
	g := f.write("g.go", "package g\n\ntype T struct{}\n")
	res = f.run(map[string]any{"path": g, "action": "set_extends", "symbol": "T", "new_code": "Base"})
	if !res.IsError || !strings.Contains(res.Output, "set_extends cannot target T") {
		t.Fatalf("go accepted: %s", res.Output)
	}
}

func TestCreateFileMakesNewSourceFile(t *testing.T) {
	f := newFixture(t)
	res := f.run(map[string]any{"path": "pkg/new.go", "action": "create_file", "new_code": "package pkg\n\nfunc F() int {\n\treturn 1\n}\n"})
	if res.IsError {
		t.Fatalf("create_file failed: %s", res.Output)
	}
	b, err := os.ReadFile(filepath.Join(f.dir, "pkg", "new.go"))
	if err != nil || !strings.Contains(string(b), "func F() int") {
		t.Fatalf("file not written: %v %q", err, b)
	}
	again := f.run(map[string]any{"path": "pkg/new.go", "action": "create_file", "new_code": "package pkg\n"})
	if !again.IsError || !strings.Contains(again.Output, "already exists") {
		t.Fatalf("second create should refuse: %s", again.Output)
	}
}

func TestCreateFileValidatesAndPreviews(t *testing.T) {
	f := newFixture(t)
	bad := f.run(map[string]any{"path": "bad.go", "action": "create_file", "new_code": "package x\n\nfunc (\n"})
	if !bad.IsError || f.exists("bad.go") {
		t.Fatalf("broken Go should not be created: %s", bad.Output)
	}
	prev := f.run(map[string]any{"path": "p.ts", "action": "create_file", "new_code": "export const a = 1;\n", "apply": false})
	if prev.IsError || f.exists("p.ts") || !strings.Contains(prev.Output, "Preview, not written") {
		t.Fatalf("preview should not write: %s", prev.Output)
	}
	mixed := f.run(map[string]any{"path": "m.ts", "edits": []map[string]any{
		{"action": "create_file", "new_code": "export const a = 1;\n"},
		{"action": "insert_text", "index": 0, "new_code": "// top\n"},
	}})
	if !mixed.IsError || f.exists("m.ts") {
		t.Fatalf("create_file combined with another op should refuse: %s", mixed.Output)
	}
}

func (f *fixture) exists(name string) bool {
	_, err := os.Stat(filepath.Join(f.dir, name))
	return err == nil
}
