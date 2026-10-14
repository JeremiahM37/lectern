// Package conflicts predicts semantic conflicts between concurrent agent
// sessions working one repository. File-level overlap (internal/awareness)
// misses about half of real conflicts: two changes can touch different files
// and still break each other, because one changes what a function does and
// the other adds a new caller of it. This package reads each side's live diff,
// records which symbols it defines or changes and which identifiers its added
// lines use, and compares the two.
//
// Everything here is pure: no git, no store, no I/O. The Scanner in scan.go
// is the part that gathers diffs; this file and predict.go decide what they
// mean. Predictions are advisory. Nothing here blocks or reorders an agent.
package conflicts

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// Footprint is what one side of a potential conflict changes, reduced from a
// unified diff.
type Footprint struct {
	// Files are the paths the diff touches, in diff order.
	Files []string
	// Defined maps file -> symbols the diff changes or adds in that file.
	// A method is qualified as "Receiver.Name" so two types' Close methods
	// never look like one symbol; everything else is the bare name.
	Defined map[string][]string
	// Reshaped maps file -> existing symbols whose definition line the diff
	// removes or rewrites: a signature change, a rename or a deletion. Unlike
	// a body edit or a brand-new symbol, this can break code someone else
	// writes against the old definition, so it is what a semantic link needs.
	Reshaped map[string][]string
	// Referenced is the set of identifiers used on added lines, minus
	// language keywords. It is what the other side's definitions are matched
	// against.
	Referenced map[string]bool
}

// symbolRule is one definition pattern. Each pattern has exactly one capture
// group for the name; a Go method pattern has two (receiver, name).
type symbolRule struct {
	re       *regexp.Regexp
	receiver bool
}

var (
	goRules = []symbolRule{
		{regexp.MustCompile(`^\s*func\s*\(\s*\w+\s+\*?(\w+)[^)]*\)\s*(\w+)\s*[\[(]`), true},
		{regexp.MustCompile(`^\s*func\s+(\w+)\s*[\[(]`), false},
		{regexp.MustCompile(`^\s*type\s+(\w+)\s`), false},
	}
	pyRules = []symbolRule{
		{regexp.MustCompile(`^\s*def\s+(\w+)`), false},
		{regexp.MustCompile(`^\s*class\s+(\w+)`), false},
	}
	tsRules = []symbolRule{
		{regexp.MustCompile(`\bfunction\s+(\w+)`), false},
		{regexp.MustCompile(`\bclass\s+(\w+)`), false},
		{regexp.MustCompile(`\bexport\s+const\s+(\w+)\s*=`), false},
	}
	rustRules = []symbolRule{
		{regexp.MustCompile(`\bfn\s+(\w+)`), false},
	}
)

// symbolSource reports whether a file's symbols mean anything for a semantic
// link. Tests and generated or vendored code do not: a test that changes or
// calls a function does not change what production callers get, and a
// minified bundle "defines" whatever its minifier emitted. Replaying the
// owner's own merge history showed those two sources produced most false
// alarms. Such files still count for file-level (textual) overlap.
func symbolSource(file string) bool {
	f := strings.ToLower(file)
	base := path.Base(f)
	for _, dir := range []string{"vendor/", "node_modules/", "dist/", "/assets/", "e2e/", "tests/", "testdata/", "__tests__/"} {
		if strings.HasPrefix(f, dir) || strings.Contains(f, "/"+dir) {
			return false
		}
	}
	switch {
	case strings.HasSuffix(base, "_test.go"), strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"),
		strings.HasSuffix(base, "_test.py"), strings.Contains(base, ".test."), strings.Contains(base, ".spec."),
		strings.Contains(base, ".min."), strings.HasSuffix(base, ".pb.go"), strings.Contains(base, "generated"):
		return false
	}
	return true
}

// rulesFor picks the definition patterns for a file by extension. An unknown
// extension gets no symbols: guessing a language would produce symbols that
// are noise, and file-level overlap still covers the file.
func rulesFor(file string) []symbolRule {
	if !symbolSource(file) {
		return nil
	}
	switch strings.ToLower(path.Ext(file)) {
	case ".go":
		return goRules
	case ".py":
		return pyRules
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		return tsRules
	case ".rs":
		return rustRules
	}
	return nil
}

// definitionsIn returns the symbols a single line (or hunk context string)
// defines, qualified as described on Footprint.Defined.
func definitionsIn(file, line string) []string {
	var out []string
	for _, rule := range rulesFor(file) {
		m := rule.re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if rule.receiver {
			out = append(out, m[1]+"."+m[2])
		} else {
			out = append(out, m[1])
		}
	}
	return out
}

var identRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// keywords are language keywords and very common built-ins. They are never
// "references" to a symbol someone else defined, so they are dropped before a
// match is attempted.
var keywords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`
		func return if else for range var const type struct interface package import
		go defer select switch case default break continue map chan fallthrough goto
		def class self None True False pass with as from lambda yield async await try
		except finally raise elif in is not and or del global nonlocal
		function let export new this typeof instanceof extends super void delete
		undefined null true false static public private protected readonly
		fn impl pub use mod mut ref where match loop while crate self Self enum trait
		dyn move unsafe extern
		string int bool byte float float64 error any nil`) {
		keywords[w] = true
	}
}

// ParseFootprint reads a unified diff (git diff output, possibly with several
// files) into a Footprint. It tolerates empty input and binary entries.
//
// Symbols come from two places. A hunk header's trailing context (git prints
// the enclosing function there) names the function the change sits in, which
// is how a change to a function body counts as changing that function. Added
// and removed lines that themselves define a symbol are counted too.
func ParseFootprint(unifiedDiff string) Footprint {
	fp := Footprint{
		Files:      []string{},
		Defined:    map[string][]string{},
		Reshaped:   map[string][]string{},
		Referenced: map[string]bool{},
	}
	seenFile := map[string]bool{}
	seenSym := map[string]map[string]bool{}
	addFile := func(name string) {
		if name != "" && !seenFile[name] {
			seenFile[name] = true
			fp.Files = append(fp.Files, name)
		}
	}
	addSym := func(file, sym string) {
		if seenSym[file] == nil {
			seenSym[file] = map[string]bool{}
		}
		if !seenSym[file][sym] {
			seenSym[file][sym] = true
			fp.Defined[file] = append(fp.Defined[file], sym)
		}
	}

	// A definition line that appears removed but is re-added verbatim is a
	// moved line, not a reshape; compare per file once the diff is read.
	removedDef := map[string]map[string]string{} // file -> sym -> removed line
	addedDef := map[string]map[string]string{}
	noteDef := func(m map[string]map[string]string, file, sym, line string) {
		if m[file] == nil {
			m[file] = map[string]string{}
		}
		m[file][sym] = strings.TrimSpace(line)
	}

	var file string
	for _, line := range strings.Split(unifiedDiff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			file = ""
			if i := strings.LastIndex(line, " b/"); i >= 0 {
				file = line[i+3:]
				addFile(file)
			}
		case strings.HasPrefix(line, "+++ "):
			// The +++ line names the new side. A deleted file reads
			// "+++ /dev/null", so the diff --git name is kept in that case.
			if p := strings.TrimPrefix(strings.TrimSpace(line[4:]), "b/"); p != "/dev/null" && p != "" {
				file = p
				addFile(file)
			}
		case strings.HasPrefix(line, "--- "):
			// Old-side header; the file is named by the +++ line or diff header.
		case strings.HasPrefix(line, "@@"):
			if file == "" {
				continue
			}
			// git puts the enclosing function after the closing @@.
			if i := strings.Index(line[2:], "@@"); i >= 0 {
				ctx := strings.TrimSpace(line[2+i+2:])
				for _, sym := range definitionsIn(file, ctx) {
					addSym(file, sym)
				}
			}
		case strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-"):
			if file == "" {
				continue
			}
			body := line[1:]
			for _, sym := range definitionsIn(file, body) {
				addSym(file, sym)
				if line[0] == '-' {
					noteDef(removedDef, file, sym, body)
				} else {
					noteDef(addedDef, file, sym, body)
				}
			}
			if line[0] == '+' && symbolSource(file) {
				for _, id := range identRe.FindAllString(body, -1) {
					if !keywords[id] {
						fp.Referenced[id] = true
					}
				}
			}
		}
	}
	for f, syms := range removedDef {
		for sym, old := range syms {
			if added, ok := addedDef[f][sym]; ok && added == old {
				continue
			}
			fp.Reshaped[f] = append(fp.Reshaped[f], sym)
		}
	}
	for f := range fp.Defined {
		sort.Strings(fp.Defined[f])
	}
	for f := range fp.Reshaped {
		sort.Strings(fp.Reshaped[f])
	}
	return fp
}

// bareName is the part of a symbol another side would write in code: the
// method name for "Receiver.Name", the symbol itself otherwise.
func bareName(sym string) string {
	if i := strings.LastIndex(sym, "."); i >= 0 {
		return sym[i+1:]
	}
	return sym
}

// isMethod reports whether a symbol is receiver-qualified. A qualified name is
// specific enough to match even when short, because "T.Close" never means the
// same thing as a bare "Close".
func isMethod(sym string) bool { return strings.Contains(sym, ".") }
