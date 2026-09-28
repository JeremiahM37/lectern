//go:build unix

package helpers

// Shared parity harness: each port is run beside the Python script it
// replaces, on the same fixture, and must print the same bytes and exit the
// same way. The Go side runs as a child process (this test binary re-executed
// as `lectern helper`), since helpers chdir, exit and pass descriptors.

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	unset []string // removed from it
	stdin string
}

func runProcess(t *testing.T, spec runSpec, argv ...string) runResult {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = spec.dir
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); !slices.Contains(spec.unset, name) {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, spec.env...)
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

// wRunGo runs `lectern helper name args…`.
func wRunGo(t *testing.T, spec runSpec, name string, args ...string) runResult {
	t.Helper()
	spec.env = append(spec.env, helperExecEnv+"=1")
	return runProcess(t, spec, append([]string{os.Args[0], "helper", name}, args...)...)
}

// runPy runs `python3 -c script args…`.
func runPy(t *testing.T, spec runSpec, script string, args ...string) runResult {
	t.Helper()
	wRequirePython(t)
	return runProcess(t, spec, append([]string{"python3", "-c", script}, args...)...)
}

func wRequirePython(t *testing.T) {
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
	goRes := wRunGo(t, spec, name, args...)
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

// twin runs the Python script in one copy of a fixture and the port in
// another, then compares their output (with each copy's directory written
// as $ROOT) and the trees they leave behind. build fills a fresh directory
// and returns how to run in it and the arguments.
func twin(t *testing.T, script, name string, build func(t *testing.T, root string) (runSpec, []string)) (py, goRes runResult) {
	t.Helper()
	var pyAll, goAll []runResult
	pyAll, goAll = twinSteps(t, script, name, nil, func(t *testing.T, root string) (runSpec, [][]string) {
		spec, args := build(t, root)
		return spec, [][]string{args}
	})
	return pyAll[0], goAll[0]
}

// twinSteps is twin for a sequence of invocations in the same fixture.
// normalize, when set, rewrites what depends on the fixture's own path
// (a hash of it, say) in outputs and trees.
func twinSteps(t *testing.T, script, name string, normalize func(root, s string) string,
	build func(t *testing.T, root string) (runSpec, [][]string)) (pyAll, goAll []runResult) {
	t.Helper()
	wRequirePython(t)
	pyRoot, goRoot := t.TempDir(), t.TempDir()
	norm := func(root, s string) string {
		if normalize != nil {
			s = normalize(root, s)
		}
		return strings.ReplaceAll(s, root, "$ROOT")
	}
	pySpec, pySteps := build(t, pyRoot)
	goSpec, goSteps := build(t, goRoot)
	for i := range pySteps {
		py := runPy(t, pySpec, script, pySteps[i]...)
		goRes := wRunGo(t, goSpec, name, goSteps[i]...)
		py.stdout, goRes.stdout = norm(pyRoot, py.stdout), norm(goRoot, goRes.stdout)
		sameResult(t, py, goRes)
		t.Logf("step %d: rc %d %s", i, goRes.rc, strings.TrimSpace(goRes.stdout))
		pyAll, goAll = append(pyAll, py), append(goAll, goRes)
		if a, b := norm(pyRoot, treeOf(t, pyRoot)), norm(goRoot, treeOf(t, goRoot)); a != b {
			t.Fatalf("after step %d trees differ\npython:\n%s\ngo:\n%s", i, a, b)
		}
	}
	return pyAll, goAll
}

// treeOf lists a directory: every entry's type, permissions and content or
// link target.
func treeOf(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		// Git's own files hold times and inode numbers; its exclude list is
		// the one a helper edits.
		if parts := strings.Split(filepath.ToSlash(rel), "/"); len(parts) > 1 && slices.Contains(parts[:len(parts)-1], ".git") &&
			!strings.HasSuffix(filepath.ToSlash(rel), ".git/info/exclude") && !strings.HasSuffix(filepath.ToSlash(rel), ".git/info") {
			return nil
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, _ := os.Readlink(path)
			fmt.Fprintf(&b, "%s -> %s\n", rel, strings.ReplaceAll(target, root, "$ROOT"))
		case info.IsDir():
			fmt.Fprintf(&b, "%s/ %v\n", rel, info.Mode().Perm())
		case !info.Mode().IsRegular() || info.Size() > 1<<20:
			fmt.Fprintf(&b, "%s %v %d\n", rel, info.Mode(), info.Size())
		default:
			data, _ := os.ReadFile(path)
			fmt.Fprintf(&b, "%s %v %q\n", rel, info.Mode().Perm(), strings.ReplaceAll(string(data), root, "$ROOT"))
		}
		return nil
	})
	return b.String()
}
