package helpers

// A port of CPython's tomllib parser (Lib/tomllib/_parser.py and _re.py,
// MIT, Taneli Hukkinen), kept structurally identical so it accepts and
// rejects exactly what tomllib does. codex-trust needs that: it decides from
// the parse whether a project is already in ~/.codex/config.toml, and
// appending a table that is already there would corrupt the file.
//
// Values are string, bool, tomlInt (the digits as written), float64,
// tomlDateTime, []any and map[string]any. Only their types and the strings
// are ever looked at.

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

type tomlInt string

type tomlDateTime string

type tomlError struct {
	msg string
	pos int
}

func (e *tomlError) Error() string { return fmt.Sprintf("%s (at char %d)", e.msg, e.pos) }

type tomlKey []string

func (k tomlKey) plus(more ...string) tomlKey {
	out := make(tomlKey, 0, len(k)+len(more))
	return append(append(out, k...), more...)
}

const (
	tomlFrozen = iota
	tomlExplicitNest
)

type tomlFlagNode struct {
	flags, recursive map[int]bool
	nested           map[string]*tomlFlagNode
}

func newTomlFlagNode() *tomlFlagNode {
	return &tomlFlagNode{flags: map[int]bool{}, recursive: map[int]bool{}, nested: map[string]*tomlFlagNode{}}
}

type tomlPending struct {
	key  tomlKey
	flag int
}

type tomlFlags struct {
	root    map[string]*tomlFlagNode
	pending []tomlPending
	seen    map[string]bool
}

func newTomlFlags() *tomlFlags {
	return &tomlFlags{root: map[string]*tomlFlagNode{}, seen: map[string]bool{}}
}

func (f *tomlFlags) addPending(key tomlKey, flag int) {
	id := fmt.Sprintf("%q/%d", []string(key), flag) // tomllib keeps a set
	if !f.seen[id] {
		f.seen[id] = true
		f.pending = append(f.pending, tomlPending{key, flag})
	}
}

func (f *tomlFlags) finalizePending() {
	for _, p := range f.pending {
		f.set(p.key, p.flag, false)
	}
	f.pending = nil
	f.seen = map[string]bool{}
}

func (f *tomlFlags) unsetAll(key tomlKey) {
	cont := f.root
	for _, k := range key[:len(key)-1] {
		n, ok := cont[k]
		if !ok {
			return
		}
		cont = n.nested
	}
	delete(cont, key[len(key)-1])
}

func (f *tomlFlags) set(key tomlKey, flag int, recursive bool) {
	cont := f.root
	for _, k := range key[:len(key)-1] {
		if _, ok := cont[k]; !ok {
			cont[k] = newTomlFlagNode()
		}
		cont = cont[k].nested
	}
	stem := key[len(key)-1]
	if _, ok := cont[stem]; !ok {
		cont[stem] = newTomlFlagNode()
	}
	if recursive {
		cont[stem].recursive[flag] = true
	} else {
		cont[stem].flags[flag] = true
	}
}

func (f *tomlFlags) is(key tomlKey, flag int) bool {
	if len(key) == 0 {
		return false
	}
	cont := f.root
	for _, k := range key[:len(key)-1] {
		n, ok := cont[k]
		if !ok {
			return false
		}
		if n.recursive[flag] {
			return true
		}
		cont = n.nested
	}
	if n, ok := cont[key[len(key)-1]]; ok {
		return n.flags[flag] || n.recursive[flag]
	}
	return false
}

var errTomlNoNest = fmt.Errorf("there is no nest behind this key")

func tomlGetOrCreateNest(root map[string]any, key tomlKey, accessLists bool) (map[string]any, error) {
	var cont any = root
	for _, k := range key {
		m := cont.(map[string]any)
		if _, ok := m[k]; !ok {
			m[k] = map[string]any{}
		}
		cont = m[k]
		if l, ok := cont.([]any); ok && accessLists {
			if len(l) == 0 {
				return nil, errTomlNoNest // an IndexError in tomllib
			}
			cont = l[len(l)-1]
		}
		if _, ok := cont.(map[string]any); !ok {
			return nil, errTomlNoNest
		}
	}
	return cont.(map[string]any), nil
}

