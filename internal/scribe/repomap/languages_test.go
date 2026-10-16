package repomap

import (
	"strings"
	"testing"
)

// findLine returns the 1-based number of the first line containing sub.
func findLine(t *testing.T, src, sub string) int {
	t.Helper()
	for i, l := range strings.Split(src, "\n") {
		if strings.Contains(l, sub) {
			return i + 1
		}
	}
	t.Fatalf("no line contains %q", sub)
	return 0
}

// endLine returns the first line at or after from (1-based) that equals exact.
func endLine(t *testing.T, src string, from int, exact string) int {
	t.Helper()
	lines := strings.Split(src, "\n")
	for i := from - 1; i < len(lines); i++ {
		if lines[i] == exact {
			return i + 1
		}
	}
	t.Fatalf("no line %q after %d", exact, from)
	return 0
}

type wantSym struct {
	name, kind, parent string
	exported           bool
	line, end          int
}

func extractFor(t *testing.T, file, src string) []Symbol {
	t.Helper()
	fi := parseSource(file, []byte(src))
	if fi == nil {
		t.Fatalf("no language for %s", file)
	}
	return fi.Symbols
}

func checkSyms(t *testing.T, got []Symbol, want []wantSym) {
	t.Helper()
	byName := map[string]Symbol{}
	for _, s := range got {
		byName[s.Name] = s
	}
	for _, w := range want {
		s, ok := byName[w.name]
		if !ok {
			t.Errorf("symbol %s not found; got %+v", w.name, got)
			continue
		}
		if s.Kind != w.kind || s.Parent != w.parent || s.Exported != w.exported || s.Line != w.line || s.EndLine != w.end {
			t.Errorf("%s: got kind=%s parent=%q exported=%v lines=%d-%d; want kind=%s parent=%q exported=%v lines=%d-%d",
				w.name, s.Kind, s.Parent, s.Exported, s.Line, s.EndLine, w.kind, w.parent, w.exported, w.line, w.end)
		}
	}
}

func TestRubyBlocks(t *testing.T) {
	src := `class Foo
  def bar(x)
    x.each do |y|
      puts y if y
    end
  end

  def baz = 42

  def self.make
    new
  end
end
`
	syms := extractFor(t, "lib/foo.rb", src)
	checkSyms(t, syms, []wantSym{
		{"Foo", "class", "", true, 1, 13},
		{"bar", "method", "Foo", true, 2, 6},
		{"baz", "method", "Foo", true, 8, 8},
		{"make", "method", "Foo", true, 10, 12},
	})
}

func TestLuaBlocksAndDottedNames(t *testing.T) {
	src := `local M = {}
function M.greet(name)
  if name then
    return "end" .. name
  end
  for i = 1, 2 do print(i) end
end
local function helper()
  return 1
end
return M
`
	syms := extractFor(t, "mod.lua", src)
	checkSyms(t, syms, []wantSym{
		{"greet", "function", "M", true, 2, 7},
		{"helper", "function", "", false, 8, 10},
	})
}

func TestElixirDoEnd(t *testing.T) {
	src := `defmodule Greeter do
  def hello(name) do
    if name, do: "hi", else: "yo"
  end

  defp secret, do: :x
end
`
	syms := extractFor(t, "lib/greeter.ex", src)
	checkSyms(t, syms, []wantSym{
		{"Greeter", "module", "", true, 1, 7},
		{"hello", "method", "Greeter", true, 2, 4},
		{"secret", "method", "Greeter", false, 6, 6},
	})
}

func TestJavaMethodsAndStrings(t *testing.T) {
	src := `package p;

public class Box {
    private int n;

    public int get() {
        String s = "}";
        return n;
    }

    void set(int v) {
        n = v;
    }
}
`
	syms := extractFor(t, "Box.java", src)
	checkSyms(t, syms, []wantSym{
		{"Box", "class", "", true, 3, 14},
		{"get", "method", "Box", true, 6, 9},
		{"set", "method", "Box", false, 11, 13},
	})
}

func TestCFunctionsAndStructs(t *testing.T) {
	src := `#include "util.h"

struct point {
    int x;
};

static int add(int a, int b)
{
    if (a > b) {
        return a;
    }
    return a + b;
}

int main(void) {
    return add(1, 2);
}
`
	syms := extractFor(t, "main.c", src)
	checkSyms(t, syms, []wantSym{
		{"point", "struct", "", true, 3, 5},
		{"add", "function", "", false, 7, 13},
		{"main", "function", "", true, 15, 17},
	})
	for _, s := range syms {
		if s.Name == "if" {
			t.Errorf("control statement recorded as a function")
		}
	}
}

func TestCppMethodsOutOfLine(t *testing.T) {
	src := `class Counter {
public:
  int value() const { return n_; }
  void bump();
  int n_;
};

void Counter::bump() {
  n_++;
}
`
	syms := extractFor(t, "counter.cc", src)
	checkSyms(t, syms, []wantSym{
		{"Counter", "class", "", true, 1, 6},
		{"value", "method", "Counter", true, 3, 3},
		{"bump", "method", "Counter", true, 8, 10},
	})
}

func TestShellFunctions(t *testing.T) {
	src := `#!/bin/sh
# a comment with } brace
deploy() {
  echo "}"
  if true; then
    echo ok
  fi
}
function cleanup {
  rm -rf /tmp/x
}
`
	syms := extractFor(t, "deploy.sh", src)
	checkSyms(t, syms, []wantSym{
		{"deploy", "function", "", true, 3, 8},
		{"cleanup", "function", "", true, 9, 11},
	})
}

