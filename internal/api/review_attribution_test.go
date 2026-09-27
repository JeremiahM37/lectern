package api

import (
	"reflect"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func TestAgentWrittenLinesPrefersClaudesStructuredPatch(t *testing.T) {
	input := map[string]any{"file_path": "/w/app.go", "old_string": "a\nb", "new_string": "a\nB\nc"}
	response := map[string]any{"structuredPatch": []any{
		map[string]any{"lines": []any{" a", "-b", "+B", "+c"}},
	}}
	got := agentWrittenLines("Edit", input, response, "/w")
	if want := []string{"B", "c"}; !reflect.DeepEqual(got["/w/app.go"], want) {
		t.Fatalf("structured patch lines: %v", got)
	}
}

func TestAgentWrittenLinesWithoutAPatchKeepsOnlyIntroducedLines(t *testing.T) {
	// "keep" was already in old_string: context the agent repeated, not a
	// line it wrote — it must not be claimed for the agent.
	got := agentWrittenLines("Edit", map[string]any{"file_path": "/w/x.py",
		"old_string": "keep\nold", "new_string": "keep\nnew one"}, nil, "")
	if want := []string{"new one"}; !reflect.DeepEqual(got["/w/x.py"], want) {
		t.Fatalf("edit fallback: %v", got)
	}
	multi := agentWrittenLines("MultiEdit", map[string]any{"file_path": "/w/x.py", "edits": []any{
		map[string]any{"old_string": "a", "new_string": "a2"},
		map[string]any{"old_string": "b", "new_string": "b\nb3"},
	}}, nil, "")
	if want := []string{"a2", "b3"}; !reflect.DeepEqual(multi["/w/x.py"], want) {
		t.Fatalf("multi edit: %v", multi)
	}
	write := agentWrittenLines("Write", map[string]any{"file_path": "rel/new.txt", "content": "one\ntwo\n"}, nil, "/w")
	if want := []string{"one", "two"}; !reflect.DeepEqual(write["/w/rel/new.txt"], want) {
		t.Fatalf("write resolves a relative path against cwd: %v", write)
	}
}

func TestAgentWrittenLinesReadsCodexApplyPatch(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: src/a.rs\n@@\n-old\n+new line\n context\n" +
		"*** Add File: docs/b.md\n+# Title\n+body\n*** Delete File: gone.txt\n*** End Patch\n"
	got := agentWrittenLines("apply_patch", map[string]any{"command": []any{"apply_patch", patch}}, nil, "/repo")
	want := map[string][]string{"/repo/src/a.rs": {"new line"}, "/repo/docs/b.md": {"# Title", "body"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("apply_patch: %v", got)
	}
	if other := agentWrittenLines("Bash", map[string]any{"command": "ls"}, nil, "/repo"); len(other) != 0 {
		t.Fatalf("a tool that wrote nothing recorded lines: %v", other)
	}
}

func TestClassifyLine(t *testing.T) {
	marks := map[string]bool{lineHash("agent wrote this"): true}
	agentCommits := map[string]bool{"a1b2c3": true}
	zero := strings.Repeat("0", 40)
	cases := []struct {
		name, line, sha string
		evidence        bool
		want            string
	}{
		{"agent-reported uncommitted line", "agent wrote this", zero, true, "agent"},
		// trailing whitespace does not change a line's identity
		{"agent line with trailing space", "agent wrote this  ", zero, true, "agent"},
		// a human edit changes the content, so the line flips to the human
		{"human edit of an agent line", "agent wrote this, then I fixed it", zero, true, "human"},
		{"uncommitted with no hook evidence is unknown", "mystery", zero, false, ""},
		{"committed by an agent commit", "from history", "a1b2c3", false, "agent"},
		{"committed by a person", "from history", "ffff00", false, "human"},
		{"agent line a person committed", "agent wrote this", "ffff00", false, "agent"},
		{"blank lines are never attributed", "   ", zero, true, ""},
	}
	for _, c := range cases {
		if got := classifyLine(lineHash(c.line), c.sha, marks, agentCommits, c.evidence); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestAddedLinesAndRanges(t *testing.T) {
	patch := "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1,3 +1,4 @@\n a\n+b\n c\n-d\n+e\n@@ -10 +11,2 @@\n x\n+y\n"
	got := addedLines(patch)
	want := []addedLine{{2, "b"}, {4, "e"}, {12, "y"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("added lines: %v", got)
	}
	if r := lineRanges([]int{5, 1, 2, 3, 9}); !reflect.DeepEqual(r, [][2]int{{1, 3}, {5, 5}, {9, 9}}) {
		t.Fatalf("ranges: %v", r)
	}
}

func TestCleanCommitMessage(t *testing.T) {
	raw := "```\nAdd hunk staging\n\nExplains why.\nCo-authored-by: Some Model <m@example.invalid>\n```"
	if got := cleanCommitMessage(raw); got != "Add hunk staging\n\nExplains why." {
		t.Fatalf("clean: %q", got)
	}
	if got := cleanCommitMessage(`"Fix typo"`); got != "Fix typo" {
		t.Fatalf("quotes: %q", got)
	}
}

func TestFixHookPromptCarriesOutputAndRules(t *testing.T) {
	out := strings.Repeat("x", 20000) + "\nlint: 2 problems in app.go"
	p := buildFixHookPrompt("/w/repo", "feat: thing", []string{"pre-commit"}, out)
	for _, want := range []string{"/w/repo", "pre-commit hook", "feat: thing", "--no-verify",
		"lint: 2 problems in app.go", "output shortened"} {
		if !strings.Contains(p, want) {
			t.Errorf("fix-hook prompt lacks %q", want)
		}
	}
	if len(p) > 14000 {
		t.Errorf("hook output was not shortened: %d bytes", len(p))
	}
}

func TestStoredReviewBatchIsOneRoundAndNamesReraisedComments(t *testing.T) {
	all := []*store.ReviewComment{
		{ID: 1, File: "a.go", Line: 3, Side: "new", Text: "rename this", Status: "sent", Round: 1},
		{ID: 2, File: "a.go", Line: 9, Side: "new", Text: "still wrong", Status: "draft", Round: 1},
		{ID: 3, File: "b.go", Line: 1, Side: "old", Text: "why removed?", Status: "draft"},
	}
	comments, ids, round, err := storedReviewBatch(all, []int64{2, 3})
	if err != nil {
		t.Fatal(err)
	}
	if round != 2 || !reflect.DeepEqual(ids, []int64{2, 3}) || len(comments) != 2 {
		t.Fatalf("batch: round %d ids %v comments %v", round, ids, comments)
	}
	prompt := formatReviewPrompt(comments, "")
	if strings.Count(prompt, "Code review feedback") != 1 || !strings.Contains(prompt, "(2 comments)") {
		t.Fatalf("not one prompt for the batch:\n%s", prompt)
	}
	if !strings.Contains(prompt, "a.go:9 (new side) (raised again; first sent in round 1)") {
		t.Fatalf("a reopened comment must say it is raised again:\n%s", prompt)
	}
	if strings.Contains(prompt, "b.go:1 (old side) (raised again") {
		t.Fatalf("a first-time comment was marked as raised again:\n%s", prompt)
	}
	if _, _, _, err := storedReviewBatch(all, []int64{1}); err == nil {
		t.Fatal("an already-sent comment must be reopened before it is sent again")
	}
	if _, _, _, err := storedReviewBatch(all, []int64{42}); err == nil {
		t.Fatal("an unknown comment id must be refused")
	}
}