func tomlAppendNestToList(root map[string]any, key tomlKey) error {
	cont, err := tomlGetOrCreateNest(root, key[:len(key)-1], true)
	if err != nil {
		return err
	}
	last := key[len(key)-1]
	if v, ok := cont[last]; ok {
		l, ok := v.([]any)
		if !ok {
			return errTomlNoNest
		}
		cont[last] = append(l, map[string]any{})
	} else {
		cont[last] = []any{map[string]any{}}
	}
	return nil
}

type tomlParser struct {
	src []rune
}

func (p *tomlParser) at(pos int) (rune, bool) {
	if pos < 0 || pos >= len(p.src) {
		return 0, false
	}
	return p.src[pos], true
}

func (p *tomlParser) startsWith(s string, pos int) bool {
	r := []rune(s)
	if pos+len(r) > len(p.src) {
		return false
	}
	for i, c := range r {
		if p.src[pos+i] != c {
			return false
		}
	}
	return true
}

func (p *tomlParser) err(pos int, msg string) error { return &tomlError{msg, pos} }

func tomlIsCtrl(c rune) bool { return c < 32 || c == 127 }

func tomlIllegalBasic(c rune) bool     { return tomlIsCtrl(c) && c != '\t' }
func tomlIllegalMultiline(c rune) bool { return tomlIsCtrl(c) && c != '\t' && c != '\n' }

