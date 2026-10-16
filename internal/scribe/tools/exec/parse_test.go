package exec

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseDiagnostics(t *testing.T) {
	cases := []struct {
		name   string
		out    string
		file   string
		line   int
		msg    string // substring of the message
		failed []string
	}{
		{
			name: "go test failure",
			out: `=== RUN   TestAdd
    add_test.go:6: Add(2,2) = 5, want 4
--- FAIL: TestAdd (0.00s)
FAIL
FAIL	example.com/add	0.012s
`,
			file: "add_test.go", line: 6, msg: "want 4", failed: []string{"TestAdd"},
		},
		{
			name: "go build error",
			out:  "# example.com/x\n./x.go:3:5: undefined: foo\n",
			file: "./x.go", line: 3, msg: "undefined: foo",
		},
		{
			name: "pytest failure",
			out: `tests/test_x.py:12: AssertionError
FAILED tests/test_x.py::test_a - AssertionError: boom
`,
			file: "tests/test_x.py", line: 12, msg: "AssertionError", failed: []string{"tests/test_x.py::test_a"},
		},
		{
			name: "tsc error",
			out:  "src/a.ts(12,5): error TS2322: Type 'string' is not assignable to type 'number'.\n",
			file: "src/a.ts", line: 12, msg: "TS2322",
		},
		{
			name: "cargo error with arrow",
			out: `error[E0308]: mismatched types
 --> src/main.rs:3:5
  |
3 |     "x"
`,
			file: "src/main.rs", line: 3, msg: "mismatched types",
		},
		{
			name: "gcc style",
			out:  "main.c:4:9: error: expected ';' before '}' token\n",
			file: "main.c", line: 4, msg: "expected ';'",
		},
		{
			name: "jest failure",
			out: `FAIL src/math.test.js
  ● math › adds numbers

    expect(received).toBe(4)
`,
			failed: []string{"math › adds numbers"},
		},
		{
			name: "urls are not locations",
			out:  "see https://example.com:8080/docs for details\n",
		},
		{
			name: "plain output",
			out:  "all good\nnothing to see\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			diags, failed := parseDiagnostics(c.out)
			if c.file == "" {
				if len(diags) != 0 {
					t.Fatalf("expected no diagnostics, got %+v", diags)
				}
			} else {
				if len(diags) == 0 {
					t.Fatalf("no diagnostics for:\n%s", c.out)
				}
				d := diags[0]
				if d.File != c.file || d.Line != c.line || !strings.Contains(d.Msg, c.msg) {
					t.Fatalf("diagnostic = %+v, want %s:%d containing %q", d, c.file, c.line, c.msg)
				}
			}
			if strings.Join(failed, "|") != strings.Join(c.failed, "|") {
				t.Fatalf("failed = %v, want %v", failed, c.failed)
			}
		})
	}
}

func TestParseDiagnosticsDedupesAndCaps(t *testing.T) {
	dup := strings.Repeat("a.go:1: same\n", 5)
	if diags, _ := parseDiagnostics(dup); len(diags) != 1 {
		t.Errorf("duplicates should collapse, got %d", len(diags))
	}

	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "f%d.py:%d: E501 line too long\n", i, i+1)
	}
	if diags, _ := parseDiagnostics(b.String()); len(diags) != maxDiagnostics {
		t.Errorf("diagnostics = %d, want the cap %d", len(diags), maxDiagnostics)
	}
}
