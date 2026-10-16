package symedit

import (
	"sort"
	"strings"
)

// lineIndex maps byte offsets of one text to 1-based line numbers.
type lineIndex struct {
	src    string
	starts []int
}

func newLineIndex(src string) lineIndex {
	starts := []int{0}
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return lineIndex{src: src, starts: starts}
}

// count is the number of lines. A trailing newline does not start a new one.
func (l lineIndex) count() int {
	n := len(l.starts)
	if n > 1 && l.starts[n-1] == len(l.src) {
		n--
	}
	return n
}

// lineOf returns the line containing byte offset off.
func (l lineIndex) lineOf(off int) int {
	return sort.Search(len(l.starts), func(i int) bool { return l.starts[i] > off })
}

// start is the offset where line begins. Past the last line it is len(src).
func (l lineIndex) start(line int) int {
	if line < 1 {
		line = 1
	}
	if line > len(l.starts) {
		return len(l.src)
	}
	return l.starts[line-1]
}

// end is the offset of the newline that closes line, or len(src).
func (l lineIndex) end(line int) int {
	s := l.start(line)
	if i := strings.IndexByte(l.src[s:], '\n'); i >= 0 {
		return s + i
	}
	return len(l.src)
}

// text returns line without its newline.
func (l lineIndex) text(line int) string {
	return l.src[l.start(line):l.end(line)]
}

// replaceLines replaces lines [from, to) with repl. Lines past the end count
// as empty. repl is newline-terminated; when the file had no final newline,
// the terminator is dropped so the file keeps its shape.
func replaceLines(idx lineIndex, from, to int, repl string) string {
	start, end := idx.start(from), idx.start(to)
	if end == len(idx.src) && !strings.HasSuffix(idx.src, "\n") {
		if repl == "" && start > 0 && idx.src[start-1] == '\n' {
			start--
		}
		repl = strings.TrimSuffix(repl, "\n")
	}
	return idx.src[:start] + repl + idx.src[end:]
}

// insertLineAt inserts line (newline-terminated) at byte offset pos.
func insertLineAt(src string, pos int, line string) string {
	if pos == len(src) && src != "" && !strings.HasSuffix(src, "\n") {
		return src + "\n" + line + "\n"
	}
	return src[:pos] + line + "\n" + src[pos:]
}

func lineIndent(s string) string { return s[:len(s)-len(strings.TrimLeft(s, " \t"))] }

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }

// reindent returns code with its common leading indentation replaced by
// indent. Blank lines become empty and leading and trailing blank lines are
// dropped. The result has no trailing newline.
func reindent(code, indent string) string {
	raw := strings.Split(strings.ReplaceAll(code, "\r\n", "\n"), "\n")
	for len(raw) > 0 && isBlank(raw[0]) {
		raw = raw[1:]
	}
	for len(raw) > 0 && isBlank(raw[len(raw)-1]) {
		raw = raw[:len(raw)-1]
	}
	common, first := "", true
	for _, l := range raw {
		if isBlank(l) {
			continue
		}
		if ind := lineIndent(l); first {
			common, first = ind, false
		} else {
			common = commonPrefix(common, ind)
		}
	}
	out := make([]string, len(raw))
	for i, l := range raw {
		switch {
		case isBlank(l):
			out[i] = ""
		default:
			out[i] = indent + l[len(common):]
		}
	}
	return strings.Join(out, "\n")
}

func commonPrefix(a, b string) string {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return a[:n]
}

// hasLeadingComment reports whether code starts with a comment, i.e. a doc
// comment the caller supplied.
func hasLeadingComment(code string) bool {
	t := strings.TrimSpace(code)
	for _, p := range []string{"//", "/*", "#", "--"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// isLeadingComment reports whether a line above a definition belongs to it:
// a comment, or an annotation or attribute line.
func isLeadingComment(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	if strings.HasPrefix(t, "#!") {
		return false
	}
	for _, p := range []string{"//", "/*", "*", "#", "--", "@", "#["} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// allIndexes returns every non-overlapping offset of sub in s.
func allIndexes(s, sub string) []int {
	var out []int
	for i := 0; ; {
		j := strings.Index(s[i:], sub)
		if j < 0 {
			return out
		}
		out = append(out, i+j)
		i += j + len(sub)
	}
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// levenshtein is the edit distance between a and b, used to rank names.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func bigrams(s string) map[string]int {
	r := []rune(s)
	m := map[string]int{}
	for i := 0; i+1 < len(r); i++ {
		m[string(r[i:i+2])]++
	}
	return m
}

// dice is the Dice coefficient of two bigram multisets, in [0, 1].
func dice(a, b map[string]int) float64 {
	na, nb, common := 0, 0, 0
	for _, c := range a {
		na += c
	}
	for _, c := range b {
		nb += c
	}
	for k, c := range a {
		common += min(c, b[k])
	}
	if na+nb == 0 {
		return 0
	}
	return 2 * float64(common) / float64(na+nb)
}
