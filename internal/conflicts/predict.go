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
// them would flag every pair of sessions.
const minSymbolLen = 4

// minMethodLen is the same floor for methods. A method is matched against the
// other side's code by its bare name, because "c.Close()" does not say which
// type c is, so "Client.Close" would otherwise link to every Close call in
// the repository. Only longer, more distinctive method names link.
const minMethodLen = 7

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
	// Method names every package has; matched bare, they link unrelated types.
	"handler": true, "request": true, "response": true, "command": true,
	"launch": true, "health": true, "server": true, "client": true,
	"config": true, "options": true, "context": true, "result": true, "status": true,
}

// linkable reports whether a defined symbol is specific enough to match on.
func linkable(sym string) bool {
	name := bareName(sym)
	floor := minSymbolLen
	if isMethod(sym) {
		floor = minMethodLen
	}
	return len(name) >= floor && !commonNames[strings.ToLower(name)]
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

// linkReasons lists the ways X's changes can break Y: X reshapes an existing
// symbol (signature, rename, deletion) that Y's added code uses, or that Y
// changes too in another file. A body edit or a new symbol is not enough.
// Replaying the owner's merge history, linking on any edited symbol flagged 20
// of 24 clean merges; requiring a reshape is what makes the warning rare
// enough to act on.
func linkReasons(xName, yName string, x, y Footprint) []string {
	files := make([]string, 0, len(x.Reshaped))
	for f := range x.Reshaped {
		files = append(files, f)
	}
	sort.Strings(files)

	// Both sides changing one symbol counts as semantic only when they do it
	// in different files (say, an interface and its implementation). In the
	// same file it is a textual overlap, which the file check already reports.
	yDefinedElsewhere := map[string]map[string]bool{}
	for yf, syms := range y.Defined {
		for _, s := range syms {
			if yDefinedElsewhere[bareName(s)] == nil {
				yDefinedElsewhere[bareName(s)] = map[string]bool{}
			}
			yDefinedElsewhere[bareName(s)][yf] = true
		}
	}
	changedElsewhere := func(name, f string) bool {
		for yf := range yDefinedElsewhere[name] {
			if yf != f {
				return true
			}
		}
		return false
	}

	var out []string
	for _, f := range files {
		for _, sym := range x.Reshaped[f] {
			if !linkable(sym) {
				continue
			}
			name := bareName(sym)
			switch {
			case y.Referenced[name]:
				out = append(out, fmt.Sprintf("%s changes %s (%s); %s's new code uses %s", xName, sym, f, yName, name))
			case changedElsewhere(name, f):
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
