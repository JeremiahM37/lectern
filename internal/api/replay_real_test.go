package api_test

// Replay evals (docs/replay-evals.md) against a REAL temp git repo with real
// merge commits — the "case construction ... from a real temp git repo with
// merges (isolated runner)" half of the feature's test requirement. The
// other half (case construction from fake `gh` output) is pure and lives in
// internal/replay/gh_test.go, with no target/executor involved at all.
//
// `gh` is forced to fail here regardless of whether a real `gh` binary
// happens to be installed and authenticated on the machine running this
// suite, so the git-log fallback path is exercised deterministically rather
// than depending on the environment.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func forceGHUnavailable(t *testing.T) {
	t.Helper()
	fakeBinDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBinDir, "gh"), []byte("#!/bin/bash\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestReplayPreviewFallsBackToGitLogAgainstARealRepoWithMerges builds two
// real merge commits — one a PR-shaped change with a Go test file (should be
// accepted, with an exact -run test name and a leak-free prompt), one a
// docs-only change (should be skipped) — and proves the whole import
// pipeline (fetchReplayPRs' fallback, PreFilter, BuildCase) against them
// with nothing stubbed above git itself.
func TestReplayPreviewFallsBackToGitLogAgainstARealRepoWithMerges(t *testing.T) {
	r := newRealRig(t)
	forceGHUnavailable(t)

	mustRun(t, r.repo, "git", "checkout", "-b", "feature/health")
	if err := os.MkdirAll(filepath.Join(r.repo, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.repo, "internal", "health.go"),
		[]byte("package internal\n\nfunc Health() bool { return true }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.repo, "internal", "health_test.go"),
		[]byte("package internal\n\nimport \"testing\"\n\nfunc TestHealthReturnsTrue(t *testing.T) {\n"+
			"\tif !Health() {\n\t\tt.Fail()\n\t}\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, r.repo, "git", "add", "-A")
	mustRun(t, r.repo, "git", "commit", "-q", "-m", "add health check")
	mustRun(t, r.repo, "git", "checkout", "main")
	mustRun(t, r.repo, "git", "merge", "--no-ff", "-m",
		"Merge pull request #12 from acme/health\n\nAdd a health endpoint\n\n"+
			"Ops wants a liveness probe so the load balancer can tell if we're up.", "feature/health")

	mustRun(t, r.repo, "git", "checkout", "-b", "docs/typo")
	if err := os.WriteFile(filepath.Join(r.repo, "NOTES.md"), []byte("start\nfixed a typo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, r.repo, "git", "commit", "-aqm", "fix typo")
	mustRun(t, r.repo, "git", "checkout", "main")
	mustRun(t, r.repo, "git", "merge", "--no-ff", "-m",
		"Merge pull request #13 from acme/typo\n\nFix a typo in NOTES.md\n\nJust a typo.", "docs/typo")

	resp := r.doJSON(t, "POST", "/api/evals/replay/preview", map[string]any{"project_id": r.project, "n": 10})
	if resp["source"] != "git-log" {
		t.Fatalf("expected the git-log fallback (gh forced unavailable), got %v", resp["source"])
	}
	candidates, ok := resp["candidates"].([]any)
	if !ok || len(candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %v", resp["candidates"])
	}
	var accepted, skipped map[string]any
	for _, raw := range candidates {
		c := raw.(map[string]any)
		if c["accepted"].(bool) {
			accepted = c
		} else {
			skipped = c
		}
	}
	if accepted == nil || skipped == nil {
		t.Fatalf("expected exactly one accepted and one skipped candidate: %v", candidates)
	}
	if accepted["title"] != "Add a health endpoint" {
		t.Errorf("accepted candidate title: %v", accepted["title"])
	}
	caseView, ok := accepted["case"].(map[string]any)
	if !ok {
		t.Fatalf("accepted candidate missing its case: %v", accepted)
	}
	prompt, _ := caseView["prompt"].(string)
	if !strings.Contains(prompt, "liveness probe") {
		t.Errorf("prompt missing PR intent: %q", prompt)
	}
	if strings.Contains(prompt, "func Health()") {
		t.Errorf("prompt leaked solution code: %q", prompt)
	}
	checkCmd, _ := caseView["check_command"].(string)
	if !strings.Contains(checkCmd, "TestHealthReturnsTrue") {
		t.Errorf("check_command should name the added Go test, got %q", checkCmd)
	}
	if !strings.Contains(checkCmd, "./internal/...") {
		t.Errorf("check_command should scope to the touched package, got %q", checkCmd)
	}
	skipReason, _ := skipped["skip_reason"].(string)
	if !strings.Contains(skipReason, "docs-only") {
		t.Errorf("expected the NOTES.md-only PR to be skipped as docs-only, got %v", skipped)
	}
}

// TestReplayCreateSuitePersistsCasesWithReferenceDiff proves the second half
// of the pipeline: creating a suite actually persists the accepted
// candidate as an is_replay case with its reference diff, and that the
// suite/run views expose it pre-split for a diff viewer (replayCaseView) —
// the "show the reference diff next to the attempt's diff" UI requirement.
func TestReplayCreateSuitePersistsCasesWithReferenceDiff(t *testing.T) {
	r := newRealRig(t)
	forceGHUnavailable(t)

	mustRun(t, r.repo, "git", "checkout", "-b", "feature/greeting")
	if err := os.WriteFile(filepath.Join(r.repo, "greet.py"), []byte("def greet():\n    return 'hi'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.repo, "test_greet.py"),
		[]byte("from greet import greet\n\ndef test_greet():\n    assert greet() == 'hi'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, r.repo, "git", "add", "-A")
	mustRun(t, r.repo, "git", "commit", "-q", "-m", "add greet()")
	mustRun(t, r.repo, "git", "checkout", "main")
	mustRun(t, r.repo, "git", "merge", "--no-ff", "-m",
		"Merge pull request #7 from acme/greet\n\nAdd a greeting helper\n\nWe need a friendly greeting function.",
		"feature/greeting")

	resp := r.doJSON(t, "POST", "/api/evals/replay/suites", map[string]any{
		"project_id": r.project, "n": 10, "name": "replay from history",
	})
	suite, ok := resp["suite"].(map[string]any)
	if !ok {
		t.Fatalf("expected a created suite in the response: %v", resp)
	}
	suiteID := int64(suite["id"].(float64))

	detail := r.doJSON(t, "GET", fmt.Sprintf("/api/evals/suites/%d", suiteID), nil)
	cases, ok := detail["cases"].([]any)
	if !ok || len(cases) != 1 {
		t.Fatalf("expected 1 persisted case, got %v", detail["cases"])
	}
	c := cases[0].(map[string]any)
	if !c["is_replay"].(bool) {
		t.Errorf("case should be marked is_replay: %v", c)
	}
	if int(c["source_pr_number"].(float64)) != 7 {
		t.Errorf("source_pr_number: %v", c)
	}
	files, ok := c["reference_files"].([]any)
	if !ok || len(files) == 0 {
		t.Fatalf("expected a pre-split reference_files for the diff viewer, got %v", c["reference_files"])
	}
	first := files[0].(map[string]any)
	if patch, _ := first["patch"].(string); !strings.Contains(patch, "greet") {
		t.Errorf("reference_files patch missing expected content: %v", first)
	}
	// the raw reference_diff itself must NOT be sent — only the pre-split view
	if _, present := c["reference_diff"]; present {
		t.Errorf("reference_diff should be excluded from the API response, got %v", c["reference_diff"])
	}
}

// replayScoringStubAgent branches on --model like evals_real_test.go's
// evalStubAgent, but the two branches write genuinely different real file
// content (not just "a marker exists or doesn't") so a real `git diff`
// capture produces two diffs with different files/lines to score — the
// point of this test. "good" reproduces the reference change; "bad" makes
// an unrelated one.
const replayScoringStubAgent = `#!/bin/bash
model=""
while [ $# -gt 0 ]; do
  case "$1" in
    --model) model="$2"; shift 2 ;;
    *) shift ;;
  esac
done
echo '{"type":"system","subtype":"init","session_id":"fake-replay","tools":["Bash","Edit"]}'
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"Working."}]}}'
if [ "$model" = "good" ]; then
  printf 'def greet():\n    return "hello from lectern"\n' > greet.py
else
  printf 'def unrelated():\n    return 0\n' > unrelated.py
fi
echo '{"type":"result","subtype":"success","is_error":false,"result":"done"}'
exit 0
`

// referenceGreetDiff is the accepted "reference" change replayScoringStubAgent's
// "good" branch reproduces exactly (same file, same added lines) and the
// "bad" branch does not — see internal/replay.Score, which reads the same
// +++/--- and +/- lines this diff carries.
const referenceGreetDiff = `diff --git a/greet.py b/greet.py
new file mode 100644
index 0000000..1111111
--- /dev/null
+++ b/greet.py
@@ -0,0 +1,2 @@
+def greet():
+    return "hello from lectern"
`

// TestReplayRunScoresSimilarityAndLeaderboardRanksTheBetterStub is the "end
// to end replay run with stub agents where one stub reproduces the
// reference change and one doesn't" case the feature spec asks for by name.
// The eval case is inserted directly (IsReplay + a hand-built
// ReferenceDiff) rather than through the import pipeline — that pipeline
// has its own dedicated tests above; this one is scoped to scoring.
func TestReplayRunScoresSimilarityAndLeaderboardRanksTheBetterStub(t *testing.T) {
	r := newRealRig(t)
	if err := os.WriteFile(r.app.Cfg.ClaudeBin, []byte(replayScoringStubAgent), 0o755); err != nil {
		t.Fatal(err)
	}

	suite := r.doJSON(t, "POST", "/api/evals/suites", map[string]any{
		"name": "replay scoring suite", "project_id": r.project,
	})
	suiteID := int64(suite["id"].(float64))
	if _, err := r.app.DB.InsertEvalCase(&store.EvalCase{
		SuiteID: suiteID, Name: "add greet()", Prompt: "Add a greet() function returning a hello message.",
		CheckCommand: "true", TimeoutS: 60,
		IsReplay: true, SourcePRNumber: 7, ReferenceDiff: referenceGreetDiff,
	}); err != nil {
		t.Fatal(err)
	}

	run := r.doJSON(t, "POST", fmt.Sprintf("/api/evals/suites/%d/runs", suiteID), map[string]any{
		"variants": []map[string]any{{"agent": "claude", "model": "good"}, {"agent": "claude", "model": "bad"}},
		"repeats":  1,
	})
	runID := int64(run["id"].(float64))

	deadline := time.Now().Add(30 * time.Second)
	var view map[string]any
	for time.Now().Before(deadline) {
		view = r.doJSON(t, "GET", fmt.Sprintf("/api/evals/runs/%d", runID), nil)
		if view["run"].(map[string]any)["status"] == "done" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if view == nil || view["run"].(map[string]any)["status"] != "done" {
		t.Fatalf("run did not finish: %v", view)
	}

	results := view["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("expected 2 cells (1 case x 2 variants), got %d: %v", len(results), results)
	}
	var good, bad map[string]any
	for _, raw := range results {
		res := raw.(map[string]any)
		if int(res["variant_idx"].(float64)) == 0 {
			good = res
		} else {
			bad = res
		}
	}
	if good["similarity_files"] == nil || good["similarity_files"].(float64) != 1 {
		t.Errorf("good variant should match the reference file exactly: %v", good)
	}
	if good["similarity_lines"] == nil || good["similarity_lines"].(float64) != 1 {
		t.Errorf("good variant should match the reference lines exactly: %v", good)
	}
	if bad["similarity_files"] == nil || bad["similarity_files"].(float64) != 0 {
		t.Errorf("bad variant touched a different file — should score 0 file overlap: %v", bad)
	}

	board := view["leaderboard"].([]any)
	v0, v1 := board[0].(map[string]any), board[1].(map[string]any)
	if v0["scored_count"].(float64) != 1 || v0["mean_similarity_files"].(float64) != 1 {
		t.Errorf("leaderboard row for the good variant: %v", v0)
	}
	if v1["scored_count"].(float64) != 1 || v1["mean_similarity_files"].(float64) != 0 {
		t.Errorf("leaderboard row for the bad variant: %v", v1)
	}
}
