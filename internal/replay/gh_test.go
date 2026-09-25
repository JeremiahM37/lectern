package replay

import (
	"strings"
	"testing"
)

// fakeGHPRListOutput is exactly the shape `gh pr list --json
// number,title,body,mergeCommit,baseRefName,files,closingIssuesReferences`
// prints — used as a fixture so ParseGHPRList (and everything downstream:
// PreFilter, BuildCase) is testable without a real `gh` binary or network
// access, per the "never spend real tokens / stub external tools" test rule.
const fakeGHPRListOutput = `[
  {
    "number": 101,
    "title": "Add a health endpoint",
    "body": "Ops wants a liveness probe so the load balancer can tell if we're up.",
    "mergeCommit": {"oid": "aaaaaaa1111111111111111111111111111111"},
    "baseRefName": "main",
    "files": [
      {"path": "internal/api/health.go", "additions": 12, "deletions": 0},
      {"path": "internal/api/health_test.go", "additions": 20, "deletions": 0}
    ],
    "closingIssuesReferences": [
      {"number": 42, "title": "No liveness probe", "body": "We can't tell if the service is up."}
    ]
  },
  {
    "number": 102,
    "title": "Reword the README",
    "body": "Fix a typo.",
    "mergeCommit": {"oid": "bbbbbbb2222222222222222222222222222222"},
    "baseRefName": "main",
    "files": [{"path": "README.md", "additions": 1, "deletions": 1}],
    "closingIssuesReferences": []
  },
  {
    "number": 103,
    "title": "Rewritten by a merge queue, no merge commit left",
    "body": "",
    "mergeCommit": {"oid": ""},
    "baseRefName": "main",
    "files": [],
    "closingIssuesReferences": []
  }
]`

func TestParseGHPRList(t *testing.T) {
	prs, err := ParseGHPRList([]byte(fakeGHPRListOutput))
	if err != nil {
		t.Fatal(err)
	}
	// PR 103 has no merge commit oid and must be dropped.
	if len(prs) != 2 {
		t.Fatalf("expected 2 PRs (one dropped for missing merge commit), got %d: %+v", len(prs), prs)
	}
	if prs[0].Number != 101 || prs[0].MergeSHA != "aaaaaaa1111111111111111111111111111111" {
		t.Fatalf("PR 0: %+v", prs[0])
	}
	if len(prs[0].Files) != 2 {
		t.Fatalf("PR 0 files: %+v", prs[0].Files)
	}
	if len(prs[0].Issues) != 1 || prs[0].Issues[0].Number != 42 {
		t.Fatalf("PR 0 issues: %+v", prs[0].Issues)
	}
	if prs[1].Number != 102 || len(prs[1].Files) != 1 {
		t.Fatalf("PR 1: %+v", prs[1])
	}
}

func TestParseGHPRListRejectsInvalidJSON(t *testing.T) {
	if _, err := ParseGHPRList([]byte("not json")); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

// TestBuildCandidateFromFakeGHOutput exercises the whole cheap+expensive
// pipeline end to end against the fake gh fixture above, standing in for
// internal/api/replay.go's orchestration (which the API layer tests cover
// against a real target) with no target/executor at all.
func TestBuildCandidateFromFakeGHOutput(t *testing.T) {
	prs, err := ParseGHPRList([]byte(fakeGHPRListOutput))
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{MaxChangedLines: 400}

	healthPR := prs[0]
	skip, lang, testFiles := PreFilter(healthPR, "", opts)
	if skip != "" {
		t.Fatalf("health endpoint PR should not be skipped: %s", skip)
	}
	if lang != "go" || len(testFiles) != 1 {
		t.Fatalf("expected go test detection, got lang=%q files=%v", lang, testFiles)
	}
	diff := "--- a/internal/api/health_test.go\n+++ b/internal/api/health_test.go\n" +
		"@@ -0,0 +1,3 @@\n+func TestHealthReturnsOK(t *testing.T) {}\n"
	c := BuildCase(healthPR, "", lang, testFiles, "parentsha123", diff)
	if c.SourcePRNumber != 101 {
		t.Errorf("SourcePRNumber: %v", c.SourcePRNumber)
	}
	if c.CheckCommand == "" || !strings.Contains(c.CheckCommand, "TestHealthReturnsOK") {
		t.Errorf("check command should name the added test: %q", c.CheckCommand)
	}
	if !strings.Contains(c.Prompt, "Add a health endpoint") || !strings.Contains(c.Prompt, "liveness probe") {
		t.Errorf("prompt missing PR intent: %q", c.Prompt)
	}

	readmePR := prs[1]
	skip, _, _ = PreFilter(readmePR, "", opts)
	if skip == "" {
		t.Fatal("docs-only README PR should have been skipped")
	}
}
