package symedit

import (
	"path/filepath"
	"strings"
	"testing"
)

const bodySrc = `package demo

func Run() int {
	first := 1
	foo()
	bar()
	middle := 2
	foo()
	last := 3
	return first + middle + last
}
`

func TestReplaceInBodyRangeWithEndAnchor(t *testing.T) {
	f := newFixture(t)
	p := f.write("r.go", bodySrc)
	mustOK(t, f.run(map[string]any{"path": "r.go", "symbol": "Run", "action": "replace_in_body",
		"old_string": "\tfoo()\n\tbar()", "end_string": "\tmiddle := 2", "new_string": "\tbaz()"}))
	got := f.read(p)
	if !strings.Contains(got, "\tbaz()\n\tfoo()\n\tlast := 3") {
		t.Fatalf("range not replaced between the anchors:\n%s", got)
	}
	if strings.Contains(got, "bar()") || strings.Contains(got, "middle := 2") {
		t.Fatalf("anchors or the span between them survived:\n%s", got)
	}
}

func TestReplaceInBodyRangeRefusesAmbiguousEnd(t *testing.T) {
	f := newFixture(t)
	p := f.write("r.go", bodySrc)
	res := f.run(map[string]any{"path": "r.go", "symbol": "Run", "action": "replace_in_body",
		"old_string": "first := 1", "end_string": "foo()", "new_string": "x"})
	mustErr(t, res, "end_string matches 2 places")
	if f.read(p) != bodySrc {
		t.Fatal("refused edit wrote to disk")
	}
}

func TestReplaceInBodyRangeEndMustFollowStart(t *testing.T) {
	f := newFixture(t)
	f.write("r.go", bodySrc)
	res := f.run(map[string]any{"path": "r.go", "symbol": "Run", "action": "replace_in_body",
		"old_string": "middle := 2", "end_string": "first := 1", "new_string": "x"})
	mustErr(t, res, "must come after old_string")
}

func TestReplaceInBodyAmbiguityHintsAtAnchorPair(t *testing.T) {
	f := newFixture(t)
	f.write("r.go", bodySrc)
	res := f.run(map[string]any{"path": "r.go", "symbol": "Run", "action": "replace_in_body",
		"old_string": "foo()", "new_string": "x"})
	mustErr(t, res, "matches 2 places")
	if !strings.Contains(res.Output, "end_string") {
		t.Fatalf("ambiguity should name the anchor pair: %s", res.Output)
	}
}

func TestReplaceInBodyIndentFuzzyPassReindents(t *testing.T) {
	f := newFixture(t)
	p := f.write("r.go", bodySrc)
	// The anchor is written with spaces; the file uses a tab. Pass 2 finds it
	// and the replacement takes the file's indentation.
	mustOK(t, f.run(map[string]any{"path": "r.go", "symbol": "Run", "action": "replace_in_body",
		"old_string": "    first := 1", "new_string": "first := 9"}))
	got := f.read(p)
	if !strings.Contains(got, "\tfirst := 9\n") || strings.Contains(got, "first := 1") {
		t.Fatalf("indent-insensitive match did not replace with file indentation:\n%s", got)
	}
}

func TestReplaceInBodyWhitespaceFuzzyPass(t *testing.T) {
	f := newFixture(t)
	p := f.write("r.go", bodySrc)
	// Trailing spaces and extra inner spaces: only pass 3 matches.
	mustOK(t, f.run(map[string]any{"path": "r.go", "symbol": "Run", "action": "replace_in_body",
		"old_string": "middle  :=  2   \n", "new_string": "\tmiddle := 20\n"}))
	if got := f.read(p); !strings.Contains(got, "middle := 20") || strings.Contains(got, "middle := 2\n") {
		t.Fatalf("whitespace-insensitive match failed:\n%s", got)
	}
}

func TestReplaceInBodyExactHitStillWinsOverFuzzy(t *testing.T) {
	f := newFixture(t)
	p := f.write("r.go", bodySrc)
	// "last := 3" exists once exactly; the fuzzy passes are never consulted.
	mustOK(t, f.run(map[string]any{"path": "r.go", "symbol": "Run", "action": "replace_in_body",
		"old_string": "last := 3", "new_string": "last := 4"}))
	if got := f.read(p); !strings.Contains(got, "last := 4") {
		t.Fatalf("exact replace failed:\n%s", got)
	}
}

