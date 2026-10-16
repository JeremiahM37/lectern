package symedit

import (
	"strings"
	"testing"
)

func TestTypeScriptBatchReextractsBetweenOps(t *testing.T) {
	f := newFixture(t)
	p := f.write("greet.ts", tsSrc)
	mustOK(t, f.run(map[string]any{
		"path": "greet.ts",
		"edits": []map[string]any{
			{"action": "delete", "symbol": "Greeter.bye"},
			{"action": "replace_body", "symbol": "top", "new_code": "return 2;"},
			{"action": "insert_before", "symbol": "top", "new_code": "export const TWO = 2;"},
		},
	}))
	got := f.read(p)
	if strings.Contains(got, "bye()") || !strings.Contains(got, "return 2;") || !strings.Contains(got, "export const TWO = 2;\n\nexport function top") {
		t.Fatalf("batch result wrong:\n%s", got)
	}
}

func TestCRLFFileKeepsLineEndings(t *testing.T) {
	f := newFixture(t)
	src := strings.ReplaceAll(tsSrc, "\n", "\r\n")
	p := f.write("crlf.ts", src)
	mustOK(t, f.run(map[string]any{"path": "crlf.ts", "symbol": "top", "action": "replace_body", "new_code": "return 3;"}))
	got := f.read(p)
	if strings.Count(got, "\r\n") != strings.Count(got, "\n") || !strings.Contains(got, "return 3;") {
		t.Fatalf("CRLF not preserved or edit missing: %q", got)
	}
}

func TestPythonRenameLocalAndReplaceInBody(t *testing.T) {
	f := newFixture(t)
	p := f.write("report.py", pySrc)
	mustOK(t, f.run(map[string]any{
		"path": "report.py", "symbol": "Report.build", "action": "rename_local",
		"old_name": "total", "new_name": "subtotal",
	}))
	mustOK(t, f.run(map[string]any{
		"path": "report.py", "symbol": "build", "action": "replace_in_body",
		"old_string": "for i in items:", "new_string": "for i in sorted(items):",
	}))
	got := f.read(p)
	if !strings.Contains(got, "        subtotal = 0\n        for i in sorted(items):\n            subtotal += i\n        return subtotal") {
		t.Fatalf("python edits wrong:\n%s", got)
	}
	// Strings and comments are not renamed.
	f.write("s.py", "def f(x):\n    # x is the input\n    return 'x' + x\n")
	mustOK(t, f.run(map[string]any{"path": "s.py", "symbol": "f", "action": "rename_local", "old_name": "x", "new_name": "value"}))
	if got := f.read(f.dir + "/s.py"); !strings.Contains(got, "# x is the input") || !strings.Contains(got, "'x' + value") {
		t.Fatalf("strings or comments renamed:\n%s", got)
	}
}

// TestOtherLanguages checks that the repomap-backed path locates and replaces
// definitions in the non-brace languages, where bodies end with "end".
func TestOtherLanguages(t *testing.T) {
	cases := []struct {
		file, symbol, src, newCode, want string
	}{
		{"calc.rb", "Calc.add", "class Calc\n  def add(a, b)\n    a + b\n  end\nend\n",
			"def add(a, b)\n  a * b\nend", "  def add(a, b)\n    a * b\n  end\nend"},
		{"calc.lua", "add", "local M = {}\n\nfunction add(a, b)\n  return a + b\nend\n",
			"function add(a, b)\n  return a * b\nend", "function add(a, b)\n  return a * b\nend"},
		{"Calc.java", "Calc.add", "class Calc {\n    /** adds */\n    int add(int a, int b) {\n        return a + b;\n    }\n}\n",
			"int add(int a, int b) {\n    return a * b;\n}", "    int add(int a, int b) {\n        return a * b;\n    }\n}"},
		{"calc.ex", "Calc.add", "defmodule Calc do\n  def add(a, b) do\n    a + b\n  end\nend\n",
			"def add(a, b) do\n  a * b\nend", "  def add(a, b) do\n    a * b\n  end\nend"},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			f := newFixture(t)
			p := f.write(c.file, c.src)
			res := f.run(map[string]any{"path": c.file, "symbol": c.symbol, "action": "replace", "new_code": c.newCode})
			if res.IsError {
				t.Fatalf("replace failed: %s", res.Output)
			}
			if got := f.read(p); !strings.Contains(got, c.want) {
				t.Fatalf("want %q in:\n%s", c.want, got)
			}
		})
	}
}
