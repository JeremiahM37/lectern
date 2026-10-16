package repomap

// reexportKind marks an import that is a re-export ("export * from", "export
// { a } from"). Only the JS/TS import rules produce it.
const reexportKind = "reexport"

// isBarrel reports whether a file is a barrel: it re-exports at least one
// module and defines fewer symbols than it has re-export statements. An index
// file that only re-exports is the typical case.
func isBarrel(fi *fileInfo) bool {
	re := 0
	for _, im := range fi.Imports {
		if im.Kind == reexportKind {
			re++
		}
	}
	return re > 0 && len(fi.Symbols) < re
}

// reexportSpecs returns the module specifiers a barrel re-exports, in source
// order and without duplicates.
func reexportSpecs(fi *fileInfo) []string {
	var out []string
	seen := map[string]bool{}
	for _, im := range fi.Imports {
		if im.Kind == reexportKind && !seen[im.Spec] {
			seen[im.Spec] = true
			out = append(out, im.Spec)
		}
	}
	return out
}
