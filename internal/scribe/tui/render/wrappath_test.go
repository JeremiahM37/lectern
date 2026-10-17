package render

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

const storagePath = "/tmp/claude-1000/-home-admin-projects-lectern/a05bd330-3cdc-49f8-94c9-c07a611fc896/scratchpad/x.txt"

// Paths break after a separator, nothing is lost, and no line is too wide.
func TestWrapPathBreaksAtSeparators(t *testing.T) {
	out := WrapPath(storagePath, 40)
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		t.Fatalf("path should wrap: %q", out)
	}
	if got := strings.Join(lines, ""); got != storagePath {
		t.Fatalf("wrapping changed the text:\n%q\n%q", got, storagePath)
	}
	for i, l := range lines {
		if ansi.StringWidth(l) > 40 {
			t.Errorf("line %d is %d wide", i, ansi.StringWidth(l))
		}
		if i < len(lines)-1 && !strings.ContainsRune("/-_.", rune(l[len(l)-1])) {
			t.Errorf("line %d breaks mid-word: %q", i, l)
		}
	}
}

// A token with no separator still has to fit: it is cut at the edge.
func TestWrapPathCutsRunWithNoBreak(t *testing.T) {
	out := WrapPath(strings.Repeat("a", 30), 10)
	for _, l := range strings.Split(out, "\n") {
		if ansi.StringWidth(l) > 10 {
			t.Fatalf("line too wide: %q", l)
		}
	}
	if strings.ReplaceAll(out, "\n", "") != strings.Repeat("a", 30) {
		t.Fatalf("characters lost: %q", out)
	}
}

// Short text and existing newlines are untouched.
func TestWrapPathLeavesShortTextAlone(t *testing.T) {
	if got := WrapPath("one two\nthree", 40); got != "one two\nthree" {
		t.Fatalf("got %q", got)
	}
}
