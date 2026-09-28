package helpers

// Shared parity harness: each port is run beside the Python script it
// replaces, on the same fixture, and must print the same bytes and exit the
// same way. The Go side runs as a child process (this test binary re-executed
// as `lectern helper`), since helpers chdir, exit and pass descriptors.

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const helperExecEnv = "LECTERN_HELPER_TEST_EXEC"

func TestMain(m *testing.M) {
	if os.Getenv(helperExecEnv) == "1" {
		args := os.Args[1:]
		if len(args) > 0 && args[0] == "helper" {
			args = args[1:]
		}
		os.Exit(Main(args))
	}
	os.Exit(m.Run())
}

type runResult struct {
	stdout, stderr string
	rc             int
}

type runSpec struct {
	dir   string
	env   []string // added to the inherited environment
	stdin string
}

func runProcess(t *testing.T, spec runSpec, argv ...string) runResult {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = spec.dir
	cmd.Env = append(os.Environ(), spec.env...)
	cmd.Stdin = strings.NewReader(spec.stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	rc := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run %s: %v", argv[0], err)
		}
		rc = ee.ExitCode()
	}
	return runResult{out.String(), errb.String(), rc}
}

// runGo runs `lectern helper name args…`.
func runGo(t *testing.T, spec runSpec, name string, args ...string) runResult {
	t.Helper()
	spec.env = append(spec.env, helperExecEnv+"=1")
	return runProcess(t, spec, append([]string{os.Args[0], "helper", name}, args...)...)
}

// runPy runs `python3 -c script args…`.
func runPy(t *testing.T, spec runSpec, script string, args ...string) runResult {
	t.Helper()
	requirePython(t)
	return runProcess(t, spec, append([]string{"python3", "-c", script}, args...)...)
}

func requirePython(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
}

// assertParity runs both versions with the same arguments and compares.
func assertParity(t *testing.T, spec runSpec, script, name string, args ...string) runResult {
	t.Helper()
	py := runPy(t, spec, script, args...)
	goRes := runGo(t, spec, name, args...)
	sameResult(t, py, goRes)
	return goRes
}

func sameResult(t *testing.T, py, goRes runResult) {
	t.Helper()
	if py.stdout != goRes.stdout || py.rc != goRes.rc {
		t.Fatalf("output differs\npython (rc %d): %q\nstderr: %s\ngo     (rc %d): %q\nstderr: %s",
			py.rc, py.stdout, py.stderr, goRes.rc, goRes.stdout, goRes.stderr)
	}
}

// pythonFile reads an embedded script from its package.
func pythonFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// pythonConst reads a string constant (the Python source) from a Go file.
func pythonConst(t *testing.T, rel, name string) string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", rel), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out string
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, id := range spec.Names {
			if id.Name != name || i >= len(spec.Values) {
				continue
			}
			lit, ok := spec.Values[i].(*ast.BasicLit)
			if !ok {
				t.Fatalf("%s is not a literal", name)
			}
			out, err = strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
		}
		return true
	})
	if out == "" {
		t.Fatalf("no constant %s in %s", name, rel)
	}
	return out
}
