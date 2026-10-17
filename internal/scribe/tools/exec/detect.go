package exec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Profile is the toolchain detected for a project directory.
type Profile struct {
	// Ecosystem names the first matching row of the detection table, e.g. "go".
	// Empty when nothing matched.
	Ecosystem string
	// Manager is the JavaScript package manager (npm, pnpm, yarn, bun) for
	// node-based ecosystems; empty otherwise.
	Manager string
	// Commands maps an action (test, lint, typecheck, build, format, run) to
	// the shell command that performs it. Missing actions are absent.
	Commands map[string]string
}

// Actions lists the project actions in display order.
var Actions = []string{"test", "lint", "typecheck", "build", "format", "run"}

// dirView answers questions about the files in one project directory.
type dirView struct{ root string }

func (d dirView) has(name string) bool {
	_, err := os.Stat(filepath.Join(d.root, name))
	return err == nil
}

func (d dirView) any(names ...string) bool {
	for _, n := range names {
		if d.has(n) {
			return true
		}
	}
	return false
}

// glob reports whether any file in the directory matches pattern.
func (d dirView) glob(pattern string) bool {
	m, _ := filepath.Glob(filepath.Join(d.root, pattern))
	return len(m) > 0
}

// text returns a file's contents, lower-cased, or "" when unreadable.
func (d dirView) text(name string) string {
	b, err := os.ReadFile(filepath.Join(d.root, name))
	if err != nil {
		return ""
	}
	return strings.ToLower(string(b))
}

// detector is one row of the detection table. match decides whether the
// directory belongs to this ecosystem; build returns the commands. The first
// row that matches wins, so order matters.
type detector struct {
	name  string
	match func(d dirView) bool
	build func(d dirView) (manager string, cmds map[string]string)
}

