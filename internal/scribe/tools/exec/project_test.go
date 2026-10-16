package exec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture writes files (path -> content) into a new temp dir.
func fixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		writeFile(t, filepath.Join(dir, name), content)
	}
	return dir
}

func TestDetectFixtures(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		eco     string
		manager string
		action  string
		command string
	}{
		{"go", map[string]string{"go.mod": "module x\n"}, "go", "", "test", "go test ./..."},
		{"rust", map[string]string{"Cargo.toml": "[package]\n"}, "rust", "", "lint", "cargo clippy"},
		{"npm scripts", map[string]string{
			"package.json": `{"scripts":{"test":"jest","lint":"eslint ."}}`,
		}, "node", "npm", "test", "npm test"},
		{"pnpm lockfile", map[string]string{
			"package.json": `{"scripts":{"test":"vitest"}}`, "pnpm-lock.yaml": "lockfileVersion: 9",
		}, "node", "pnpm", "test", "pnpm test"},
		{"yarn lockfile", map[string]string{
			"package.json": `{"scripts":{"build":"tsc"}}`, "yarn.lock": "# yarn",
		}, "node", "yarn", "build", "yarn run build"},
		{"bun lockfile", map[string]string{
			"package.json": `{}`, "bun.lock": "{}",
		}, "bun", "bun", "test", "bun test"},
		{"deno", map[string]string{"deno.json": "{}"}, "deno", "", "test", "deno test"},
		{"python ruff + pytest", map[string]string{
			"pyproject.toml": "[tool.ruff]\nline-length = 100\n",
		}, "python", "", "lint", "ruff check ."},
		{"python with uv", map[string]string{
			"pyproject.toml": "[project]\n", "uv.lock": "version = 1",
		}, "python", "", "test", "uv run pytest"},
		{"dotnet", map[string]string{"App.csproj": "<Project/>"}, "dotnet", "", "test", "dotnet test"},
		{"php", map[string]string{"composer.json": `{"require":{"laravel/pint":"^1"}}`}, "php", "", "format", "vendor/bin/pint"},
		{"swift", map[string]string{"Package.swift": "// swift-tools"}, "swift", "", "test", "swift test"},
		{"flutter", map[string]string{"pubspec.yaml": "dependencies:\n  flutter:\n    sdk: flutter\n"}, "dart", "", "test", "flutter test"},
		{"elixir", map[string]string{"mix.exs": "defmodule X do end"}, "elixir", "", "test", "mix test"},
		{"ruby with rspec", map[string]string{"Gemfile": "source 'x'", "spec/a_spec.rb": ""}, "ruby", "", "test", "bundle exec rspec"},
		{"gradle kotlin", map[string]string{"build.gradle.kts": `plugins { kotlin("jvm") }`}, "gradle", "", "typecheck", "gradle compileKotlin"},
		{"maven", map[string]string{"pom.xml": "<project/>"}, "maven", "", "build", "mvn package"},
		{"cmake", map[string]string{"CMakeLists.txt": "project(x)"}, "cmake", "", "test", "ctest --test-dir build"},
		{"make with targets", map[string]string{
			"Makefile": "CC := gcc\n\nbuild:\n\tcc x.c\n\ntest:\n\t./run\n",
		}, "make", "", "test", "make test"},
		{"zig", map[string]string{"build.zig": "const std = @import(\"std\");"}, "zig", "", "build", "zig build"},
		{"haskell", map[string]string{"stack.yaml": "resolver: lts"}, "haskell", "", "lint", "hlint ."},
		{"scala", map[string]string{"build.sbt": "name := \"x\""}, "scala", "", "test", "sbt test"},
		{"clojure", map[string]string{"project.clj": "(defproject x)"}, "clojure", "", "test", "lein test"},
	}
	if len(Ecosystems()) < 15 {
		t.Fatalf("detection table has %d ecosystems, want at least 15", len(Ecosystems()))
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Detect(fixture(t, c.files))
			if c.eco != "" && p.Ecosystem != c.eco {
				t.Fatalf("ecosystem = %q, want %q", p.Ecosystem, c.eco)
			}
			if p.Manager != c.manager {
				t.Fatalf("manager = %q, want %q", p.Manager, c.manager)
			}
			if got := p.Commands[c.action]; got != c.command {
				t.Fatalf("%s command = %q, want %q (all: %v)", c.action, got, c.command, p.Commands)
			}
		})
	}
}

func TestDetectFirstMatchWins(t *testing.T) {
	p := Detect(fixture(t, map[string]string{
		"package.json": `{"scripts":{"test":"jest"}}`,
		"Cargo.toml":   "[package]\n",
	}))
	if p.Ecosystem != "node" {
		t.Fatalf("package.json should win over Cargo.toml, got %q", p.Ecosystem)
	}
}

func TestDetectNothing(t *testing.T) {
	p := Detect(t.TempDir())
	if p.Ecosystem != "" || len(p.Commands) != 0 {
		t.Fatalf("empty dir detected %+v", p)
	}
}

