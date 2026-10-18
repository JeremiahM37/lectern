package symedit

import (
	"strings"
	"testing"
)

// rustCrate writes a small crate whose lib.rs declares util and shapes.
func rustCrate(f *refactorFixture) {
	f.write("src/lib.rs", "pub mod util;\npub mod shapes;\npub mod app;\npub mod net;\n")
	f.write("src/shapes.rs", "pub struct Shape;\n")
	f.write("src/net/mod.rs", "pub mod client;\n")
}

func TestRustMoveRewritesCrateUsePaths(t *testing.T) {
	f := newRefactorFixture(t)
	rustCrate(f)
	f.write("src/util.rs", "pub fn helper(n: i32) -> i32 {\n    n * 2\n}\n\npub fn keep() -> i32 {\n    1\n}\n")
	f.write("src/app.rs", "use crate::util::{helper, keep};\n\npub fn run() -> i32 {\n    helper(1) + keep()\n}\n")
	mustOK(t, f.run("move_symbol", map[string]any{"symbol": "helper", "from": "src/util.rs", "to": "src/shapes.rs"}))

	app := f.read("src/app.rs")
	if !strings.Contains(app, "use crate::shapes::helper;") {
		t.Fatalf("importer should name the new module:\n%s", app)
	}
	if !strings.Contains(app, "use crate::util::keep;") {
		t.Fatalf("unmoved name should stay on the old path:\n%s", app)
	}
	shapes := f.read("src/shapes.rs")
	if !strings.Contains(shapes, "pub fn helper(n: i32) -> i32") {
		t.Fatalf("destination must hold the item:\n%s", shapes)
	}
	if strings.Contains(f.read("src/util.rs"), "fn helper") {
		t.Fatalf("item still in source:\n%s", f.read("src/util.rs"))
	}
}

func TestRustMovePrivateItemBecomesPubAndImportsBack(t *testing.T) {
	f := newRefactorFixture(t)
	rustCrate(f)
	f.write("src/util.rs", "fn helper() -> i32 {\n    7\n}\n\npub fn keep() -> i32 {\n    helper() + 1\n}\n")
	mustOK(t, f.run("move_symbol", map[string]any{"symbol": "helper", "from": "src/util.rs", "to": "src/shapes.rs"}))

	util := f.read("src/util.rs")
	if !strings.Contains(util, "use crate::shapes::helper;") {
		t.Fatalf("source still uses helper, so it must import it back:\n%s", util)
	}
	if !strings.Contains(f.read("src/shapes.rs"), "pub fn helper() -> i32") {
		t.Fatalf("moved item must be pub to be reachable from the source module:\n%s", f.read("src/shapes.rs"))
	}
}

func TestRustMoveUsesSuperPathFromNestedModule(t *testing.T) {
	f := newRefactorFixture(t)
	rustCrate(f)
	f.write("src/util.rs", "pub fn helper() -> i32 {\n    2\n}\n")
	f.write("src/net/client.rs", "use super::super::util::helper;\n\npub fn go() -> i32 {\n    helper()\n}\n")
	mustOK(t, f.run("move_symbol", map[string]any{"symbol": "helper", "from": "src/util.rs", "to": "src/shapes.rs"}))

	client := f.read("src/net/client.rs")
	if !strings.Contains(client, "use super::super::shapes::helper;") {
		t.Fatalf("relative importer should keep its relative form:\n%s", client)
	}
}

func TestRustMoveRefusesPrivateDependency(t *testing.T) {
	f := newRefactorFixture(t)
	rustCrate(f)
	f.write("src/util.rs", "fn secret() -> i32 {\n    3\n}\n\npub fn helper() -> i32 {\n    secret()\n}\n")
	res := f.run("move_symbol", map[string]any{"symbol": "helper", "from": "src/util.rs", "to": "src/shapes.rs"})
	if !res.IsError || !strings.Contains(res.Output, "private") {
		t.Fatalf("expected refusal naming the private dependency, got %q", res.Output)
	}
	if !strings.Contains(f.read("src/util.rs"), "pub fn helper() -> i32") {
		t.Fatalf("refused move must not change the source")
	}
}

