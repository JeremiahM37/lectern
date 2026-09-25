package api_test

// Eval-judge verdict propagation (docs/replay-evals.md "Scoring"), against
// the mock target via the SAME `[mock:...]` directive convention
// bestofn_test.go's TestJudgeTaskRecordsVerdictOnParent already uses for its
// own judge — see internal/executor/mock.go's `[mock:eval-judge:match]`.
// The replay case's ReferenceDiff/similarity scoring is covered separately
// by internal/replay's own tests and by replay_real_test.go's real-process
// scoring test; this one is scoped to the judge round trip.

import (
	"fmt"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

const fakeReferenceDiff = `diff --git a/app.py b/app.py
--- a/app.py
+++ b/app.py
@@ -1,2 +1,2 @@
-print("hello")
+print("hello, lectern")
`

func TestEvalJudgeRecordsMatchVerdictOnTheCell(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	suite := h.post("/api/evals/suites", obj{"name": "judge suite", "project_id": pid}, 201)
	if _, err := h.App.DB.InsertEvalCase(&store.EvalCase{
		SuiteID: suite.id(), Name: "replay case", Prompt: "do it [mock:eval-judge:match]",
		CheckCommand: "mockverify-pass", TimeoutS: 60,
		IsReplay: true, SourcePRNumber: 1, ReferenceDiff: fakeReferenceDiff,
	}); err != nil {
		t.Fatal(err)
	}

	run := h.post(fmt.Sprintf("/api/evals/suites/%d/runs", suite.id()), obj{
		"variants": []obj{{"model": "sonnet"}}, "repeats": 1, "with_judge": true,
	}, 201)

	var view obj
	h.waitUntil("the cell to be judged", func() bool {
		view = h.get(fmt.Sprintf("/api/evals/runs/%d", run.id()))
		results := view.list("results")
		return len(results) == 1 && results[0].str("judge_status") == "done"
	})
	res := view.list("results")[0]
	if res.str("status") != "passed" {
		t.Fatalf("cell should still pass its own check independent of the judge: %v", res)
	}
	if res.num("judge_match") != 1 {
		t.Fatalf("expected judge_match=1 for [mock:eval-judge:match], got %v", res)
	}
	if !contains(res.str("judge_reason"), "mock verdict") {
		t.Errorf("expected the judge's reason text to be recorded: %v", res)
	}
	if res.num("similarity_files") != 1 { // MockDiff scored against fakeReferenceDiff's same file/lines
		t.Errorf("expected similarity scoring to have run for this replay cell: %v", res)
	}

	// the judge card itself retires off the board once its verdict lands
	var judgeTaskFound bool
	h.waitUntil("the judge card to close itself", func() bool {
		for _, x := range h.getList("/api/tasks") {
			if x.str("created_by") == "eval-judge" {
				judgeTaskFound = true
				return x.str("status") == "done"
			}
		}
		return false
	})
	if !judgeTaskFound {
		t.Error("expected an eval-judge task to have been dispatched")
	}
}

func TestEvalJudgeRecordsNoMatchVerdictOnTheCell(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	suite := h.post("/api/evals/suites", obj{"name": "judge suite 2", "project_id": pid}, 201)
	if _, err := h.App.DB.InsertEvalCase(&store.EvalCase{
		SuiteID: suite.id(), Name: "replay case", Prompt: "do it [mock:eval-judge:no-match]",
		CheckCommand: "mockverify-pass", TimeoutS: 60,
		IsReplay: true, SourcePRNumber: 2, ReferenceDiff: fakeReferenceDiff,
	}); err != nil {
		t.Fatal(err)
	}

	run := h.post(fmt.Sprintf("/api/evals/suites/%d/runs", suite.id()), obj{
		"variants": []obj{{"model": "sonnet"}}, "repeats": 1, "with_judge": true,
	}, 201)

	var view obj
	h.waitUntil("the cell to be judged", func() bool {
		view = h.get(fmt.Sprintf("/api/evals/runs/%d", run.id()))
		results := view.list("results")
		return len(results) == 1 && results[0].str("judge_status") == "done"
	})
	res := view.list("results")[0]
	if res.num("judge_match") != 0 {
		t.Fatalf("expected judge_match=0 for [mock:eval-judge:no-match], got %v", res)
	}
}

// TestEvalRunWithoutJudgeNeverDispatchesOne proves with_judge defaults to
// off: a replay case's cell is scored (similarity fields populate) but no
// eval-judge task is ever filed.
func TestEvalRunWithoutJudgeNeverDispatchesOne(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	suite := h.post("/api/evals/suites", obj{"name": "no judge suite", "project_id": pid}, 201)
	if _, err := h.App.DB.InsertEvalCase(&store.EvalCase{
		SuiteID: suite.id(), Name: "replay case", Prompt: "do it",
		CheckCommand: "mockverify-pass", TimeoutS: 60,
		IsReplay: true, SourcePRNumber: 3, ReferenceDiff: fakeReferenceDiff,
	}); err != nil {
		t.Fatal(err)
	}

	run := h.post(fmt.Sprintf("/api/evals/suites/%d/runs", suite.id()), obj{
		"variants": []obj{{"model": "sonnet"}}, "repeats": 1,
	}, 201)

	var view obj
	h.waitUntil("the run to finish", func() bool {
		view = h.get(fmt.Sprintf("/api/evals/runs/%d", run.id()))
		return view.sub("run").str("status") == "done"
	})
	res := view.list("results")[0]
	if res.str("judge_status") != "" {
		t.Errorf("expected no judge dispatch without with_judge, got judge_status=%q", res.str("judge_status"))
	}
	if res.num("similarity_files") != 1 { // MockDiff scored against itself-shaped fakeReferenceDiff's same file
		t.Errorf("expected similarity scoring to still run for a replay cell: %v", res)
	}
	for _, x := range h.getList("/api/tasks") {
		if x.str("created_by") == "eval-judge" {
			t.Fatalf("no eval-judge task should have been dispatched: %v", x)
		}
	}
}
