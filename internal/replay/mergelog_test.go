package replay

import "testing"

func rec(hash, subject, body string) string {
	return hash + "\x1f" + subject + "\x1f" + body + "\x1e"
}

func TestParseMergeLogGitHubStyleMergeCommit(t *testing.T) {
	raw := rec("deadbeef1", "Merge pull request #77 from acme/fix-health",
		"Add a health endpoint\n\nOps wants a liveness probe.")
	prs := ParseMergeLog(raw)
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %d: %+v", len(prs), prs)
	}
	pr := prs[0]
	if pr.Number != 77 {
		t.Errorf("Number: %v", pr.Number)
	}
	if pr.Title != "Add a health endpoint" {
		t.Errorf("Title: %q", pr.Title)
	}
	if pr.Body != "Ops wants a liveness probe." {
		t.Errorf("Body: %q", pr.Body)
	}
	if pr.MergeSHA != "deadbeef1" {
		t.Errorf("MergeSHA: %q", pr.MergeSHA)
	}
}

func TestParseMergeLogSquashCommitHasNoPRNumber(t *testing.T) {
	raw := rec("cafef00d", "Add a health endpoint (#77)", "Ops wants a liveness probe.")
	prs := ParseMergeLog(raw)
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(prs))
	}
	pr := prs[0]
	// a squash commit's subject is not "Merge pull request ..." so it is
	// used as-is: no number parsed (this repo's squash template doesn't
	// match the GitHub merge-commit pattern this parser recognizes), title
	// is the raw subject.
	if pr.Number != 0 {
		t.Errorf("Number: %v, want 0 (not a recognized merge-commit subject)", pr.Number)
	}
	if pr.Title != "Add a health endpoint (#77)" {
		t.Errorf("Title: %q", pr.Title)
	}
}

func TestParseMergeLogMultipleRecords(t *testing.T) {
	raw := rec("sha1", "Merge pull request #1 from a/b", "First PR\n\nBody one.") +
		rec("sha2", "Merge pull request #2 from a/c", "Second PR\n\nBody two.")
	prs := ParseMergeLog(raw)
	if len(prs) != 2 {
		t.Fatalf("expected 2 PRs, got %d: %+v", len(prs), prs)
	}
	if prs[0].Number != 1 || prs[1].Number != 2 {
		t.Fatalf("PR numbers out of order: %+v", prs)
	}
}

func TestParseMergeLogIgnoresBlankRecords(t *testing.T) {
	raw := "\x1e" + rec("sha1", "Merge pull request #1 from a/b", "First\n\nBody.") + "\x1e\x1e"
	prs := ParseMergeLog(raw)
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR ignoring blank records, got %d: %+v", len(prs), prs)
	}
}