func TestPreviewReturnsDiffWithoutWriting(t *testing.T) {
	f := newFixture(t)
	p := f.write("r.go", bodySrc)
	no := false
	res := mustOK(t, f.run(map[string]any{"path": "r.go", "symbol": "Run", "action": "replace_in_body",
		"old_string": "last := 3", "new_string": "last := 99", "apply": no}))
	if f.read(p) != bodySrc {
		t.Fatal("preview wrote to disk")
	}
	if !strings.Contains(res.Output, "Preview, not written") || !strings.Contains(res.Diff, "+\tlast := 99") {
		t.Fatalf("preview output/diff wrong: %q\n%s", res.Output, res.Diff)
	}
	if len(f.trk.written) != 0 {
		t.Fatalf("preview called BeforeWrite: %v", f.trk.written)
	}
	// apply true (and absent) writes.
	mustOK(t, f.run(map[string]any{"path": "r.go", "symbol": "Run", "action": "replace_in_body",
		"old_string": "last := 3", "new_string": "last := 99", "apply": true}))
	if !strings.Contains(f.read(p), "last := 99") {
		t.Fatal("apply true did not write")
	}
}

func TestIdenticalReplaceIsExplicitNoop(t *testing.T) {
	f := newFixture(t)
	p := f.write("demo.go", goSrc)
	res := mustOK(t, f.run(map[string]any{"path": "demo.go", "symbol": "ParseList", "action": "replace",
		"new_code": "func ParseList(in string) []string {\n\tparts := strings.Split(in, \",\")\n\treturn parts\n}"}))
	if res.Summary != "no changes" || !strings.Contains(res.Output, "already present") {
		t.Fatalf("identical replace should report already present: %q / %q", res.Summary, res.Output)
	}
	if f.read(p) != goSrc {
		t.Fatal("no-op changed the file")
	}
}

func TestNoopInBatchWithRealChangeStillWrites(t *testing.T) {
	f := newFixture(t)
	p := f.write("demo.go", goSrc)
	res := mustOK(t, f.run(map[string]any{"path": "demo.go", "edits": []map[string]any{
		{"action": "add_import", "new_code": `"strings"`},
		{"action": "replace_in_body", "symbol": "ParseList", "old_string": "return parts", "new_string": "return parts[:0]"},
	}}))
	if res.Summary == "no changes" || !strings.Contains(f.read(p), "return parts[:0]") {
		t.Fatalf("batch with a real change was not written: %q", res.Summary)
	}
}

func TestOutlineListsMembersWithPositions(t *testing.T) {
	defs, err := Outline("demo.go", goSrc)
	if err != nil {
		t.Fatal(err)
	}
	var start, parse *Definition
	for i := range defs {
		switch {
		case defs[i].Name == "Start" && defs[i].Parent == "Server":
			start = &defs[i]
		case defs[i].Name == "ParseList":
			parse = &defs[i]
		}
	}
	if start == nil || parse == nil {
		t.Fatalf("outline missed definitions: %+v", defs)
	}
	if parse.Column != 6 || parse.Line != 23 || parse.EndLine != 26 {
		t.Fatalf("ParseList position = line %d col %d end %d", parse.Line, parse.Column, parse.EndLine)
	}
	if start.Line <= 0 || start.EndLine <= start.Line {
		t.Fatalf("method span wrong: %+v", start)
	}
}

func TestOutlineRefusesUnknownLanguage(t *testing.T) {
	if _, err := Outline(filepath.Join("notes", "readme.txt"), "hello"); err == nil {
		t.Fatal("expected an error for a file with no parser")
	}
}

func TestOutlineColumnsAreCharacters(t *testing.T) {
	// Two characters of é before the name are two bytes, so the column is
	// counted in characters, not bytes.
	src := "const é = 1;\n/* é */ function hello() { return 1 }\n"
	defs, err := Outline("m.js", src)
	if err != nil {
		t.Fatal(err)
	}
	var hello *Definition
	for i := range defs {
		if defs[i].Name == "hello" {
			hello = &defs[i]
		}
	}
	if hello == nil {
		t.Fatalf("hello not found: %+v", defs)
	}
	if hello.Column != 18 {
		t.Fatalf("column = %d, want 18 (characters before the name, plus one; bytes would give 19)", hello.Column)
	}
}
