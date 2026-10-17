package symedit

import (
	"strings"
	"testing"
)

const tsModifierSrc = `class Box {
  public open() {
    return 1;
  }
}
`

func TestModifierSetStaticTypeScript(t *testing.T) {
	f := newFixture(t)
	p := f.write("box.ts", tsModifierSrc)
	got := editOK(t, f, p, map[string]any{"action": "set_static", "symbol": "Box.open"})
	if !strings.Contains(got, "  public static open() {") {
		t.Fatalf("static must follow the access modifier:\n%s", got)
	}
	again := f.run(map[string]any{"path": p, "action": "set_static", "symbol": "Box.open"})
	if again.IsError || !strings.Contains(again.Output, "already") {
		t.Fatalf("second set_static should be a no-op: %s", again.Output)
	}
	got = editOK(t, f, p, map[string]any{"action": "set_static", "symbol": "Box.open", "new_code": "false"})
	if !strings.Contains(got, "  public open() {") || strings.Contains(got, "static") {
		t.Fatalf("static not removed:\n%s", got)
	}
	bare := f.write("bare.ts", "class Bare {\n  run() {\n    return 1;\n  }\n}\n")
	got = editOK(t, f, bare, map[string]any{"action": "set_static", "symbol": "Bare.run"})
	if !strings.Contains(got, "  static run() {") {
		t.Fatalf("method without access modifier:\n%s", got)
	}
}

func TestModifierSetStaticRejectsClassAndPython(t *testing.T) {
	f := newFixture(t)
	p := f.write("box.ts", tsModifierSrc)
	res := f.run(map[string]any{"path": p, "action": "set_static", "symbol": "class Box"})
	if !res.IsError || !strings.Contains(res.Output, "Valid targets") {
		t.Fatalf("set_static on a class should be refused: %s", res.Output)
	}
	py := f.write("item.py", "class Item:\n    def run(self):\n        return 1\n")
	res = f.run(map[string]any{"path": py, "action": "set_static", "symbol": "Item.run"})
	if !res.IsError || !strings.Contains(res.Output, "staticmethod") {
		t.Fatalf("set_static in Python should point at add_decorator: %s", res.Output)
	}
}

func TestModifierPythonDecorators(t *testing.T) {
	f := newFixture(t)
	p := f.write("item.py", "class Item:\n    def run(self):\n        return 1\n")
	got := editOK(t, f, p, map[string]any{"action": "add_decorator", "symbol": "Item.run", "new_code": "@staticmethod"})
	if !strings.Contains(got, "    @staticmethod\n    def run(self):") {
		t.Fatalf("decorator not added above the method at its indent:\n%s", got)
	}
	again := f.run(map[string]any{"path": p, "action": "add_decorator", "symbol": "Item.run", "new_code": "staticmethod"})
	if again.IsError || !strings.Contains(again.Output, "already") {
		t.Fatalf("adding a present decorator should be a no-op: %s", again.Output)
	}
	got = editOK(t, f, p, map[string]any{"action": "remove_decorator", "symbol": "Item.run", "new_code": "@staticmethod()"})
	if strings.Contains(got, "@staticmethod") || !strings.Contains(got, "    def run(self):") {
		t.Fatalf("decorator not removed:\n%s", got)
	}
	miss := f.run(map[string]any{"path": p, "action": "remove_decorator", "symbol": "Item.run", "new_code": "dataclass"})
	if !miss.IsError || !strings.Contains(miss.Output, "@dataclass") {
		t.Fatalf("removing an absent decorator should say so: %s", miss.Output)
	}
}

func TestModifierTypeScriptDecoratorAboveClass(t *testing.T) {
	f := newFixture(t)
	p := f.write("svc.ts", "export class Svc {\n  run() {\n    return 1;\n  }\n}\n")
	got := editOK(t, f, p, map[string]any{"action": "add_decorator", "symbol": "class Svc", "new_code": "@Injectable()"})
	if !strings.Contains(got, "@Injectable()\nexport class Svc {") {
		t.Fatalf("decorator not above the class:\n%s", got)
	}
}

func TestModifierAccessorsAndConstructor(t *testing.T) {
	f := newFixture(t)
	p := f.write("box.ts", tsModifierSrc)
	got := editOK(t, f, p, map[string]any{"action": "add_getter", "symbol": "class Box", "new_code": "get size(): number {\n  return 2;\n}"})
	if !strings.Contains(got, "  get size(): number {") {
		t.Fatalf("getter not added to the class:\n%s", got)
	}
	got = editOK(t, f, p, map[string]any{"action": "add_constructor", "symbol": "class Box", "new_code": "constructor(n: number) {\n  this.count = n;\n}"})
	if !strings.Contains(got, "  constructor(n: number) {") {
		t.Fatalf("constructor not added:\n%s", got)
	}
}
