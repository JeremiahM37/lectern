package replay

import "strings"

// ChangedLines sums additions+deletions across every file a PR touched —
// cheap (no diff fetch: gh's `files` field already carries per-file counts,
// and the git-log fallback fills the same shape from --numstat) — and is
// exactly the number the size cap filters on.
func ChangedLines(files []FileStat) int {
	n := 0
	for _, f := range files {
		n += f.Additions + f.Deletions
	}
	return n
}

var docPathSuffixes = []string{".md", ".mdx", ".rst", ".txt", ".adoc"}

var docPathNames = []string{
	"license", "changelog", "readme", "notice", "authors",
	"contributing", "code_of_conduct", "codeowners",
}

// IsDocsOnly reports whether every file a PR touched is documentation — a
// markdown/rst/txt/adoc file, anything under a doc/ or docs/ directory, or a
// well-known unversioned-code file like LICENSE/CHANGELOG/README (with or
// without an extension) — so a PR that only reworded a doc never
// manufactures a case with nothing for an agent to actually build. An empty
// file list is not "docs-only": it means gh/git gave us nothing to judge, a
// different problem the caller handles separately.
func IsDocsOnly(files []FileStat) bool {
	if len(files) == 0 {
		return false
	}
	for _, f := range files {
		if !isDocPath(f.Path) {
			return false
		}
	}
	return true
}

func isDocPath(p string) bool {
	lower := strings.ToLower(p)
	for _, seg := range strings.Split(lower, "/") {
		if seg == "docs" || seg == "doc" || seg == "documentation" {
			return true
		}
	}
	for _, suf := range docPathSuffixes {
		if strings.HasSuffix(lower, suf) {
			return true
		}
	}
	base := lower
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndex(base, "."); i >= 0 {
		base = base[:i]
	}
	for _, name := range docPathNames {
		if base == name {
			return true
		}
	}
	return false
}
