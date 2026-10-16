package repomap

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
)

type endMode int

const (
	endBrace endMode = iota
	endIndent
	endKeyword
)

// kwConfig drives block matching for end-terminated languages (Ruby, Lua,
// Elixir). always words open a block wherever they appear; lineStart words
// open one only as the first word on a line (so "x if y" is not a block).
type kwConfig struct {
	always    map[string]bool
	lineStart map[string]bool
	closers   map[string]bool
	// endlessDef: a "def" whose line is "def f = expr" opens no block (Ruby).
	endlessDef bool
}

// defRule is one definition pattern. Its regex runs over the masked source.
type defRule struct {
	re          *regexp.Regexp
	kind        string
	kindGroup   int             // take the kind from this capture group
	nameGroup   int             // capture holding the name (default 1)
	guardGroup  int             // capture checked against skipTypes (e.g. a return type)
	skipTypes   map[string]bool // values of guardGroup that reject the match
	skipNames   map[string]bool
	container   bool // its body is a parent for symbols inside it
	hidden      bool // container only, never emitted (Rust impl blocks)
	methodOnly  bool // emitted only inside a container, as a method
	requireBody bool // reject declarations that end in ';' (calls, prototypes)
	inParen     bool // the match already consumed '(' of a parameter list
	braceEnd    bool // the match ends with '{'; scanning starts there
	splitName   bool // Lua style "M.f" / "M:f": parent and name are split
	endless     bool // Ruby "def f = expr" has no end
	custom      func(m []byte, from int) (term, end int, ok bool)
}

// scan finds where a declaration's header ends (term) and where its body ends
// (end), both as byte offsets into the masked source.
func (lg *language) scan(m []byte, r *defRule, match []int, sigStart int) (int, int, bool) {
	if r.custom != nil {
		return r.custom(m, match[1])
	}
	switch lg.end {
	case endIndent:
		colon := scanToColon(m, match[1])
		if colon >= len(m) {
			return len(m), len(m), true
		}
		return colon, indentBlockEnd(m, colon), true
	case endKeyword:
		if r.endless && rubyEndless.Match(m[lineStartOf(m, sigStart):lineEnd(m, sigStart)]) {
			le := lineEnd(m, sigStart)
			return le, le, true
		}
		t, e := kwBlockEnd(m, sigStart, lg.kw)
		return t, e, true
	default:
		start := match[1]
		if r.braceEnd {
			start = match[1] - 1
		}
		depth := 0
		if r.inParen {
			depth = 1
		}
		term, end := scanDecl(m, start, depth, lg.newlineEnds && !r.container)
		return term, end, true
	}
}

// scanDecl walks from `from` to the '{' that opens the body, or the ';' or
// newline that ends a bodyless declaration. Brackets are tracked so braces or
// semicolons inside parameter lists do not count.
func scanDecl(m []byte, from, depth int, newlineEnds bool) (int, int) {
	for i := from; i < len(m); i++ {
		switch m[i] {
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		case '{':
			if depth == 0 {
				return i, matchBrace(m, i)
			}
		case ';':
			if depth == 0 {
				return i, i
			}
		case '\n':
			if depth == 0 && newlineEnds {
				return i, i
			}
		}
	}
	return len(m), len(m)
}

