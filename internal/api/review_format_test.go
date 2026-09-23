package api

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// formatReviewPrompt is what turns a batch of inline diff comments into the
// single message sent to a session (SendText) or a task (request-changes
// feedback) — see docs/agent-events.md section 4. This is a pure formatting
// test, no harness needed.
func TestFormatReviewPromptSingleComment(t *testing.T) {
	got := formatReviewPrompt([]reviewComment{
		{File: "app.py", Line: 12, Side: "new", Text: "handle the empty case", Code: "def health():"},
	}, "")
	for _, want := range []string{
		"Code review feedback (1 comment):",
		"1. app.py:12 (new side)",
		"> def health():",
		"handle the empty case",
		"Please address each point above.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
}

func TestFormatReviewPromptMultipleCommentsAndSummary(t *testing.T) {
	got := formatReviewPrompt([]reviewComment{
		{File: "a.go", Line: 3, Side: "old", Text: "why remove this?"},
		{File: "b.go", Line: 9, Side: "new", Text: "missing nil check"},
	}, "overall looks close, two small things")
	if !strings.Contains(got, "(2 comments):") {
		t.Errorf("expected a plural count: %s", got)
	}
	if !strings.Contains(got, "overall looks close, two small things") {
		t.Errorf("summary missing: %s", got)
	}
	if !strings.Contains(got, "1. a.go:3 (old side)") || !strings.Contains(got, "2. b.go:9 (new side)") {
		t.Errorf("comments not numbered in order: %s", got)
	}
	// no quoted code line was supplied for either comment, so none should appear
	if strings.Contains(got, ">") {
		t.Errorf("no code was supplied; should not fabricate a quote: %s", got)
	}
}

// A side outside {old,new} (a client bug, or a future field lectern doesn't
// know yet) must not corrupt the prompt — treat it as "new" rather than fail.
func TestFormatReviewPromptUnknownSideDefaultsToNew(t *testing.T) {
	got := formatReviewPrompt([]reviewComment{{File: "x", Line: 1, Side: "bogus", Text: "t"}}, "")
	if !strings.Contains(got, "x:1 (new side)") {
		t.Errorf("unknown side should fall back to new: %s", got)
	}
}

func TestFormatReviewPromptSummaryOnlyNeedsNoNumberedList(t *testing.T) {
	got := formatReviewPrompt(nil, "just ship it after fixing the typo in the title")
	if strings.Contains(got, "1.") {
		t.Errorf("a summary-only review should not fabricate numbered comments: %s", got)
	}
	if !strings.Contains(got, "just ship it after fixing the typo in the title") {
		t.Errorf("summary missing: %s", got)
	}
}

func TestValidReviewRejectsEmptyAndIncompleteComments(t *testing.T) {
	if err := validReview(reviewIn{}); err == nil {
		t.Error("an empty review must be rejected")
	}
	if err := validReview(reviewIn{Comments: []reviewComment{{File: "", Text: "x"}}}); err == nil {
		t.Error("a comment with no file must be rejected")
	}
	if err := validReview(reviewIn{Comments: []reviewComment{{File: "a", Text: "  "}}}); err == nil {
		t.Error("a comment with blank text must be rejected")
	}
	if err := validReview(reviewIn{Summary: "looks good"}); err != nil {
		t.Errorf("a summary alone should be enough: %v", err)
	}
}

func TestResolveBaseRefPrefersARealBranchOverTheLiteralHEAD(t *testing.T) {
	proj := &store.Project{DefaultBaseBranch: "develop"}
	if got := resolveBaseRef("feature/x", proj); got != "feature/x" {
		t.Errorf("an explicit base must win: %q", got)
	}
	if got := resolveBaseRef("HEAD", proj); got != "develop" {
		t.Errorf("the literal HEAD a plan stores is not a durable ref name: %q", got)
	}
	if got := resolveBaseRef("", nil); got != "main" {
		t.Errorf("with nothing at all, fall back to main: %q", got)
	}
}

func TestRefuseOnBaseBranch(t *testing.T) {
	cases := []struct {
		branch, base string
		wantErr      bool
	}{
		{"main", "main", true},
		{"master", "develop", true}, // well-known default, even if base resolved differently
		{"lec/session1-abcd1234", "main", false},
		{"", "main", false}, // unknown branch (detached HEAD, resolution failed): don't guess
	}
	for _, c := range cases {
		err := refuseOnBaseBranch(c.branch, c.base)
		if (err != nil) != c.wantErr {
			t.Errorf("refuseOnBaseBranch(%q, %q) = %v, want err=%v", c.branch, c.base, err, c.wantErr)
		}
	}
}

func TestParsePRDescriptionTolerantOfFencesAndChatter(t *testing.T) {
	out := parsePRDescription("Sure! ```json\n{\"title\":\"Fix the thing\",\"body\":\"- did x\\n- did y\"}\n```", "fallback")
	if out.Title != "Fix the thing" || !strings.Contains(out.Body, "did x") {
		t.Errorf("did not tolerate fenced/chatty JSON: %+v", out)
	}
}

func TestParsePRDescriptionFallsBackToRawTextAsBody(t *testing.T) {
	out := parsePRDescription("I changed the health check to return 200.", "Update health check")
	if out.Title != "Update health check" {
		t.Errorf("prose response should keep the fallback title: %+v", out)
	}
	if out.Body != "I changed the health check to return 200." {
		t.Errorf("prose response should become the body verbatim: %+v", out)
	}
}