func TestNodeCommandsFromConfigFiles(t *testing.T) {
	dir := fixture(t, map[string]string{
		"package.json":  `{"scripts":{"dev":"vite"}}`,
		"tsconfig.json": "{}",
		"biome.json":    "{}",
	})
	p := Detect(dir)
	if p.Commands["typecheck"] != "npx tsc --noEmit" {
		t.Errorf("typecheck = %q", p.Commands["typecheck"])
	}
	if p.Commands["lint"] != "npx biome check ." {
		t.Errorf("lint = %q", p.Commands["lint"])
	}
	if p.Commands["run"] != "npm run dev" {
		t.Errorf("run = %q", p.Commands["run"])
	}
}

func TestJSManagerWalksUpToFiveLevels(t *testing.T) {
	root := fixture(t, map[string]string{"pnpm-lock.yaml": "x"})
	deep := filepath.Join(root, "packages", "app")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := jsManager(deep); got != "pnpm" {
		t.Fatalf("lockfile two levels up: manager %q, want pnpm", got)
	}
}

func TestCommandForTargets(t *testing.T) {
	node := Profile{Ecosystem: "node", Manager: "pnpm", Commands: map[string]string{"test": "pnpm test", "run": "pnpm run start"}}
	if got := commandFor(node, "run", "dev"); got != "pnpm run dev" {
		t.Errorf("node run target = %q", got)
	}
	if got := commandFor(node, "test", "src/a b.test.ts"); got != "pnpm test 'src/a b.test.ts'" {
		t.Errorf("test target quoting = %q", got)
	}
	mk := Profile{Ecosystem: "make", Commands: map[string]string{"run": "make run"}}
	if got := commandFor(mk, "run", "serve"); got != "make serve" {
		t.Errorf("make run target = %q", got)
	}
	if got := commandFor(Profile{}, "lint", "x"); got != "" {
		t.Errorf("no command must stay empty, got %q", got)
	}
}

// TestProjectTestFailureIsParsed runs go test for real on a failing package.
func TestProjectTestFailureIsParsed(t *testing.T) {
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
	res := runTool(t, r, "project", newTestEnv(dir, nil), map[string]any{"action": "test"})

	if !res.IsError || res.Meta["passed"] != false {
		t.Fatalf("expected a failing test run: %+v", res)
	}
	if !strings.Contains(res.Output, "— failed (exit 1)") {
		t.Errorf("verdict missing from output:\n%s", res.Output)
	}
	failed, _ := res.Meta["failed"].([]string)
	if len(failed) != 1 || failed[0] != "TestAdd" {
		t.Errorf("failed tests = %v, want [TestAdd]", failed)
	}
	diags, _ := res.Meta["errors"].([]Diagnostic)
	if len(diags) == 0 {
		t.Fatalf("no diagnostics parsed:\n%s", res.Output)
	}
	d := diags[0]
	if d.File != "add_test.go" || d.Line != 7 || !strings.Contains(d.Msg, "want 4") {
		t.Errorf("diagnostic = %+v, want add_test.go:7 with 'want 4'", d)
	}
}

func TestProjectTestPass(t *testing.T) {
	skipOnWindows(t)
	t.Setenv("GOTOOLCHAIN", "local")
	dir := fixture(t, map[string]string{
		"go.mod":     "module example.com/ok\n\ngo 1.21\n",
		"ok.go":      "package ok\n\nfunc One() int { return 1 }\n",
		"ok_test.go": "package ok\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {\n\tif One() != 1 {\n\t\tt.Fatal(\"no\")\n\t}\n}\n",
	})
	r, _ := newTestRegistry(t, Options{})
	res := runTool(t, r, "project", newTestEnv(dir, nil), map[string]any{"action": "test"})
	if res.IsError || res.Meta["passed"] != true || !strings.Contains(res.Output, "— passed") {
		t.Fatalf("passing project reported as failure: %+v", res)
	}
}

func TestProjectListAndNoCommand(t *testing.T) {
	dir := fixture(t, map[string]string{"go.mod": "module x\n"})
	r, _ := newTestRegistry(t, Options{})

	list := runTool(t, r, "project", newTestEnv(dir, nil), map[string]any{"action": "list"})
	if list.IsError || !strings.Contains(list.Output, "Detected: go") || !strings.Contains(list.Output, "go test ./...") {
		t.Fatalf("list output: %q", list.Output)
	}

	missing := runTool(t, r, "project", newTestEnv(t.TempDir(), nil), map[string]any{"action": "lint"})
	if !missing.IsError || missing.Meta["error"] != "no command" {
		t.Fatalf("expected no-command error: %+v", missing)
	}
	if !strings.Contains(missing.Output, "Use shell to run it manually") {
		t.Fatalf("no-command message should point to shell: %q", missing.Output)
	}

	bad := runTool(t, r, "project", newTestEnv(dir, nil), map[string]any{"action": "deploy"})
	if !bad.IsError {
		t.Fatalf("unknown action should be an error")
	}
}
