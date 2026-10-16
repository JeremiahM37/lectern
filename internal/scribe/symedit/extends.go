package symedit

import (
	"fmt"
	"strings"
)

// setExtends changes the base of a class: the parent list in Python, the
// extends clause in TypeScript, JavaScript and Java. An empty base removes
// the base. The clause is located on the same header spans the other
// micro operations use, so text in comments and strings is never matched.
func (e *editor) setExtends(text string, idx lineIndex, masked string, s symbol, base string) (string, string, error) {
	base = strings.TrimSpace(base)
	switch e.lang.name {
	case "python", "typescript", "javascript", "java":
	default:
		return "", "", fmt.Errorf("set_extends is supported for Python, TypeScript, JavaScript and Java. %s is in a language without a single base clause", s.qual())
	}
	h, err := findHeader(e, idx, masked, s)
	if err != nil {
		return "", "", err
	}
	if e.lang.name == "python" {
		return setPyBases(text, h, s, base)
	}
	return setExtendsClause(text, masked, h, s, base)
}

// setPyBases edits the parenthesised base list of "class Name(...):".
func setPyBases(text string, h header, s symbol, base string) (string, string, error) {
	if s.kind != "class" {
		return "", "", fmt.Errorf("set_extends needs a class, and %s is a %s", s.qual(), s.kind)
	}
	if h.open < 0 {
		if base == "" {
			return text, "already present: " + s.qual() + " has no base", nil
		}
		at := h.nameAt + len(s.name)
		return text[:at] + "(" + base + ")" + text[at:], fmt.Sprintf("made %s extend %s", s.qual(), base), nil
	}
	if h.close < 0 {
		return "", "", fmt.Errorf("the base list of %s is not closed on its declaration line", s.qual())
	}
	if base == "" {
		return text[:h.open] + text[h.close+1:], fmt.Sprintf("removed the base of %s", s.qual()), nil
	}
	return text[:h.open+1] + base + text[h.close:], fmt.Sprintf("set the base of %s to %s", s.qual(), base), nil
}

// setExtendsClause edits the "extends X" clause of a brace-family class or
// interface header. A class may carry an implements clause after extends; the
// new base goes before it.
func setExtendsClause(text, masked string, h header, s symbol, base string) (string, string, error) {
	if s.kind != "class" && s.kind != "interface" {
		return "", "", fmt.Errorf("set_extends needs a class or interface, and %s is a %s", s.qual(), s.kind)
	}
	// Type parameters may themselves say "extends"; search after them.
	from0 := h.nameAt + len(s.name)
	if from0 < h.end && masked[from0] == '<' {
		from0 = matchAngle(masked, from0, h.end)
	}
	region := masked[from0:h.end]
	ext := findWord(region, "extends")
	impl := findWord(region, "implements")
	if ext >= 0 && (impl < 0 || ext < impl) {
		from := from0 + ext
		to := h.end
		if impl >= 0 {
			to = from0 + impl
		}
		if base == "" {
			if impl < 0 {
				// "class A extends B {" becomes "class A {".
				return text[:trimRightIdx(text, from)] + " " + text[to:], fmt.Sprintf("removed the base of %s", s.qual()), nil
			}
			return text[:from] + text[to:], fmt.Sprintf("removed the base of %s", s.qual()), nil
		}
		return text[:from] + "extends " + base + " " + text[to:], fmt.Sprintf("set the base of %s to %s", s.qual(), base), nil
	}
	if base == "" {
		return text, "already present: " + s.qual() + " has no base", nil
	}
	at := h.end
	if impl >= 0 {
		at = from0 + impl
	}
	before := trimRightIdx(text, at)
	return text[:before] + " extends " + base + text[before:], fmt.Sprintf("made %s extend %s", s.qual(), base), nil
}

// trimRightIdx returns the offset just after the last non-space byte before at.
func trimRightIdx(text string, at int) int {
	for at > 0 && (text[at-1] == ' ' || text[at-1] == '\t') {
		at--
	}
	return at
}

// matchAngle returns the offset just after the ">" that closes the "<" at
// from, or limit when it is not closed before limit.
func matchAngle(masked string, from, limit int) int {
	depth := 0
	for i := from; i < limit; i++ {
		switch masked[i] {
		case '<':
			depth++
		case '>':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return limit
}
