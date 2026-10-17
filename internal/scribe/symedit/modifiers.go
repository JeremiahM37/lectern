package symedit

import (
	"fmt"
	"strings"
)

// Modifier micro operations: static on a member, and decorators or annotations
// above a definition. Both work on the same spans as the other operations.

// staticRemoves reads a set_static new_code that asks for the member to be an
// instance member again.
func staticRemoves(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "false", "no", "remove", "instance":
		return true
	}
	return false
}

// staticLangs are the languages with a static keyword on members.
var staticLangs = map[string]bool{"typescript": true, "javascript": true, "java": true, "csharp": true, "php": true, "swift": true}

// accessWords may come before static in a member's modifiers.
var accessWords = map[string]bool{"public": true, "private": true, "protected": true, "internal": true, "export": true, "declare": true}

// setStatic adds or removes the static keyword on a member. The keyword goes
// after any access modifier, which is where TypeScript, Java and C# expect it.
func (e *editor) setStatic(text string, idx lineIndex, masked string, s symbol, remove bool) (string, string, error) {
	if !staticLangs[e.lang.name] {
		return "", "", fmt.Errorf("set_static is supported for TypeScript, JavaScript, Java, C#, PHP and Swift. %s is in a language without a static keyword; for Python use add_decorator with staticmethod", s.qual())
	}
	if s.kind == "class" || s.kind == "struct" || s.kind == "interface" {
		return "", "", fmt.Errorf("set_static applies to a member such as a method or property, and %s is a %s", s.qual(), s.kind)
	}
	h, err := findHeader(e, idx, masked, s)
	if err != nil {
		return "", "", err
	}
	head := masked[s.start:h.nameAt]
	at := staticInsertAt(head)
	if remove {
		if p := findWord(head, "static"); p >= 0 {
			from := s.start + p
			to := from + len("static")
			for to < len(text) && (text[to] == ' ' || text[to] == '\t') {
				to++
			}
			return text[:from] + text[to:], fmt.Sprintf("made %s an instance member", s.qual()), nil
		}
		return text, "already present: " + s.qual() + " is not static", nil
	}
	if findWord(head, "static") >= 0 {
		return text, "already present: " + s.qual() + " is already static", nil
	}
	return text[:s.start+at] + "static " + text[s.start+at:], fmt.Sprintf("made %s static", s.qual()), nil
}

// staticInsertAt returns the offset in head where the static keyword goes:
// the first token after any leading access modifiers.
func staticInsertAt(head string) int {
	at := 0
	for {
		i := skipSpace(head, at)
		j := i
		for j < len(head) && isWordByte(head[j]) {
			j++
		}
		if j == i || !accessWords[head[i:j]] {
			return i
		}
		at = j
	}
}

func skipSpace(s string, at int) int {
	for at < len(s) && (s[at] == ' ' || s[at] == '\t' || s[at] == '\n' || s[at] == '\r') {
		at++
	}
	return at
}

func isWordByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// decoratorLangs are the languages with a leading @ on definitions.
var decoratorLangs = map[string]bool{"python": true, "typescript": true, "javascript": true, "java": true}

// decoratorName returns the bare name of a decorator written as "@name(args)",
// "name(args)" or "@name", so a removal can match it however it was written.
func decoratorName(s string) string {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "@"))
	if i := strings.IndexAny(s, "( \t"); i >= 0 {
		s = s[:i]
	}
	return s
}

// decoratorBlock returns the lines [top, bottom) holding the decorators of s:
// the @ lines directly above its definition, and any it starts on. bottom is
// the definition's own line.
func decoratorBlock(idx lineIndex, s symbol) (int, int) {
	isDec := func(ln int) bool {
		return ln >= 0 && ln < idx.count() && strings.HasPrefix(strings.TrimSpace(idx.text(ln)), "@")
	}
	top := s.startLine
	for isDec(top - 1) {
		top--
	}
	bottom := s.startLine
	for isDec(bottom) {
		bottom++
	}
	return top, bottom
}

// addDecorator puts a decorator line directly above the existing decorators of
// the definition, at its indentation. It is a no-op when one of the same name
// is already there.
func (e *editor) addDecorator(idx lineIndex, s symbol, dec string) (string, string, error) {
	if !decoratorLangs[e.lang.name] {
		return "", "", fmt.Errorf("add_decorator is supported for Python, TypeScript, JavaScript and Java. %s is in a language without decorators", s.qual())
	}
	dec = strings.TrimSpace(dec)
	if dec == "" {
		return "", "", fmt.Errorf("new_code is the decorator to add, such as @dataclass or @Injectable()")
	}
	name := decoratorName(dec)
	if name == "" {
		return "", "", fmt.Errorf("new_code has no decorator name")
	}
	top, bottom := decoratorBlock(idx, s)
	for ln := top; ln < bottom; ln++ {
		if decoratorName(idx.text(ln)) == name {
			return idx.src, "already present: " + s.qual() + " already has @" + name, nil
		}
	}
	line := lineIndent(idx.text(bottom)) + "@" + strings.TrimPrefix(dec, "@") + "\n"
	return replaceLines(idx, top, top, line), fmt.Sprintf("added @%s to %s", name, s.qual()), nil
}

// removeDecorator deletes the decorator line of that name above the definition.
func (e *editor) removeDecorator(idx lineIndex, s symbol, dec string) (string, string, error) {
	if !decoratorLangs[e.lang.name] {
		return "", "", fmt.Errorf("remove_decorator is supported for Python, TypeScript, JavaScript and Java. %s is in a language without decorators", s.qual())
	}
	name := decoratorName(dec)
	if name == "" {
		return "", "", fmt.Errorf("new_code is the decorator to remove, such as @dataclass")
	}
	top, bottom := decoratorBlock(idx, s)
	for ln := top; ln < bottom; ln++ {
		if decoratorName(idx.text(ln)) == name {
			return replaceLines(idx, ln, ln+1, ""), fmt.Sprintf("removed @%s from %s", name, s.qual()), nil
		}
	}
	return "", "", fmt.Errorf("%s has no @%s decorator directly above it. Use the decorator name as written in the file", s.qual(), name)
}
