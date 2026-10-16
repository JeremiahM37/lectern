package lsp

import (
	"io/fs"
	"path/filepath"
	"sort"
)

const (
	// maxDetectDirs caps the directories DetectLanguages visits, so a huge
	// tree cannot hold up startup.
	maxDetectDirs = 5000
	// minSourceFiles is how many source files of a language make it count
	// when no marker file names it, so one stray script does not.
	minSourceFiles = 3
)

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
			for lang, spec := range serverSpecs {
				if !found[lang] && hasMarker(path, spec.Markers) {
					found[lang] = true
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
