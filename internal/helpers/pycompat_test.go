//go:build unix

package helpers

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pyEval runs a Python expression over each input (bound to x) and returns
// what it printed, one line per input.
func pyEval(t *testing.T, expr string, inputs []string) []string {
	t.Helper()
	requirePython(t)
	raw, _ := json.Marshal(inputs)
	script := "import json,os,sys,posixpath\nfor x in json.loads(sys.argv[1]):\n try: r=" + expr +
		"\n except Exception as e: r='!'+type(e).__name__+': '+str(e)\n print(json.dumps(r))"
	res := runPy(t, runSpec{}, script, string(raw))
	if res.rc != 0 {
		t.Fatalf("python: %s", res.stderr)
	}
	return strings.Split(strings.TrimSuffix(res.stdout, "\n"), "\n")
}

func TestPyJSONMatchesPython(t *testing.T) {
	docs := []string{
		`{"b": 1, "a": [1.0, 2.5, -0.0, 1e16, 1e15, 0.0001, 0.00001, 123456789012345678901234567890, 1E400]}`,
		`{"a": 1, "b": 2, "a": 3}`,
		`["\u00e9", "\ud83d\ude00", "\ud800", "tab\t\"q\"\\", "\u007f\u0001", "caf\u00e9 ☕"]`,
		`{"nested": {"x": [], "y": {}, "z": [true, false, null]}, "n": -5, "f": 3.14159}`,
		`[NaN, Infinity, -Infinity, 1e-7, 5e-324, 1.7976931348623157e308]`,
		` {"trailing": "ws"} `,
		``, `[1,]`, `{"a":1,}`, `{"a" 1}`, `[1 2]`, `{"a":1} x`, `"abc`, "\"a\x01\"", `{a:1}`, `[-]`, `tru`,
		`"\x"`, `"\u12"`,
	}
	for _, mode := range []struct{ expr string }{
		{"json.dumps(json.loads(x))"},
		{"json.dumps(json.loads(x), indent=2)"},
		{"json.dumps(json.loads(x), separators=(',', ':'))"},
	} {
		want := pyEval(t, mode.expr, docs)
		for i, doc := range docs {
			var got string
			v, err := pyLoads(doc)
			if err != nil {
				got = "!JSONDecodeError: " + err.Error()
			} else {
				switch {
				case strings.Contains(mode.expr, "indent"):
					got = pyDumpsIndent(v, 2)
				case strings.Contains(mode.expr, "separators"):
					got = pyDumpsCompact(v)
				default:
					got = pyDumps(v)
				}
			}
			if pyDumps(got) != want[i] {
				t.Errorf("%s on %q:\n got %s\nwant %s", mode.expr, doc, pyDumps(got), want[i])
			}
		}
	}
}

func TestPyFloatRepr(t *testing.T) {
	values := []float64{0, 1, -1, 0.1, 1.5, 100, 1e15, 1e16, 1e17, 123456789.123, 1e-4, 1e-5, 2.5e-7,
		1.7976931348623157e308, 5e-324, 1700000000.5, 11644473600, math.Pi}
	var inputs []string
	for _, v := range values {
		inputs = append(inputs, pyFloatRepr(v))
	}
	want := pyEval(t, "repr(float(x))", inputs)
	for i, v := range values {
		if pyDumps(pyFloatRepr(v)) != want[i] {
			t.Errorf("repr(%v) = %s, python %s", v, pyFloatRepr(v), want[i])
		}
	}
}

func TestPyPathsMatchPython(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "a", "b"), 0o755)
	os.Symlink("a/b", filepath.Join(root, "link"))
	os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "abs"))
	os.Symlink("loop2", filepath.Join(root, "loop1"))
	os.Symlink("loop1", filepath.Join(root, "loop2"))
	os.Symlink("missing/x", filepath.Join(root, "dangling"))
	os.Symlink("../..", filepath.Join(root, "a", "up"))
	os.WriteFile(filepath.Join(root, "file"), nil, 0o644)
	paths := []string{
		root, root + "/link", root + "/link/../x", root + "/abs/b/c/d", root + "/loop1/x", root + "/dangling",
		root + "/a/up/tmp", root + "/file/x", "//x//y/./z/..", "///a/../../b", "", ".", "a/../../b", "/..",
		root + "/a/b/..", "rel/path",
	}
	checks := []struct {
		expr string
		fn   func(string) string
	}{
		{"posixpath.realpath(x)", pyRealpath},
		{"posixpath.normpath(x)", pyNormpath},
		{"posixpath.dirname(x)", pyDirname},
		{"posixpath.basename(x)", pyBasename},
		{"posixpath.join(x, 'k', '/abs', 'z/')", func(s string) string { return pyJoin(s, "k", "/abs", "z/") }},
		{"posixpath.relpath(x or '.', " + pyReprString(root) + ")", func(s string) string {
			if s == "" {
				s = "."
			}
			return pyRelpath(s, root)
		}},
		{"posixpath.commonpath([x, " + pyReprString(root+"/a/b") + "])", func(s string) string {
			c, err := pyCommonpath(s, root+"/a/b")
			if err != nil {
				return "!ValueError: " + err.Error()
			}
			return c
		}},
		{"posixpath.expanduser(x)", pyExpanduser},
	}
	paths = append(paths, "~", "~/x", "~root/y", "~nosuchuser-lectern/z")
	cwd, _ := os.Getwd()
	for _, c := range checks {
		want := pyEval(t, c.expr, paths)
		for i, p := range paths {
			if got := pyDumps(c.fn(p)); got != want[i] {
				t.Errorf("%s with x=%q (cwd %s): go %s, python %s", c.expr, p, cwd, got, want[i])
			}
		}
	}
}

