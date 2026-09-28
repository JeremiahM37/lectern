// Package helperstest runs target-side helpers the way a target does — as a
// separate process with its own environment, working directory, stdin and
// exit status — so a test can hold a Go helper and the Python script it
// replaces to the same observable behaviour.
//
// The test binary itself is the helper binary: a package's tests declare
//
//	func TestHelperProcess(t *testing.T) { helperstest.Serve() }
//
// and Go re-executes the test binary into that function.
package helperstest

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/helpers"
)

const processFlag = "LECTERN_HELPERSTEST_PROCESS"

// Serve turns this test process into `lectern helper …` when it was started
// by Go; otherwise it returns and the test passes.
func Serve() {
	if os.Getenv(processFlag) != "1" {
		return
	}
	for i, a := range os.Args {
		if a == "--" {
			os.Exit(helpers.Main(os.Args[i+1:]))
		}
	}
	os.Exit(2)
}

// Result is what a process left behind.
type Result struct {
	Stdout, Stderr string
	Code           int
}

// Proc describes how to start one process.
type Proc struct {
	Dir   string
	Env   []string
	Stdin []byte
}

// Env is a minimal environment: PATH, a private HOME and TMPDIR, a UTF-8
// locale, plus extra KEY=VALUE entries. Nothing of the caller's (proxies,
// CODEX_HOME, CLAUDE_CONFIG_DIR, LECTERN_*) leaks in.
func Env(home string, extra ...string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + os.TempDir(),
		"LANG=C.UTF-8", "PYTHONDONTWRITEBYTECODE=1"}
	return append(env, extra...)
}

// Go runs `lectern helper NAME ARGS…` in the test binary.
func Go(t *testing.T, p Proc, name string, args ...string) Result {
	t.Helper()
	argv := append([]string{"-test.run=^TestHelperProcess$", "--", name}, args...)
	p.Env = append(append([]string{}, p.Env...), processFlag+"=1")
	return Run(t, p, os.Args[0], argv...)
}

// Command is `lectern helper NAME ARGS…` as an unstarted command, for a test
// that talks to a running helper. env replaces the environment.
func Command(env []string, name string, args ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestHelperProcess$", "--", name}, args...)...)
	cmd.Env = append(append([]string{}, env...), processFlag+"=1")
	return cmd
}

// Run runs any program.
func Run(t *testing.T, p Proc, prog string, args ...string) Result {
	t.Helper()
	cmd := exec.Command(prog, args...)
	cmd.Dir = p.Dir
	cmd.Env = p.Env
	cmd.Stdin = bytes.NewReader(p.Stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running %s: %v", prog, err)
	}
	return Result{stdout.String(), stderr.String(), code}
}

// RequirePython skips a parity test where there is no Python to compare
// against.
func RequirePython(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable; nothing to compare the Go helper with")
	}
}

// Tree reads every file and directory under root as "mode content", keyed by
// path relative to root, so two runs' side effects can be compared.
// Directories named in skip are left out.
func Tree(t *testing.T, root string, skip ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, s := range skip {
			if rel == s || strings.HasPrefix(rel, s+string(filepath.Separator)) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entry := info.Mode().String()
		if !d.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry += " " + string(data)
		}
		out[rel] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Same reports whether two results match on stdout and exit status.
func Same(t *testing.T, what string, py, got Result) {
	t.Helper()
	if py.Stdout != got.Stdout || py.Code != got.Code {
		t.Errorf("%s: Go helper differs from Python\npython: exit %d stdout %q stderr %q\ngo:     exit %d stdout %q stderr %q",
			what, py.Code, py.Stdout, py.Stderr, got.Code, got.Stdout, got.Stderr)
	}
}

// SameTree reports every difference between two Tree snapshots.
func SameTree(t *testing.T, what string, py, got map[string]string) {
	t.Helper()
	for k, v := range py {
		if got[k] != v {
			t.Errorf("%s: %s differs\npython: %q\ngo:     %q", what, k, v, got[k])
		}
	}
	for k, v := range got {
		if _, ok := py[k]; !ok {
			t.Errorf("%s: Go helper left %s (%q) that Python did not", what, k, v)
		}
	}
}
