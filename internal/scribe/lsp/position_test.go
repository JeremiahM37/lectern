package lsp

import "testing"

func TestUTF16PositionsWithEmojiAndCJK(t *testing.T) {
	// Line 0: "a😀b" (emoji is 2 UTF-16 units). Line 1: "中文x" (BMP, 1 unit each).
	text := "a😀b\n中文x\nend"

	cases := []struct {
		name string
		pos  Position
		want int // byte offset
	}{
		{"start", Position{0, 0}, 0},
		{"before emoji", Position{0, 1}, 1},
		{"after emoji", Position{0, 3}, 5},
		{"inside surrogate clamps before it", Position{0, 2}, 1},
		{"CJK x", Position{1, 2}, 7 + 6},
		{"past line end clamps", Position{1, 99}, 7 + 6 + 1},
		{"past last line", Position{9, 0}, len(text)},
	}
	for _, c := range cases {
		if got := offsetFromPosition(text, c.pos); got != c.want {
			t.Errorf("%s: offsetFromPosition(%+v) = %d, want %d", c.name, c.pos, got, c.want)
		}
	}
	if got := text[offsetFromPosition(text, Position{1, 2}):][:1]; got != "x" {
		t.Errorf("CJK position points at %q, want x", got)
	}
	// Round trip from byte offsets.
	for _, off := range []int{0, 1, 5, 7, 13, 14} {
		p := positionAt(text, off)
		if back := offsetFromPosition(text, p); back != off {
			t.Errorf("round trip %d -> %+v -> %d", off, p, back)
		}
	}
}

func TestPointColumnsAreCodePoints(t *testing.T) {
	text := "a😀b\n中文x\n"
	// Code-point column 3 on line 1 is "b"; its UTF-16 character is 3.
	if got := pointToPosition(text, Point{1, 3}); got != (Position{0, 3}) {
		t.Errorf("pointToPosition(1:3) = %+v, want {0 3}", got)
	}
	if got := positionToPoint(text, Position{0, 3}); got != (Point{1, 3}) {
		t.Errorf("positionToPoint({0 3}) = %+v, want 1:3", got)
	}
	// Code-point column 3 on line 2 is "x" (after two CJK characters).
	if got := pointToPosition(text, Point{2, 3}); got != (Position{1, 2}) {
		t.Errorf("pointToPosition(2:3) = %+v, want {1 2}", got)
	}
	if got := positionToPoint(text, Position{1, 2}); got != (Point{2, 3}) {
		t.Errorf("positionToPoint({1 2}) = %+v, want 2:3", got)
	}
	// Emoji itself is one code point, two UTF-16 units: column 2 is char 1.
	if got := pointToPosition(text, Point{1, 2}); got != (Position{0, 1}) {
		t.Errorf("pointToPosition(1:2) = %+v, want {0 1}", got)
	}
}

func TestCRLFAndMissingLines(t *testing.T) {
	text := "ab\r\ncd"
	if got := positionToPoint(text, Position{1, 1}); got != (Point{2, 2}) {
		t.Errorf("CRLF line 2 col: %+v", got)
	}
	if got := pointToPosition(text, Point{2, 1}); got != (Position{1, 0}) {
		t.Errorf("pointToPosition on CRLF file: %+v", got)
	}
	if got := pointToPosition(text, Point{50, 1}); got.Line != 49 {
		t.Errorf("point past EOF keeps its line: %+v", got)
	}
}

func TestIdentAtHandlesUnicodeIdentifiers(t *testing.T) {
	text := "x := 中文x + y_1\n"
	if got := identAt(text, Point{1, 1}); got != "x" {
		t.Errorf("ident at 1:1 = %q", got)
	}
	if got := identAt(text, Point{1, 6}); got != "中文x" {
		t.Errorf("CJK ident at 1:6 = %q", got)
	}
	if got := identAt(text, Point{1, 13}); got != "y_1" {
		t.Errorf("ident at 1:13 = %q", got)
	}
	if got := identAt(text, Point{1, 3}); got != "" {
		t.Errorf("operator should give no ident, got %q", got)
	}
}