func TestPyTextMatchesPython(t *testing.T) {
	blobs := []string{"ok", "\xff", "a\xe2\x82b", "\xe2\x82", "\xed\xa0\x80", "\xf0\x9f\x98\x80\xf4\x90\x80\x80",
		"\xc0\xaf", "x\xe0\x80y", "\xf0\x9f\x98"}
	var inputs []string
	for _, b := range blobs {
		inputs = append(inputs, pyDecodeReplace([]byte(b))) // placeholder list for length
	}
	// Python receives the bytes through latin-1 so every byte survives JSON.
	var latin []string
	for _, b := range blobs {
		var r []rune
		for i := 0; i < len(b); i++ {
			r = append(r, rune(b[i]))
		}
		latin = append(latin, string(r))
	}
	want := pyEval(t, "x.encode('latin-1').decode('utf-8','replace')", latin)
	strict := pyEval(t, "x.encode('latin-1').decode('utf-8')", latin)
	for i, b := range blobs {
		if got := pyDumps(pyDecodeReplace([]byte(b))); got != want[i] {
			t.Errorf("replace %q: go %s python %s", b, got, want[i])
		}
		got, err := pyDecodeStrict([]byte(b))
		if err != nil {
			got = "!UnicodeDecodeError: " + err.Error()
		}
		if pyDumps(got) != strict[i] {
			t.Errorf("strict %q: go %s python %s", b, pyDumps(got), strict[i])
		}
	}
	_ = inputs
	texts := []string{"a\nb\r\nc\rd\x0be\x0cf\x1cg\x1dh\x1ei\u0085j\u2028k\u2029l\x1fm", "\n", "", "x\r", "tail\n\n"}
	keep := pyEval(t, "x.splitlines(True)", texts)
	drop := pyEval(t, "x.splitlines()", texts)
	strip := pyEval(t, "x.strip()", append(texts, " \x1c\u3000 x \u00a0\t"))
	for i, s := range texts {
		if got := pyDumps(toAny(pySplitLines(s, true))); got != keep[i] {
			t.Errorf("splitlines(True) %q: %s vs %s", s, got, keep[i])
		}
		if got := pyDumps(toAny(pySplitLines(s, false))); got != drop[i] {
			t.Errorf("splitlines() %q: %s vs %s", s, got, drop[i])
		}
	}
	for i, s := range append(texts, " \x1c\u3000 x \u00a0\t") {
		if got := pyDumps(pyStrip(s)); got != strip[i] {
			t.Errorf("strip %q: %s vs %s", s, got, strip[i])
		}
	}
	reprs := []string{"plain", "it's", `both ' and "`, "tab\tnl\n\x00\x7f", "é☕\u200b\U0001f600", "\\"}
	want = pyEval(t, "repr(x)", reprs)
	for i, s := range reprs {
		if got := pyDumps(pyReprString(s)); got != want[i] {
			t.Errorf("repr %q: %s vs %s", s, got, want[i])
		}
	}
}

func toAny(list []string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}

func TestPyOSErrorText(t *testing.T) {
	_, err := os.Open(filepath.Join(t.TempDir(), "missing"))
	if got := pyErr(err, "missing").Error(); got != "[Errno 2] No such file or directory: 'missing'" {
		t.Fatal(got)
	}
	e := &pyTimeoutError{argv: []string{"git", "status"}, timeout: "15"}
	if e.Error() != "Command '['git', 'status']' timed out after 15 seconds" {
		t.Fatal(e.Error())
	}
}
