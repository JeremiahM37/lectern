package helpers

// Python's text semantics where the helpers' output depends on them: bytes
// decoded with errors='replace', str.splitlines, str.strip and the quoting
// repr() uses in error messages.

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// utf8Span is the length of the valid sequence at b[0:], or -(n) where n is
// the length of the maximal invalid prefix Python replaces with one U+FFFD
// (its "maximal subpart" rule), and whether the input ended mid-sequence.
func utf8Span(b []byte) (n int, truncated bool) {
	c := b[0]
	if c < 0x80 {
		return 1, false
	}
	var need int
	lo, hi := byte(0x80), byte(0xbf)
	switch {
	case c >= 0xc2 && c <= 0xdf:
		need = 1
	case c == 0xe0:
		need, lo = 2, 0xa0
	case c >= 0xe1 && c <= 0xec, c == 0xee, c == 0xef:
		need = 2
	case c == 0xed:
		need, hi = 2, 0x9f
	case c == 0xf0:
		need, lo = 3, 0x90
	case c >= 0xf1 && c <= 0xf3:
		need = 3
	case c == 0xf4:
		need, hi = 3, 0x8f
	default:
		return -1, false
	}
	for i := 1; i <= need; i++ {
		if i >= len(b) {
			return -i, true
		}
		if b[i] < lo || b[i] > hi {
			return -i, false
		}
		lo, hi = 0x80, 0xbf
	}
	return need + 1, false
}

// pyDecodeReplace is data.decode('utf-8', 'replace').
func pyDecodeReplace(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	var b strings.Builder
	for len(data) > 0 {
		n, _ := utf8Span(data)
		if n > 0 {
			b.Write(data[:n])
			data = data[n:]
			continue
		}
		b.WriteRune(utf8.RuneError)
		data = data[-n:]
	}
	return b.String()
}

// pyDecodeStrict is data.decode('utf-8'), with UnicodeDecodeError's text.
func pyDecodeStrict(data []byte) (string, error) {
	for i := 0; i < len(data); {
		n, truncated := utf8Span(data[i:])
		if n > 0 {
			i += n
			continue
		}
		reason := "invalid continuation byte"
		switch {
		case truncated:
			reason = "unexpected end of data"
		case n == -1:
			if c := data[i]; c < 0xc2 || c > 0xf4 {
				reason = "invalid start byte"
			}
		}
		if n == -1 {
			return "", fmt.Errorf("'utf-8' codec can't decode byte 0x%02x in position %d: %s", data[i], i, reason)
		}
		return "", fmt.Errorf("'utf-8' codec can't decode bytes in position %d-%d: %s", i, i-n-1, reason)
	}
	return string(data), nil
}

// pyIsSpace is str.isspace for one character.
func pyIsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// pyStrip is str.strip() with no argument.
func pyStrip(s string) string { return strings.TrimFunc(s, pyIsSpace) }

// pyRstrip is str.rstrip().
func pyRstrip(s string) string { return strings.TrimRightFunc(s, pyIsSpace) }

// pyLineBreak reports whether r ends a line for str.splitlines.
func pyLineBreak(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

// pySplitLines is str.splitlines(keepends).
func pySplitLines(s string, keepends bool) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if !pyLineBreak(r) {
			i += size
			continue
		}
		end := i + size
		if r == '\r' && end < len(s) && s[end] == '\n' {
			end++
		}
		if keepends {
			out = append(out, s[start:end])
		} else {
			out = append(out, s[start:i])
		}
		start, i = end, end
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// pyUniversalNewlines is how text=True reads a child's output: "\r\n" and
// "\r" become "\n".
func pyUniversalNewlines(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// pyCasefold approximates str.casefold for sorting: lower case, plus the
// common full foldings.
func pyCasefold(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case 'ß', 'ẞ':
			b.WriteString("ss")
		case 'ſ':
			b.WriteByte('s')
		case 'ς':
			b.WriteRune('σ')
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// pyLen is len(str) for a decoded string: code points, with an undecodable
// byte (a surrogate escape) counting as one.
func pyLen(s string) int {
	n := 0
	for i := 0; i < len(s); {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		n++
	}
	return n
}

// pyReprString is repr(str).
func pyReprString(s string) string {
	quote := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for i := 0; i < len(s); {
		if sur, ok := wtf8Surrogate(s[i:]); ok {
			fmt.Fprintf(&b, `\u%04x`, sur)
			i += 3
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size <= 1 {
			fmt.Fprintf(&b, `\udc%02x`, s[i])
			i++
			continue
		}
		i += size
		switch {
		case r == rune(quote) || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x7f || unicode.IsPrint(r):
			b.WriteRune(r)
		case r <= 0xff:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r <= 0xffff:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}