func tomlBare(c rune) bool {
	return c == '-' || c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func tomlWS(c rune) bool   { return c == ' ' || c == '\t' }
func tomlWSNL(c rune) bool { return c == ' ' || c == '\t' || c == '\n' }

// tomlLoads is tomllib.loads.
func tomlLoads(s string) (map[string]any, error) {
	p := &tomlParser{src: []rune(strings.ReplaceAll(s, "\r\n", "\n"))}
	data := map[string]any{}
	flags := newTomlFlags()
	var header tomlKey
	pos := 0
	for {
		pos = p.skip(pos, tomlWS)
		char, ok := p.at(pos)
		if !ok {
			break
		}
		if char == '\n' {
			pos++
			continue
		}
		var err error
		switch {
		case tomlBare(char) || char == '"' || char == '\'':
			pos, err = p.keyValueRule(pos, data, flags, header)
			if err != nil {
				return nil, err
			}
			pos = p.skip(pos, tomlWS)
		case char == '[':
			second, _ := p.at(pos + 1)
			flags.finalizePending()
			if second == '[' {
				pos, header, err = p.createListRule(pos, data, flags)
			} else {
				pos, header, err = p.createDictRule(pos, data, flags)
			}
			if err != nil {
				return nil, err
			}
			pos = p.skip(pos, tomlWS)
		case char != '#':
			return nil, p.err(pos, "Invalid statement")
		}
		pos, err = p.skipComment(pos)
		if err != nil {
			return nil, err
		}
		char, ok = p.at(pos)
		if !ok {
			break
		}
		if char != '\n' {
			return nil, p.err(pos, "Expected newline or end of document after a statement")
		}
		pos++
	}
	return data, nil
}

func (p *tomlParser) skip(pos int, in func(rune) bool) int {
	for pos < len(p.src) && in(p.src[pos]) {
		pos++
	}
	return pos
}

func (p *tomlParser) index(expect string, pos int) int {
	r := []rune(expect)
	for i := pos; i+len(r) <= len(p.src); i++ {
		if p.startsWith(expect, i) {
			return i
		}
	}
	return -1
}

func (p *tomlParser) skipUntil(pos int, expect string, errorOn func(rune) bool, errorOnEOF bool) (int, error) {
	newPos := p.index(expect, pos)
	if newPos < 0 {
		newPos = len(p.src)
		if errorOnEOF {
			return 0, p.err(newPos, fmt.Sprintf("Expected %q", expect))
		}
	}
	for i := pos; i < newPos; i++ {
		if errorOn(p.src[i]) {
			return 0, p.err(i, fmt.Sprintf("Found invalid character %q", p.src[i]))
		}
	}
	return newPos, nil
}

func (p *tomlParser) skipComment(pos int) (int, error) {
	if c, ok := p.at(pos); ok && c == '#' {
		return p.skipUntil(pos+1, "\n", tomlIllegalBasic, false)
	}
	return pos, nil
}

func (p *tomlParser) skipCommentsAndArrayWS(pos int) (int, error) {
	for {
		before := pos
		pos = p.skip(pos, tomlWSNL)
		var err error
		if pos, err = p.skipComment(pos); err != nil {
			return 0, err
		}
		if pos == before {
			return pos, nil
		}
	}
}

func (p *tomlParser) createDictRule(pos int, data map[string]any, flags *tomlFlags) (int, tomlKey, error) {
	pos++
	pos = p.skip(pos, tomlWS)
	pos, key, err := p.parseKey(pos)
	if err != nil {
		return 0, nil, err
	}
	if flags.is(key, tomlExplicitNest) || flags.is(key, tomlFrozen) {
		return 0, nil, p.err(pos, fmt.Sprintf("Cannot declare %v twice", key))
	}
	flags.set(key, tomlExplicitNest, false)
	if _, err := tomlGetOrCreateNest(data, key, true); err != nil {
		return 0, nil, p.err(pos, "Cannot overwrite a value")
	}
	if !p.startsWith("]", pos) {
		return 0, nil, p.err(pos, "Expected ']' at the end of a table declaration")
	}
	return pos + 1, key, nil
}

func (p *tomlParser) createListRule(pos int, data map[string]any, flags *tomlFlags) (int, tomlKey, error) {
	pos += 2
	pos = p.skip(pos, tomlWS)
	pos, key, err := p.parseKey(pos)
	if err != nil {
		return 0, nil, err
	}
	if flags.is(key, tomlFrozen) {
		return 0, nil, p.err(pos, fmt.Sprintf("Cannot mutate immutable namespace %v", key))
	}
	flags.unsetAll(key)
	flags.set(key, tomlExplicitNest, false)
	if err := tomlAppendNestToList(data, key); err != nil {
		return 0, nil, p.err(pos, "Cannot overwrite a value")
	}
	if !p.startsWith("]]", pos) {
		return 0, nil, p.err(pos, "Expected ']]' at the end of an array declaration")
	}
	return pos + 2, key, nil
}

func (p *tomlParser) keyValueRule(pos int, data map[string]any, flags *tomlFlags, header tomlKey) (int, error) {
	pos, key, value, err := p.parseKeyValuePair(pos)
	if err != nil {
		return 0, err
	}
	parent, stem := key[:len(key)-1], key[len(key)-1]
	absParent := header.plus(parent...)
	for i := 1; i < len(key); i++ {
		cont := header.plus(key[:i]...)
		if flags.is(cont, tomlExplicitNest) {
			return 0, p.err(pos, fmt.Sprintf("Cannot redefine namespace %v", cont))
		}
		flags.addPending(cont, tomlExplicitNest)
	}
	if flags.is(absParent, tomlFrozen) {
		return 0, p.err(pos, fmt.Sprintf("Cannot mutate immutable namespace %v", absParent))
	}
	nest, err := tomlGetOrCreateNest(data, absParent, true)
	if err != nil {
		return 0, p.err(pos, "Cannot overwrite a value")
	}
	if _, ok := nest[stem]; ok {
		return 0, p.err(pos, "Cannot overwrite a value")
	}
	switch value.(type) {
	case map[string]any, []any:
		flags.set(header.plus(key...), tomlFrozen, true)
	}
	nest[stem] = value
	return pos, nil
}

func (p *tomlParser) parseKeyValuePair(pos int) (int, tomlKey, any, error) {
	pos, key, err := p.parseKey(pos)
	if err != nil {
		return 0, nil, nil, err
	}
	if c, ok := p.at(pos); !ok || c != '=' {
		return 0, nil, nil, p.err(pos, "Expected '=' after a key in a key/value pair")
	}
	pos++
	pos = p.skip(pos, tomlWS)
	pos, value, err := p.parseValue(pos)
	if err != nil {
		return 0, nil, nil, err
	}
	return pos, key, value, nil
}

func (p *tomlParser) parseKey(pos int) (int, tomlKey, error) {
	pos, part, err := p.parseKeyPart(pos)
	if err != nil {
		return 0, nil, err
	}
	key := tomlKey{part}
	pos = p.skip(pos, tomlWS)
	for {
		if c, ok := p.at(pos); !ok || c != '.' {
			return pos, key, nil
		}
		pos++
		pos = p.skip(pos, tomlWS)
		pos, part, err = p.parseKeyPart(pos)
		if err != nil {
			return 0, nil, err
		}
		key = append(key, part)
		pos = p.skip(pos, tomlWS)
	}
}

func (p *tomlParser) parseKeyPart(pos int) (int, string, error) {
	c, ok := p.at(pos)
	switch {
	case ok && tomlBare(c):
		start := pos
		pos = p.skip(pos, tomlBare)
		return pos, string(p.src[start:pos]), nil
	case ok && c == '\'':
		return p.parseLiteralStr(pos)
	case ok && c == '"':
		return p.parseBasicStr(pos+1, false)
	}
	return 0, "", p.err(pos, "Invalid initial character for a key part")
}

func (p *tomlParser) parseArray(pos int) (int, []any, error) {
	pos++
	array := []any{}
	pos, err := p.skipCommentsAndArrayWS(pos)
	if err != nil {
		return 0, nil, err
	}
	if p.startsWith("]", pos) {
		return pos + 1, array, nil
	}
	for {
		var val any
		pos, val, err = p.parseValue(pos)
		if err != nil {
			return 0, nil, err
		}
		array = append(array, val)
		if pos, err = p.skipCommentsAndArrayWS(pos); err != nil {
			return 0, nil, err
		}
		c, _ := p.at(pos)
		if c == ']' {
			return pos + 1, array, nil
		}
		if c != ',' {
			return 0, nil, p.err(pos, "Unclosed array")
		}
		pos++
		if pos, err = p.skipCommentsAndArrayWS(pos); err != nil {
			return 0, nil, err
		}
		if p.startsWith("]", pos) {
			return pos + 1, array, nil
		}
	}
}

func (p *tomlParser) parseInlineTable(pos int) (int, map[string]any, error) {
	pos++
	table := map[string]any{}
	flags := newTomlFlags()
	pos = p.skip(pos, tomlWS)
	if p.startsWith("}", pos) {
		return pos + 1, table, nil
	}
	for {
		var key tomlKey
		var value any
		var err error
		pos, key, value, err = p.parseKeyValuePair(pos)
		if err != nil {
			return 0, nil, err
		}
		parent, stem := key[:len(key)-1], key[len(key)-1]
		if flags.is(key, tomlFrozen) {
			return 0, nil, p.err(pos, fmt.Sprintf("Cannot mutate immutable namespace %v", key))
		}
		nest, err := tomlGetOrCreateNest(table, parent, false)
		if err != nil {
			return 0, nil, p.err(pos, "Cannot overwrite a value")
		}
		if _, ok := nest[stem]; ok {
			return 0, nil, p.err(pos, fmt.Sprintf("Duplicate inline table key %q", stem))
		}
		nest[stem] = value
		pos = p.skip(pos, tomlWS)
		c, _ := p.at(pos)
		if c == '}' {
			return pos + 1, table, nil
		}
		if c != ',' {
			return 0, nil, p.err(pos, "Unclosed inline table")
		}
		switch value.(type) {
		case map[string]any, []any:
			flags.set(key, tomlFrozen, true)
		}
		pos++
		pos = p.skip(pos, tomlWS)
	}
}

var tomlEscapes = map[string]string{
	`\b`: "\b", `\t`: "\t", `\n`: "\n", `\f`: "\f", `\r`: "\r", `\"`: `"`, `\\`: `\`,
}

func (p *tomlParser) parseBasicStrEscape(pos int, multiline bool) (int, string, error) {
	end := pos + 2
	if end > len(p.src) {
		end = len(p.src)
	}
	id := string(p.src[pos:end])
	pos += 2
	if multiline && (id == "\\ " || id == "\\\t" || id == "\\\n") {
		if id != "\\\n" {
			pos = p.skip(pos, tomlWS)
			c, ok := p.at(pos)
			if !ok {
				return pos, "", nil
			}
			if c != '\n' {
				return 0, "", p.err(pos, `Unescaped '\' in a string`)
			}
			pos++
		}
		pos = p.skip(pos, tomlWSNL)
		return pos, "", nil
	}
	switch id {
	case `\u`:
		return p.parseHexChar(pos, 4)
	case `\U`:
		return p.parseHexChar(pos, 8)
	}
	if r, ok := tomlEscapes[id]; ok {
		return pos, r, nil
	}
	return 0, "", p.err(pos, `Unescaped '\' in a string`)
}

