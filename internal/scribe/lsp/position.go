package lsp

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Point is a 1-based line and a 1-based column counted in Unicode code
// points. It is what Scribe's tools accept and report. LSP uses 0-based lines
// and UTF-16 code units; the conversions below bridge the two.
type Point struct {
	Line   int
	Column int
}

func utf16Units(r rune) int {
	if r >= 0x10000 {
		return 2
	}
	return 1
}

// lineBounds returns the byte range [start, end) of 0-based line n, without
// its "\n". ok is false when n is past the last line.
func lineBounds(text string, n int) (start, end int, ok bool) {
	if n < 0 {
		return 0, 0, false
	}
	start = 0
	for i := 0; i < n; i++ {
		idx := strings.IndexByte(text[start:], '\n')
		if idx < 0 {
			return 0, 0, false
		}
		start += idx + 1
	}
	end = len(text)
	if idx := strings.IndexByte(text[start:], '\n'); idx >= 0 {
		end = start + idx
	}
	return start, end, true
}

// offsetFromPosition converts an LSP position to a byte offset. Characters
// past the end of a line clamp to its end; a character that falls inside a
// surrogate pair clamps to before that code point.
func offsetFromPosition(text string, p Position) int {
	start, end, ok := lineBounds(text, p.Line)
	if !ok {
		return len(text)
	}
	line := text[start:end]
	units, i := 0, 0
	for i < len(line) && units < p.Character {
		r, size := utf8.DecodeRuneInString(line[i:])
		u := utf16Units(r)
		if units+u > p.Character {
			break
		}
		units += u
		i += size
	}
	return start + i
}

// positionAt converts a byte offset to an LSP position.
func positionAt(text string, off int) Position {
	if off > len(text) {
		off = len(text)
	}
	if off < 0 {
		off = 0
	}
	prefix := text[:off]
	line := strings.Count(prefix, "\n")
	lineStart := strings.LastIndexByte(prefix, '\n') + 1
	units := 0
	for _, r := range prefix[lineStart:] {
		units += utf16Units(r)
	}
	return Position{Line: line, Character: units}
}

// pointToPosition converts a 1-based code-point Point to an LSP position.
func pointToPosition(text string, p Point) Position {
	line := p.Line - 1
	if line < 0 {
		line = 0
	}
	start, end, ok := lineBounds(text, line)
	if !ok {
		return Position{Line: line}
	}
	want := p.Column - 1
	units, n := 0, 0
	for _, r := range text[start:end] {
		if n >= want {
			break
		}
		units += utf16Units(r)
		n++
	}
	return Position{Line: line, Character: units}
}

// positionToPoint converts an LSP position to a 1-based code-point Point.
func positionToPoint(text string, pos Position) Point {
	start, end, ok := lineBounds(text, pos.Line)
	if !ok {
		return Point{Line: pos.Line + 1, Column: 1}
	}
	units, n := 0, 0
	for _, r := range text[start:end] {
		u := utf16Units(r)
		if units+u > pos.Character {
			break
		}
		units += u
		n++
	}
	return Point{Line: pos.Line + 1, Column: n + 1}
}

// identAt returns the identifier that covers p, or "" when p is not on one.
func identAt(text string, p Point) string {
	start, end, ok := lineBounds(text, p.Line-1)
	if !ok {
		return ""
	}
	line := text[start:end]
	idx := 0
	col := 1
	for idx < len(line) && col < p.Column {
		_, size := utf8.DecodeRuneInString(line[idx:])
		idx += size
		col++
	}
	if idx >= len(line) {
		return ""
	}
	isWord := func(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }
	lo := idx
	for lo > 0 {
		r, size := utf8.DecodeLastRuneInString(line[:lo])
		if !isWord(r) {
			break
		}
		lo -= size
	}
	hi := idx
	for hi < len(line) {
		r, size := utf8.DecodeRuneInString(line[hi:])
		if !isWord(r) {
			break
		}
		hi += size
	}
	return line[lo:hi]
}
