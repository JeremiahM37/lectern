package replay

import (
	"strings"
	"testing"
)

func TestStripSolutionDetailsRemovesFencedCode(t *testing.T) {
	in := "Fix the off-by-one bug.\n\nHere's the fix:\n```go\nfunc Foo() {\n\treturn n - 1\n}\n```\nThanks!"
	out := StripSolutionDetails(in)
	if strings.Contains(out, "return n - 1") {
		t.Fatalf("fenced code survived stripping: %q", out)
	}
	if !strings.Contains(out, "Fix the off-by-one bug.") || !strings.Contains(out, "Thanks!") {
		t.Fatalf("prose around the fence was lost: %q", out)
	}
}

func TestStripSolutionDetailsRemovesUnfencedPastedDiff(t *testing.T) {
	in := "The fix touches app.py:\n\ndiff --git a/app.py b/app.py\nindex 111..222 100644\n" +
		"--- a/app.py\n+++ b/app.py\n@@ -1,2 +1,2 @@\n-old_line_of_code_here\n+new_line_of_code_here\n\n" +
		"That should do it."
	out := StripSolutionDetails(in)
	if strings.Contains(out, "new_line_of_code_here") || strings.Contains(out, "old_line_of_code_here") {
		t.Fatalf("unfenced pasted diff survived stripping: %q", out)
	}
	if !strings.Contains(out, "The fix touches app.py:") || !strings.Contains(out, "That should do it.") {
		t.Fatalf("prose around the diff was lost: %q", out)
	}
}

func TestBuildPromptIncludesIntentNeverCode(t *testing.T) {
	pr := PR{
		Title: "Add a health endpoint",
		Body:  "Ops wants a liveness probe.\n\n```go\nfunc Health() { return true }\n```",
		Issues: []Issue{
			{Number: 42, Title: "No liveness probe", Body: "We have no way to check if the service is up."},
		},
	}
	prompt := BuildPrompt(pr)
	if !strings.Contains(prompt, "Add a health endpoint") {
		t.Errorf("prompt missing title: %q", prompt)
	}
	if !strings.Contains(prompt, "Ops wants a liveness probe.") {
		t.Errorf("prompt missing body intent: %q", prompt)
	}
	if !strings.Contains(prompt, "We have no way to check if the service is up.") {
		t.Errorf("prompt missing linked issue text: %q", prompt)
	}
	if strings.Contains(prompt, "func Health()") {
		t.Errorf("prompt leaked solution code: %q", prompt)
	}
}

// TestLeakageGuardCatchesLinesStripSolutionDetailsMisses is the test the
// feature spec asks for by name: the prompt must never contain a line from
// the reference diff, even when it arrives by a path StripSolutionDetails'
// heuristics do not cover (a short inline snippet, not fenced, not
// diff-shaped enough to trip diffHunkHeaderRe).
func TestLeakageGuardCatchesLinesStripSolutionDetailsMisses(t *testing.T) {
	referenceDiff := "diff --git a/app.py b/app.py\n--- a/app.py\n+++ b/app.py\n" +
		"@@ -1,2 +1,2 @@\n-def old_behavior_function():\n+def new_behavior_function():\n"
	// This line is inline prose containing the EXACT text of an added
	// reference line, but with no fence and no diff markers around it — the
	// case StripSolutionDetails alone cannot catch.
	prompt := "Please rename the function.\ndef new_behavior_function():\nThat's the new name to use."
	redacted := RedactLeakedLines(prompt, referenceDiff)
	if strings.Contains(redacted, "def new_behavior_function():") {
		t.Fatalf("leaked reference line survived RedactLeakedLines: %q", redacted)
	}
	if !strings.Contains(redacted, "Please rename the function.") || !strings.Contains(redacted, "That's the new name to use.") {
		t.Fatalf("prose around the leak was lost: %q", redacted)
	}
}

func TestRedactLeakedLinesIgnoresShortIncidentalMatches(t *testing.T) {
	referenceDiff := "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-}\n+}\n"
	prompt := "Close the brace properly.\n}\nDone."
	// "}" alone is far too short/generic to count as a leak — redacting it
	// would make ordinary prose unreadable for no safety benefit.
	if got := RedactLeakedLines(prompt, referenceDiff); got != prompt {
		t.Fatalf("short generic line was redacted: %q", got)
	}
}

func TestRedactLeakedLinesNoOpWithoutAReferenceDiff(t *testing.T) {
	prompt := "Anything goes here, no reference yet."
	if got := RedactLeakedLines(prompt, ""); got != prompt {
		t.Fatalf("empty reference diff should be a no-op, got %q", got)
	}
}