// matchBrace returns the offset of the '}' matching the '{' at open.
func matchBrace(m []byte, open int) int {
	depth := 0
	for i := open; i < len(m); i++ {
		switch m[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	if len(m) == 0 {
		return 0
	}
	return len(m) - 1
}

// scanToColon finds the ':' that ends a Python header, at bracket depth 0.
func scanToColon(m []byte, from int) int {
	depth := 0
	for i := from; i < len(m); i++ {
		switch m[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		case ':':
			if depth == 0 {
				return i
			}
		case '\n':
			if depth == 0 {
				return i
			}
		}
	}
	return len(m)
}

// indentBlockEnd returns the end offset of a Python block whose header colon
// is at colon. The block runs over lines indented deeper than the header.
func indentBlockEnd(m []byte, colon int) int {
	hdrEnd := lineEnd(m, colon)
	if colon+1 > hdrEnd {
		return hdrEnd // no colon on this line: the header never completed
	}
	if strings.TrimSpace(string(m[colon+1:hdrEnd])) != "" {
		return hdrEnd // one-line body: def f(): return 1
	}
	hdrIndent := leadingWS(m, lineStartOf(m, colon))
	end := hdrEnd
	pos := hdrEnd + 1
	for pos < len(m) {
		le := lineEnd(m, pos)
		if strings.TrimSpace(string(m[pos:le])) != "" {
			if leadingWS(m, pos) <= hdrIndent {
				break
			}
			end = le
		}
		pos = le + 1
	}
	return end
}

// kwBlockEnd matches open and close keywords starting at from. It returns the
// end of the header line and the offset of the closing keyword.
func kwBlockEnd(m []byte, from int, kw *kwConfig) (int, int) {
	depth := 0
	i := from
	for i < len(m) {
		if !isIdentStart(m[i]) {
			i++
			continue
		}
		j := i
		for j < len(m) && isIdentPart(m[j]) {
			j++
		}
		if j < len(m) && (m[j] == '?' || m[j] == '!') {
			j++
		}
		w := string(m[i:j])
		open := kw.always[w] || (kw.lineStart[w] && onlyBlankBefore(m, i))
		if w == "do" && j < len(m) && m[j] == ':' {
			open = false // Elixir keyword list: def f, do: x
		}
		if w == "def" && kw.endlessDef && rubyEndless.Match(m[lineStartOf(m, i):lineEnd(m, i)]) {
			open = false
		}
		if open {
			depth++
		} else if kw.closers[w] && depth > 0 {
			depth--
			if depth == 0 {
				return lineEnd(m, from), j
			}
		}
		i = j
	}
	le := lineEnd(m, from)
	return le, le
}

var rubyEndless = rx(`^[ \t]*def[ \t]+(?:self\.)?\w+[?!]?(?:\([^)\n]*\))?[ \t]*=[ \t]+\S`)

// cFuncScan accepts a C/C++ function definition: a parameter list, optional
// qualifiers or a constructor initializer list, then '{'. Prototypes, calls
// and macros are rejected.
func cFuncScan(m []byte, from int) (int, int, bool) {
	depth := 1
	i := from
	for ; i < len(m) && depth > 0; i++ {
		switch m[i] {
		case '(':
			depth++
		case ')':
			depth--
		}
	}
	if depth > 0 {
		return 0, 0, false
	}
	pdepth := 0
	for i < len(m) {
		c := m[i]
		switch {
		case c == '{':
			if pdepth == 0 {
				return i, matchBrace(m, i), true
			}
		case c == ';' || c == '=':
			if pdepth == 0 {
				return 0, 0, false
			}
		case c == ':' && pdepth == 0 && i+1 < len(m) && m[i+1] != ':':
			// constructor initializer list: skip to the body
		case c == '(':
			pdepth++
		case c == ')':
			pdepth--
		case c == '-' && i+1 < len(m) && m[i+1] == '>':
			i += 2
			continue
		case isIdentStart(c) || c == ',' || c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '<' || c == '>' || c == '*' || c == '&' || c == ':' || c == '.':
		default:
			return 0, 0, false
		}
		i++
	}
	return 0, 0, false
}

// lineIndex maps byte offsets to 1-based line numbers.
type lineIndex struct{ starts []int }

func newLineIndex(m []byte) *lineIndex {
	starts := []int{0}
	for i, c := range m {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	return &lineIndex{starts: starts}
}

func (l *lineIndex) lineOf(off int) int {
	return sort.Search(len(l.starts), func(i int) bool { return l.starts[i] > off })
}

func lineStartOf(m []byte, off int) int {
	for off > 0 && m[off-1] != '\n' {
		off--
	}
	return off
}

func lineEnd(m []byte, off int) int {
	for off < len(m) && m[off] != '\n' {
		off++
	}
	return off
}

func leadingWS(m []byte, off int) int {
	n := 0
	for off+n < len(m) && (m[off+n] == ' ' || m[off+n] == '\t') {
		n++
	}
	return n
}

func firstNonSpace(m []byte, off int) int {
	for off < len(m) && (m[off] == ' ' || m[off] == '\t' || m[off] == '\r' || m[off] == '\n') {
		off++
	}
	return off
}

func onlyBlankBefore(m []byte, i int) bool {
	for k := i - 1; k >= 0; k-- {
		if m[k] == '\n' {
			return true
		}
		if m[k] != ' ' && m[k] != '\t' {
			return false
		}
	}
	return true
}

// collapse folds a multi-line signature onto one line and bounds its length.
func collapse(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

func firstWord(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

type cand struct {
	sym         Symbol
	start, end  int
	rule        *defRule
	parentFound bool
}

// extractByRules runs a language's definition rules over one file.
func extractByRules(rel string, lg *language, src, masked []byte) []Symbol {
	li := newLineIndex(masked)
	var cands []cand
	seen := map[int]bool{}
	for ri := range lg.rules {
		r := &lg.rules[ri]
		ng := r.nameGroup
		if ng == 0 {
			ng = 1
		}
		for _, m := range r.re.FindAllSubmatchIndex(masked, -1) {
			if r.guardGroup > 0 && m[2*r.guardGroup] >= 0 &&
				r.skipTypes[string(src[m[2*r.guardGroup]:m[2*r.guardGroup+1]])] {
				continue
			}
			if m[2*ng] < 0 {
				continue
			}
			ns, ne := m[2*ng], m[2*ng+1]
			name := string(src[ns:ne])
			if seen[ns] || r.skipNames[name] {
				continue
			}
			seen[ns] = true
			sigStart := firstNonSpace(masked, m[0])
			kind := r.kind
			if r.kindGroup > 0 && m[2*r.kindGroup] >= 0 {
				kind = firstWord(string(src[m[2*r.kindGroup]:m[2*r.kindGroup+1]]))
			}
			term, end, ok := lg.scan(masked, r, m, sigStart)
			if !ok || (r.requireBody && (term >= len(masked) || masked[term] != '{')) {
				continue
			}
			startLine := li.lineOf(sigStart)
			if lg.end == endIndent {
				for startLine > 1 {
					prev := strings.TrimSpace(string(src[li.starts[startLine-2]:lineEnd(src, li.starts[startLine-2])]))
					if !strings.HasPrefix(prev, "@") {
						break
					}
					startLine--
				}
			}
			var sig string
			if lg.end == endKeyword {
				sig = collapse(string(src[sigStart:lineEnd(src, sigStart)]))
			} else {
				if term > len(src) {
					term = len(src)
				}
				sig = collapse(string(src[sigStart:term]))
			}
			sym := Symbol{Name: name, Kind: kind, File: rel, Line: startLine, EndLine: li.lineOf(end), Signature: sig, start: sigStart, end: end}
			if r.splitName {
				splitQualified(&sym, name)
			}
			sym.Exported = lg.exported(sig, sym.Name, sym.Kind)
			cands = append(cands, cand{sym: sym, start: sigStart, end: end, rule: r})
		}
	}

	// Parents: the innermost container whose body contains the candidate.
	for i := range cands {
		c := &cands[i]
		best := -1
		for j := range cands {
			p := &cands[j]
			if j == i || !p.rule.container {
				continue
			}
			if p.start < c.start && c.start < p.end && (best < 0 || p.start > cands[best].start) {
				best = j
			}
		}
		if best >= 0 {
			c.parentFound = true
			if c.sym.Parent == "" {
				c.sym.Parent = cands[best].sym.Name
			}
			if c.sym.Kind == "function" {
				c.sym.Kind = "method"
			}
		}
	}

	var out []Symbol
	for i := range cands {
		c := &cands[i]
		if c.rule.hidden || (c.rule.methodOnly && !c.parentFound) {
			continue
		}
		if c.sym.Kind == "function" || c.sym.Kind == "method" {
			c.sym.BodyHash = bodyHash(masked, c.start, c.end, li)
		}
		out = append(out, c.sym)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > 10000 {
		out = out[:10000]
	}
	return out
}

// bodyHash hashes a definition's body with whitespace removed, so two copies
// that differ only in layout (or in comments, already masked) hash the same.
// Signatures are skipped so renamed clones still match. Bodies under three
// lines return "".
func bodyHash(m []byte, start, end int, li *lineIndex) string {
	if end > len(m) {
		end = len(m)
	}
	if start >= end {
		return ""
	}
	first := lineEnd(m, start)
	if first >= end || li.lineOf(end)-li.lineOf(start) < 2 {
		return ""
	}
	var b []byte
	for _, c := range m[first:end] {
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			b = append(b, c)
		}
	}
	if len(b) == 0 {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// splitQualified splits "A::b" (C++), "M.f" and "M:f" (Lua) into parent and name.
func splitQualified(sym *Symbol, name string) {
	if i := strings.LastIndex(name, "::"); i >= 0 {
		parent := name[:i]
		if j := strings.LastIndex(parent, "::"); j >= 0 {
			parent = parent[j+2:]
		}
		sym.Parent = parent
		sym.Name = name[i+2:]
		sym.Kind = "method"
		return
	}
	if idx := strings.LastIndexAny(name, ".:"); idx >= 0 {
		sym.Parent = name[:idx]
		sym.Name = name[idx+1:]
		if strings.Contains(name, ":") {
			sym.Kind = "method"
		}
	}
}
