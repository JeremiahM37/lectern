package lsp

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// maxDetectDirs caps the directories DetectLanguages visits, so a huge
	// tree cannot hold up startup.
	maxDetectDirs = 5000
	// minSourceFiles is how many source files of a language make it count
	// when no marker file names it, so one stray script does not.
	minSourceFiles = 3
)

// projectMarker is one project marker file (or glob) and the language it
// marks. A marker names the project; it does not start a server by itself.
type projectMarker struct {
	pattern  string
	language string
}

// projectMarkers is the one table of project markers. Detection and the
// per-file search for a project root both read it, so a marker is added here
// and nowhere else. Markers for languages without a catalogued server (Swift,
// Scala, Dart) are kept: DetectLanguages drops them, and the table stays a
// faithful copy of the list the spec gives.
var projectMarkers = []projectMarker{
	{"go.mod", "go"},
	{"go.work", "go"},
	{"tsconfig.json", "typescript"},
	{"jsconfig.json", "typescript"},
	{"package.json", "typescript"},
	{"pyproject.toml", "python"},
	{"setup.py", "python"},
	{"setup.cfg", "python"},
	{"pyrightconfig.json", "python"},
	{"requirements.txt", "python"},
	{"Cargo.toml", "rust"},
	{"pom.xml", "java"},
	{"build.gradle", "java"},
	{"build.gradle.kts", "java"},
	{"Gemfile", "ruby"},
	{"composer.json", "php"},
	{"Package.swift", "swift"},
	{"build.sbt", "scala"},
	{"mix.exs", "elixir"},
	{"pubspec.yaml", "dart"},
	{"build.zig", "zig"},
	{"Makefile", "cpp"},
	{"CMakeLists.txt", "cpp"},
	{"compile_commands.json", "cpp"},
	{".clangd", "cpp"},
	{"*.sln", "csharp"},
	{"*.csproj", "csharp"},
	{".luarc.json", "lua"},
	{".luarc.jsonc", "lua"},
}

// markersFor lists the marker patterns that mark a project of language.
func markersFor(language string) []string {
	var out []string
	for _, m := range projectMarkers {
		if m.language == language {
			out = append(out, m.pattern)
		}
	}
	return out
}

// DetectLanguages lists the languages a project uses, sorted by name. A
// language counts when a marker file (go.mod, tsconfig.json, Cargo.toml and
// so on) is found in root or below it, or when it has at least
// minSourceFiles source files. Hidden and dependency directories are skipped.
// It returns what it found so far when the directory cap is reached.
//
// Nothing starts a server from this; a caller decides what to warm up.
func DetectLanguages(root string) []string {
	found := map[string]bool{}
	counts := map[string]int{}
	dirs := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			dirs++
			if dirs > maxDetectDirs {
				return fs.SkipAll
			}
			for _, m := range projectMarkers {
				if !found[m.language] && markerPresent(path, m.pattern) {
					found[m.language] = true
				}
			}
			return nil
		}
		if info, ok := languageForPath(path); ok {
			counts[info.key]++
		}
		return nil
	})
	for lang, n := range counts {
		if n >= minSourceFiles {
			found[lang] = true
		}
	}
	out := make([]string, 0, len(found))
	for lang := range found {
		if _, known := serverSpecs[lang]; known {
			out = append(out, lang)
		}
	}
	sort.Strings(out)
	return out
}

// markerPresent reports whether dir holds a file matching pattern, which is
// either a plain file name or a glob such as *.csproj.
func markerPresent(dir, pattern string) bool {
	if strings.ContainsAny(pattern, "*?[") {
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		return len(matches) > 0
	}
	_, err := os.Stat(filepath.Join(dir, pattern))
	return err == nil
}

// hasAnyMarker reports whether dir holds any of the marker patterns.
func hasAnyMarker(dir string, patterns []string) bool {
	for _, p := range patterns {
		if markerPresent(dir, p) {
			return true
		}
	}
	return false
}
