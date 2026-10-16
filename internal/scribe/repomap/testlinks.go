package repomap

import (
	"path"
	"sort"
	"strings"
)

// Test linking (spec 4.4, step 5). A test file is paired with the source file
// it covers by name: foo_test.go covers foo.go, foo.test.ts and foo.spec.ts
// cover foo.ts, test_foo.py and foo_test.py cover foo.py. Files under a
// __tests__ directory are tests too. When several source files share the
// name, the one nearest the test (longest common directory) wins.

// testSubjectBase returns the source base name that a test base name covers.
// ok is false when base does not look like a test file.
func testSubjectBase(base string, inTestsDir bool) (string, bool) {
	ext := path.Ext(base)
	name := strings.TrimSuffix(base, ext)
	switch ext {
	case ".go":
		if strings.HasSuffix(name, "_test") && len(name) > len("_test") {
			return strings.TrimSuffix(name, "_test") + ext, true
		}
	case ".py":
		if strings.HasPrefix(name, "test_") && len(name) > len("test_") {
			return strings.TrimPrefix(name, "test_") + ext, true
		}
		if strings.HasSuffix(name, "_test") && len(name) > len("_test") {
			return strings.TrimSuffix(name, "_test") + ext, true
		}
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts":
		for _, suf := range []string{".test", ".spec"} {
			if strings.HasSuffix(name, suf) && len(name) > len(suf) {
				return strings.TrimSuffix(name, suf) + ext, true
			}
		}
		if inTestsDir {
			return base, true
		}
	}
	return "", false
}

// linkTests maps each test file in paths to its source file ("" when none).
// Only files with a matching source file are present in the result.
func linkTests(paths []string) map[string]string {
	sources := map[string][]string{} // base name -> source paths
	type testFile struct{ rel, subject string }
	var tests []testFile
	for _, p := range paths {
		base := path.Base(p)
		inTests := strings.Contains("/"+p, "/__tests__/")
		if sub, ok := testSubjectBase(base, inTests); ok {
			tests = append(tests, testFile{rel: p, subject: sub})
			continue
		}
		sources[base] = append(sources[base], p)
	}
	out := map[string]string{}
	for _, t := range tests {
		cands := sources[t.subject]
		best, bestLen := "", -1
		for _, c := range cands {
			if n := commonDirLen(path.Dir(t.rel), path.Dir(c)); n > bestLen || (n == bestLen && c < best) {
				best, bestLen = c, n
			}
		}
		if best != "" {
			out[t.rel] = best
		}
	}
	return out
}

// commonDirLen counts the leading path segments two directories share.
func commonDirLen(a, b string) int {
	as, bs := strings.Split(a, "/"), strings.Split(b, "/")
	n := 0
	for n < len(as) && n < len(bs) && as[n] == bs[n] {
		n++
	}
	return n
}

// TestSubject returns the source file that the test file covers, or "" when
// file is not a test file or no source file matches its name.
func (m *Map) TestSubject(file string) string {
	m.mu.RLock()
	paths := append([]string(nil), m.paths...)
	m.mu.RUnlock()
	return linkTests(paths)[m.relPath(file)]
}

// TestsOf returns the test files that cover file, sorted.
func (m *Map) TestsOf(file string) []string {
	rel := m.relPath(file)
	m.mu.RLock()
	paths := append([]string(nil), m.paths...)
	m.mu.RUnlock()
	var out []string
	for t, s := range linkTests(paths) {
		if s == rel {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}
