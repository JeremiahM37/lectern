package exec

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// eslintFixture builds a Node project that lints with the eslint on PATH,
// linked into node_modules/.bin so npx runs it without touching the network.
// It is configured so that a missing semicolon is a warning: eslint exits 0
// with the warning in its output, which is the case check must catch. The
// semi rule is autofixable, so fix can clear it.
func eslintFixture(t *testing.T) string {
	t.Helper()
	eslint, err := exec.LookPath("eslint")
	if err != nil {
		t.Skip("eslint is not installed")
	}
	dir := fixture(t, map[string]string{
		"package.json":   `{"name":"lint-fixture","version":"1.0.0"}`,
		".eslintrc.json": `{"root":true,"parserOptions":{"ecmaVersion":2018},"rules":{"semi":["warn","always"]}}`,
		"a.js":           "const x = 1\nconsole.log(x)\n",
	})
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", ".bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(eslint, filepath.Join(dir, "node_modules", ".bin", "eslint")); err != nil {
		t.Skipf("cannot link eslint: %v", err)
	}
	return dir
}

// stageStatus returns the status of one stage from a check result's meta.
func stageStatus(t *testing.T, meta map[string]any, action string) string {
	t.Helper()
	stages, ok := meta["stages"].([]map[string]any)
	if !ok {
		t.Fatalf("stages missing from meta: %+v", meta)
	}
	for _, s := range stages {
		if s["action"] == action {
			return s["status"].(string)
		}
	}
	t.Fatalf("no %s stage in %+v", action, stages)
	return ""
}

func TestProjectCheckLintWarningFailsAndFixClears(t *testing.T) {
	skipOnWindows(t)
	dir := eslintFixture(t)
	r, _ := newTestRegistry(t, Options{})
	env := newTestEnv(dir, nil)

	check := runTool(t, r, "project", env, map[string]any{"action": "check"})
	if !check.IsError || check.Meta["passed"] != false {
		t.Fatalf("a lint warning must fail check even with exit 0: %+v", check)
	}
	if stageStatus(t, check.Meta, "lint") != "fail" {
		t.Errorf("lint stage should fail: %+v", check.Meta["stages"])
	}
	if stageStatus(t, check.Meta, "typecheck") != "skip" || stageStatus(t, check.Meta, "test") != "skip" {
		t.Errorf("no typecheck or test command here, so both skip: %+v", check.Meta["stages"])
	}
	if !strings.Contains(check.Output, "Missing semicolon") || !strings.Contains(check.Output, "— check failed: lint.") {
		t.Errorf("summary should show the warning and the failing stage:\n%s", check.Output)
	}

	fix := runTool(t, r, "project", env, map[string]any{"action": "lint", "fix": true})
	if fix.IsError {
		t.Fatalf("lint with fix: %+v", fix)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.js")); !strings.Contains(string(b), "const x = 1;") {
		t.Fatalf("fix did not add the semicolon: %q", b)
	}

	after := runTool(t, r, "project", env, map[string]any{"action": "check"})
	if after.IsError || after.Meta["passed"] != true || !strings.Contains(after.Output, "— check passed.") {
		t.Fatalf("check after fix should pass: %+v", after)
	}
}

func TestProjectCheckReportsEveryStage(t *testing.T) {
	skipOnWindows(t)
	t.Setenv("GOTOOLCHAIN", "local")
	dir := fixture(t, map[string]string{
		"go.mod": "module example.com/add\n\ngo 1.21\n",
		"add.go": "package add\n\nfunc Add(a, b int) int { return a + b + 1 }\n",
		"add_test.go": `package add

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(2, 2); got != 4 {
		t.Errorf("Add(2,2) = %d, want 4", got)
	}
}
`,
	})
	r, _ := newTestRegistry(t, Options{})
	res := runTool(t, r, "project", newTestEnv(dir, nil), map[string]any{"action": "check"})
	if !res.IsError {
		t.Fatalf("a failing test must fail check: %+v", res)
	}
	failed, _ := res.Meta["failed_stages"].([]string)
	if len(failed) != 1 || failed[0] != "test" {
		t.Fatalf("failed stages = %v, want [test]", failed)
	}
	if stageStatus(t, res.Meta, "typecheck") != "pass" || stageStatus(t, res.Meta, "lint") != "pass" {
		t.Errorf("typecheck and lint should pass: %+v", res.Meta["stages"])
	}
	if !strings.Contains(res.Output, "typecheck") || !strings.Contains(res.Output, "lint") || !strings.Contains(res.Output, "test") {
		t.Errorf("every stage should appear in the report:\n%s", res.Output)
	}
	errs, _ := res.Meta["errors"].([]Diagnostic)
	if len(errs) == 0 || errs[0].File != "add_test.go" {
		t.Errorf("test locations should reach meta: %+v", errs)
	}
}

func TestProjectCheckSkipsMissingStagesAndNeedsSomething(t *testing.T) {
	skipOnWindows(t)
	t.Setenv("GOTOOLCHAIN", "local")
	// A Makefile with only a test target: typecheck and lint are skipped, not failed.
	dir := fixture(t, map[string]string{"Makefile": "test:\n\t@echo ok\n"})
	r, _ := newTestRegistry(t, Options{})
	res := runTool(t, r, "project", newTestEnv(dir, nil), map[string]any{"action": "check"})
	if res.IsError || stageStatus(t, res.Meta, "typecheck") != "skip" || stageStatus(t, res.Meta, "test") != "pass" {
		t.Fatalf("missing stages should be skipped: %+v", res)
	}

	none := runTool(t, r, "project", newTestEnv(t.TempDir(), nil), map[string]any{"action": "check"})
	if !none.IsError || none.Output != "No typecheck, lint, or test commands detected." {
		t.Fatalf("nothing to check should say so: %+v", none)
	}
}

