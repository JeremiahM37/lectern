package replay

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// TestFiles classifies which test files (if any) a PR touched, cheaply — from
// the file list alone, no content or diff needed. This is the first of two
// steps a full "run exactly these tests" command takes (see TestCommand):
// the import pipeline's cheap filter pass calls only this one, so deciding
// "is there a runnable check here at all" never requires fetching a diff.
//
// Detection order is Go, then Python, then JS/TS: a PR mixing test
// languages (rare) gets one language's command rather than an attempt to
// merge incompatible test runners into one shell line.
func TestFiles(files []FileStat) (lang string, matched []string) {
	var goFiles, pyFiles, jsFiles []string
	for _, f := range files {
		switch {
		case strings.HasSuffix(f.Path, "_test.go"):
			goFiles = append(goFiles, f.Path)
		case isPytestFile(f.Path):
			pyFiles = append(pyFiles, f.Path)
		case isJSTestFile(f.Path):
			jsFiles = append(jsFiles, f.Path)
		}
	}
	switch {
	case len(goFiles) > 0:
		sort.Strings(goFiles)
		return "go", goFiles
	case len(pyFiles) > 0:
		sort.Strings(pyFiles)
		return "python", pyFiles
	case len(jsFiles) > 0:
		sort.Strings(jsFiles)
		return "js", jsFiles
	default:
		return "", nil
	}
}

func isPytestFile(p string) bool {
	base := path.Base(p)
	return strings.HasSuffix(p, ".py") && (strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py"))
}

func isJSTestFile(p string) bool {
	for _, suf := range []string{
		".test.js", ".test.ts", ".test.jsx", ".test.tsx",
		".spec.js", ".spec.ts", ".spec.jsx", ".spec.tsx",
	} {
		if strings.HasSuffix(p, suf) {
			return true
		}
	}
	return false
}

// TestCommand builds the "run exactly these tests" command once a diff is
// available. Only Go uses diff at all — to name the exact Test funcs the PR
// added/changed (goAddedTestFuncRe, matched against the diff's OWN added
// lines) — so detection never needs a second fetch beyond the reference diff
// callers already retrieve for scoring. Python and JS run the whole matched
// file(s), which is what "exactly those tests" means for runners that are
// already file-scoped.
func TestCommand(lang string, files []string, diff string) string {
	sorted := append([]string(nil), files...)
	sort.Strings(sorted)
	switch lang {
	case "go":
		return goTestCommand(sorted, diff)
	case "python":
		return "pytest " + strings.Join(quoteAll(sorted), " ")
	case "js":
		return "npx jest " + strings.Join(quoteAll(sorted), " ")
	default:
		return ""
	}
}

var goAddedTestFuncRe = regexp.MustCompile(`(?m)^\+func\s+(Test[A-Za-z0-9_]*)\s*\(`)

// goTestCommand groups touched test files by package directory (`go test`
// needs a package, not a file) and, when the reference diff is available,
// narrows to exactly the Test funcs it added via -run — a package that
// doesn't have a matching name simply runs zero tests under -run rather
// than failing, so one command safely spans every touched package.
func goTestCommand(files []string, diff string) string {
	dirSet := map[string]bool{}
	for _, f := range files {
		dir := path.Dir(f)
		if dir == "." {
			dir = ""
		}
		dirSet["./"+strings.TrimPrefix(dir+"/...", "/")] = true
	}
	dirs := make([]string, 0, len(dirSet))
	for d := range dirSet {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	if len(dirs) == 0 {
		dirs = []string{"./..."}
	}
	cmd := "go test " + strings.Join(dirs, " ")

	nameSet := map[string]bool{}
	for _, m := range goAddedTestFuncRe.FindAllStringSubmatch(diff, -1) {
		nameSet[m[1]] = true
	}
	if len(nameSet) > 0 {
		names := make([]string, 0, len(nameSet))
		for n := range nameSet {
			names = append(names, n)
		}
		sort.Strings(names)
		cmd += " -run " + shellQuote("^("+strings.Join(names, "|")+")$")
	}
	return cmd
}

func quoteAll(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = shellQuote(p)
	}
	return out
}

// shellQuote is a minimal single-quote shell escape (the same rule
// internal/shellq.Quote applies), kept local so this package stays
// dependency-free and unit-testable without importing the API layer's
// executor plumbing.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
