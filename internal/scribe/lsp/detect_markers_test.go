package lsp

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// The spec's marker list (06 §6.1) must all be in the shared table, so a
// project is recognised by any of them.
func TestProjectMarkerTableCoversSpecList(t *testing.T) {
	want := []string{"tsconfig.json", "jsconfig.json", "package.json", "pyproject.toml", "setup.py",
		"go.mod", "Cargo.toml", "pom.xml", "build.gradle", "build.gradle.kts", "Gemfile",
		"composer.json", "Package.swift", "build.sbt", "mix.exs", "pubspec.yaml", "build.zig",
		"Makefile", "CMakeLists.txt"}
	have := map[string]bool{}
	for _, m := range projectMarkers {
		have[m.pattern] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("marker %s is missing from projectMarkers", w)
		}
	}
}

// Each marker alone is enough to detect its language, through the same table
// the per-file root search uses.
func TestEachMarkerDetectsItsLanguage(t *testing.T) {
	cases := map[string]string{
		"go.mod":           "go",
		"package.json":     "typescript",
		"pyproject.toml":   "python",
		"Cargo.toml":       "rust",
		"pom.xml":          "java",
		"build.gradle.kts": "java",
		"Gemfile":          "ruby",
		"composer.json":    "php",
		"build.zig":        "zig",
		"mix.exs":          "elixir",
		"CMakeLists.txt":   "cpp",
		"app.csproj":       "csharp",
	}
	for marker, lang := range cases {
		dir := t.TempDir()
		mustWrite(t, filepath.Join(dir, marker), "x\n")
		if got := DetectLanguages(dir); !containsString(got, lang) {
			t.Errorf("marker %s: want language %s, got %v", marker, lang, got)
		}
	}
}

// Markers for languages without a catalogued server are kept in the table but
// name no language Scribe can start, so detection drops them.
func TestUncataloguedMarkersDoNotNameALanguage(t *testing.T) {
	for _, marker := range []string{"Package.swift", "build.sbt", "pubspec.yaml"} {
		dir := t.TempDir()
		mustWrite(t, filepath.Join(dir, marker), "x\n")
		if got := DetectLanguages(dir); len(got) != 0 {
			t.Errorf("marker %s should name no catalogued language, got %v", marker, got)
		}
	}
}

func TestMarkerPatternsMatchGlobs(t *testing.T) {
	dir := t.TempDir()
	if markerPresent(dir, "*.sln") {
		t.Fatal("empty directory has no solution file")
	}
	mustWrite(t, filepath.Join(dir, "App.sln"), "x\n")
	if !markerPresent(dir, "*.sln") {
		t.Fatal("a .sln file should match the *.sln marker")
	}
}

// The root search for a file uses the same table, so the nearest marker wins
// and a directory with no marker falls back to the workdir.
func TestRootSearchUsesSharedMarkers(t *testing.T) {
	workdir := t.TempDir()
	sub := filepath.Join(workdir, "web")
	mustWrite(t, filepath.Join(sub, "package.json"), "{}")
	m := NewManager(workdir)

	abs := filepath.Join(sub, "src", "a.ts")
	if got := m.rootFor(serverSpecs["typescript"], abs); got != sub {
		t.Errorf("typescript root = %s, want %s", got, sub)
	}
	if got := m.rootFor(serverSpecs["rust"], abs); got != workdir {
		t.Errorf("rust root = %s, want the workdir %s (no Cargo.toml above the file)", got, workdir)
	}
}

func TestMarkersForKeepsCppRootMarkers(t *testing.T) {
	got := markersFor("cpp")
	for _, want := range []string{"Makefile", "CMakeLists.txt", "compile_commands.json", ".clangd"} {
		if !containsString(got, want) {
			t.Errorf("cpp markers %v should include %s", got, want)
		}
	}
}

// The refactor tool's schema lists the new actions, and only action and path
// are required, so format and organize_imports need no start_line.
func TestRefactorSchemaRequiresOnlyActionAndPath(t *testing.T) {
	var s struct {
		Required []string `json:"required"`
	}
	spec := (&refactorTool{}).Spec()
	if err := json.Unmarshal(spec.Schema, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Required) != 2 || s.Required[0] != "action" || s.Required[1] != "path" {
		t.Fatalf("refactor requires %v, want [action path]", s.Required)
	}
	for _, name := range []string{"format", "organize_imports"} {
		if !containsString(refactorActionNames, name) {
			t.Errorf("refactor action %s is not accepted", name)
		}
	}
}