func (p *tomlParser) parseHexChar(pos, n int) (int, string, error) {
	if pos+n > len(p.src) {
		return 0, "", p.err(pos, "Invalid hex value")
	}
	hex := string(p.src[pos : pos+n])
	for _, c := range hex {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return 0, "", p.err(pos, "Invalid hex value")
		}
	}
	pos += n
	v, _ := strconv.ParseUint(hex, 16, 64)
	if !(v <= 0xD7FF || (v >= 0xE000 && v <= 0x10FFFF)) {
		return 0, "", p.err(pos, "Escaped character is not a Unicode scalar value")
	}
	return pos, string(rune(v)), nil
}

func (p *tomlParser) parseLiteralStr(pos int) (int, string, error) {
	pos++
	start := pos
	pos, err := p.skipUntil(pos, "'", tomlIllegalBasic, true)
	if err != nil {
		return 0, "", err
	}
	return pos + 1, string(p.src[start:pos]), nil
}

func (p *tomlParser) parseMultilineStr(pos int, literal bool) (int, string, error) {
	pos += 3
	if p.startsWith("\n", pos) {
		pos++
	}
	var delim, result string
	if literal {
		delim = "'"
		end, err := p.skipUntil(pos, "'''", tomlIllegalMultiline, true)
		if err != nil {
			return 0, "", err
		}
		result = string(p.src[pos:end])
		pos = end + 3
	} else {
		delim = `"`
		var err error
		pos, result, err = p.parseBasicStr(pos, true)
		if err != nil {
			return 0, "", err
		}
	}
	if !p.startsWith(delim, pos) {
		return pos, result, nil
	}
	pos++
	if !p.startsWith(delim, pos) {
		return pos, result + delim, nil
	}
	pos++
	return pos, result + delim + delim, nil
}

