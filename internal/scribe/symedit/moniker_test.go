package symedit

import (
	"strings"
	"testing"
)

const monikerGo = `package p

func init() {
	first()
}

func init() {
	second()
}

type Server struct{}

func (s *Server) Start() {
	run()
}
`

const monikerTS = `export class Greeter {
  greet(): string { return "hi" }
}

export function make(a: string): void;
export function make(a: any) {
  console.log("make")
}
`

func TestParseMonikerForms(t *testing.T) {
	cases := []struct {
		in   string
		ok   bool
		want moniker
	}{
		{"internal/app/server.go#method:Server.Start", true, moniker{path: "internal/app/server.go", kind: "method", qual: "Server.Start"}},
		{"web/app.ts#function:init~2", true, moniker{path: "web/app.ts", kind: "function", qual: "init", ord: 2}},
		{"  p.go#struct:Server  ", true, moniker{path: "p.go", kind: "struct", qual: "Server"}},
		{"ParseList", false, moniker{}},
		{"Server.Start", false, moniker{}},
		{"class Foo", false, moniker{}},
		{"", false, moniker{}},
	}
	for _, c := range cases {
		got, ok := parseMoniker(c.in)
		if ok != c.ok {
			t.Fatalf("parseMoniker(%q) ok=%v, want %v", c.in, ok, c.ok)
		}
		if ok && got != c.want {
			t.Fatalf("parseMoniker(%q) = %+v, want %+v", c.in, got, c.want)
		}
		if ok && got.String() != strings.TrimSpace(c.in) {
			t.Fatalf("round trip of %q gave %q", c.in, got.String())
		}
	}
}

func TestGoMonikersFromOutline(t *testing.T) {
	defs, err := Outline("internal/p.go", monikerGo)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"internal/p.go#function:init~1":     true,
		"internal/p.go#function:init~2":     true,
		"internal/p.go#struct:Server":       true,
		"internal/p.go#method:Server.Start": true,
	}
	if len(defs) != len(want) {
		t.Fatalf("got %d definitions, want %d: %+v", len(defs), len(want), defs)
	}
	for _, d := range defs {
		if !want[d.Moniker] {
			t.Fatalf("unexpected moniker %q for %s", d.Moniker, d.Name)
		}
	}
}

func TestGoMonikerEditsOnlyTheNamedDuplicate(t *testing.T) {
	f := newFixture(t)
	p := f.write("p.go", monikerGo)
	mustOK(t, f.run(map[string]any{"path": "p.go", "symbol": "p.go#function:init~2",
		"action": "replace_body", "new_code": "\tthird()"}))
	got := f.read(p)
	if !strings.Contains(got, "first()") || !strings.Contains(got, "third()") || strings.Contains(got, "second()") {
		t.Fatalf("the second init was not the one edited:\n%s", got)
	}
}

func TestGoMonikerSurvivesEditsElsewhere(t *testing.T) {
	before, err := Outline("internal/p.go", monikerGo)
	if err != nil {
		t.Fatal(err)
	}
	var startBefore string
	for _, d := range before {
		if d.Name == "Start" {
			startBefore = d.Moniker
		}
	}
	// A new declaration above Server.Start moves its lines but not its moniker.
	edited := strings.Replace(monikerGo, "type Server struct{}", "func added() {}\n\ntype Server struct{}", 1)
	after, err := Outline("internal/p.go", edited)
	if err != nil {
		t.Fatal(err)
	}
	var startAfter string
	var startLine, startLineBefore int
	for _, d := range after {
		if d.Name == "Start" {
			startAfter, startLine = d.Moniker, d.Line
		}
	}
	for _, d := range before {
		if d.Name == "Start" {
			startLineBefore = d.Line
		}
	}
	if startAfter != startBefore {
		t.Fatalf("moniker changed from %q to %q", startBefore, startAfter)
	}
	if startLine == startLineBefore {
		t.Fatalf("test did not move the symbol: line %d in both", startLine)
	}

	f := newFixture(t)
	p := f.write("internal/p.go", edited)
	mustOK(t, f.run(map[string]any{"path": "internal/p.go", "symbol": startBefore,
		"action": "replace_body", "new_code": "\tstopped()"}))
	if got := f.read(p); !strings.Contains(got, "stopped()") || strings.Contains(got, "run()") {
		t.Fatalf("moniker from before the edit did not resolve after it:\n%s", got)
	}
}

func TestMonikerFailsWithCandidatesNeverGuesses(t *testing.T) {
	f := newFixture(t)
	f.write("internal/p.go", monikerGo)

	// Wrong path: the edit targets this file, the moniker names another.
	res := f.run(map[string]any{"path": "internal/p.go", "symbol": "other/p.go#method:Server.Start",
		"action": "replace_body", "new_code": "\tx()"})
	mustErr(t, res, "names other/p.go, but this edit targets internal/p.go")

	// A third init does not exist. The error lists the two that do, rather
	// than picking one.
	res = f.run(map[string]any{"path": "internal/p.go", "symbol": "internal/p.go#function:init~3",
		"action": "replace_body", "new_code": "\tx()"})
	mustErr(t, res, "no definition has moniker")
	mustErr(t, res, "internal/p.go#function:init~1, internal/p.go#function:init~2")

	// A bare moniker for a name declared twice asks for an ordinal.
	res = f.run(map[string]any{"path": "internal/p.go", "symbol": "internal/p.go#function:init",
		"action": "replace_body", "new_code": "\tx()"})
	mustErr(t, res, "matches 2 definitions")
	mustErr(t, res, "Add an ordinal to pick one")

	// The wrong kind does not match the method of that name.
	res = f.run(map[string]any{"path": "internal/p.go", "symbol": "internal/p.go#function:Server.Start",
		"action": "replace_body", "new_code": "\tx()"})
	mustErr(t, res, "Monikers in this file: internal/p.go#function:init~1, internal/p.go#function:init~2, internal/p.go#struct:Server, internal/p.go#method:Server.Start")
}

func TestTypeScriptMonikers(t *testing.T) {
	defs, err := Outline("src/greet.ts", monikerTS)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, d := range defs {
		got[d.Moniker] = true
	}
	for _, want := range []string{
		"src/greet.ts#class:Greeter",
		"src/greet.ts#method:Greeter.greet",
		"src/greet.ts#function:make~1",
		"src/greet.ts#function:make~2",
	} {
		if !got[want] {
			t.Fatalf("missing moniker %q in %v", want, got)
		}
	}
}

func TestTypeScriptMonikerEdits(t *testing.T) {
	f := newFixture(t)
	p := f.write("src/greet.ts", monikerTS)
	mustOK(t, f.run(map[string]any{"path": "src/greet.ts", "symbol": "src/greet.ts#method:Greeter.greet",
		"action": "replace_body", "new_code": "return \"bye\""}))
	mustOK(t, f.run(map[string]any{"path": "src/greet.ts", "symbol": "src/greet.ts#function:make~2",
		"action": "replace_body", "new_code": "  console.log(\"overload\")"}))
	got := f.read(p)
	if !strings.Contains(got, `return "bye"`) || !strings.Contains(got, `console.log("overload")`) {
		t.Fatalf("monikers did not reach their definitions:\n%s", got)
	}
	if !strings.Contains(got, "export function make(a: string): void;") {
		t.Fatalf("the overload signature was touched:\n%s", got)
	}
}