// detectors is the detection table. Rows are tried in order; the first match
// wins, and nothing walks up the tree except the JavaScript lockfile search.
var detectors = []detector{
	{"bun",
		func(d dirView) bool { return d.any("bun.lock", "bun.lockb") },
		func(d dirView) (string, map[string]string) {
			return "bun", nodeCommands(d, "bun")
		}},
	{"deno",
		func(d dirView) bool { return d.any("deno.json", "deno.jsonc", "deno.lock") },
		func(d dirView) (string, map[string]string) {
			return "", map[string]string{
				"test": "deno test", "lint": "deno lint", "typecheck": "deno check .", "format": "deno fmt",
			}
		}},
	{"node",
		func(d dirView) bool { return d.has("package.json") },
		func(d dirView) (string, map[string]string) {
			pm := jsManager(d.root)
			return pm, nodeCommands(d, pm)
		}},
	{"rust",
		func(d dirView) bool { return d.has("Cargo.toml") },
		func(d dirView) (string, map[string]string) {
			return "", map[string]string{
				"test": "cargo test", "lint": "cargo clippy", "typecheck": "cargo check",
				"build": "cargo build", "format": "cargo fmt", "run": "cargo run",
			}
		}},
	{"go",
		func(d dirView) bool { return d.has("go.mod") },
		func(d dirView) (string, map[string]string) {
			lint := "go vet ./..."
			if d.glob(".golangci*") {
				lint = "golangci-lint run ./..."
			}
			// The checks compile without writing: with ./... matching a
			// single main package, go build writes the program into the
			// project, where a later "git add -A" commits it.
			check := "go build -o " + os.DevNull + " ./..."
			return "", map[string]string{
				"test": "go test ./...", "lint": lint, "typecheck": check,
				"build": check, "format": "gofmt -w .",
			}
		}},
	{"python",
		func(d dirView) bool {
			return d.any("pyproject.toml", "setup.py", "setup.cfg", "requirements.txt", "Pipfile")
		},
		pythonCommands},
	{"dotnet",
		func(d dirView) bool { return d.any("global.json") || d.glob("*.csproj") || d.glob("*.sln") },
		func(d dirView) (string, map[string]string) {
			return "", map[string]string{
				"test": "dotnet test", "lint": "dotnet format --verify-no-changes",
				"typecheck": "dotnet build", "build": "dotnet build", "format": "dotnet format",
			}
		}},
	{"php",
		func(d dirView) bool { return d.has("composer.json") },
		func(d dirView) (string, map[string]string) {
			c := map[string]string{"test": "vendor/bin/phpunit"}
			if d.any("phpstan.neon", "phpstan.neon.dist") {
				c["typecheck"] = "vendor/bin/phpstan analyse"
			} else if d.has("psalm.xml") {
				c["typecheck"] = "vendor/bin/psalm"
			}
			if strings.Contains(d.text("composer.json"), "laravel/pint") {
				c["lint"] = "vendor/bin/pint --test"
				c["format"] = "vendor/bin/pint"
			}
			return "", c
		}},
	{"swift",
		func(d dirView) bool { return d.has("Package.swift") },
		func(d dirView) (string, map[string]string) {
			c := map[string]string{"test": "swift test", "build": "swift build", "typecheck": "swift build"}
			if d.has(".swiftlint.yml") {
				c["lint"] = "swiftlint"
			}
			if d.has(".swiftformat") {
				c["format"] = "swiftformat ."
			}
			return "", c
		}},
	{"xcode",
		func(d dirView) bool { return d.glob("*.xcodeproj") || d.glob("*.xcworkspace") },
		func(d dirView) (string, map[string]string) {
			return "", map[string]string{
				"test": "xcodebuild test", "build": "xcodebuild build", "typecheck": "xcodebuild build",
			}
		}},
	{"dart",
		func(d dirView) bool { return d.has("pubspec.yaml") },
		func(d dirView) (string, map[string]string) {
			spec := d.text("pubspec.yaml")
			c := map[string]string{"lint": "dart analyze", "typecheck": "dart analyze", "format": "dart format ."}
			if strings.Contains(spec, "sdk: flutter") {
				c["test"] = "flutter test"
				c["build"] = "flutter build"
			} else {
				c["test"] = "dart test"
			}
			return "", c
		}},
	{"elixir",
		func(d dirView) bool { return d.has("mix.exs") },
		func(d dirView) (string, map[string]string) {
			text := d.text("mix.exs")
			c := map[string]string{"test": "mix test", "build": "mix compile", "format": "mix format"}
			if strings.Contains(text, "credo") {
				c["lint"] = "mix credo"
			}
			if strings.Contains(text, "dialyxir") {
				c["typecheck"] = "mix dialyzer"
			}
			return "", c
		}},
	{"ruby",
		func(d dirView) bool { return d.has("Gemfile") },
		func(d dirView) (string, map[string]string) {
			test := "bundle exec rake test"
			switch {
			case d.has("spec"):
				test = "bundle exec rspec"
			case d.has("bin/rails"):
				test = "bin/rails test"
			}
			return "", map[string]string{
				"test": test, "lint": "bundle exec rubocop", "format": "bundle exec rubocop -a",
			}
		}},
	{"gradle",
		func(d dirView) bool {
			return d.any("gradlew", "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts")
		},
		gradleCommands},
	{"maven",
		func(d dirView) bool { return d.any("pom.xml", "mvnw") },
		func(d dirView) (string, map[string]string) {
			r := "mvn"
			if d.has("mvnw") {
				r = "./mvnw"
			}
			return "", map[string]string{
				"test": r + " test", "lint": r + " checkstyle:check", "typecheck": r + " compile", "build": r + " package",
			}
		}},
	{"cmake",
		func(d dirView) bool { return d.has("CMakeLists.txt") },
		func(d dirView) (string, map[string]string) {
			return "", map[string]string{
				"test": "ctest --test-dir build", "typecheck": "cmake --build build", "build": "cmake --build build",
			}
		}},
	{"make",
		func(d dirView) bool { return d.any("Makefile", "makefile", "GNUmakefile") },
		makeCommands},
	{"zig",
		func(d dirView) bool { return d.has("build.zig") },
		func(d dirView) (string, map[string]string) {
			return "", map[string]string{
				"test": "zig build test", "build": "zig build", "typecheck": "zig build",
				"lint": "zig fmt --check src/", "format": "zig fmt src/",
			}
		}},
	{"haskell",
		func(d dirView) bool { return d.has("stack.yaml") },
		func(d dirView) (string, map[string]string) {
			return "", map[string]string{
				"test": "stack test", "build": "stack build", "typecheck": "stack build", "lint": "hlint .",
			}
		}},
	{"scala",
		func(d dirView) bool { return d.has("build.sbt") },
		func(d dirView) (string, map[string]string) {
			c := map[string]string{"test": "sbt test", "build": "sbt compile", "typecheck": "sbt compile"}
			if d.has(".scalafmt.conf") {
				c["format"] = "sbt scalafmt"
			}
			return "", c
		}},
	{"clojure",
		func(d dirView) bool { return d.any("deps.edn", "project.clj") },
		func(d dirView) (string, map[string]string) {
			c := map[string]string{"lint": "clj-kondo --lint src"}
			if d.has("project.clj") {
				c["test"] = "lein test"
				c["build"] = "lein uberjar"
			} else {
				c["test"] = "clojure -M:test"
			}
			return "", c
		}},
}