func (p *tomlParser) parseBasicStr(pos int, multiline bool) (int, string, error) {
	illegal := tomlIllegalBasic
	if multiline {
		illegal = tomlIllegalMultiline
	}
	var result strings.Builder
	start := pos
	for {
		c, ok := p.at(pos)
		if !ok {
			return 0, "", p.err(pos, "Unterminated string")
		}
		if c == '"' {
			if !multiline {
				result.WriteString(string(p.src[start:pos]))
				return pos + 1, result.String(), nil
			}
			if p.startsWith(`"""`, pos) {
				result.WriteString(string(p.src[start:pos]))
				return pos + 3, result.String(), nil
			}
			pos++
			continue
		}
		if c == '\\' {
			result.WriteString(string(p.src[start:pos]))
			var esc string
			var err error
			pos, esc, err = p.parseBasicStrEscape(pos, multiline)
			if err != nil {
				return 0, "", err
			}
			result.WriteString(esc)
			start = pos
			continue
		}
		if illegal(c) {
			return 0, "", p.err(pos, fmt.Sprintf("Illegal character %q", c))
		}
		pos++
	}
}

func (p *tomlParser) parseValue(pos int) (int, any, error) {
	c, _ := p.at(pos)
	switch c {
	case '"':
		if p.startsWith(`"""`, pos) {
			return p.parseMultilineStr(pos, false)
		}
		return p.parseBasicStr(pos+1, false)
	case '\'':
		if p.startsWith("'''", pos) {
			return p.parseMultilineStr(pos, true)
		}
		return p.parseLiteralStr(pos)
	case 't':
		if p.startsWith("true", pos) {
			return pos + 4, true, nil
		}
	case 'f':
		if p.startsWith("false", pos) {
			return pos + 5, false, nil
		}
	case '[':
		return p.parseArray(pos)
	case '{':
		return p.parseInlineTable(pos)
	}
	if end, ok, valid := p.matchDateTime(pos); ok {
		if !valid {
			return 0, nil, p.err(pos, "Invalid date or datetime")
		}
		return end, tomlDateTime(string(p.src[pos:end])), nil
	}
	if end, ok := p.matchTime(pos); ok {
		return end, tomlDateTime(string(p.src[pos:end])), nil
	}
	if end, isFloat, ok := p.matchNumber(pos); ok {
		text := string(p.src[pos:end])
		if isFloat {
			f, err := strconv.ParseFloat(strings.ReplaceAll(text, "_", ""), 64)
			if err != nil && !isRangeErr(err) {
				return 0, nil, p.err(pos, "Invalid value")
			}
			return end, f, nil
		}
		return end, tomlInt(text), nil
	}
	for _, s := range []string{"inf", "nan"} {
		if p.startsWith(s, pos) {
			return pos + 3, tomlSpecialFloat(s), nil
		}
	}
	for _, s := range []string{"-inf", "+inf", "-nan", "+nan"} {
		if p.startsWith(s, pos) {
			return pos + 4, tomlSpecialFloat(s), nil
		}
	}
	return 0, nil, p.err(pos, "Invalid value")
}