func TestRustMoveReportsUnreadableUse(t *testing.T) {
	f := newRefactorFixture(t)
	rustCrate(f)
	f.write("src/util.rs", "pub fn helper() -> i32 {\n    2\n}\n")
	f.write("src/app.rs", "use crate::util::{helper, other::Thing};\n\npub fn run() -> i32 {\n    helper()\n}\n")
	res := f.run("move_symbol", map[string]any{"symbol": "helper", "from": "src/util.rs", "to": "src/shapes.rs"})
	mustOK(t, res)
	if !strings.Contains(res.Output, "cannot read") || !strings.Contains(res.Output, "src/app.rs") {
		t.Fatalf("an unreadable use naming the item must be reported:\n%s", res.Output)
	}
	if !strings.Contains(f.read("src/app.rs"), "use crate::util::{helper, other::Thing};") {
		t.Fatalf("unreadable use must be left unchanged:\n%s", f.read("src/app.rs"))
	}
}

func TestRustMoveCarriesAttributesAndReportsUndeclaredModule(t *testing.T) {
	f := newRefactorFixture(t)
	rustCrate(f)
	f.write("src/util.rs", "#[derive(Debug)]\npub struct Point {\n    pub x: i32,\n}\n\npub fn keep() {}\n")
	res := f.run("move_symbol", map[string]any{"symbol": "Point", "from": "src/util.rs", "to": "src/geo/point.rs"})
	mustOK(t, res)
	geo := f.read("src/geo/point.rs")
	if !strings.Contains(geo, "#[derive(Debug)]\npub struct Point") {
		t.Fatalf("attribute must move with the struct:\n%s", geo)
	}
	if strings.Contains(f.read("src/util.rs"), "derive") {
		t.Fatalf("attribute left behind in source:\n%s", f.read("src/util.rs"))
	}
	if !strings.Contains(res.Output, "mod point;") {
		t.Fatalf("an undeclared destination module must be reported:\n%s", res.Output)
	}
}

func TestRustModuleOfPaths(t *testing.T) {
	cases := map[string]string{
		"src/lib.rs":      "src|",
		"src/a/mod.rs":    "src|a",
		"src/a/b.rs":      "src|a/b",
		"app/src/x/y.rs":  "app/src|x/y",
		"src/bin/tool.rs": "",
		"build.rs":        "",
		"tests/integ.rs":  "",
	}
	for rel, want := range cases {
		m, ok := rustModuleOf(rel)
		got := ""
		if ok {
			got = m.crate + "|" + strings.Join(m.path, "/")
		}
		if got != want {
			t.Errorf("rustModuleOf(%q) = %q, want %q", rel, got, want)
		}
	}
}

func TestRustMoveRelocatedSelfPathNeedsNoImport(t *testing.T) {
	f := newRefactorFixture(t)
	rustCrate(f)
	f.write("src/util.rs", "pub fn secret() -> i32 {\n    3\n}\n\npub fn helper() -> i32 {\n    self::secret()\n}\n")
	mustOK(t, f.run("move_symbol", map[string]any{"symbol": "helper", "from": "src/util.rs", "to": "src/shapes.rs"}))

	shapes := f.read("src/shapes.rs")
	if strings.Contains(shapes, "use crate::util::secret;") {
		t.Fatalf("self:: path became crate:: in the body, so no import is needed:\n%s", shapes)
	}
	if !strings.Contains(shapes, "crate::util::secret()") {
		t.Fatalf("self:: path should be rewritten as crate::util:\n%s", shapes)
	}
}

func TestRustMoveReportsBarePathUse(t *testing.T) {
	f := newRefactorFixture(t)
	rustCrate(f)
	f.write("src/util.rs", "pub fn helper() -> i32 {\n    2\n}\n")
	f.write("src/app.rs", "use util::helper;\n\npub fn run() -> i32 {\n    helper()\n}\n")
	res := f.run("move_symbol", map[string]any{"symbol": "helper", "from": "src/util.rs", "to": "src/shapes.rs"})
	mustOK(t, res)
	if !strings.Contains(res.Output, "not crate::, self:: or super::") {
		t.Fatalf("a bare-path use of the item must be reported as unchecked:\n%s", res.Output)
	}
	if !strings.Contains(f.read("src/app.rs"), "use util::helper;") {
		t.Fatalf("bare path must be left unchanged")
	}
}