func TestProjectCheckRefusesTargetAndArgs(t *testing.T) {
	dir := fixture(t, map[string]string{"go.mod": "module x\n"})
	r, _ := newTestRegistry(t, Options{})
	env := newTestEnv(dir, nil)
	if res := runTool(t, r, "project", env, map[string]any{"action": "check", "target": "x.go"}); !res.IsError {
		t.Fatal("check with a target must be refused")
	}
	if res := runTool(t, r, "project", env, map[string]any{"action": "check", "args": "-v"}); !res.IsError {
		t.Fatal("check with args must be refused")
	}
}

func TestProjectFixRefusals(t *testing.T) {
	dir := fixture(t, map[string]string{"go.mod": "module x\n"})
	r, _ := newTestRegistry(t, Options{})
	env := newTestEnv(dir, nil)

	// go vet has no auto-fix, so fix must refuse rather than run it plain.
	noFix := runTool(t, r, "project", env, map[string]any{"action": "lint", "fix": true})
	if !noFix.IsError || !strings.Contains(noFix.Output, "no auto-fix") {
		t.Fatalf("lint fix on a linter without a fix must be refused: %+v", noFix)
	}
	wrong := runTool(t, r, "project", env, map[string]any{"action": "test", "fix": true})
	if !wrong.IsError || !strings.Contains(wrong.Output, "fix applies to lint and check only") {
		t.Fatalf("fix on test must be refused: %+v", wrong)
	}
}

func TestProjectCheckFixOnUnfixableLintStillFailsOnWarnings(t *testing.T) {
	skipOnWindows(t)
	t.Setenv("GOTOOLCHAIN", "local")
	dir := fixture(t, map[string]string{"go.mod": "module x\n\ngo 1.21\n", "x.go": "package x\n"})
	r, _ := newTestRegistry(t, Options{})
	res := runTool(t, r, "project", newTestEnv(dir, nil), map[string]any{"action": "check", "fix": true})
	if res.IsError {
		t.Fatalf("go vet on a clean module should pass check with fix set: %+v", res)
	}
	if !strings.Contains(res.Output, "no auto-fix for this linter") {
		t.Errorf("the report should say the linter has no fix:\n%s", res.Output)
	}
}

func TestProjectReadOnlyCall(t *testing.T) {
	tool := &projectTool{}
	cases := map[string]bool{
		`{"action":"check"}`:            true,
		`{"action":"check","fix":true}`: false,
		`{"action":"lint"}`:             false,
		`{"action":"lint","fix":true}`:  false,
		`{"action":"test"}`:             false,
		`{"action":"list"}`:             false,
	}
	for raw, want := range cases {
		if got := tool.ReadOnlyCall(json.RawMessage(raw)); got != want {
			t.Errorf("ReadOnlyCall(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestFixCommandRules(t *testing.T) {
	cases := map[string]string{
		"golangci-lint run ./...":           "golangci-lint run --fix ./...",
		"npx eslint .":                      "npx eslint --fix .",
		"npx biome check .":                 "npx biome check --write .",
		"uv run ruff check .":               "uv run ruff check --fix .",
		"cargo clippy":                      "cargo clippy --fix --allow-dirty",
		"bundle exec rubocop":               "bundle exec rubocop -a",
		"swiftlint":                         "swiftlint --fix",
		"dart analyze":                      "dart fix --apply",
		"./gradlew spotlessCheck":           "./gradlew spotlessApply",
		"dotnet format --verify-no-changes": "dotnet format",
	}
	for in, want := range cases {
		got, ok := fixCommand(in)
		if !ok || got != want {
			t.Errorf("fixCommand(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"go vet ./...", "npm run lint", "deno lint", "clj-kondo --lint src"} {
		if got, ok := fixCommand(in); ok {
			t.Errorf("fixCommand(%q) = %q; want no fix", in, got)
		}
	}
}

func TestWarningLines(t *testing.T) {
	eslint := "/src/a.ts\n  3:5  warning  'x' is defined but never used  no-unused-vars\n  4:1  error    bad\n\n✖ 2 problems (1 error, 1 warning)\n"
	if got := warningLines(eslint); len(got) != 1 || !strings.Contains(got[0], "no-unused-vars") {
		t.Errorf("eslint warnings = %q", got)
	}

	clippy := "warning: unused variable: `x`\n --> src/main.rs:2:9\n\nwarning: `demo` (bin \"demo\") generated 1 warning\n"
	got := warningLines(clippy)
	if len(got) != 1 || got[0] != "warning: unused variable: `x`" {
		t.Errorf("clippy warnings = %q; the summary line must not count", got)
	}

	gcc := "main.go:3:1: warning: exported function One should have a comment\nmain.go:4:2: undefined: y\n"
	if got := warningLines(gcc); len(got) != 1 {
		t.Errorf("file:line warning = %q", got)
	}

	if got := warningLines("all good\n"); len(got) != 0 {
		t.Errorf("clean output has warnings: %q", got)
	}
}