func tomlSpecialFloat(s string) float64 {
	if strings.HasSuffix(s, "nan") {
		return math.NaN()
	}
	if s[0] == '-' {
		return math.Inf(-1)
	}
	return math.Inf(1)
}

// digitsAt matches exactly n ASCII digits at pos.
func (p *tomlParser) digitsAt(pos, n int) (int, bool) {
	if pos+n > len(p.src) {
		return 0, false
	}
	v := 0
	for i := 0; i < n; i++ {
		c := p.src[pos+i]
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + int(c-'0')
	}
	return v, true
}

// matchTime is _TIME_RE_STR:
// ([01][0-9]|2[0-3]):([0-5][0-9]):([0-5][0-9])(?:\.([0-9]{1,6})[0-9]*)?
func (p *tomlParser) matchTime(pos int) (int, bool) {
	h, ok := p.digitsAt(pos, 2)
	if !ok || h > 23 || !p.startsWith(":", pos+2) {
		return 0, false
	}
	m, ok := p.digitsAt(pos+3, 2)
	if !ok || m > 59 || !p.startsWith(":", pos+5) {
		return 0, false
	}
	s, ok := p.digitsAt(pos+6, 2)
	if !ok || s > 59 {
		return 0, false
	}
	end := pos + 8
	if p.startsWith(".", end) {
		if _, ok := p.digitsAt(end+1, 1); ok {
			end += 2
			for end < len(p.src) && p.src[end] >= '0' && p.src[end] <= '9' {
				end++
			}
		}
	}
	return end, true
}

