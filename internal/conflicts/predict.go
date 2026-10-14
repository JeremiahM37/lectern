package conflicts

import (
	"fmt"
	"sort"
	"strings"
)

// Risk levels, ordered from least to most serious.
const (
	LevelNone     = "none"
	LevelTextual  = "textual"  // both sides touch one file, no symbol link
	LevelSemantic = "semantic" // one side changes a symbol the other uses or changes
)

// Risk is the prediction for one pair of footprints.
type Risk struct {
	Level   string
	Reasons []string
}

// minSymbolLen is the shortest bare name that counts as a symbol link. Shorter
// names ("err", "ctx", "New", "id") appear in almost every diff, so matching on
// them would flag every pair of sessions. A method-qualified name is exempt
// because "Store.Get" already says what it is.
const minSymbolLen = 4

// commonNames are real, longer identifiers that still show up everywhere in
// unrelated code. Matching them would be noise, so they never link on their
// own. A method-qualified form of the same name still links.
var commonNames = map[string]bool{
	"main": true, "init": true, "test": true, "setup": true, "teardown": true,
	"string": true, "error": true, "close": true, "reset": true, "value": true,
	"data": true, "name": true, "path": true, "read": true, "write": true,
	"open": true, "update": true, "delete": true, "create": true, "handle": true,
	"process": true, "execute": true, "run": true, "start": true, "stop": true,
	"get": true, "set": true,
}

// linkable reports whether a defined symbol is specific enough to match on.
func linkable(sym string) bool {
	if isMethod(sym) {
		return true
	}
	name := bareName(sym)
	return len(name) >= minSymbolLen && !commonNames[strings.ToLower(name)]
}

// Predict compares two footprints with the default labels "A" and "B".
func Predict(a, b Footprint) Risk {
	return PredictNamed("A", "B", a, b)
}

// PredictNamed is Predict with caller-supplied labels, which end up in the
// reason text. The scanner passes session names so a reason reads
// "Session #12 changes Parse", not "A changes Parse".
func PredictNamed(aName, bName string, a, b Footprint) Risk {
	var semantic []string
	semantic = append(semantic, linkReasons(aName, bName, a, b)...)
	semantic = append(semantic, linkReasons(bName, aName, b, a)...)
	if len(semantic) > 0 {
		return Risk{Level: LevelSemantic, Reasons: dedupeSorted(semantic)}
	}

	var shared []string
	inB := map[string]bool{}
	for _, f := range b.Files {
		inB[f] = true
	}
	for _, f := range a.Files {
		if inB[f] {
			shared = append(shared, fmt.Sprintf("%s and %s both change %s", aName, bName, f))
		}
	}
	if len(shared) > 0 {
		return Risk{Level: LevelTextual, Reasons: dedupeSorted(shared)}
	}
	return Risk{Level: LevelNone}
}

// linkReasons lists the ways X's definitions reach into Y: Y's added code uses
// a symbol X changes, or Y changes the same symbol too.
func linkReasons(xName, yName string, x, y Footprint) []string {
	files := make([]string, 0, len(x.Defined))
	for f := range x.Defined {
		files = append(files, f)
	}
	sort.Strings(files)

	yDefinedNames := map[string]bool{}
	for _, syms := range y.Defined {
		for _, s := range syms {
			yDefinedNames[bareName(s)] = true
		}
	}

	var out []string
	for _, f := range files {
		for _, sym := range x.Defined[f] {
			if !linkable(sym) {
				continue
			}
			name := bareName(sym)
			switch {
			case y.Referenced[name]:
				out = append(out, fmt.Sprintf("%s changes %s (%s); %s's new code uses %s", xName, sym, f, yName, name))
			case yDefinedNames[name]:
				out = append(out, fmt.Sprintf("%s and %s both change %s (%s)", xName, yName, sym, f))
			}
		}
	}
	return out
}

func dedupeSorted(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
