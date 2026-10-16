package exec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Check runs the stages that do not write files at the same time, so three
// one-second stages take about one second, not three.
func TestCheckStagesRunInParallel(t *testing.T) {
	skipOnWindows(t)
	jobs := NewJobs(Options{})
	t.Cleanup(jobs.KillAll)
	tool := &projectTool{jobs: jobs}
	p := Profile{Ecosystem: "test", Commands: map[string]string{
		"typecheck": "sleep 1", "lint": "sleep 1", "test": "sleep 1",
	}}
	dir := t.TempDir()
	start := time.Now()
	res := tool.runCheck(context.Background(), newTestEnv(dir, &fakeHost{}), projectArgs{Action: "check"}, p, dir)
	elapsed := time.Since(start)
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("three 1 s stages took %v; they should run together", elapsed)
	}
	if res.IsError {
		t.Fatalf("all stages exit 0, check should pass: %s", res.Output)
	}
	// The report keeps the stage order whatever order they finish in.
	order := []string{}
	for _, s := range res.Meta["stages"].([]map[string]any) {
		order = append(order, s["action"].(string))
	}
	if strings.Join(order, ",") != "typecheck,lint,test" {
		t.Fatalf("stage order = %v", order)
	}
}

// A stage that fails does not stop the others, and the failure lands on its
// own row of the report.
func TestCheckParallelFailureKeepsItsRow(t *testing.T) {
	skipOnWindows(t)
	jobs := NewJobs(Options{})
	t.Cleanup(jobs.KillAll)
	tool := &projectTool{jobs: jobs}
	p := Profile{Ecosystem: "test", Commands: map[string]string{
		"typecheck": "exit 3", "lint": "echo fine", "test": "echo ok",
	}}
	dir := t.TempDir()
	res := tool.runCheck(context.Background(), newTestEnv(dir, &fakeHost{}), projectArgs{Action: "check"}, p, dir)
	if !res.IsError {
		t.Fatal("a failing typecheck must fail check")
	}
	if got := stageStatus(t, res.Meta, "typecheck"); got != "fail" {
		t.Fatalf("typecheck status = %s", got)
	}
	if got := stageStatus(t, res.Meta, "test"); got != "pass" {
		t.Fatalf("test should still run and pass, got %s", got)
	}
}

func TestLegacyFixCommand(t *testing.T) {
	cases := []struct {
		fixed, output, want string
		ok                  bool
	}{
		{"npx biome check --write .", "error: unknown option `--write`", "npx biome check --apply .", true},
		{"bundle exec rubocop -a", "rubocop: unrecognized option '-a'", "bundle exec rubocop --auto-correct", true},
		{"ktlint --format", "Unknown argument --format", "ktlint -F", true},
		// Output that does not reject a flag is not retried.
		{"npx biome check --write .", "2 files formatted", "", false},
		// A rejection with no known legacy spelling is not retried.
		{"eslint --fix .", "unknown option --fix", "", false},
	}
	for _, c := range cases {
		got, ok := legacyFixCommand(c.fixed, c.output)
		if ok != c.ok || got != c.want {
			t.Errorf("legacyFixCommand(%q, %q) = %q, %v; want %q, %v", c.fixed, c.output, got, ok, c.want, c.ok)
		}
	}
}

func TestScopeFixAppendsOnlyTheEditedFiles(t *testing.T) {
	workdir := "/work/proj"
	cmd, n, ok := scopeFix("npx eslint --fix .", []string{"src/a.js", "README.md", "/work/proj/lib/b.ts"}, workdir)
	if !ok || n != 2 {
		t.Fatalf("scopeFix = %q, %d, %v", cmd, n, ok)
	}
	if cmd != "npx eslint --fix lib/b.ts src/a.js" {
		t.Fatalf("scoped command = %q", cmd)
	}
	// Go lints by package, so the files map to their directories.
	cmd, _, ok = scopeFix("golangci-lint run --fix ./...", []string{"internal/x/a.go", "internal/x/b.go"}, workdir)
	if !ok || cmd != "golangci-lint run --fix ./internal/x" {
		t.Fatalf("golangci scope = %q, %v", cmd, ok)
	}
	// A linter that cannot take files, or no file it handles, is not scoped.
	if _, _, ok := scopeFix("cargo clippy --fix --allow-dirty", []string{"src/lib.rs"}, workdir); ok {
		t.Error("clippy cannot be scoped to files")
	}
	if _, _, ok := scopeFix("npx eslint --fix .", []string{"main.go"}, workdir); ok {
		t.Error("no eslint-handled file given, so nothing should run")
	}
}

// The post-dispatch lint fix changes only the files it was given.
func TestLintFixFilesFixesOnlyTheGivenFiles(t *testing.T) {
	skipOnWindows(t)
	dir := eslintFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "b.js"), []byte("const y = 2\nconsole.log(y)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	jobs := NewJobs(Options{})
	t.Cleanup(jobs.KillAll)
	tool := &projectTool{jobs: jobs}
	report, ran := tool.LintFixFiles(context.Background(), newTestEnv(dir, &fakeHost{}), []string{"a.js"}, lintFixTimeout)
	if !ran {
		t.Fatalf("lint fix should run on a.js: %s", report)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "a.js"))
	b, _ := os.ReadFile(filepath.Join(dir, "b.js"))
	if !strings.Contains(string(a), "const x = 1;") {
		t.Errorf("a.js should be fixed, got %q", a)
	}
	if string(b) != "const y = 2\nconsole.log(y)\n" {
		t.Errorf("b.js was not in the edit set and must not change, got %q", b)
	}
}

// The shell schema text and the code agree on the output limit.
func TestShellSchemaStatesTheOutputLimit(t *testing.T) {
	desc := (&shellTool{}).Spec().Description
	if !strings.Contains(desc, "capped at 16 KB") || strings.Contains(desc, "30000") {
		t.Fatalf("shell description does not state the 16 KB limit: %q", desc)
	}
	if outputLimit != 16384 {
		t.Fatalf("outputLimit = %d; the description says 16 KB", outputLimit)
	}
}

// Biome's format action prefers the combined format-and-lint pass, while the
// per-file formatter used after an edit stays the plain one.
func TestBiomeFormatPrefersFormatAndLint(t *testing.T) {
	dir := fixture(t, map[string]string{
		"package.json": `{"name":"biome-fixture","version":"1.0.0"}`,
		"biome.json":   `{}`,
	})
	p := Detect(dir)
	if got := commandFor(p, "format", ""); got != "npx biome check --write ." {
		t.Fatalf("format = %q, want the combined biome check", got)
	}
	if p.Commands["format"] != "npx biome format --write ." {
		t.Fatalf("per-file format command changed to %q", p.Commands["format"])
	}
}

// A project-wide lint action, such as list, does not show the internal key.
func TestListHidesFormatAndLintKey(t *testing.T) {
	dir := fixture(t, map[string]string{
		"package.json": `{"name":"biome-fixture","version":"1.0.0"}`,
		"biome.json":   `{}`,
	})
	r := listResult(Detect(dir), dir, dir)
	if strings.Contains(r.Output, "formatAndLint") {
		t.Fatalf("list output should not name the internal key: %s", r.Output)
	}
}
