package symedit

import (
	"strings"
	"testing"
)

const cGeom = "#pragma once\n\nstruct Point {\n    int x;\n    int y;\n};\n\nstruct Size {\n    int w;\n};\n"

func TestCMoveStructToNewHeaderAddsIncludeAndKeepsOldWhileNeeded(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("geom.h", cGeom)
	f.write("main.c", "#include \"geom.h\"\n\nint main(void) {\n    struct Point p = {1, 2};\n    struct Size s = {3};\n    return p.x + s.w;\n}\n")
	f.write("other.c", "#include \"geom.h\"\n\nint other(void) {\n    struct Size s = {1};\n    return s.w;\n}\n")
	mustOK(t, f.run("move_symbol", map[string]any{"symbol": "Point", "from": "geom.h", "to": "point.h"}))

	main := f.read("main.c")
	if !strings.Contains(main, "#include \"geom.h\"\n#include \"point.h\"\n") {
		t.Fatalf("importer should gain the new header right after the old include:\n%s", main)
	}
	other := f.read("other.c")
	if strings.Contains(other, "point.h") {
		t.Fatalf("file that does not use Point must not change:\n%s", other)
	}
	geom := f.read("geom.h")
	if strings.Contains(geom, "\n\n\n") {
		t.Fatalf("removing the struct must not leave a double blank line:\n%q", geom)
	}
	if strings.Contains(geom, "struct Point") || !strings.Contains(geom, "struct Size") {
		t.Fatalf("source should keep Size and lose Point:\n%s", geom)
	}
	point := f.read("point.h")
	if !strings.Contains(point, "#pragma once") || !strings.Contains(point, "struct Point {") {
		t.Fatalf("new header must hold the struct:\n%s", point)
	}
}

func TestCMoveDropsOldIncludeWhenNothingLeftIsUsed(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("geom.h", cGeom)
	f.write("only.c", "#include \"geom.h\"\n\nint f(void) {\n    struct Point p = {1, 2};\n    return p.x;\n}\n")
	mustOK(t, f.run("move_symbol", map[string]any{"symbol": "Point", "from": "geom.h", "to": "point.h"}))

	only := f.read("only.c")
	if strings.Contains(only, "geom.h") {
		t.Fatalf("old include is no longer needed and should go:\n%s", only)
	}
	if !strings.Contains(only, "#include \"point.h\"") {
		t.Fatalf("new include must be added:\n%s", only)
	}
}

func TestCMoveFunctionBetweenSourceFilesLeavesIncludesAlone(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("mathx.h", "#pragma once\nint twice(int n);\nint quad(int n);\n")
	f.write("mathx.c", "#include \"mathx.h\"\n\nint twice(int n) {\n    return 2 * n;\n}\n\nint quad(int n) {\n    return twice(twice(n));\n}\n")
	f.write("user.c", "#include \"mathx.h\"\n\nint use(void) {\n    return twice(3);\n}\n")
	res := f.run("move_symbol", map[string]any{"symbol": "twice", "from": "mathx.c", "to": "mathx2.c"})
	mustOK(t, res)
	if !strings.Contains(f.read("mathx2.c"), "int twice(int n) {") {
		t.Fatalf("definition must land in the destination:\n%s", f.read("mathx2.c"))
	}
	if strings.Contains(f.read("mathx.c"), "int twice(int n) {") {
		t.Fatalf("definition still in source")
	}
	if strings.Contains(f.read("user.c"), "mathx2") {
		t.Fatalf("callers reach the function through the existing declaration; their includes must not change")
	}
	if !strings.Contains(res.Output, "still uses twice") {
		t.Fatalf("source still calling the moved function must be reported:\n%s", res.Output)
	}
}

func TestCMoveFunctionIntoHeaderNeedsInline(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("mathx.c", "int twice(int n) {\n    return 2 * n;\n}\n")
	res := f.run("move_symbol", map[string]any{"symbol": "twice", "from": "mathx.c", "to": "twice.h"})
	if !res.IsError || !strings.Contains(res.Output, "inline") {
		t.Fatalf("a non-inline definition must not enter a header, got %q", res.Output)
	}
	if f.exists("twice.h") {
		t.Fatalf("refused move must not create the header")
	}
}

func TestCMoveFunctionIntoHeaderWhenInline(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("mathx.c", "inline int twice(int n) {\n    return 2 * n;\n}\n")
	f.write("main.c", "#include \"mathx.c\"\n")
	mustOK(t, f.run("move_symbol", map[string]any{"symbol": "twice", "from": "mathx.c", "to": "twice.h"}))
	if !strings.Contains(f.read("twice.h"), "inline int twice") {
		t.Fatalf("inline definition should move into the header:\n%s", f.read("twice.h"))
	}
}

func TestCMoveRefusesSourceOnlyDependency(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("mathx.h", "#pragma once\n")
	f.write("mathx.c", "#include \"mathx.h\"\n\nstatic int base(void) {\n    return 1;\n}\n\nint twice(int n) {\n    return base() * n;\n}\n")
	res := f.run("move_symbol", map[string]any{"symbol": "twice", "from": "mathx.c", "to": "mathx2.c"})
	if !res.IsError || !strings.Contains(res.Output, "no header declares") {
		t.Fatalf("expected refusal for a source-only dependency, got %q", res.Output)
	}
}
