package symedit

import (
	"fmt"
	"regexp"
	"strings"
)

// Micro operations change one part of a definition: its return type, a
// parameter, an initializer, its name. Member operations add or remove a
// member of a type. They work on text, located by the same definition spans
// the other operations use, so they stay within the families below.

var identPattern = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// header is where the signature of a definition sits in the text.
type header struct {
	nameAt int // offset of the declared name, -1 when not found
	open   int // offset of the parameter list "(", -1 when there is none
	close  int // offset of its ")", -1 when there is none
	end    int // offset where the signature ends: the body "{", the Python newline, or the definition end
}

// findWord returns the offset of the first whole-word occurrence of word in s,
// or -1.
func findWord(s, word string) int {
	for i := 0; ; {
		j := strings.Index(s[i:], word)
		if j < 0 {
			return -1
		}
		p := i + j
		e := p + len(word)
		if (p == 0 || !isIdentByte(s[p-1])) && (e == len(s) || !isIdentByte(s[e])) {
			return p
		}
		i = e
	}
}

// matchParen returns the offset of the ")" that closes the "(" at open, or -1.
func matchParen(masked string, open, lim int) int {
	depth := 0
	for i := open; i < lim && i < len(masked); i++ {
		switch masked[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// findHeader locates the name and parameter list of a definition.
func findHeader(e *editor, idx lineIndex, masked string, s symbol) (header, error) {
	h := header{nameAt: -1, open: -1, close: -1, end: s.end}
	switch e.lang.fam {
	case famGo, famBrace:
		if s.bodyOpen >= 0 {
			h.end = s.bodyOpen
		} else if open, _, ok := braceBody(masked, s.start, s.end); ok {
			h.end = open
		}
	case famPy:
		if hdr, ok := pyHeaderEnd(masked, s.start, s.end); ok {
			h.end = hdr
		}
	case famEnd:
		h.end = idx.end(s.startLine)
	}
	if at := findWord(masked[s.start:h.end], s.name); at >= 0 {
		h.nameAt = s.start + at
	} else {
		return h, fmt.Errorf("could not find the name %s in its declaration", s.qual())
	}
	if i := strings.IndexByte(masked[h.nameAt:h.end], '('); i >= 0 {
		h.open = h.nameAt + i
		h.close = matchParen(masked, h.open, h.end)
	}
	return h, nil
}

// splitParams splits the text between open and close at top-level commas and
// returns each segment's offsets (absolute, into masked).
func splitParams(masked string, open, close int) [][2]int {
	var segs [][2]int
	depth, start := 0, open+1
	for i := open + 1; i < close; i++ {
		switch masked[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case '<':
			depth++
		case '>':
			if i == 0 || (masked[i-1] != '=' && masked[i-1] != '-') {
				depth--
			}
		case ',':
			if depth == 0 {
				segs = append(segs, [2]int{start, i})
				start = i + 1
			}
		}
	}
	if strings.TrimSpace(masked[start:close]) != "" {
		segs = append(segs, [2]int{start, close})
	}
	return segs
}

// paramName returns the parameter's declared name: its first identifier that
// is not a modifier.
func paramName(seg string) string {
	skip := map[string]bool{"mut": true, "const": true, "readonly": true, "final": true, "public": true, "private": true, "protected": true, "ref": true}
	for _, w := range regexp.MustCompile(`[A-Za-z_$][A-Za-z0-9_$]*`).FindAllString(seg, -1) {
		if !skip[w] {
			return w
		}
	}
	return ""
}

// assignAt returns the offset of the first top-level "=" in masked[from:to]
// that is an assignment (not ==, <=, >=, !=, =>, :=), or -1.
func assignAt(masked string, from, to int) int {
	depth := 0
	for i := from; i < to; i++ {
		switch masked[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ';':
			if depth <= 0 {
				return -1
			}
		case '=':
			if depth > 0 || i+1 >= len(masked) {
				continue
			}
			next := masked[i+1]
			prev := byte(' ')
			if i > 0 {
				prev = masked[i-1]
			}
			if next == '=' || next == '>' || strings.IndexByte("=!<>:+-*/%&|^", prev) >= 0 {
				continue
			}
			return i
		}
	}
	return -1
}

// statementEnd returns the offset just before the ";" that ends a declaration
// at s.end, or s.end. Trailing spaces are not counted.
func statementEnd(text string, s symbol) int {
	end := s.end
	if end > 0 && end <= len(text) && strings.HasSuffix(strings.TrimRight(text[:end], " \t\r"), ";") {
		end = len(strings.TrimRight(text[:end], " \t\r")) - 1
	}
	return end
}

func trimLeftIdx(text string, at int) int {
	for at > 0 && (text[at-1] == ' ' || text[at-1] == '\t') {
		at--
	}
	return at
}

// setReturnType replaces the return type of a function or method.
func (e *editor) setReturnType(text string, idx lineIndex, masked string, s symbol, typ string) (string, string, error) {
	if isBlank(typ) {
		return "", "", fmt.Errorf("new_code is the new return type, for example string or Promise<void>")
	}
	typ = strings.TrimSpace(typ)
	h, err := findHeader(e, idx, masked, s)
	if err != nil {
		return "", "", err
	}
	if h.open < 0 || h.close < 0 {
		return "", "", fmt.Errorf("%s has no parameter list to put a return type after", s.qual())
	}
	body := h.end < len(text) && text[h.end] == '{'
	msg := fmt.Sprintf("set the return type of %s to %s", s.qual(), typ)
	switch e.lang.name {
	case "go", "rust", "typescript", "javascript":
		var tail string
		switch e.lang.name {
		case "go":
			tail = " " + typ
		case "rust":
			tail = " -> " + typ
		default:
			tail = ": " + typ
		}
		if body {
			tail += " "
		}
		return text[:h.close+1] + tail + text[h.end:], msg, nil
	case "python":
		colon := h.end
		for colon > 0 && (text[colon-1] == ' ' || text[colon-1] == '\t' || text[colon-1] == '\r') {
			colon--
		}
		if colon == 0 || text[colon-1] != ':' {
			return "", "", fmt.Errorf("could not find the ':' that ends the header of %s", s.qual())
		}
		colon--
		return text[:h.close+1] + " -> " + typ + text[colon:], msg, nil
	}
	return "", "", fmt.Errorf("set_return_type is supported for Go, Rust, TypeScript, JavaScript and Python. Use replace to change the signature of %s in %s", s.qual(), e.lang.name)
}

// setAsync marks a TypeScript/JavaScript function or method, or a Python def, async.
func (e *editor) setAsync(text string, idx lineIndex, masked string, s symbol) (string, string, error) {
	if e.lang.name != "typescript" && e.lang.name != "javascript" && e.lang.name != "python" {
		return "", "", fmt.Errorf("set_async is supported for TypeScript, JavaScript and Python. %s is in a language without async functions", s.qual())
	}
	h, err := findHeader(e, idx, masked, s)
	if err != nil {
		return "", "", err
	}
	at := h.nameAt
	for _, kw := range []string{"function", "def"} {
		if p := findWord(masked[s.start:h.nameAt], kw); p >= 0 {
			at = s.start + p
			break
		}
	}
	if findWord(masked[s.start:at], "async") >= 0 {
		return text, "already async", nil
	}
	return text[:at] + "async " + text[at:], fmt.Sprintf("made %s async", s.qual()), nil
}

// addParameter appends a parameter to a function's parameter list.
func (e *editor) addParameter(text string, idx lineIndex, masked string, s symbol, param string) (string, string, error) {
	param = strings.TrimSpace(param)
	if param == "" {
		return "", "", fmt.Errorf("new_code is the parameter to add, written as the language declares it, for example ctx Context or name: string")
	}
	h, err := findHeader(e, idx, masked, s)
	if err != nil {
		return "", "", err
	}
	if h.open < 0 || h.close < 0 {
		return "", "", fmt.Errorf("%s has no parameter list to add to", s.qual())
	}
	msg := fmt.Sprintf("added parameter %s to %s", param, s.qual())
	if strings.TrimSpace(text[h.open+1:h.close]) == "" {
		return text[:h.open+1] + param + text[h.close:], msg, nil
	}
	return text[:h.close] + ", " + param + text[h.close:], msg, nil
}

// removeParameter deletes the parameter named by name.
func (e *editor) removeParameter(text string, idx lineIndex, masked string, s symbol, name string) (string, string, error) {
	name = strings.TrimSpace(name)
	if !identPattern.MatchString(name) {
		name = paramName(name)
	}
	if name == "" {
		return "", "", fmt.Errorf("new_code is the name of the parameter to remove")
	}
	h, err := findHeader(e, idx, masked, s)
	if err != nil {
		return "", "", err
	}
	if h.open < 0 || h.close < 0 {
		return "", "", fmt.Errorf("%s has no parameter list", s.qual())
	}
	segs := splitParams(masked, h.open, h.close)
	var names []string
	for i, sg := range segs {
		n := paramName(masked[sg[0]:sg[1]])
		names = append(names, n)
		if n != name {
			continue
		}
		msg := fmt.Sprintf("removed parameter %s from %s", name, s.qual())
		switch {
		case len(segs) == 1:
			return text[:sg[0]] + text[sg[1]:], msg, nil
		case i < len(segs)-1:
			return text[:sg[0]] + text[segs[i+1][0]:], msg, nil
		default:
			return text[:segs[i-1][1]] + text[sg[1]:], msg, nil
		}
	}
	return "", "", fmt.Errorf("%s has no parameter named %s. Its parameters are: %s", s.qual(), name, strings.Join(names, ", "))
}

// setType sets the declared type of a variable, constant or property.
func (e *editor) setType(text string, idx lineIndex, masked string, s symbol, typ string) (string, string, error) {
	typ = strings.TrimSpace(typ)
	if typ == "" {
		return "", "", fmt.Errorf("new_code is the new type")
	}
	var sep string
	switch e.lang.name {
	case "typescript", "javascript":
		sep = ": "
	case "go":
		sep = " "
	default:
		return "", "", fmt.Errorf("set_type is supported for TypeScript, JavaScript and Go. Use replace for %s", s.qual())
	}
	at := findWord(masked[s.start:s.end], s.name)
	if at < 0 {
		return "", "", fmt.Errorf("could not find the name %s in its declaration", s.qual())
	}
	nameEnd := s.start + at + len(s.name)
	stop := assignAt(masked, nameEnd, s.end)
	if stop < 0 {
		stop = statementEnd(text, s)
	}
	stop = trimLeftIdx(text, stop)
	if stop < nameEnd {
		stop = nameEnd
	}
	tail := sep + typ
	if assignAt(masked, nameEnd, s.end) >= 0 {
		tail += " "
	}
	return text[:nameEnd] + tail + text[stop:], fmt.Sprintf("set the type of %s to %s", s.qual(), typ), nil
}

// setInitializer sets or adds the initializer of a variable, constant or
// property. remove drops it instead.
func (e *editor) setInitializer(text string, idx lineIndex, masked string, s symbol, value string, remove bool) (string, string, error) {
	if e.lang.fam != famBrace && e.lang.fam != famGo {
		return "", "", fmt.Errorf("initializers are edited for brace-delimited languages and Go; use replace for %s", s.qual())
	}
	at := findWord(masked[s.start:s.end], s.name)
	if at < 0 {
		return "", "", fmt.Errorf("could not find the name %s in its declaration", s.qual())
	}
	nameEnd := s.start + at + len(s.name)
	eq := assignAt(masked, nameEnd, s.end)
	if remove {
		if eq < 0 {
			return text, fmt.Sprintf("%s has no initializer", s.qual()), nil
		}
		from := trimLeftIdx(text, eq)
		return text[:from] + text[statementEnd(text, s):], fmt.Sprintf("removed the initializer of %s", s.qual()), nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", fmt.Errorf("new_code is the initializer value; use remove_initializer to drop it")
	}
	if eq < 0 {
		pos := statementEnd(text, s)
		pos = trimLeftIdx(text, pos)
		return text[:pos] + " = " + value + text[pos:], fmt.Sprintf("added an initializer to %s", s.qual()), nil
	}
	valEnd := statementEnd(text, s)
	valEnd = trimLeftIdx(text, valEnd)
	return text[:eq+1] + " " + value + text[valEnd:], fmt.Sprintf("set the initializer of %s", s.qual()), nil
}

// renameDecl renames a definition's name only, leaving references alone.
func (e *editor) renameDecl(text string, idx lineIndex, masked string, s symbol, name string) (string, string, error) {
	name = strings.TrimSpace(name)
	if !identPattern.MatchString(name) {
		return "", "", fmt.Errorf("new_name %q is not an identifier", name)
	}
	h, err := findHeader(e, idx, masked, s)
	if err != nil {
		return "", "", err
	}
	if name == s.name {
		return text, "already named " + name, nil
	}
	return text[:h.nameAt] + name + text[h.nameAt+len(s.name):], fmt.Sprintf("renamed the declaration %s to %s (references are not changed)", s.qual(), name), nil
}

// addMember inserts code as the last member of a type's body.
func (e *editor) addMember(idx lineIndex, masked string, s symbol, code string) (string, string, error) {
	if isBlank(code) {
		return "", "", fmt.Errorf("new_code is the member to add")
	}
	symIndent := lineIndent(idx.text(s.startLine))
	msg := fmt.Sprintf("added a member to %s", s.qual())
	switch e.lang.fam {
	case famGo, famBrace:
		open, cl := s.bodyOpen, s.bodyClose
		if open < 0 {
			var ok bool
			if open, cl, ok = braceBody(masked, s.start, s.end); !ok {
				return "", "", fmt.Errorf("%s has no { } body to add a member to", s.qual())
			}
		}
		bodyIndent := symIndent + unitFor(e.lang.fam)
		for ln := idx.lineOf(open) + 1; ln < idx.lineOf(cl); ln++ {
			if t := idx.text(ln); !isBlank(t) {
				if ind := lineIndent(t); len(ind) > len(symIndent) && strings.HasPrefix(ind, symIndent) {
					bodyIndent = ind
				}
				break
			}
		}
		text := idx.src
		if idx.lineOf(open) == idx.lineOf(cl) {
			return text[:open+1] + "\n" + reindent(code, bodyIndent) + "\n" + symIndent + text[cl:], msg, nil
		}
		closeLine := idx.lineOf(cl)
		if !strings.HasPrefix(strings.TrimLeft(idx.text(closeLine), " \t"), "}") {
			return "", "", fmt.Errorf("the closing brace of %s shares a line with other code. Put it on its own line first", s.qual())
		}
		return text[:idx.start(closeLine)] + reindent(code, bodyIndent) + "\n" + text[idx.start(closeLine):], msg, nil

	case famPy:
		bodyIndent := symIndent + "    "
		for ln := s.startLine + 1; ln <= s.endLine; ln++ {
			if t := idx.text(ln); !isBlank(t) && len(lineIndent(t)) > len(symIndent) {
				bodyIndent = lineIndent(t)
				break
			}
		}
		pos := idx.start(s.endLine + 1)
		ins := reindent(code, bodyIndent) + "\n"
		if pos == len(idx.src) && !strings.HasSuffix(idx.src, "\n") {
			ins = "\n" + reindent(code, bodyIndent)
			return idx.src + ins, msg, nil
		}
		return idx.src[:pos] + ins + idx.src[pos:], msg, nil

	default: // famEnd
		if s.endLine-s.startLine < 2 || strings.TrimSpace(idx.text(s.endLine)) != "end" {
			return "", "", fmt.Errorf("could not find the closing end of %s", s.qual())
		}
		pos := idx.start(s.endLine)
		return idx.src[:pos] + reindent(code, symIndent+"  ") + "\n" + idx.src[pos:], msg, nil
	}
}

// removeMember deletes a member. The member is named Owner.member.
func (e *editor) removeMember(idx lineIndex, s symbol) (string, string, error) {
	if s.parent == "" {
		return "", "", fmt.Errorf("remove_member needs a member written Owner.member; %s is top-level, so use delete", s.name)
	}
	return e.remove(idx, s)
}

// Target kinds for the operations added in wave 8. Kinds are repomap's names.
// An operation absent from this table accepts any definition.
var actionTargets = map[string][]string{
	"set_return_type":    {"function", "method"},
	"set_async":          {"function", "method"},
	"add_parameter":      {"function", "method", "constructor"},
	"remove_parameter":   {"function", "method", "constructor"},
	"set_type":           {"variable", "const", "var", "property", "field"},
	"set_initializer":    {"variable", "const", "var", "property", "field"},
	"remove_initializer": {"variable", "const", "var", "property", "field"},
	"add_method":         {"class", "struct", "interface", "trait"},
	"add_property":       {"class", "struct", "interface"},
	"set_extends":        {"class", "interface"},
	"set_static":         {"method", "property", "field"},
	"add_getter":         {"class", "struct"},
	"add_setter":         {"class", "struct"},
	"add_constructor":    {"class", "struct"},
}

// checkTarget reports when a symbol-addressed operation cannot apply to the
// definition it names, and lists the kinds that can be targeted.
func checkTarget(action string, s symbol) error {
	if action == "remove_member" {
		if s.parent == "" {
			return fmt.Errorf("remove_member needs a member written Owner.member, and %s is top-level. Use delete for a top-level definition", s.name)
		}
		return nil
	}
	kinds, ok := actionTargets[action]
	if !ok || contains(kinds, s.kind) {
		return nil
	}
	return fmt.Errorf("%s cannot target %s, which is a %s. Valid targets for %s: %s", action, s.qual(), s.kind, action, strings.Join(kinds, ", "))
}