func TestKotlinAndSwift(t *testing.T) {
	kt := `class Repo {
    fun load(id: Int): String {
        return "}"
    }
    private fun hidden() = 1
}
`
	checkSyms(t, extractFor(t, "Repo.kt", kt), []wantSym{
		{"Repo", "class", "", true, 1, 6},
		{"load", "method", "Repo", true, 2, 4},
		{"hidden", "method", "Repo", false, 5, 5},
	})
	sw := `public struct Model {
    public func save() -> Bool {
        return true
    }
    func internalOnly() {}
}
`
	checkSyms(t, extractFor(t, "Model.swift", sw), []wantSym{
		{"Model", "struct", "", true, 1, 6},
		{"save", "method", "Model", true, 2, 4},
		{"internalOnly", "method", "Model", false, 5, 5},
	})
}

func TestCSharpAllmanBraces(t *testing.T) {
	src := `public class Store
{
    public void Save(string s)
    {
        var t = "}";
    }
}
`
	checkSyms(t, extractFor(t, "Store.cs", src), []wantSym{
		{"Store", "class", "", true, 1, 7},
		{"Save", "method", "Store", true, 3, 6},
	})
}

func TestPHPClassMethods(t *testing.T) {
	src := `<?php
class Repo {
    public function find($id) {
        return "}";
    }
    private function hide() {}
}
`
	checkSyms(t, extractFor(t, "repo.php", src), []wantSym{
		{"Repo", "class", "", true, 2, 7},
		{"find", "method", "Repo", true, 3, 5},
		{"hide", "method", "Repo", false, 6, 6},
	})
}

func TestZigStructMethods(t *testing.T) {
	src := `const std = @import("std");
pub const Stack = struct {
    items: u32,
    pub fn push(self: *Stack) void {
        _ = self;
    }
};
pub fn main() void {}
`
	checkSyms(t, extractFor(t, "stack.zig", src), []wantSym{
		{"Stack", "struct", "", true, 2, 7},
		{"push", "method", "Stack", true, 4, 6},
		{"main", "function", "", true, 8, 8},
	})
}

func TestRustLifetimesAndRawStrings(t *testing.T) {
	src := `pub struct View<'a> {
    s: &'a str,
}

impl<'a> View<'a> {
    pub fn text(&self) -> &'a str {
        let open = '{';
        let raw = r#"}"#;
        self.s
    }
}
`
	syms := extractFor(t, "view.rs", src)
	checkSyms(t, syms, []wantSym{
		{"View", "struct", "", true, 1, 3},
		{"text", "method", "View", true, 6, 10},
	})
}

func TestMaskBlanksCommentsAndStrings(t *testing.T) {
	src := []byte("a := \"}{\" // } comment\n/* block { */ b\n")
	lg := languages["go"]
	m := mask(src, &lg.lex, false)
	if len(m) != len(src) {
		t.Fatalf("length changed: %d -> %d", len(src), len(m))
	}
	if strings.Contains(string(m), "}") || strings.Contains(string(m), "{") {
		t.Errorf("braces survived masking: %q", m)
	}
	if strings.Count(string(m), "\n") != 2 {
		t.Errorf("newlines not preserved: %q", m)
	}
	kept := mask(src, &lg.lex, true)
	if !strings.Contains(string(kept), `"}{"`) {
		t.Errorf("keepStrings dropped the string contents: %q", kept)
	}
}

func TestImportSpecifiers(t *testing.T) {
	ts := "import { a } from './lib/x';\nimport './side';\nconst y = require('../z');\n// import nope from 'commented';\n"
	imps := extractImports(languages["typescript"], []byte(ts))
	want := map[string]bool{"./lib/x": true, "./side": true, "../z": true}
	got := map[string]bool{}
	for _, i := range imps {
		got[i.Spec] = true
	}
	for w := range want {
		if !got[w] {
			t.Errorf("missing import %q in %v", w, imps)
		}
	}
	if got["commented"] {
		t.Errorf("commented import was extracted")
	}
	py := "from app.core import helpers, util as u\nimport os.path\nfrom . import sibling\n"
	pimps := extractImports(languages["python"], []byte(py))
	found := map[string]rawImport{}
	for _, i := range pimps {
		found[i.Spec] = i
	}
	if _, ok := found["app.core"]; !ok {
		t.Errorf("from-import module missing: %v", pimps)
	}
	if got := found["app.core"].Names; len(got) != 2 || got[0] != "helpers" || got[1] != "util" {
		t.Errorf("from-import names = %v", got)
	}
	if _, ok := found["os.path"]; !ok {
		t.Errorf("plain import missing: %v", pimps)
	}
	if _, ok := found["."]; !ok {
		t.Errorf("relative import missing: %v", pimps)
	}
}

func TestMalformedInputDoesNotPanic(t *testing.T) {
	inputs := map[string]string{
		"a.py":   "def f(x)",          // header with no colon at end of file
		"b.py":   "class A\n  def g(", // unterminated header
		"c.rs":   "impl X {\nfn f(",   // unterminated
		"d.c":    "int f(int a) {",    // unterminated body
		"e.rb":   "def f\n  if x\n",   // unclosed block
		"f.go":   "package p\nfunc (", // parse error
		"g.lua":  "function f(",       // unterminated
		"h.sh":   "f() {",             // unterminated
		"i.ts":   "`unterminated",     // open template literal
		"j.java": "class A { void f(", // truncated
	}
	for name, src := range inputs {
		if fi := parseSafe(name, []byte(src)); fi != nil {
			_ = fi.Symbols
		}
		// parseSource itself must not panic either; parseSafe is the net for
		// anything the scanners still miss.
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s panicked: %v", name, r)
				}
			}()
			parseSource(name, []byte(src))
		}()
	}
}
