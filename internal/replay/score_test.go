package replay

import "testing"

const refDiff = `diff --git a/app.py b/app.py
--- a/app.py
+++ b/app.py
@@ -1,3 +1,4 @@
 def main():
-    print("hello")
+    print("hello, lectern")
+    return True
`

func TestScoreIdenticalDiffIsPerfectMatch(t *testing.T) {
	sim := Score(refDiff, refDiff)
	if sim.FileOverlap != 1 {
		t.Errorf("FileOverlap: %v", sim.FileOverlap)
	}
	if sim.LineOverlap != 1 {
		t.Errorf("LineOverlap: %v", sim.LineOverlap)
	}
	if sim.SizeRatio != 1 {
		t.Errorf("SizeRatio: %v", sim.SizeRatio)
	}
}

func TestScoreUnrelatedDiffIsNoMatch(t *testing.T) {
	attempt := `diff --git a/other.py b/other.py
--- a/other.py
+++ b/other.py
@@ -1,2 +1,3 @@
 def unrelated():
-    pass
+    return 42
+    log("done")
`
	sim := Score(attempt, refDiff)
	if sim.FileOverlap != 0 {
		t.Errorf("FileOverlap should be 0 for disjoint files: %v", sim.FileOverlap)
	}
	if sim.LineOverlap != 0 {
		t.Errorf("LineOverlap should be 0 for disjoint content: %v", sim.LineOverlap)
	}
}

func TestScorePartialOverlap(t *testing.T) {
	// same file, one of the two changed lines matches exactly, plus one
	// extra line the reference doesn't have — a partial, plausible replay.
	attempt := `diff --git a/app.py b/app.py
--- a/app.py
+++ b/app.py
@@ -1,3 +1,5 @@
 def main():
-    print("hello")
+    print("hello, lectern")
+    return True
+    extra_unrelated_line_here()
`
	sim := Score(attempt, refDiff)
	if sim.FileOverlap != 1 {
		t.Errorf("FileOverlap: %v, want 1 (same file)", sim.FileOverlap)
	}
	if sim.LineOverlap <= 0 || sim.LineOverlap >= 1 {
		t.Errorf("LineOverlap should be a partial match strictly between 0 and 1: %v", sim.LineOverlap)
	}
	if sim.SizeRatio <= 1 {
		t.Errorf("SizeRatio should be >1 (attempt changed more lines): %v", sim.SizeRatio)
	}
}

func TestScoreEmptyDiffsAreAPerfectTrivialMatch(t *testing.T) {
	sim := Score("", "")
	if sim.FileOverlap != 1 || sim.LineOverlap != 1 || sim.SizeRatio != 1 {
		t.Fatalf("two empty diffs should score as a trivial match: %+v", sim)
	}
}

func TestParseDiffExtractsFilesAndLines(t *testing.T) {
	info := ParseDiff(refDiff)
	if len(info.Files) != 1 || info.Files[0] != "app.py" {
		t.Fatalf("Files: %v", info.Files)
	}
	if !info.Lines[`print("hello, lectern")`] {
		t.Errorf("expected added line content in set: %v", info.Lines)
	}
	if !info.Lines[`print("hello")`] {
		t.Errorf("expected removed line content in set: %v", info.Lines)
	}
}