// matchDateTime is RE_DATETIME plus match_to_datetime's validation: ok says
// the pattern matched, valid that the date exists.
func (p *tomlParser) matchDateTime(pos int) (end int, ok, valid bool) {
	y, ok1 := p.digitsAt(pos, 4)
	if !ok1 || !p.startsWith("-", pos+4) {
		return 0, false, false
	}
	mo, ok2 := p.digitsAt(pos+5, 2)
	if !ok2 || mo < 1 || mo > 12 || !p.startsWith("-", pos+7) {
		return 0, false, false
	}
	d, ok3 := p.digitsAt(pos+8, 2)
	if !ok3 || d < 1 || d > 31 {
		return 0, false, false
	}
	end = pos + 10
	if c, has := p.at(end); has && (c == 'T' || c == 't' || c == ' ') {
		if tEnd, tok := p.matchTime(end + 1); tok {
			end = tEnd
			if c, has := p.at(end); has && (c == 'Z' || c == 'z') {
				end++
			} else if has && (c == '+' || c == '-') {
				oh, okh := p.digitsAt(end+1, 2)
				om, okm := p.digitsAt(end+4, 2)
				if okh && oh <= 23 && p.startsWith(":", end+3) && okm && om <= 59 {
					end += 6
				}
			}
		}
	}
	days := [...]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}[mo-1]
	if mo == 2 && y%4 == 0 && (y%100 != 0 || y%400 == 0) {
		days = 29
	}
	return end, true, y >= 1 && d <= days
}

// matchNumber is RE_NUMBER; isFloat reports a non-empty floatpart.
func (p *tomlParser) matchNumber(pos int) (end int, isFloat, ok bool) {
	// digits matches D(?:_?D)* at i for the class in.
	digits := func(i int, in func(rune) bool) (int, bool) {
		c, has := p.at(i)
		if !has || !in(c) {
			return i, false
		}
		i++
		for {
			c, has := p.at(i)
			if has && in(c) {
				i++
				continue
			}
			if has && c == '_' {
				if n, has := p.at(i + 1); has && in(n) {
					i += 2
					continue
				}
			}
			return i, true
		}
	}
	dec := func(c rune) bool { return c >= '0' && c <= '9' }
	if c, _ := p.at(pos); c == '0' {
		next, _ := p.at(pos + 1)
		var in func(rune) bool
		switch next {
		case 'x':
			in = func(c rune) bool { return strings.ContainsRune("0123456789abcdefABCDEF", c) }
		case 'b':
			in = func(c rune) bool { return c == '0' || c == '1' }
		case 'o':
			in = func(c rune) bool { return c >= '0' && c <= '7' }
		}
		if in != nil {
			if e, ok := digits(pos+2, in); ok {
				return e, false, true
			}
		}
	}
	i := pos
	if c, _ := p.at(i); c == '+' || c == '-' {
		i++
	}
	switch c, has := p.at(i); {
	case has && c == '0':
		i++
	case has && c >= '1' && c <= '9':
		i, _ = digits(i, dec)
	default:
		return 0, false, false
	}
	floatStart := i
	if p.startsWith(".", i) {
		if e, ok := digits(i+1, dec); ok {
			i = e
		}
	}
	if c, has := p.at(i); has && (c == 'e' || c == 'E') {
		j := i + 1
		if c, has := p.at(j); has && (c == '+' || c == '-') {
			j++
		}
		if e, ok := digits(j, dec); ok {
			i = e
		}
	}
	return i, i > floatStart, true
}
