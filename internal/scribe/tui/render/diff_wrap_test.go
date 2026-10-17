package render

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// A long added line must wrap between words, never inside one, and mark its
// continuation rows.
func TestDiffWrapKeepsWordsWhole(t *testing.T) {
	src := "--- a/f.go\n+++ b/f.go\n@@ -1,1 +1,1 @@\n-x\n+// Median returns the middle value of xs when sorted, or 0 when xs is empty.\n"
	out := plain(Diff(src, 44, theme.Default(), DiffOpts{}))
	if !strings.Contains(out, "empty.") {
		t.Fatalf("word split across rows:\n%s", out)
	}
	if !strings.Contains(out, continuationMark) {
		t.Errorf("wrapped row has no continuation marker:\n%s", out)
	}
	if maxWidth(out) != 44 {
		t.Errorf("box width %d, want 44", maxWidth(out))
	}
}

// A word with no break point still hard-breaks, but the row count grows
// instead of the box overflowing.
func TestDiffWrapHardBreaksLongToken(t *testing.T) {
	long := strings.Repeat("a", 90)
	src := "--- a/f.go\n+++ b/f.go\n@@ -1,1 +1,1 @@\n-x\n+" + long + "\n"
	out := plain(Diff(src, 40, theme.Default(), DiffOpts{}))
	if strings.Count(out, "a") < 90 {
		t.Fatalf("token lost characters:\n%s", out)
	}
	if maxWidth(out) != 40 {
		t.Errorf("box width %d, want 40", maxWidth(out))
	}
}

// Side-by-side must show a visible marker on any line it cuts.
func TestDiffSideBySideCutsWithEllipsis(t *testing.T) {
	long := "func f() []int { return []int{5, 4, 3, 2, 1, 0, 9, 8, 7, 6, 5, 4, 3, 2, 1} }"
	src := "--- a/f.go\n+++ b/f.go\n@@ -1,1 +1,1 @@\n-" + long + "\n+short\n"
	out := plain(Diff(src, 60, theme.Default(), DiffOpts{SideBySide: true}))
	if !strings.Contains(out, "…") {
		t.Fatalf("cut line has no marker:\n%s", out)
	}
	if maxWidth(out) != 60 {
		t.Errorf("side by side width %d, want 60", maxWidth(out))
	}
}

// A line that fits is not marked.
func TestDiffSideBySideShortLineUnmarked(t *testing.T) {
	out := plain(Diff(smallDiff, 60, theme.Default(), DiffOpts{SideBySide: true}))
	if strings.Contains(out, "…") {
		t.Errorf("short lines should not be marked:\n%s", out)
	}
}