// Detect returns the toolchain profile for dir. It looks only at files in dir
// itself (plus the JavaScript lockfile search). An unknown directory yields a
// Profile with an empty Ecosystem and no commands.
func Detect(dir string) Profile {
	d := dirView{root: dir}
	for _, det := range detectors {
		if !det.match(d) {
			continue
		}
		mgr, cmds := det.build(d)
		if cmds == nil {
			cmds = map[string]string{}
		}
		return Profile{Ecosystem: det.name, Manager: mgr, Commands: cmds}
	}
	return Profile{Commands: map[string]string{}}
}

// Ecosystems lists every ecosystem the detection table knows, in table order.
func Ecosystems() []string {
	out := make([]string, 0, len(detectors))
	for _, det := range detectors {
		out = append(out, det.name)
	}
	return out
}

// jsManager finds the JavaScript package manager. It checks dir and up to four
// parents for pnpm or yarn lockfiles, and defaults to npm.
func jsManager(dir string) string {
	cur := dir
	for i := 0; i < 5; i++ {
		d := dirView{root: cur}
		switch {
		case d.has("pnpm-lock.yaml"):
			return "pnpm"
		case d.has("yarn.lock"):
			return "yarn"
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return "npm"
}

// packageScripts returns the "scripts" map of package.json.
func packageScripts(d dirView) map[string]string {
	raw, err := os.ReadFile(filepath.Join(d.root, "package.json"))
	if err != nil {
		return nil
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(raw, &pkg) != nil {
		return nil
	}
	return pkg.Scripts
}

// nodeCommands builds the commands for a JavaScript project. Scripts in
// package.json win; config files supply linters and formatters otherwise.
func nodeCommands(d dirView, pm string) map[string]string {
	scripts := packageScripts(d)
	runScript := func(name string) string {
		if pm == "npm" {
			return "npm run " + name
		}
		return pm + " run " + name
	}
	c := map[string]string{}
	if pm == "bun" {
		c["test"] = "bun test"
	} else if scripts["test"] != "" {
		c["test"] = pm + " test"
	}
	switch {
	case scripts["lint"] != "":
		c["lint"] = runScript("lint")
	case d.glob("biome.json*"):
		c["lint"] = "npx biome check ."
	case d.any(".oxlintrc.json", "oxlint.json"):
		c["lint"] = "npx oxlint ."
	case d.glob(".eslintrc*") || d.glob("eslint.config.*"):
		c["lint"] = "npx eslint ."
	}
	switch {
	case scripts["typecheck"] != "":
		c["typecheck"] = runScript("typecheck")
	case d.has("tsconfig.json") && pm == "bun":
		c["typecheck"] = "bunx tsc --noEmit"
	case d.has("tsconfig.json"):
		c["typecheck"] = "npx tsc --noEmit"
	}
	if scripts["build"] != "" {
		c["build"] = runScript("build")
	}
	switch {
	case scripts["format"] != "":
		c["format"] = runScript("format")
	case d.glob("biome.json*"):
		c["format"] = "npx biome format --write ."
		// Biome's check formats and lints in one pass; the format action
		// prefers it (spec 06 9.1). FormatFile keeps the plain formatter.
		c["formatAndLint"] = "npx biome check --write ."
	case d.glob(".prettierrc*") || d.glob("prettier.config.*"):
		c["format"] = "npx prettier --write ."
	}
	switch {
	case scripts["start"] != "":
		c["run"] = runScript("start")
	case scripts["dev"] != "":
		c["run"] = runScript("dev")
	}
	return c
}

func pythonCommands(d dirView) (string, map[string]string) {
	prefix := ""
	switch {
	case d.has("uv.lock"):
		prefix = "uv run "
	case d.has("poetry.lock"):
		prefix = "poetry run "
	case d.has("Pipfile.lock"):
		prefix = "pipenv run "
	}
	pyproject := d.text("pyproject.toml")
	ruff := d.any("ruff.toml", ".ruff.toml") || strings.Contains(pyproject, "[tool.ruff")
	c := map[string]string{"test": prefix + "pytest"}
	switch {
	case ruff:
		c["lint"] = prefix + "ruff check ."
	case d.has(".flake8") || strings.Contains(d.text("setup.cfg"), "[flake8]"):
		c["lint"] = prefix + "flake8 ."
	}
	if d.any("pyrightconfig.json") || strings.Contains(pyproject, "[tool.pyright]") {
		c["typecheck"] = prefix + "pyright"
	} else {
		c["typecheck"] = prefix + "mypy ."
	}
	switch {
	case ruff:
		c["format"] = prefix + "ruff format ."
	case strings.Contains(pyproject, "[tool.black]"):
		c["format"] = prefix + "black ."
	}
	return "", c
}

func gradleCommands(d dirView) (string, map[string]string) {
	r := "gradle"
	if d.has("gradlew") {
		r = "./gradlew"
	}
	build := d.text("build.gradle") + d.text("build.gradle.kts")
	c := map[string]string{"test": r + " test", "build": r + " build"}
	switch {
	case strings.Contains(build, "spotless"):
		c["lint"] = r + " spotlessCheck"
		c["format"] = r + " spotlessApply"
	case strings.Contains(build, "ktlint"):
		c["lint"] = r + " ktlintCheck"
	default:
		c["lint"] = r + " check"
	}
	if strings.Contains(build, "kotlin") {
		c["typecheck"] = r + " compileKotlin"
	} else {
		c["typecheck"] = r + " compileJava"
	}
	return "", c
}

// makeTargetRe matches a target definition line in a Makefile.
var makeTargetRe = regexp.MustCompile(`^([A-Za-z0-9_][A-Za-z0-9_.-]*)\s*:([^=]|$)`)

// makeCommands maps well-known Makefile targets to actions. build falls back
// to a bare make, which runs the default target.
func makeCommands(d dirView) (string, map[string]string) {
	targets := map[string]bool{}
	for _, name := range []string{"Makefile", "makefile", "GNUmakefile"} {
		b, err := os.ReadFile(filepath.Join(d.root, name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if m := makeTargetRe.FindStringSubmatch(line); m != nil {
				targets[m[1]] = true
			}
		}
		break
	}
	c := map[string]string{}
	for _, action := range []string{"test", "lint", "typecheck", "format", "run"} {
		if targets[action] {
			c[action] = "make " + action
		}
	}
	if targets["build"] {
		c["build"] = "make build"
	} else {
		c["build"] = "make"
	}
	return "", c
}
