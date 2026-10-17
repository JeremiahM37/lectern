package fs

import (
	"fmt"
	"strings"
	"testing"
)

// countOps reports how many lines the diff marks removed and added.
func countOps(t *testing.T, old, new string) (int, int) {
	t.Helper()
	_, added, removed := UnifiedDiff("f.txt", old, new)
	return added, removed
}

func TestDiffMovedBlockKeepsUniqueLinesAligned(t *testing.T) {
	var block []string
	for i := 0; i < 20; i++ {
		block = append(block, fmt.Sprintf("func helper%d() { return %d }", i, i))
	}
	head := []string{"package p", "", "// A", "// B"}
	tail := []string{"// C", "// D"}
	old := strings.Join(append(append(append([]string{}, head...), block...), tail...), "\n") + "\n"
	// Move the block below the tail: only the block's position changes.
	moved := strings.Join(append(append(append([]string{}, head...), tail...), block...), "\n") + "\n"

	added, removed := countOps(t, old, moved)
	// The smallest change moves the two tail lines up past the block, so the
	// block's 20 lines must not show as changed.
	if added != 2 || removed != 2 {
		t.Fatalf("moved block: got +%d -%d, want +2 -2", added, removed)
	}
	diff, _, _ := UnifiedDiff("f.txt", old, moved)
	if strings.Contains(diff, "-func helper") || strings.Contains(diff, "+func helper") {
		t.Fatalf("moved block shown as changed:\n%s", diff)
	}
}

func TestDiffLargeEditDoesNotRewriteWholeFile(t *testing.T) {
	// More than maxDiffD edits in a gap that is bounded by unique anchors
	// must not turn the whole file into removed and added lines.
	var oldLines, newLines []string
	oldLines = append(oldLines, "anchor-top")
	newLines = append(newLines, "anchor-top")
	for i := 0; i < maxDiffD+500; i++ {
		oldLines = append(oldLines, fmt.Sprintf("old %d", i))
		newLines = append(newLines, fmt.Sprintf("new %d", i))
	}
	oldLines = append(oldLines, "anchor-bottom")
	newLines = append(newLines, "anchor-bottom")
	for i := 0; i < 50; i++ {
		oldLines = append(oldLines, fmt.Sprintf("keep %d", i))
		newLines = append(newLines, fmt.Sprintf("keep %d", i))
	}
	old := strings.Join(oldLines, "\n") + "\n"
	new := strings.Join(newLines, "\n") + "\n"

	added, removed := countOps(t, old, new)
	if removed != maxDiffD+500 || added != maxDiffD+500 {
		t.Fatalf("got +%d -%d, want +%d -%d", added, removed, maxDiffD+500, maxDiffD+500)
	}
}

func TestLongestIncreasing(t *testing.T) {
	js := []int{3, 1, 4, 1, 5, 9, 2, 6}
	got := longestIncreasing(js, func(v int) int { return v })
	var vals []int
	for _, idx := range got {
		vals = append(vals, js[idx])
	}
	if len(vals) != 4 {
		t.Fatalf("longest increasing run = %v, want length 4", vals)
	}
	for i := 1; i < len(vals); i++ {
		if vals[i] <= vals[i-1] {
			t.Fatalf("run is not strictly increasing: %v", vals)
		}
	}
	if len(longestIncreasing([]int{}, func(v int) int { return v })) != 0 {
		t.Fatal("empty input should give no run")
	}
}
